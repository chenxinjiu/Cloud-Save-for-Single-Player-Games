package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ============ 存档识别规则（支持任意单机游戏）============
//
// 一个"存档条目"（saveEntry）要么是一个目录、要么是一个文件：
//   - subdir：存档目录下，每个匹配 Include 的子目录 = 一个存档（远行星号即此模式）
//   - file：存档目录下，每个匹配 Include 的文件 = 一个存档（如 *.sav）
//   - single：整个存档目录 = 一个存档
// Include/Exclude 是作用在"条目名"上的 glob（大小写不敏感）。

// singleSaveName 是 single 模式下这个"整体存档"的固定名字
const singleSaveName = "save"

type saveEntry struct {
	Name  string // 条目名（云端资产名/基线 key）
	Path  string // 真实路径
	IsDir bool
}

// matchAny 命中任一 Include 且未命中任一 Exclude 才算通过
func matchAny(name string, include, exclude []string) bool {
	hit := false
	for _, pat := range include {
		if globMatchFold(pat, name) {
			hit = true
			break
		}
	}
	if !hit {
		return false
	}
	for _, pat := range exclude {
		if globMatchFold(pat, name) {
			return false
		}
	}
	return true
}

func globMatchFold(pattern, name string) bool {
	if ok, err := filepath.Match(pattern, name); err == nil && ok {
		return true
	}
	if ok, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(name)); err == nil && ok {
		return true
	}
	return false
}

// selectSaveEntries 按 prof 的规则枚举本机存档条目
func (p *GameProfile) selectSaveEntries() ([]saveEntry, error) {
	if p.SaveDir == "" {
		return nil, fmt.Errorf("未配置存档目录（save_dir）")
	}
	switch p.SaveMode {
	case SaveModeSingle:
		return []saveEntry{{Name: singleSaveName, Path: p.SaveDir, IsDir: true}}, nil
	case SaveModeFile:
		ents, err := os.ReadDir(p.SaveDir)
		if err != nil {
			return nil, err
		}
		var out []saveEntry
		for _, e := range ents {
			if e.IsDir() || !matchAny(e.Name(), p.Include, p.Exclude) {
				continue
			}
			out = append(out, saveEntry{Name: e.Name(), Path: filepath.Join(p.SaveDir, e.Name())})
		}
		sortEntries(out)
		return out, nil
	default: // subdir
		ents, err := os.ReadDir(p.SaveDir)
		if err != nil {
			return nil, err
		}
		var out []saveEntry
		for _, e := range ents {
			if !e.IsDir() || !matchAny(e.Name(), p.Include, p.Exclude) {
				continue
			}
			out = append(out, saveEntry{Name: e.Name(), Path: filepath.Join(p.SaveDir, e.Name()), IsDir: true})
		}
		sortEntries(out)
		return out, nil
	}
}

func sortEntries(es []saveEntry) {
	sort.Slice(es, func(i, j int) bool { return es[i].Name < es[j].Name })
}

// entryFor 由名称复原条目（云端/基线里的名字，本机可能已不存在）
func (p *GameProfile) entryFor(name string) saveEntry {
	if p.SaveMode == SaveModeSingle {
		return saveEntry{Name: singleSaveName, Path: p.SaveDir, IsDir: true}
	}
	if p.SaveMode == SaveModeFile {
		return saveEntry{Name: name, Path: filepath.Join(p.SaveDir, name)}
	}
	return saveEntry{Name: name, Path: filepath.Join(p.SaveDir, name), IsDir: true}
}

// entryBaseDir 扫描/打包时，用来把 SaveMeta.Files 的相对路径拼回真实路径的基准目录
func entryBaseDir(e saveEntry) string {
	if e.IsDir {
		return e.Path
	}
	return filepath.Dir(e.Path)
}

// stageDir 下载解包时的临时目录：single 模式（条目就是存档目录本身）要放到上一级，避免自我嵌套
func stageDir(saveDir string, e saveEntry) string {
	base := saveDir
	if filepath.Clean(e.Path) == filepath.Clean(saveDir) {
		base = filepath.Dir(saveDir)
	}
	return filepath.Join(base, ".gssync_tmp_"+e.Name)
}

// resolveSaveTime 计算存档时间（冲突判定用）。
//   - descriptor：解析指定文件里的字段（远行星号用 descriptor.xml / campaign.xml）
//   - mtime：取条目内最新文件的修改时间
func resolveSaveTime(p *GameProfile, e saveEntry, m *SaveMeta) {
	if p.SaveTimeSource == TimeDescriptor {
		base := entryBaseDir(e)
		desc := filepath.Join(base, p.DescriptorFile)
		if _, err := os.Stat(desc); err == nil {
			parseDescriptor(m, desc, filepath.Join(base, "campaign.xml"))
		}
		return
	}
	if t, ok := newestModTime(e); ok {
		m.SaveDate = t.UTC().Format(time.RFC3339)
	}
}

func newestModTime(e saveEntry) (time.Time, bool) {
	if !e.IsDir {
		if st, err := os.Stat(e.Path); err == nil {
			return st.ModTime(), true
		}
		return time.Time{}, false
	}
	var newest time.Time
	_ = filepath.WalkDir(e.Path, func(_ string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if st, ierr := d.Info(); ierr == nil && st.ModTime().After(newest) {
			newest = st.ModTime()
		}
		return nil
	})
	if newest.IsZero() {
		return time.Time{}, false
	}
	return newest, true
}

// extraAssetName 额外同步文件对应的云端资产名（vmparams 保持原名以兼容旧云端）
func extraAssetName(rel string) string {
	if rel == "vmparams" {
		return "vmparams"
	}
	return "extra_" + strings.NewReplacer("/", "__", "\\", "__").Replace(filepath.ToSlash(rel))
}
