package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// CloudSave 云端一个存档的元数据
type CloudSave struct {
	Asset       string `json:"asset"`
	AssetSHA    string `json:"asset_sha256"`
	SaveSHA     string `json:"save_sha256"`
	SaveDate    string `json:"save_date"` // RFC3339
	Character   string `json:"character"`
	GameVersion string `json:"game_version"`
	Size        int64  `json:"size"`
	UpdatedBy   string `json:"updated_by"`
	UpdatedAt   string `json:"updated_at"`
}

// CloudManifest 云端清单（作为 manifest.json 资产存储）
type CloudManifest struct {
	Schema    int                   `json:"schema"`
	UpdatedAt string                `json:"updated_at"`
	UpdatedBy string                `json:"updated_by"`
	Saves     map[string]*CloudSave `json:"saves"`
	Mods      *ModsInfo             `json:"mods,omitempty"`
	ModsSHA   string                `json:"mods_sha,omitempty"`
	VmSHA     string                `json:"vm_sha256,omitempty"`        // 兼容旧版：等价于 Extras["vmparams"]
	Extras    map[string]string     `json:"extras,omitempty"`           // 额外同步文件 rel -> sha256
}

func newCloudManifest() *CloudManifest {
	return &CloudManifest{Schema: 1, Saves: map[string]*CloudSave{}}
}

// seedExtras 兼容旧清单：把旧的 VmSHA 视为 Extras["vmparams"]
func (cm *CloudManifest) seedExtras() {
	if cm.Extras == nil {
		cm.Extras = map[string]string{}
	}
	if _, ok := cm.Extras["vmparams"]; !ok && cm.VmSHA != "" {
		cm.Extras["vmparams"] = cm.VmSHA
	}
}

// BaselineEntry 上次同步成功时两侧的状态，用于区分"谁改过"
type BaselineEntry struct {
	LocalSHA string `json:"local_sha"` // 上次同步时本机内容哈希
	CloudSHA string `json:"cloud_sha"` // 上次同步时云端内容哈希
}

// ExtraBaseline 额外同步文件的上次同步状态
type ExtraBaseline struct {
	LocalSHA string `json:"local_sha"`
	CloudSHA string `json:"cloud_sha"`
}

type Baseline struct {
	Saves        map[string]*BaselineEntry `json:"saves"`
	ModsLocalSHA string                    `json:"mods_local_sha,omitempty"`
	ModsCloudSHA string                    `json:"mods_cloud_sha,omitempty"`
	VmLocalSHA   string                    `json:"vm_local_sha,omitempty"`
	VmCloudSHA   string                    `json:"vm_cloud_sha,omitempty"`
	Extras       map[string]*ExtraBaseline `json:"extras,omitempty"` // 额外文件 rel -> 状态
}

func newBaseline() *Baseline {
	return &Baseline{Saves: map[string]*BaselineEntry{}, Extras: map[string]*ExtraBaseline{}}
}

// seedExtras 兼容旧状态：把旧的 VmLocalSHA/VmCloudSHA 视为 Extras["vmparams"]
func (b *Baseline) seedExtras() {
	if b.Extras == nil {
		b.Extras = map[string]*ExtraBaseline{}
	}
	if _, ok := b.Extras["vmparams"]; !ok && (b.VmLocalSHA != "" || b.VmCloudSHA != "") {
		b.Extras["vmparams"] = &ExtraBaseline{LocalSHA: b.VmLocalSHA, CloudSHA: b.VmCloudSHA}
	}
}

// StateFile 本地状态（baseline + 扫描缓存）
type StateFile struct {
	Baseline *Baseline   `json:"baseline"`
	Local    *LocalState `json:"local"`
}

func loadState() *StateFile {
	st := readStateFile()
	if st.Baseline == nil {
		st.Baseline = newBaseline()
	}
	st.Baseline.seedExtras()
	return st
}

func readStateFile() *StateFile {
	st := &StateFile{}
	p, err := statePath()
	if err != nil {
		return st
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return st
	}
	if json.Unmarshal(data, st) != nil {
		return &StateFile{}
	}
	return st
}

func saveState(st *StateFile) error {
	p, err := statePath()
	if err != nil {
		return err
	}
	data, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(p, data, 0o600)
}

