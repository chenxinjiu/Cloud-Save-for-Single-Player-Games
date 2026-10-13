package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// FileMeta 单文件元数据
type FileMeta struct {
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	FP     string `json:"fp"` // "size:mtimeNano" 快速指纹，用于免哈希缓存
}

// SaveMeta 一个存档文件夹的元数据
type SaveMeta struct {
	Name        string               `json:"name"`
	Files       map[string]*FileMeta `json:"files"` // 相对路径 -> 元数据
	SHA256      string               `json:"sha256"`
	SaveDate    string               `json:"save_date"` // RFC3339；common 为空
	Character   string               `json:"character"`
	GameVersion string               `json:"game_version"`
	Size        int64                `json:"size"`
}

// ModsInfo mod 清单
type ModsInfo struct {
	Installed map[string]string `json:"installed"` // id -> version
	Enabled   []string          `json:"enabled"`
}

// LocalState 本地扫描结果
type LocalState struct {
	Saves    map[string]*SaveMeta `json:"saves"`
	Mods     *ModsInfo            `json:"mods,omitempty"`
	VmSHA256 string               `json:"vm_sha256,omitempty"` // 兼容旧版：等价于 Extras["vmparams"]
	Extras   map[string]string    `json:"extras,omitempty"`    // 额外同步文件 rel -> sha256
}

// ---- 哈希与扫描 ----

func hashBytes(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashDir 扫描一个目录并计算完整哈希（不含存档时间/角色，由 resolveSaveTime 补）
func hashDir(dir string) (*SaveMeta, error) {
	m := &SaveMeta{Files: map[string]*FileMeta{}}
	var rels []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(rels)
	var hashInput strings.Builder
	for _, rel := range rels {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		st, err := os.Stat(full)
		if err != nil {
			return nil, err
		}
		sha, err := hashFile(full)
		if err != nil {
			return nil, err
		}
		m.Files[rel] = &FileMeta{Size: st.Size(), SHA256: sha, FP: fingerprint(st)}
		hashInput.WriteString(rel)
		hashInput.WriteString(":")
		hashInput.WriteString(sha)
		hashInput.WriteString("\n")
		m.Size += st.Size()
	}
	m.SHA256 = hashBytes([]byte(hashInput.String()))
	return m, nil
}

// hashFileEntry 单个文件作为一个存档条目（file 模式）
func hashFileEntry(path string) (*SaveMeta, error) {
	name := filepath.Base(path)
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	sha, err := hashFile(path)
	if err != nil {
		return nil, err
	}
	m := &SaveMeta{Files: map[string]*FileMeta{}}
	m.Files[name] = &FileMeta{Size: st.Size(), SHA256: sha, FP: fingerprint(st)}
	m.Size = st.Size()
	m.SHA256 = hashBytes([]byte(name + ":" + sha + "\n"))
	return m, nil
}

// scanEntryMeta 扫描一个存档条目（目录或单文件）并解析存档时间
func scanEntryMeta(p *GameProfile, e saveEntry) (*SaveMeta, error) {
	var m *SaveMeta
	var err error
	if e.IsDir {
		m, err = hashDir(e.Path)
	} else {
		m, err = hashFileEntry(e.Path)
	}
	if err != nil {
		return nil, err
	}
	m.Name = e.Name
	resolveSaveTime(p, e, m)
	return m, nil
}

func fingerprint(st os.FileInfo) string {
	return fmt.Sprintf("%d:%d", st.Size(), st.ModTime().UnixNano())
}

// descriptor.xml 结构（只取需要的字段）
type xmlDescriptor struct {
	CharacterName string `xml:"characterName"`
	SaveDate      string `xml:"saveDate"`
	GameVersion   string `xml:"gameVersion"`
}

func parseDescriptor(m *SaveMeta, descPath, campaignPath string) {
	m.Character = "?"
	var d xmlDescriptor
	if data, err := os.ReadFile(descPath); err == nil {
		data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
		if xml.Unmarshal(data, &d) == nil {
			m.Character = d.CharacterName
			m.GameVersion = d.GameVersion
			if t, err := parseSaveDate(d.SaveDate); err == nil {
				m.SaveDate = t.UTC().Format(time.RFC3339)
			}
		}
	}
	// 日期解析失败时退化为 campaign.xml 的修改时间
	if m.SaveDate == "" {
		if st, err := os.Stat(campaignPath); err == nil {
			m.SaveDate = st.ModTime().UTC().Format(time.RFC3339)
		}
	}
}

func parseSaveDate(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"2006-01-02 15:04:05.999 MST", "2006-01-02 15:04:05 MST", time.RFC3339} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析存档时间: %q", s)
}

// ---- 压缩打包 / 解包 ----

