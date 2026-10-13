package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ============ mod 同步（mods-push / mods-pull）============
//
// 设计要点：
//   - mod 本体按文件夹打包为 zip，存到云端 Release 资产；
//     资产名 = mod_<目录名哈希16位>.zip（纯 ASCII，避免中文/空格资产名的兼容问题）
//   - 云端注册表 mods_files.json 记录 目录名 -> {资产名, 内容哈希, 版本, 大小}
//   - push：只上传新增或内容有变化的 mod（按规范化哈希判断），并顺带上传 mod 清单与启用列表
//   - pull：以云端为准下载覆盖本机；本机多出来的 mod 保持不动（保守不删除）
//   - pull 会按云端的 mods.json 重写本机 mods\enabled_mods.json（启用列表对齐）
//   - 游戏运行中拒绝 pull（mod 文件被占用/加载中）

const (
	modsRegistryAsset = "mods_files.json"
	modAssetPrefix    = "mod_"
)

// ModFileRec 注册表中的单个 mod 记录
type ModFileRec struct {
	Asset     string `json:"asset"`
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Version   string `json:"version"`
	UpdatedBy string `json:"updated_by"`
	UpdatedAt string `json:"updated_at"`
}

// ModsRegistry 云端 mod 注册表（资产 mods_files.json）
type ModsRegistry struct {
	Schema    int                    `json:"schema"`
	UpdatedAt string                 `json:"updated_at"`
	UpdatedBy string                 `json:"updated_by"`
	Mods      map[string]*ModFileRec `json:"mods"` // key = mod 文件夹名
}

func newModsRegistry() *ModsRegistry {
	return &ModsRegistry{Schema: 1, Mods: map[string]*ModFileRec{}}
}

// modAssetName 由 mod 文件夹名派生纯 ASCII 的资产名
func modAssetName(dirName string) string {
	h := sha256.Sum256([]byte(dirName))
	return modAssetPrefix + hex.EncodeToString(h[:])[:16] + ".zip"
}