// Summary 一次同步的结果
type Summary struct {
	Uploaded   []string
	Downloaded []string
	Conflicts  []string
	Deferred   []string
	Deleted    []string
	Restored   []string
	Warnings   []string
}

func (s *Summary) empty() bool {
	return len(s.Uploaded) == 0 && len(s.Downloaded) == 0 && len(s.Conflicts) == 0 &&
		len(s.Deferred) == 0 && len(s.Deleted) == 0 && len(s.Restored) == 0 && len(s.Warnings) == 0
}

func (s *Summary) print(device string) {
	for _, w := range s.Warnings {
		fmt.Println("  [警告]", w)
	}
	for _, x := range s.Uploaded {
		fmt.Println("  ↑ 已上传", x)
	}
	for _, x := range s.Downloaded {
		fmt.Println("  ↓ 已下载", x)
	}
	for _, x := range s.Conflicts {
		fmt.Println("  ⚡ 冲突处理:", x)
	}
	for _, x := range s.Restored {
		fmt.Println("  ↩ 已从云端恢复:", x)
	}
	for _, x := range s.Deleted {
		fmt.Println("  🗑 已同步删除:", x)
	}
	for _, x := range s.Deferred {
		fmt.Println("  ⏸ 暂缓下载(游戏运行中):", x)
	}
	if !s.empty() {
		fmt.Printf("(%s · %s)\n", device, time.Now().Format("15:04:05"))
	}
}

// SyncOpts 同步选项
type SyncOpts struct {
	UploadOnly    bool
	DownloadOnly  bool
	Quiet         bool
	ResetCloud    bool // 以本机为准重置云端（灾难恢复用）
	ForceUpload   bool // push：以本机为准，强制把本地存档/配置上传覆盖云端
	ForceDownload bool // pull：以云端为准，强制下载覆盖本机存档/配置
}

// Syncer 同步引擎
type Syncer struct {
	cfg   *Config
	prof  *GameProfile
	gh    *GH
	rel   *Release
	state *StateFile
	cm    *CloudManifest
	base  *Baseline

	local     *LocalState
	cmChanged bool
	summary   *Summary
	opts      SyncOpts
}

func doSync(cfg *Config, opts SyncOpts) (*Summary, error) {
	token, err := getToken()
	if err != nil {
		return nil, err
	}
	prof, err := cfg.game()
	if err != nil {
		return nil, err
	}
	gh := NewGH(token, cfg.Repo)
	rel, err := gh.EnsureRelease(cfg.ReleaseTag)
	if err != nil {
		return nil, fmt.Errorf("连接云端失败: %w", err)
	}
	s := &Syncer{
		cfg: cfg, prof: prof, gh: gh, rel: rel,
		state: loadState(), opts: opts,
		summary: &Summary{},
	}
	if err := s.run(); err != nil {
		return nil, err
	}
	return s.summary, nil
}