// zipSave 将存档文件夹打包为 zip（扁平结构）
func zipSave(dir string, m *SaveMeta) ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	rels := make([]string, 0, len(m.Files))
	for rel := range m.Files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		f, err := os.Open(full)
		if err != nil {
			return nil, fmt.Errorf("读取 %s 失败(游戏可能正在写入): %w", rel, err)
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, err
		}
		hdr := &zip.FileHeader{Name: rel, Method: zip.Deflate, Modified: st.ModTime()}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			f.Close()
			return nil, err
		}
		if _, err := io.Copy(w, f); err != nil {
			f.Close()
			return nil, err
		}
		f.Close()
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unzipTo 解压到目标目录（带路径安全检查）
func unzipTo(data []byte, dest string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	destAbs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	var total int64
	for _, f := range zr.File {
		name := filepath.FromSlash(f.Name)
		if strings.Contains(name, "..") || filepath.IsAbs(name) || strings.HasPrefix(name, `\`) {
			return fmt.Errorf("zip 内含非法路径: %s", f.Name)
		}
		target := filepath.Join(destAbs, name)
		if !strings.HasPrefix(target, destAbs+string(os.PathSeparator)) && target != destAbs {
			return fmt.Errorf("zip 内含非法路径: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		total += int64(f.UncompressedSize64)
		if total > 1<<30 {
			rc.Close()
			return fmt.Errorf("解压内容超过 1GB，中止")
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		rc.Close()
		out.Close()
		if err != nil {
			return err
		}
		if !f.Modified.IsZero() {
			os.Chtimes(target, f.Modified, f.Modified)
		}
	}
	return nil
}

// ---- mod 清单 ----

// modsInfo 读取 mod 清单。prof.ModsDir 为空表示该游戏不启用 mod 同步（返回 nil）。
func modsInfo(prof *GameProfile) (*ModsInfo, error) {
	if prof == nil || prof.ModsDir == "" {
		return nil, nil
	}
	modsDir := prof.ModsDir
	enabledName := prof.ModsEnabledFile
	if enabledName == "" {
		enabledName = "enabled_mods.json"
	}
	metaName := prof.ModsMetaFile
	if metaName == "" {
		metaName = "mod_info.json"
	}
	info := &ModsInfo{Installed: map[string]string{}}

	// 启用列表（远行星号 0.97+ 位于 mods 目录）
	var enabled struct {
		EnabledMods []string `json:"enabledMods"`
	}
	if data, err := os.ReadFile(filepath.Join(modsDir, enabledName)); err == nil {
		if lenientJSON(data, &enabled) == nil {
			info.Enabled = enabled.EnabledMods
		}
	}

	entries, err := os.ReadDir(modsDir)
	if err != nil {
		if os.IsNotExist(err) {
			// mods 目录不存在（如游戏未安装 mod 或路径不完整）：
			// 返回 nil 表示"无 mod 信息"，避免被当成"0 个 mod"上传覆盖云端清单
			return nil, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(modsDir, e.Name(), metaName))
		if err != nil {
			continue
		}
		var mi struct {
			ID      string          `json:"id"`
			Name    string          `json:"name"`
			Version json.RawMessage `json:"version"`
		}
		if lenientJSON(data, &mi) != nil {
			continue
		}
		id := mi.ID
		if id == "" {
			id = e.Name()
		}
		info.Installed[id] = normalizeModVersion(mi.Version)
	}
	return info, nil
}

// lenientJSON 容错解析：Starsector 的 mod_info.json 常带尾逗号，标准 JSON 会解析失败
func lenientJSON(data []byte, v any) error {
	trimmed := bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})
	if err := json.Unmarshal(trimmed, v); err == nil {
		return nil
	}
	fixed := trailingCommaRe.ReplaceAll(trimmed, []byte("$1"))
	return json.Unmarshal(fixed, v)
}

var trailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)

// mod 版本可能是字符串或 {major,minor,patch} 对象
func normalizeModVersion(raw json.RawMessage) string {
	if len(raw) == 0 {
		return "?"
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return "?"
		}
		return s
	}
	var obj map[string]any
	if json.Unmarshal(raw, &obj) != nil {
		return "?"
	}
	parts := []string{}
	for _, k := range []string{"major", "minor", "patch"} {
		if v, ok := obj[k]; ok && v != nil {
			parts = append(parts, fmt.Sprintf("%v", v))
		}
	}
	if len(parts) == 0 {
		return "?"
	}
	return strings.Join(parts, ".")
}

func modsEqual(a, b *ModsInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	if len(a.Enabled) != len(b.Enabled) || len(a.Installed) != len(b.Installed) {
		return false
	}
	for k, v := range a.Installed {
		if b.Installed[k] != v {
			return false
		}
	}
	ae, be := mapSet(a.Enabled), mapSet(b.Enabled)
	for k := range ae {
		if !be[k] {
			return false
		}
	}
	return true
}

func mapSet(list []string) map[string]bool {
	m := make(map[string]bool, len(list))
	for _, s := range list {
		m[s] = true
	}
	return m
}

// ---- 游戏进程检测 ----

func gameRunning(names []string) bool {
	if runtime.GOOS != "windows" {
		return false
	}
	for _, n := range names {
		out, err := exec.Command("tasklist", "/FI", "IMAGENAME eq "+n, "/FO", "CSV", "/NH").Output()
		if err == nil && strings.Contains(strings.ToLower(string(out)), strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// ---- 目录复制（历史归档用） ----

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
