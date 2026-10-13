package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setAppRepoFiles 配置 mock 应用仓库：version.txt + 若干文件
func setAppRepoFiles(mock *mockGH, version string, files map[string][]byte) {
	files["version.txt"] = []byte(version + "\n")
	mock.appFiles = files
}

// TestSelfUpdate：版本比较、下载替换、.old 备份与清理、--check 语义
func TestSelfUpdate(t *testing.T) {
	mock := newMockGH()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	os.Setenv("GSSYNC_API_BASE", srv.URL+"/api")
	os.Setenv("GSSYNC_UPLOAD_BASE", srv.URL+"/uploads")

	setAppRepoFiles(mock, "9.9.9", map[string][]byte{
		"gssync.exe":     []byte("NEW-CLI-EXE"),
		"gssync-gui.exe": []byte("NEW-GUI-EXE"),
	})

	// 模拟已部署的目录（旧版本文件）
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "gssync.exe"), "OLD-CLI")
	writeFile(t, filepath.Join(dir, "gssync-gui.exe"), "OLD-GUI")

	home := t.TempDir()
	root := t.TempDir()
	cfg := setupDevice(t, mock, home, root, "PC-UPD")
	cfg.AppRepo = "tester/app-repo"
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}

	var logs []string
	logf := func(f string, a ...any) { logs = append(logs, f) }

	// ---- 云端版本更高 → 下载替换 ----
	res, err := runSelfUpdate(cfg, false, dir, logf)
	if err != nil {
		t.Fatalf("更新失败: %v (logs=%v)", err, logs)
	}
	if !res.Updated || res.RemoteVersion != "9.9.9" {
		t.Fatalf("应更新到 9.9.9: %+v", res)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "gssync.exe")); string(got) != "NEW-CLI-EXE" {
		t.Fatalf("gssync.exe 未更新: %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "gssync-gui.exe")); string(got) != "NEW-GUI-EXE" {
		t.Fatalf("gssync-gui.exe 未更新: %q", got)
	}
	ents, _ := os.ReadDir(dir)
	oldCount := 0
	for _, e := range ents {
		if strings.Contains(e.Name(), ".old-") {
			oldCount++
		}
	}
	if oldCount != 2 {
		t.Fatalf("应保留 2 个 .old-* 备份，实际 %d", oldCount)
	}
	cleanupOldExes(dir)
	ents2, _ := os.ReadDir(dir)
	for _, e := range ents2 {
		if strings.Contains(e.Name(), ".old-") {
			t.Fatal("cleanupOldExes 未清理干净")
		}
	}

	// ---- 版本相同 → 不动 ----
	setAppRepoFiles(mock, version, map[string][]byte{})
	writeFile(t, filepath.Join(dir, "gssync.exe"), "SAME-CLI")
	res2, err := runSelfUpdate(cfg, false, dir, logf)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Updated {
		t.Fatal("版本相同不应更新")
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "gssync.exe")); string(got) != "SAME-CLI" {
		t.Fatal("版本相同不应改动文件")
	}

	// ---- --check：云端更高也只检查、不下载 ----
	setAppRepoFiles(mock, "9.9.9", map[string][]byte{"gssync.exe": []byte("X")})
	res3, err := runSelfUpdate(cfg, true, dir, logf)
	if err != nil {
		t.Fatal(err)
	}
	if res3.Updated || res3.RemoteVersion != "9.9.9" {
		t.Fatalf("--check 不应下载: %+v", res3)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "gssync.exe")); string(got) != "SAME-CLI" {
		t.Fatal("--check 不应改动文件")
	}

	// ---- 云端没有 version.txt → 静默跳过 ----
	mock.appFiles = map[string][]byte{}
	res4, err := runSelfUpdate(cfg, false, dir, logf)
	if err != nil {
		t.Fatalf("无 version.txt 时不应报错: %v", err)
	}
	if res4.Updated {
		t.Fatal("无版本信息不应更新")
	}
}