func (s *Syncer) run() error {
	if st, err := os.Stat(s.prof.SaveDir); err != nil || !st.IsDir() {
		return fmt.Errorf("存档目录不存在: %s", s.prof.SaveDir)
	}
	// 清理遗留的临时解包目录（single 模式的暂存放在存档目录上一级）
	for _, dir := range []string{s.prof.SaveDir, filepath.Dir(s.prof.SaveDir)} {
		ents, _ := os.ReadDir(dir)
		for _, e := range ents {
			if e.IsDir() && strings.HasPrefix(e.Name(), ".gssync_tmp_") {
				os.RemoveAll(filepath.Join(dir, e.Name()))
			}
		}
	}

	// 云端清单
	if s.opts.ResetCloud {
		s.cm = newCloudManifest()
		s.state.Baseline = newBaseline()
		s.summary.Warnings = append(s.summary.Warnings, "已启用 --reset-cloud：以本机数据为准重建云端（云端旧资产未删除）")
	} else {
		cm, err := s.fetchManifest()
		if err != nil {
			return err
		}
		s.cm = cm
		// 防呆：云端清单为空但基线非空 → 疑似云端数据丢失，拒绝自动删除
		// （push 为"以本机为准"的显式操作，不受此限制）
		if len(s.cm.Saves) == 0 && len(s.state.Baseline.Saves) > 0 && !s.opts.ForceUpload {
			return errors.New("云端清单为空但本地同步基线非空，疑似云端数据丢失；如确认要以本机为准重建云端，请运行: gssync sync --reset-cloud")
		}
	}
	s.base = s.state.Baseline
	if s.base == nil {
		s.base = newBaseline()
	}

	// 本地扫描（带指纹缓存）
	local, err := s.scanLocal()
	if err != nil {
		return err
	}
	s.local = local

	gameOn := gameRunning(s.prof.ProcessNames)

	// ---- 存档同步 ----
	union := map[string]bool{}
	for k := range local.Saves {
		union[k] = true
	}
	for k := range s.cm.Saves {
		union[k] = true
	}
	for k := range s.base.Saves {
		union[k] = true
	}
	for _, name := range sortedKeys(union) {
		if err := s.syncOne(name, gameOn); err != nil {
			s.summary.Warnings = append(s.summary.Warnings, fmt.Sprintf("%s: %v", name, err))
		}
	}

	// ---- mod 清单 ----
	s.syncMods()

	// ---- 额外同步文件（如远行星号的 vmparams）----
	s.syncExtraFiles(gameOn)

	// ---- 上传清单 & 落盘状态 ----
	if s.cmChanged {
		s.cm.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		s.cm.UpdatedBy = s.cfg.Device
		data, err := json.MarshalIndent(s.cm, "", "  ")
		if err != nil {
			return err
		}
		if err := s.gh.ReplaceAsset(s.rel, "manifest.json", data); err != nil {
			return fmt.Errorf("上传云端清单失败: %w", err)
		}
	}
	s.state.Baseline = s.base
	s.state.Local = local
	if err := saveState(s.state); err != nil {
		return err
	}

	if !s.opts.Quiet {
		if s.summary.empty() {
			fmt.Println("本地与云端已一致。")
			s.printModsStatus()
		} else {
			s.summary.print(s.cfg.Device)
			s.printModsStatus()
		}
	}
	return nil
}

func (s *Syncer) syncOne(name string, gameOn bool) error {
	l := s.local.Saves[name]
	c := s.cm.Saves[name]
	b := s.base.Saves[name]

	if l == nil && c == nil {
		delete(s.base.Saves, name)
		return nil
	}

	// ---- 手动强制模式 ----
	// push：以本机为准，无条件上传本机该存档（覆盖云端同名资产）
	if s.opts.ForceUpload {
		if l == nil {
			return nil // 本机已无该存档：不删除云端（保守策略）
		}
		if c != nil && l.SHA256 == c.SaveSHA {
			s.setBaseline(name, l.SHA256, c.SaveSHA)
			return nil // 内容已一致，无需重复上传
		}
		return s.applyUpload(name, l)
	}
	// pull：以云端为准，无条件下载覆盖本机（旧版本自动归档到本地历史）
	if s.opts.ForceDownload {
		if c == nil {
			return nil // 云端没有该存档：保留本机
		}
		if l != nil && l.SHA256 == c.SaveSHA {
			s.setBaseline(name, l.SHA256, c.SaveSHA)
			return nil
		}
		if err := s.applyDownload(name, c); err != nil {
			return err
		}
		s.summary.Downloaded = append(s.summary.Downloaded,
			fmt.Sprintf("%s (%s, 来自 %s)", name, shortTime(c.SaveDate), c.UpdatedBy))
		return nil
	}

	if s.opts.UploadOnly && c != nil {
		// 仅上传模式：云端已有则跳过
		return nil
	}

	switch {
	case c == nil && b == nil:
		// 本机新存档 → 上传
		if s.opts.DownloadOnly {
			return nil
		}
		return s.applyUpload(name, l)

	case c == nil && b != nil:
		// 云端已删除（另一台设备删的）→ 本机跟随删除
		if l != nil {
			target := s.prof.entryFor(name).Path
			if _, err := archiveEntryToHistory(s.cfg, s.prof, name, target); err != nil {
				return fmt.Errorf("归档到历史失败: %w", err)
			}
			if err := os.RemoveAll(target); err != nil {
				return err
			}
			delete(s.local.Saves, name)
			s.summary.Deleted = append(s.summary.Deleted, name+"（云端已删除，本机旧版本已存入历史）")
		}
		delete(s.base.Saves, name)
		return nil

	case l == nil && c != nil:
		// 本机没有，云端有 → 下载（无论云端是否变化都取回，保护性恢复）
		if s.opts.UploadOnly {
			return nil
		}
		if err := s.applyDownload(name, c); err != nil {
			return err
		}
		s.summary.Restored = append(s.summary.Restored, name+"（云端取回）")
		return nil

	default: // 两侧都有
		lch := b == nil || l.SHA256 != b.LocalSHA
		cch := b == nil || c.SaveSHA != b.CloudSHA
		switch {
		case !lch && !cch:
			return nil // 已一致
		case lch && !cch:
			// 本机改动 → 上传
			if s.opts.DownloadOnly {
				return nil
			}
			return s.applyUpload(name, l)
		case !lch && cch:
			// 云端改动 → 下载
			if s.opts.UploadOnly {
				return nil
			}
			if gameOn {
				s.summary.Deferred = append(s.summary.Deferred, name)
				return nil
			}
			if err := s.applyDownload(name, c); err != nil {
				return err
			}
			s.summary.Downloaded = append(s.summary.Downloaded,
				fmt.Sprintf("%s (%s, 来自 %s)", name, shortTime(c.SaveDate), c.UpdatedBy))
			return nil
		default:
			// 两侧都改了
			if l.SHA256 == c.SaveSHA {
				s.setBaseline(name, l.SHA256, c.SaveSHA)
				return nil
			}
			if s.opts.UploadOnly || s.opts.DownloadOnly {
				return fmt.Errorf("检测到冲突，但当前处于单向同步模式，请用双向 sync 处理")
			}
			return s.resolveConflict(name, l, c, gameOn)
		}
	}
}