// validModDirName 拒绝注册表里可疑的目录名（路径穿越等）
func validModDirName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	if strings.ContainsAny(name, `/\`) {
		return false
	}
	if filepath.Base(name) != name {
		return false
	}
	return true
}

// listModDirs 列出 mods 目录下的候选 mod 文件夹（跳过隐藏目录与 gssync 临时目录）
func listModDirs(modsDir string) ([]string, error) {
	ents, err := os.ReadDir(modsDir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

// scanModDir 计算 mod 目录的规范化哈希、总大小与文件数
// （排序后的 相对路径+文件哈希，与存档同步同一思路）
func scanModDir(dir string) (sha string, size int64, fileCount int, err error) {
	var rels []string
	err = filepath.WalkDir(dir, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return "", 0, 0, err
	}
	sort.Strings(rels)
	var b strings.Builder
	for _, rel := range rels {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		h, herr := hashFile(full)
		if herr != nil {
			return "", 0, 0, herr
		}
		st, serr := os.Stat(full)
		if serr != nil {
			return "", 0, 0, serr
		}
		size += st.Size()
		fileCount++
		fmt.Fprintf(&b, "%s:%s\n", rel, h)
	}
	return hashBytes([]byte(b.String())), size, fileCount, nil
}

// zipDirToBytes 把目录整体打包为 zip（保留相对路径）
func zipDirToBytes(dir string) ([]byte, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, path)
		if rerr != nil {
			return rerr
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(rels)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, rel := range rels {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		f, oerr := os.Open(full)
		if oerr != nil {
			return nil, fmt.Errorf("读取 %s 失败: %w", rel, oerr)
		}
		st, serr := f.Stat()
		if serr != nil {
			f.Close()
			return nil, serr
		}
		hdr := &zip.FileHeader{Name: rel, Method: zip.Deflate, Modified: st.ModTime()}
		w, cerr := zw.CreateHeader(hdr)
		if cerr != nil {
			f.Close()
			return nil, cerr
		}
		if _, cerr := io.Copy(w, f); cerr != nil {
			f.Close()
			return nil, cerr
		}
		f.Close()
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// modVersionOf 读取 mod 版本元数据文件里的版本号
func modVersionOf(dir, metaFile string) string {
	if metaFile == "" {
		metaFile = "mod_info.json"
	}
	data, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return "?"
	}
	var mi struct {
		Version json.RawMessage `json:"version"`
	}
	if lenientJSON(data, &mi) != nil {
		return "?"
	}
	return normalizeModVersion(mi.Version)
}

// ---- 云端注册表读写 ----

func loadModsRegistry(gh *GH, rel *Release) (*ModsRegistry, error) {
	reg := newModsRegistry()
	a := gh.AssetByName(rel, modsRegistryAsset)
	if a == nil {
		return reg, nil
	}
	data, err := gh.DownloadAsset(*a)
	if err != nil {
		return nil, fmt.Errorf("下载 mod 注册表失败: %w", err)
	}
	if err := json.Unmarshal(data, reg); err != nil {
		return nil, fmt.Errorf("mod 注册表解析失败: %w", err)
	}
	if reg.Mods == nil {
		reg.Mods = map[string]*ModFileRec{}
	}
	return reg, nil
}

func saveModsRegistry(gh *GH, rel *Release, reg *ModsRegistry, device string) error {
	reg.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	reg.UpdatedBy = device
	data, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	if err := gh.ReplaceAsset(rel, modsRegistryAsset, data); err != nil {
		return fmt.Errorf("上传 mod 注册表失败: %w", err)
	}
	return nil
}

// uploadModsList 上传 mod 清单（mods.json，含启用列表），并同步更新 manifest 与本地基线
func uploadModsList(gh *GH, rel *Release, prof *GameProfile, cfg *Config) error {
	mods, err := modsInfo(prof)
	if err != nil {
		return err
	}
	if mods == nil {
		return nil // 没有 mods 目录时不覆盖云端清单
	}
	canonical, err := json.Marshal(mods)
	if err != nil {
		return err
	}
	if a := gh.AssetByName(rel, "mods.json"); a != nil {
		if old, derr := gh.DownloadAsset(*a); derr == nil && bytes.Equal(old, canonical) {
			// 内容一致，无需重复上传
			lsha := hashBytes(canonical)
			st := loadState()
			if st.Baseline.ModsLocalSHA != lsha || st.Baseline.ModsCloudSHA != lsha {
				st.Baseline.ModsLocalSHA = lsha
				st.Baseline.ModsCloudSHA = lsha
				_ = saveState(st)
			}
			return nil
		}
	}
	if err := gh.ReplaceAsset(rel, "mods.json", canonical); err != nil {
		return fmt.Errorf("上传 mod 清单失败: %w", err)
	}
	lsha := hashBytes(canonical)

	// 同步更新云端 manifest 的 mods 字段，保证 status 显示一致
	cm := newCloudManifest()
	if a := gh.AssetByName(rel, "manifest.json"); a != nil {
		if data, derr := gh.DownloadAsset(*a); derr == nil {
			if json.Unmarshal(data, cm) == nil && cm.Saves == nil {
				cm.Saves = map[string]*CloudSave{}
			}
		}
	}
	cm.Mods = mods
	cm.ModsSHA = lsha
	cm.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	cm.UpdatedBy = cfg.Device
	if data, merr := json.MarshalIndent(cm, "", "  "); merr == nil {
		if err := gh.ReplaceAsset(rel, "manifest.json", data); err != nil {
			fmt.Println("  ⚠ 更新云端清单失败（不影响 mod 同步）:", err)
		}
	}

	st := loadState()
	st.Baseline.ModsLocalSHA = lsha
	st.Baseline.ModsCloudSHA = lsha
	return saveState(st)
}

// applyCloudEnabledList 按云端 mods.json 重写本机启用列表（启用列表对齐）
func applyCloudEnabledList(gh *GH, rel *Release, modsDir, enabledName string, missing int) (int, error) {
	if enabledName == "" {
		enabledName = "enabled_mods.json"
	}
	a := gh.AssetByName(rel, "mods.json")
	if a == nil {
		return 0, nil
	}
	data, err := gh.DownloadAsset(*a)
	if err != nil {
		return 0, fmt.Errorf("下载云端 mod 清单失败: %w", err)
	}
	var cloud ModsInfo
	if err := json.Unmarshal(data, &cloud); err != nil || len(cloud.Enabled) == 0 {
		return 0, nil
	}
	payload, err := json.MarshalIndent(map[string][]string{"enabledMods": cloud.Enabled}, "", "  ")
	if err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(modsDir, enabledName), payload, 0o644); err != nil {
		return 0, fmt.Errorf("写入启用列表失败: %w", err)
	}
	if missing > 0 {
		fmt.Printf("  ⚠ 有 %d 个 mod 未成功拉取，启用列表可能引用缺失的 mod\n", missing)
	}
	return len(cloud.Enabled), nil
}

// ---- mods-push ----

// cmdModsPush 上传 MOD：把本机新增/改动的 mod 打包上传云端
func cmdModsPush(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	prof, err := cfg.game()
	if err != nil {
		return err
	}
	token, err := getToken()
	if err != nil {
		return err
	}
	gh := NewGH(token, cfg.Repo)
	rel, err := gh.EnsureRelease(cfg.ReleaseTag)
	if err != nil {
		return fmt.Errorf("连接云端失败: %w", err)
	}
	modsDir := prof.ModsDir
	if modsDir == "" {
		return errors.New("该游戏未配置 mod 目录（mods_dir），不支持 MOD 同步")
	}
	if st, err := os.Stat(modsDir); err != nil || !st.IsDir() {
		return fmt.Errorf("mods 目录不存在: %s", modsDir)
	}
	dirs, err := listModDirs(modsDir)
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		return errors.New("mods 目录为空，没有可上传的 mod")
	}
	reg, err := loadModsRegistry(gh, rel)
	if err != nil {
		return err
	}

	fmt.Printf("检查 %d 个 mod（云端已有 %d 个）...\n", len(dirs), len(reg.Mods))
	uploaded, unchanged, failed := 0, 0, 0
	var upBytes int64
	for _, name := range dirs {
		dir := filepath.Join(modsDir, name)
		sha, size, nfiles, serr := scanModDir(dir)
		if serr != nil {
			fmt.Printf("  ⚠ 跳过 %s（读取失败: %v）\n", name, serr)
			failed++
			continue
		}
		if rec := reg.Mods[name]; rec != nil && rec.SHA256 == sha && gh.AssetByName(rel, rec.Asset) != nil {
			unchanged++
			continue
		}
		zipData, zerr := zipDirToBytes(dir)
		if zerr != nil {
			fmt.Printf("  ⚠ 跳过 %s（打包失败: %v）\n", name, zerr)
			failed++
			continue
		}
		asset := modAssetName(name)
		if uerr := gh.ReplaceAsset(rel, asset, zipData); uerr != nil {
			return fmt.Errorf("上传 %s 失败: %w", name, uerr)
		}
		reg.Mods[name] = &ModFileRec{
			Asset: asset, SHA256: sha, Size: size, Version: modVersionOf(dir, prof.ModsMetaFile),
			UpdatedBy: cfg.Device, UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		uploaded++
		upBytes += size
		fmt.Printf("  ↑ %s (v%s, %d 个文件, %.1fMB)\n", name, modVersionOf(dir, prof.ModsMetaFile), nfiles, float64(size)/1048576)
	}

	if uploaded > 0 {
		if err := saveModsRegistry(gh, rel, reg, cfg.Device); err != nil {
			return err
		}
	}
	// 顺带同步 mod 清单与启用列表，供另一台对齐
	if err := uploadModsList(gh, rel, prof, cfg); err != nil {
		fmt.Println("  ⚠ mod 清单同步失败（不影响 mod 本体上传）:", err)
	}

	if uploaded == 0 {
		fmt.Printf("完成：没有新增或改动的 mod（共 %d 个，无需同步）。\n", unchanged)
	} else {
		fmt.Printf("完成：上传 %d 个 mod（%.1fMB），未变化 %d 个。\n", uploaded, float64(upBytes)/1048576, unchanged)
	}
	if failed > 0 {
		fmt.Printf("有 %d 个 mod 读取失败被跳过，可重试。\n", failed)
	}
	return nil
}

// ---- mods-pull ----

// cmdModsPull 拉取 MOD：以云端为准下载 mod 覆盖本机（旧内容直接替换；游戏运行中拒绝）
func cmdModsPull(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	prof, err := cfg.game()
	if err != nil {
		return err
	}
	if gameRunning(prof.ProcessNames) {
		return errors.New("检测到游戏正在运行，请先退出游戏再拉取 mod，避免文件被占用或损坏")
	}
	token, err := getToken()
	if err != nil {
		return err
	}
	gh := NewGH(token, cfg.Repo)
	rel, err := gh.EnsureRelease(cfg.ReleaseTag)
	if err != nil {
		return fmt.Errorf("连接云端失败: %w", err)
	}
	modsDir := prof.ModsDir
	if modsDir == "" {
		return errors.New("该游戏未配置 mod 目录（mods_dir），不支持 MOD 同步")
	}
	if err := os.MkdirAll(modsDir, 0o755); err != nil {
		return err
	}
	// 清理遗留临时目录
	if ents, err := os.ReadDir(modsDir); err == nil {
		for _, e := range ents {
			if e.IsDir() && strings.HasPrefix(e.Name(), ".gssync_tmp_") {
				os.RemoveAll(filepath.Join(modsDir, e.Name()))
			}
		}
	}

	reg, err := loadModsRegistry(gh, rel)
	if err != nil {
		return err
	}
	if len(reg.Mods) == 0 {
		return errors.New("云端还没有 mod 数据：请先在装有 mod 的电脑上执行【上传MOD】")
	}

	fmt.Printf("云端共 %d 个 mod，开始核对本机...\n", len(reg.Mods))
	pulled, unchanged, missingLocalCheck := 0, 0, 0
	for _, name := range sortedModNames(reg) {
		if !validModDirName(name) {
			fmt.Printf("  ⚠ 跳过非法目录名: %q\n", name)
			missingLocalCheck++
			continue
		}
		rec := reg.Mods[name]
		dir := filepath.Join(modsDir, name)
		if _, serr := os.Stat(dir); serr == nil {
			if sha, _, _, herr := scanModDir(dir); herr == nil && sha == rec.SHA256 {
				unchanged++
				continue
			}
		}
		asset := gh.AssetByName(rel, rec.Asset)
		if asset == nil {
			fmt.Printf("  ⚠ %s: 云端缺少资产 %s，跳过\n", name, rec.Asset)
			missingLocalCheck++
			continue
		}
		data, derr := gh.DownloadAsset(*asset)
		if derr != nil {
			fmt.Printf("  ⚠ %s: 下载失败: %v\n", name, derr)
			missingLocalCheck++
			continue
		}
		staged := filepath.Join(modsDir, ".gssync_tmp_"+name)
		os.RemoveAll(staged)
		if err := os.MkdirAll(staged, 0o755); err != nil {
			fmt.Printf("  ⚠ %s: 创建临时目录失败: %v\n", name, err)
			missingLocalCheck++
			continue
		}
		if err := unzipTo(data, staged); err != nil {
			os.RemoveAll(staged)
			fmt.Printf("  ⚠ %s: 解包失败: %v\n", name, err)
			missingLocalCheck++
			continue
		}
		if got, _, _, herr := scanModDir(staged); herr != nil || got != rec.SHA256 {
			os.RemoveAll(staged)
			fmt.Printf("  ⚠ %s: 解包后内容校验失败，跳过\n", name)
			missingLocalCheck++
			continue
		}
		os.RemoveAll(dir)
		if err := os.Rename(staged, dir); err != nil {
			os.RemoveAll(staged)
			fmt.Printf("  ⚠ %s: 就位失败: %v\n", name, err)
			missingLocalCheck++
			continue
		}
		pulled++
		fmt.Printf("  ↓ %s (v%s, %.1fMB)\n", name, rec.Version, float64(rec.Size)/1048576)
	}

	enabled, eerr := applyCloudEnabledList(gh, rel, modsDir, prof.ModsEnabledFile, missingLocalCheck)
	if eerr != nil {
		fmt.Println("  ⚠ 启用列表同步失败:", eerr)
	}

	// 刷新本地 mod 清单基线，避免下次普通同步误报
	if local, lerr := modsInfo(prof); lerr == nil && local != nil {
		if canonical, merr := json.Marshal(local); merr == nil {
			lsha := hashBytes(canonical)
			st := loadState()
			st.Baseline.ModsLocalSHA = lsha
			st.Baseline.ModsCloudSHA = lsha
			_ = saveState(st)
		}
	}

	if pulled == 0 {
		fmt.Printf("完成：本机 mod 已与云端一致（%d 个未变化）。\n", unchanged)
	} else {
		fmt.Printf("完成：下载 %d 个 mod，未变化 %d 个。\n", pulled, unchanged)
	}
	if enabled > 0 {
		fmt.Printf("启用列表已按云端对齐：共启用 %d 个 mod。\n", enabled)
	}
	return nil
}

func sortedModNames(reg *ModsRegistry) []string {
	out := make([]string, 0, len(reg.Mods))
	for k := range reg.Mods {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}