package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeMod 在 mods 目录下创建一个测试 mod
func writeMod(t *testing.T, modsDir, name, id, version, marker string) {
	t.Helper()
	writeFile(t, filepath.Join(modsDir, name, "mod_info.json"),
		`{"id":"`+id+`","name":"`+name+`","version":"`+version+`"}`)
	writeFile(t, filepath.Join(modsDir, name, "data", "content.txt"), marker)
}

func fetchModsRegistry(t *testing.T, m *mockGH) *ModsRegistry {
	t.Helper()
	a := m.assetByName(modsRegistryAsset)
	if a == nil {
		t.Fatal("云端缺少 mod 注册表")
	}
	reg := newModsRegistry()
	if err := json.Unmarshal(a.data, reg); err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestModsPushPull：mod 上传（新增/改动判断）→ 另一台下载 + 启用列表对齐
func TestModsPushPull(t *testing.T) {
	mock := newMockGH()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	os.Setenv("GSSYNC_API_BASE", srv.URL+"/api")
	os.Setenv("GSSYNC_UPLOAD_BASE", srv.URL+"/uploads")

	// ---- 设备 A：两个 mod（含中文目录名）→ push ----
	rootA := t.TempDir()
	modsA := filepath.Join(rootA, "mods")
	writeMod(t, modsA, "Nexerelin", "nexerelin", "0.12.1b", "execerelin-v1")
	writeMod(t, modsA, "航母现代化", "carrier_modern", "1.1.5", "carrier-v1")
	writeFile(t, filepath.Join(modsA, "enabled_mods.json"), `{"enabledMods":["nexerelin","carrier_modern"]}`)
	homeA := t.TempDir()
	setupDevice(t, mock, homeA, rootA, "PC-A")

	if err := cmdModsPush(nil); err != nil {
		t.Fatalf("mods-push 失败: %v", err)
	}
	reg := fetchModsRegistry(t, mock)
	if len(reg.Mods) != 2 {
		t.Fatalf("云端注册表应有 2 个 mod，实际 %d", len(reg.Mods))
	}
	if mock.assetByName("mods.json") == nil {
		t.Fatal("云端缺少 mods.json（启用列表）")
	}

	// 二次 push：无变化时资产数量不变
	before := len(mock.snapshotNames())
	if err := cmdModsPush(nil); err != nil {
		t.Fatal(err)
	}
	if len(mock.snapshotNames()) != before {
		t.Fatalf("无变化时资产数量不应变化: %v", mock.snapshotNames())
	}

	// ---- 设备 B：空 mods 目录 → pull ----
	rootB := t.TempDir()
	homeB := t.TempDir()
	setupDevice(t, mock, homeB, rootB, "PC-B")
	if err := cmdModsPull(nil); err != nil {
		t.Fatalf("mods-pull 失败: %v", err)
	}
	modsB := filepath.Join(rootB, "mods")
	for _, name := range []string{"Nexerelin", "航母现代化"} {
		shaA, _, _, errA := scanModDir(filepath.Join(modsA, name))
		shaB, _, _, errB := scanModDir(filepath.Join(modsB, name))
		if errA != nil || errB != nil || shaA != shaB {
			t.Fatalf("%s 内容应一致: A=%s B=%s errA=%v errB=%v", name, shaA, shaB, errA, errB)
		}
	}
	data, err := os.ReadFile(filepath.Join(modsB, "enabled_mods.json"))
	if err != nil || !strings.Contains(string(data), "nexerelin") || !strings.Contains(string(data), "carrier_modern") {
		t.Fatalf("启用列表未按云端对齐: %s err=%v", data, err)
	}

	// ---- 设备 A 改动一个 mod → push 只更新它的哈希 ----
	writeFile(t, filepath.Join(modsA, "Nexerelin", "data", "content.txt"), "execerelin-v2")
	os.Setenv("GSSYNC_HOME", homeA)
	if err := cmdModsPush(nil); err != nil {
		t.Fatal(err)
	}
	reg2 := fetchModsRegistry(t, mock)
	shaNew, _, _, _ := scanModDir(filepath.Join(modsA, "Nexerelin"))
	if reg2.Mods["Nexerelin"].SHA256 != shaNew {
		t.Fatal("注册表未更新为改动后的哈希")
	}

	// ---- 设备 B 再 pull → 内容更新 ----
	os.Setenv("GSSYNC_HOME", homeB)
	if err := cmdModsPull(nil); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(modsB, "Nexerelin", "data", "content.txt"))
	if string(got) != "execerelin-v2" {
		t.Fatalf("pull 后内容应为 v2，实际 %q", got)
	}
}

// TestModsPullNoData：云端还没有 mod 数据时应给出明确提示
func TestModsPullNoData(t *testing.T) {
	mock := newMockGH()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	os.Setenv("GSSYNC_API_BASE", srv.URL+"/api")
	os.Setenv("GSSYNC_UPLOAD_BASE", srv.URL+"/uploads")

	root := t.TempDir()
	home := t.TempDir()
	setupDevice(t, mock, home, root, "PC-X")
	err := cmdModsPull(nil)
	if err == nil || !strings.Contains(err.Error(), "云端还没有 mod 数据") {
		t.Fatalf("应提示云端无 mod 数据，实际: %v", err)
	}
}