// resolveConflict 冲突处理：descriptor 里的存档时间较新者胜，输家归档
func (s *Syncer) resolveConflict(name string, l *SaveMeta, c *CloudSave, gameOn bool) error {
	lt := parseTimeOrZero(l.SaveDate)
	ct := parseTimeOrZero(c.SaveDate)
	localWins := !ct.After(lt) // 平手时本机胜

	if localWins {
		// 云端输家转存为冲突资产
		if asset := s.gh.AssetByName(s.rel, c.Asset); asset != nil {
			if data, err := s.gh.DownloadAsset(*asset); err == nil {
				conflictName := fmt.Sprintf("%s.conflict.%d.zip", name, time.Now().Unix())
				if err := s.gh.UploadAsset(s.rel.ID, conflictName, data); err != nil {
					s.summary.Warnings = append(s.summary.Warnings, fmt.Sprintf("%s: 云端旧版本转存失败: %v", name, err))
				} else {
					s.summary.Conflicts = append(s.summary.Conflicts,
						fmt.Sprintf("%s: 本机较新(%s)已上传，云端旧版本(%s)已保留为资产 %s", name, shortTime(l.SaveDate), shortTime(c.SaveDate), conflictName))
				}
			} else {
				s.summary.Warnings = append(s.summary.Warnings, fmt.Sprintf("%s: 云端旧版本下载失败，直接覆盖: %v", name, err))
			}
		}
		if err := s.applyUpload(name, l); err != nil {
			return err
		}
		return nil
	}

	// 云端较新 → 本机归档后下载
	target := s.prof.entryFor(name).Path
	if _, err := archiveEntryToHistory(s.cfg, s.prof, name, target); err != nil {
		return fmt.Errorf("本机旧版本归档失败: %w", err)
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := s.applyDownload(name, c); err != nil {
		return err
	}
	s.summary.Conflicts = append(s.summary.Conflicts,
		fmt.Sprintf("%s: 云端较新(%s)已下载，本机旧版本(%s)已存入本地历史", name, shortTime(c.SaveDate), shortTime(l.SaveDate)))
	return nil
}

func (s *Syncer) setBaseline(name, localSHA, cloudSHA string) {
	s.base.Saves[name] = &BaselineEntry{LocalSHA: localSHA, CloudSHA: cloudSHA}
}

func (s *Syncer) applyUpload(name string, m *SaveMeta) error {
	data, err := zipSave(entryBaseDir(s.prof.entryFor(name)), m)
	if err != nil {
		return err
	}
	assetName := name + ".zip"
	if err := s.gh.ReplaceAsset(s.rel, assetName, data); err != nil {
		return err
	}
	s.cm.Saves[name] = &CloudSave{
		Asset: assetName, AssetSHA: hashBytes(data), SaveSHA: m.SHA256,
		SaveDate: m.SaveDate, Character: m.Character, GameVersion: m.GameVersion,
		Size: m.Size, UpdatedBy: s.cfg.Device,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	s.setBaseline(name, m.SHA256, m.SHA256)
	s.cmChanged = true
	s.summary.Uploaded = append(s.summary.Uploaded, fmt.Sprintf("%s (%s, %.1fMB)", name, shortTime(m.SaveDate), float64(m.Size)/1048576))
	return nil
}

func (s *Syncer) applyDownload(name string, cs *CloudSave) error {
	asset := s.gh.AssetByName(s.rel, cs.Asset)
	if asset == nil {
		return fmt.Errorf("云端缺少资产 %s", cs.Asset)
	}
	data, err := s.gh.DownloadAsset(*asset)
	if err != nil {
		return err
	}
	if cs.AssetSHA != "" && hashBytes(data) != cs.AssetSHA {
		return fmt.Errorf("资产 %s 校验和不匹配", cs.Asset)
	}
	e := s.prof.entryFor(name)
	staged := stageDir(s.prof.SaveDir, e)
	os.RemoveAll(staged)
	if err := os.MkdirAll(staged, 0o755); err != nil {
		return err
	}
	success := false
	defer func() {
		if !success {
			os.RemoveAll(staged)
		}
	}()
	if err := unzipTo(data, staged); err != nil {
		return err
	}
	got, err := hashDir(staged)
	if err != nil {
		return err
	}
	if got.SHA256 != cs.SaveSHA {
		return fmt.Errorf("解包后内容校验失败")
	}
	target := e.Path
	if _, err := os.Stat(target); err == nil {
		if _, err := archiveEntryToHistory(s.cfg, s.prof, name, target); err != nil {
			s.summary.Warnings = append(s.summary.Warnings, fmt.Sprintf("%s: 旧版本归档失败（继续覆盖）: %v", name, err))
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
	}
	if e.IsDir {
		if err := os.Rename(staged, target); err != nil {
			return err
		}
	} else {
		// 单文件条目：把解出来的文件放到目标路径
		if err := os.Rename(filepath.Join(staged, name), target); err != nil {
			return err
		}
		os.RemoveAll(staged)
	}
	success = true
	s.setBaseline(name, cs.SaveSHA, cs.SaveSHA)
	// 注意：汇总上报由各调用方完成（正常下载→Downloaded / 保护性恢复→Restored / 冲突→Conflicts），
	// 避免同一次下载被重复计入多个列表
	return nil
}

// ---- mod 清单同步 ----

func (s *Syncer) syncMods() {
	if s.local.Mods == nil {
		return
	}
	canonical, _ := json.Marshal(s.local.Mods)
	lsha := hashBytes(canonical)
	bModsL, bModsC := s.base.ModsLocalSHA, s.base.ModsCloudSHA
	csha := s.cm.ModsSHA
	if lsha == csha {
		// 本机与云端 mod 配置完全一致（如新机器首次同步）：
		// 静默对齐基线即可，无需上传，也不产生"两边均被修改"的误报
		s.base.ModsLocalSHA, s.base.ModsCloudSHA = lsha, lsha
		return
	}
	lch := lsha != bModsL
	cch := csha != "" && csha != bModsC
	// 手动强制模式：以指定一端为准
	if s.opts.ForceUpload {
		lch = lsha != csha // 与云端不同即强制上传
		cch = false
	}
	if s.opts.ForceDownload {
		lch = false
		cch = csha != "" && csha != lsha // 与云端不同即提示（mod 文件不自动传）
	}

	switch {
	case lch:
		// 本机 mod 配置变化 → 上传清单
		old := s.cm.Mods
		if err := s.gh.ReplaceAsset(s.rel, "mods.json", canonical); err != nil {
			s.summary.Warnings = append(s.summary.Warnings, "上传 mod 清单失败: "+err.Error())
			return
		}
		s.cm.Mods = s.local.Mods
		s.cm.ModsSHA = lsha
		s.base.ModsLocalSHA, s.base.ModsCloudSHA = lsha, lsha
		s.cmChanged = true
		if cch {
			s.summary.Warnings = append(s.summary.Warnings, "两台机器 mod 配置均被修改，已以上传本机清单为准，请核对另一台机器")
			s.warnModsDiff(old)
		}
	case cch:
		// 云端 mod 配置变化 → 仅警告（mod 文件本体不自动同步）
		s.summary.Warnings = append(s.summary.Warnings, "另一台机器的 mod 配置已更新，本机未跟随（mod 文件需手动安装）")
		s.warnModsDiff(s.cm.Mods)
		s.base.ModsCloudSHA = csha
	}
}

func (s *Syncer) warnModsDiff(cloud *ModsInfo) {
	if cloud == nil || s.local.Mods == nil {
		return
	}
	ce, le := mapSet(cloud.Enabled), mapSet(s.local.Mods.Enabled)
	var onlyCloud, onlyLocal []string
	for k := range ce {
		if !le[k] {
			onlyCloud = append(onlyCloud, k)
		}
	}
	for k := range le {
		if !ce[k] {
			onlyLocal = append(onlyLocal, k)
		}
	}
	sort.Strings(onlyCloud)
	sort.Strings(onlyLocal)
	if len(onlyCloud) > 0 {
		s.summary.Warnings = append(s.summary.Warnings, "  云端已启用而本机未启用: "+strings.Join(onlyCloud, ", "))
	}
	if len(onlyLocal) > 0 {
		s.summary.Warnings = append(s.summary.Warnings, "  本机已启用而云端未启用: "+strings.Join(onlyLocal, ", "))
	}
	// 版本不一致
	var verDiff []string
	for id, v := range cloud.Installed {
		if lv, ok := s.local.Mods.Installed[id]; ok && lv != v {
			verDiff = append(verDiff, fmt.Sprintf("%s(云端 %s / 本机 %s)", id, v, lv))
		}
	}
	sort.Strings(verDiff)
	if len(verDiff) > 0 {
		s.summary.Warnings = append(s.summary.Warnings, "  mod 版本不一致: "+strings.Join(verDiff, ", "))
	}
}

func (s *Syncer) printModsStatus() {
	if s.summary.empty() {
		return
	}
	if s.local.Mods != nil {
		fmt.Printf("  ℹ mod 清单: 已安装 %d 个，已启用 %d 个\n", len(s.local.Mods.Installed), len(s.local.Mods.Enabled))
	}
}

// ---- 额外同步文件（ExtraFiles，如远行星号的 vmparams）----

// syncExtraFiles 逐个同步 prof.ExtraFiles 里列出的文件（本机不存在的跳过）
func (s *Syncer) syncExtraFiles(gameOn bool) {
	for _, rel := range s.prof.ExtraFiles {
		s.syncOneExtra(rel, gameOn)
	}
}

func (s *Syncer) syncOneExtra(rel string, gameOn bool) {
	p := filepath.Join(s.prof.GameRoot, filepath.FromSlash(rel))
	data, err := os.ReadFile(p)
	if err != nil {
		return // 本机没有该文件 → 跳过
	}
	if s.cm.Extras == nil {
		s.cm.Extras = map[string]string{}
	}
	if s.base.Extras == nil {
		s.base.Extras = map[string]*ExtraBaseline{}
	}
	assetName := extraAssetName(rel)
	lsha := hashBytes(data)
	csha := s.cm.Extras[rel]
	var bL, bC string
	if b := s.base.Extras[rel]; b != nil {
		bL, bC = b.LocalSHA, b.CloudSHA
	}
	lch := lsha != bL
	cch := csha != "" && csha != bC
	// 手动强制模式：以指定一端为准
	if s.opts.ForceUpload {
		lch = lsha != csha
		cch = false
	}
	if s.opts.ForceDownload {
		lch = false
		cch = csha != "" && csha != lsha
		gameOn = false // 用户明确手动拉取，不受"游戏运行中"限制
	}

	switch {
	case lch:
		// 本机改动 → 上传
		if err := s.gh.ReplaceAsset(s.rel, assetName, data); err != nil {
			s.summary.Warnings = append(s.summary.Warnings, "上传 "+rel+" 失败: "+err.Error())
			return
		}
		s.cm.Extras[rel] = lsha
		s.cm.VmSHA = s.cm.Extras["vmparams"]
		s.base.Extras[rel] = &ExtraBaseline{LocalSHA: lsha, CloudSHA: lsha}
		s.cmChanged = true
	case cch && !gameOn:
		// 云端改动 → 备份本机后下载覆盖
		asset := s.gh.AssetByName(s.rel, assetName)
		if asset == nil {
			return
		}
		ndata, err := s.gh.DownloadAsset(*asset)
		if err != nil {
			s.summary.Warnings = append(s.summary.Warnings, "下载 "+rel+" 失败: "+err.Error())
			return
		}
		backupCurrentExtra(s.cfg, s.prof, rel, data)
		if err := os.WriteFile(p, ndata, 0o644); err != nil {
			s.summary.Warnings = append(s.summary.Warnings, "写入 "+rel+" 失败: "+err.Error())
			return
		}
		nsha := hashBytes(ndata)
		s.cm.Extras[rel] = nsha
		s.cm.VmSHA = s.cm.Extras["vmparams"]
		s.base.Extras[rel] = &ExtraBaseline{LocalSHA: nsha, CloudSHA: nsha}
		s.cmChanged = true
		s.summary.Downloaded = append(s.summary.Downloaded, rel+"（额外配置文件）")
	case cch && gameOn:
		s.summary.Deferred = append(s.summary.Deferred, rel)
	default:
		// 一致：仅刷新基线
		s.base.Extras[rel] = &ExtraBaseline{LocalSHA: lsha, CloudSHA: csha}
	}
}

// ---- 扫描（带缓存） ----

func (s *Syncer) scanLocal() (*LocalState, error) {
	s.prof.normalize()
	ls := &LocalState{Saves: map[string]*SaveMeta{}, Extras: map[string]string{}}
	entries, err := s.prof.selectSaveEntries()
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		m, err := s.scanEntryCached(e)
		if err != nil {
			return nil, fmt.Errorf("扫描 %s 失败: %w", e.Name, err)
		}
		ls.Saves[e.Name] = m
	}
	mods, err := modsInfo(s.prof)
	if err != nil {
		return nil, err
	}
	ls.Mods = mods
	for _, rel := range s.prof.ExtraFiles {
		p := filepath.Join(s.prof.GameRoot, filepath.FromSlash(rel))
		if data, err := os.ReadFile(p); err == nil {
			sha := hashBytes(data)
			ls.Extras[rel] = sha
			if rel == "vmparams" {
				ls.VmSHA256 = sha
			}
		}
	}
	return ls, nil
}

// scanEntryCached 指纹未变时复用上次扫描结果，避免反复哈希大文件
func (s *Syncer) scanEntryCached(e saveEntry) (*SaveMeta, error) {
	fps := map[string]string{}
	if !e.IsDir {
		st, err := os.Stat(e.Path)
		if err != nil {
			return nil, err
		}
		fps[e.Name] = fingerprint(st)
	} else {
		err := filepath.WalkDir(e.Path, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			st, err := d.Info()
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(e.Path, path)
			fps[filepath.ToSlash(rel)] = fingerprint(st)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if cached := s.state.Local; cached != nil {
		if m := cached.Saves[e.Name]; m != nil && fpEqual(m.Files, fps) {
			return m, nil
		}
	}
	return scanEntryMeta(s.prof, e)
}

func fpEqual(files map[string]*FileMeta, fps map[string]string) bool {
	if len(files) != len(fps) {
		return false
	}
	for rel, fm := range files {
		if fp, ok := fps[rel]; !ok || fp != fm.FP {
			return false
		}
	}
	return true
}

// ---- 云端清单 ----

func (s *Syncer) fetchManifest() (*CloudManifest, error) {
	asset := s.gh.AssetByName(s.rel, "manifest.json")
	if asset == nil {
		return newCloudManifest(), nil
	}
	data, err := s.gh.DownloadAsset(*asset)
	if err != nil {
		return nil, fmt.Errorf("下载云端清单失败: %w", err)
	}
	cm := newCloudManifest()
	if err := json.Unmarshal(data, cm); err != nil {
		return nil, fmt.Errorf("云端清单解析失败: %w", err)
	}
	if cm.Saves == nil {
		cm.Saves = map[string]*CloudSave{}
	}
	cm.seedExtras()
	return cm, nil
}

// ---- 工具 ----

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func parseTimeOrZero(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func shortTime(s string) string {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return "?"
	}
	return t.Local().Format("01-02 15:04")
}

func backupCurrentExtra(cfg *Config, prof *GameProfile, rel string, data []byte) {
	d, err := configDir()
	if err != nil {
		return
	}
	dir := filepath.Join(d, "history", prof.Name, "extra", extraAssetName(rel), time.Now().Format("20060102-150405"))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, filepath.Base(rel)), data, 0o644)
}
