package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// ---- 存档识别规则 ----

func TestSelectSaveEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "save_A"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "junk"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "slot1.sav"), "x")
	writeFile(t, filepath.Join(dir, "readme.txt"), "y")

	// subdir：只认 save_*
	p := &GameProfile{Name: "g", Preset: PresetGeneric, SaveDir: dir, SaveMode: SaveModeSubdir, Include: []string{"save_*"}}
	p.normalize()
	es, err := p.selectSaveEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].Name != "save_A" || !es[0].IsDir {
		t.Fatalf("subdir 识别异常: %+v", es)
	}

	// file：只认 *.sav
	p2 := &GameProfile{Name: "g", Preset: PresetGeneric, SaveDir: dir, SaveMode: SaveModeFile, Include: []string{"*.sav"}}
	p2.normalize()
	es2, err := p2.selectSaveEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(es2) != 1 || es2[0].Name != "slot1.sav" || es2[0].IsDir {
		t.Fatalf("file 识别异常: %+v", es2)
	}

	// single：整个目录一个存档
	p3 := &GameProfile{Name: "g", Preset: PresetGeneric, SaveDir: dir, SaveMode: SaveModeSingle}
	p3.normalize()
	es3, err := p3.selectSaveEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(es3) != 1 || es3[0].Name != singleSaveName || !es3[0].IsDir || es3[0].Path != dir {
		t.Fatalf("single 识别异常: %+v", es3)
	}

	// exclude 排除
	p4 := &GameProfile{Name: "g", Preset: PresetGeneric, SaveDir: dir, SaveMode: SaveModeFile, Include: []string{"*"}, Exclude: []string{"*.txt"}}
	p4.normalize()
	es4, err := p4.selectSaveEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(es4) != 1 || es4[0].Name != "slot1.sav" {
		t.Fatalf("exclude 异常: %+v", es4)
	}
}

func TestMatchAnyCaseInsensitive(t *testing.T) {
	if !matchAny("Slot1.SAV", []string{"*.sav"}, nil) {
		t.Fatal("glob 应大小写不敏感")
	}
	if matchAny("a.log", []string{"*"}, []string{"*.LOG"}) {
		t.Fatal("exclude 应大小写不敏感")
	}
}

func TestScanEntryModesAndTime(t *testing.T) {
	dir := t.TempDir()
	makeSave(t, dir, "save_A", "CXJ", "2026-10-12 01:31:36.761 UTC", "body")
	writeFile(t, filepath.Join(dir, "slot1.sav"), "data1")

	// subdir + descriptor（远行星号预设）
	ps := &GameProfile{Name: "g", Preset: PresetStarsector, GameRoot: filepath.Dir(dir), SaveDir: dir}
	ps.normalize()
	es, err := ps.selectSaveEntries()
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 1 || es[0].Name != "save_A" {
		t.Fatalf("starsector 预设识别异常: %+v", es)
	}
	m, err := scanEntryMeta(ps, es[0])
	if err != nil {
		t.Fatal(err)
	}
	if m.Character != "CXJ" {
		t.Fatalf("descriptor 角色名未解析: %q", m.Character)
	}
	if m.SaveDate == "" {
		t.Fatal("descriptor 存档时间未解析")
	}

	// file + mtime（通用预设）
	pf := &GameProfile{Name: "g", Preset: PresetGeneric, SaveDir: dir, SaveMode: SaveModeFile, Include: []string{"*.sav"}}
	pf.normalize()
	ef, err := pf.selectSaveEntries()
	if err != nil {
		t.Fatal(err)
	}
	mf, err := scanEntryMeta(pf, ef[0])
	if err != nil {
		t.Fatal(err)
	}
	if mf.SaveDate == "" {
		t.Fatal("mtime 存档时间应非空")
	}
	if len(mf.Files) != 1 {
		t.Fatalf("单文件条目应只有 1 个文件: %v", mf.Files)
	}
	if mf.SHA256 == "" {
		t.Fatal("单文件条目哈希为空")
	}
}

