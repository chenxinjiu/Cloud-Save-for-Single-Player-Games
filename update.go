package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ============ 应用自更新（打开 GUI 时自动检查；CLI: gssync update）============
//
// 机制：
//   - 应用仓库（默认 chenxinjiu/Cloud-Save-for-Single-Player-Games）根目录的 version.txt 记录"当前已发布的版本号"
//   - 启动时对比本地版本与云端版本：只有云端更高才更新（相同或更低一律不动，保护本地开发版）
//   - 更新对象 = 本应用所在目录下的 gssync.exe / gssync-gui.exe（只更新本地已存在的文件）
//   - 下载后按 git blob 哈希校验完整性；替换时先把旧文件改名为 .old-<时间戳>
//     （Windows 允许改名运行中的 exe，但不允许覆盖），下次启动时自动清理 .old-* 文件
//
// 发布新版本的清单（详见 HANDOFF 第 6 节）：
//   ① 改 main.go 的 version ② 更新 version.txt 为同一版本号
//   ③ 重新构建两个 exe ④ 一起提交并推送

const defaultAppRepo = "chenxinjiu/Cloud-Save-for-Single-Player-Games"

// updateFileNames 需要随版本更新的文件（仓库根目录 → 本地 exe 目录）
var updateFileNames = []string{"gssync.exe", "gssync-gui.exe"}

// UpdateResult 更新检查结果
type UpdateResult struct {
	LocalVersion  string
	RemoteVersion string
	Updated       bool
	Skipped       []string // 本地不存在、未更新的文件
}

func appRepoOf(cfg *Config) string {
	if cfg != nil && strings.TrimSpace(cfg.AppRepo) != "" {
		return strings.TrimSpace(cfg.AppRepo)
	}
	return defaultAppRepo
}

// runSelfUpdate 检查并（可选地）应用更新
//   - cfg：配置（用于取应用仓库地址；为 nil 时用默认仓库）
//   - checkOnly：只检查不下载
//   - targetDir：exe 所在目录（通常取运行中 exe 的目录）
//   - logf：输出回调（CLI 用 fmt.Printf，GUI 用静默或收集）
func runSelfUpdate(cfg *Config, checkOnly bool, targetDir string, logf func(string, ...any)) (*UpdateResult, error) {
	token, err := getToken()
	if err != nil {
		return nil, err
	}
	repo := appRepoOf(cfg)
	gh := NewGH(token, repo)
	res := &UpdateResult{LocalVersion: version}

	vb, err := gh.downloadRepoFile(repo, "version.txt")
	if err != nil {
		if isNotFound(err) {
			res.RemoteVersion = version
			logf("云端未提供版本信息（version.txt 不存在），跳过更新检查")
			return res, nil
		}
		return nil, fmt.Errorf("获取云端版本失败: %w", err)
	}
	remote := strings.TrimSpace(strings.SplitN(string(vb), "\n", 2)[0])
	res.RemoteVersion = remote
	if remote == "" || compareVersions(remote, version) <= 0 {
		logf("已是最新版本 v%s", version)
		return res, nil
	}
	logf("发现新版本 v%s（当前 v%s）", remote, version)
	if checkOnly {
		return res, nil
	}

	for _, name := range updateFileNames {
		target := filepath.Join(targetDir, name)
		if _, serr := os.Stat(target); serr != nil {
			res.Skipped = append(res.Skipped, name)
			continue
		}
		fi, ierr := gh.getRepoFileInfo(repo, name)
		if ierr != nil {
			return res, fmt.Errorf("获取 %s 信息失败: %w", name, ierr)
		}
		data, derr := gh.downloadRepoFile(repo, name)
		if derr != nil {
			return res, fmt.Errorf("下载 %s 失败: %w", name, derr)
		}
		if fi.SHA != "" && gitBlobSHA(data) != fi.SHA {
			return res, fmt.Errorf("%s 校验失败（下载不完整，已放弃本次更新）", name)
		}
		if rerr := replaceFile(target, data); rerr != nil {
			return res, fmt.Errorf("替换 %s 失败: %w", name, rerr)
		}
		logf("已更新 %s（%.1fMB）", name, float64(len(data))/1048576)
	}
	res.Updated = true
	logf("更新完成：重启应用后生效（v%s）", remote)
	return res, nil
}

// replaceFile 就地替换文件：先把原文件改名为 .old-<时间戳>，再写入新内容
// （运行中的 exe 无法被覆盖，但允许改名；改名遗留的文件由 cleanupOldExes 清理）
func replaceFile(target string, data []byte) error {
	old := target + ".old-" + time.Now().Format("20060102-150405")
	renamed := false
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, old); err == nil {
			renamed = true
		} else if werr := os.WriteFile(target, data, 0o755); werr != nil {
			return fmt.Errorf("改名失败(%v) 且直接覆盖失败: %w", err, werr)
		} else {
			return nil
		}
	}
	if err := os.WriteFile(target, data, 0o755); err != nil {
		if renamed {
			os.Rename(old, target) // 写失败回滚
		}
		return err
	}
	return nil
}

// cleanupOldExes 清理上一次更新遗留的 .old-* 文件（此时旧进程已退出，可删除）
func cleanupOldExes(dir string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), ".old-") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}

// gitBlobSHA 计算内容的 git blob 哈希（与 GitHub contents API 返回的 sha 对应）
func gitBlobSHA(data []byte) string {
	h := sha1.New()
	fmt.Fprintf(h, "blob %d\x00", len(data))
	h.Write(data)
	return hex.EncodeToString(h.Sum(nil))
}

// compareVersions 比较 x.y.z 版本号：>0 表示 a 比 b 新
func compareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] > pb[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func versionParts(s string) [3]int {
	var out [3]int
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "-+"); i >= 0 {
		s = s[:i]
	}
	for i, p := range strings.SplitN(s, ".", 3) {
		if i > 2 {
			break
		}
		n, _ := strconv.Atoi(strings.TrimFunc(p, func(r rune) bool { return r < '0' || r > '9' }))
		out[i] = n
	}
	return out
}

// ---- 仓库文件读取（contents API，支持私有仓库） ----

type repoFileInfo struct {
	Name string `json:"name"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

// getRepoFileInfo 获取仓库内文件的元数据（blob sha / 大小）
func (g *GH) getRepoFileInfo(repo, path string) (*repoFileInfo, error) {
	var fi repoFileInfo
	if err := g.api("GET", "/repos/"+repo+"/contents/"+path, nil, &fi); err != nil {
		return nil, err
	}
	return &fi, nil
}

// downloadRepoFile 下载仓库内文件的原始内容
func (g *GH) downloadRepoFile(repo, path string) ([]byte, error) {
	resp, err := g.do("GET", apiBase()+"/repos/"+repo+"/contents/"+path,
		map[string]string{"Accept": "application/vnd.github.raw"}, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, &apiError{Status: resp.StatusCode, Message: string(b)}
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}