// 老配置（无 preset 字段、Name=starsector）应被识别为远行星号预设并保持原行为
func TestLegacyStarsectorConfigCompat(t *testing.T) {
	cfg := &Config{Device: "d", Repo: "a/b", Games: []GameProfile{{
		Name: "starsector", GameRoot: `E:\g`, SaveDir: `E:\g\saves`,
		ProcessNames: []string{"starsector.exe", "javaw.exe"},
	}}}
	cfg.normalize()
	g := cfg.Games[0]
	if g.Preset != PresetStarsector {
		t.Fatalf("preset=%q", g.Preset)
	}
	if g.SaveMode != SaveModeSubdir || g.SaveTimeSource != TimeDescriptor {
		t.Fatalf("starsector 预设默认项异常: %+v", g)
	}
	if len(g.ExtraFiles) != 1 || g.ExtraFiles[0] != "vmparams" {
		t.Fatalf("额外文件应为 [vmparams]: %v", g.ExtraFiles)
	}
	if g.ModsDir != filepath.Join(`E:\g`, "mods") {
		t.Fatalf("mods 目录异常: %q", g.ModsDir)
	}
	if len(g.Include) != 2 {
		t.Fatalf("include 异常: %v", g.Include)
	}

	cfg2 := &Config{Games: []GameProfile{{Name: "other", SaveDir: `E:\s`}}}
	cfg2.normalize()
	g2 := cfg2.Games[0]
	if g2.Preset != PresetGeneric || g2.SaveTimeSource != TimeMTIME {
		t.Fatalf("通用预设默认项异常: %+v", g2)
	}
	if g2.ModsDir != "" {
		t.Fatalf("通用模式不应默认 mod 目录: %q", g2.ModsDir)
	}
	if len(g2.Include) != 1 || g2.Include[0] != "*" {
		t.Fatalf("通用 include 应为 [*]: %v", g2.Include)
	}
}

// ---- 通用（file 模式 + 额外文件）端到端 ----

func TestGenericFileModeEndToEnd(t *testing.T) {
	mock := newMockGH()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	os.Setenv("GSSYNC_API_BASE", srv.URL+"/api")
	os.Setenv("GSSYNC_UPLOAD_BASE", srv.URL+"/uploads")
	os.Setenv("GITHUB_TOKEN", "test-token")

	newCfg := func(root, device string) *Config {
		saves := filepath.Join(root, "saves")
		if err := os.MkdirAll(saves, 0o755); err != nil {
			t.Fatal(err)
		}
		return &Config{
			Device: device, Repo: "tester/saves-repo", ReleaseTag: "gssync",
			PollSeconds: 10, HistoryKeep: 5,
			Games: []GameProfile{{
				Name: "generic", Preset: PresetGeneric, GameRoot: root, SaveDir: saves,
				SaveMode: SaveModeFile, Include: []string{"*.sav"},
				ExtraFiles: []string{"settings.ini"},
			}},
		}
	}

	// 设备 A
	rootA := t.TempDir()
	cfgA := newCfg(rootA, "PC-A")
	writeFile(t, filepath.Join(rootA, "saves", "slot1.sav"), "A1")
	writeFile(t, filepath.Join(rootA, "saves", "slot2.sav"), "A2")
	writeFile(t, filepath.Join(rootA, "settings.ini"), "cfgA")
	homeA := t.TempDir()
	os.Setenv("GSSYNC_HOME", homeA)
	if err := saveConfig(cfgA); err != nil {
		t.Fatal(err)
	}
	sumA, err := doSync(cfgA, SyncOpts{})
	if err != nil {
		t.Fatalf("A 同步失败: %v", err)
	}
	if len(sumA.Uploaded) != 2 {
		t.Fatalf("A 应上传 2 个存档: %+v", sumA)
	}
	if mock.assetByName("slot1.sav.zip") == nil || mock.assetByName("slot2.sav.zip") == nil {
		t.Fatalf("云端缺少存档资产: %v", mock.snapshotNames())
	}
	if mock.assetByName("extra_settings.ini") == nil {
		t.Fatalf("云端缺少额外文件资产: %v", mock.snapshotNames())
	}

	// 设备 B：本机无存档，应下载；额外文件本机存在则按同一规则处理
	rootB := t.TempDir()
	cfgB := newCfg(rootB, "PC-B")
	writeFile(t, filepath.Join(rootB, "settings.ini"), "cfgB")
	os.Setenv("GSSYNC_HOME", t.TempDir())
	if err := saveConfig(cfgB); err != nil {
		t.Fatal(err)
	}
	sumB, err := doSync(cfgB, SyncOpts{})
	if err != nil {
		t.Fatalf("B 同步失败: %v", err)
	}
	if len(sumB.Restored) != 2 {
		t.Fatalf("B 应从云端取回 2 个存档: %+v", sumB)
	}
	got, _ := os.ReadFile(filepath.Join(rootB, "saves", "slot1.sav"))
	if string(got) != "A1" {
		t.Fatalf("B 的 slot1.sav 内容异常: %q", got)
	}

	// A 再同步：额外文件以云端为准（此时云端已被 B 覆盖为 cfgB）
	os.Setenv("GSSYNC_HOME", homeA)
	if _, err := doSync(cfgA, SyncOpts{}); err != nil {
		t.Fatalf("A 二轮同步失败: %v", err)
	}
	gotIni, _ := os.ReadFile(filepath.Join(rootA, "settings.ini"))
	if string(gotIni) != "cfgB" {
		t.Fatalf("A 的 settings.ini 应同步为云端版本 cfgB: %q", gotIni)
	}
}
