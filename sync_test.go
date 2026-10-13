package main

import (
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// ---- mock GitHub Releases 服务器 ----

type mockAsset struct {
	name string
	data []byte
}

type mockGH struct {
	mu       sync.Mutex
	assets   map[int64]*mockAsset
	order    []int64
	nextID   int64
	release  *Release
	upErrCnt int
	appFiles map[string][]byte // 应用仓库（tester/app-repo）的测试文件

	repoExists  bool // GET /api/repos/tester/saves-repo 的存在性（设置页测试用）
	createdRepo bool // 是否收到过 POST /api/user/repos
}

func newMockGH() *mockGH {
	return &mockGH{assets: map[int64]*mockAsset{}, nextID: 100}
}

func (m *mockGH) snapshotNames() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, id := range m.order {
		out = append(out, m.assets[id].name)
	}
	return out
}

func (m *mockGH) assetByName(name string) *mockAsset {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range m.order {
		if m.assets[id].name == name {
			cp := *m.assets[id]
			return &cp
		}
	}
	return nil
}

func (m *mockGH) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path := r.URL.Path
	switch {
	case r.Method == "GET" && strings.HasPrefix(path, "/api/repos/tester/app-repo/contents/"):
		// 应用仓库文件（自更新用）：Accept 含 raw 返回原文，否则返回 JSON 元数据（含 git blob sha）
		name := strings.TrimPrefix(path, "/api/repos/tester/app-repo/contents/")
		data, ok := m.appFiles[name]
		if !ok {
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "raw") {
			w.Write(data)
			return
		}
		h := sha1.New()
		fmt.Fprintf(h, "blob %d\x00", len(data))
		h.Write(data)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "sha": hex.EncodeToString(h.Sum(nil)), "size": len(data)})
		return

	case r.Method == "GET" && path == "/api/user":
		w.Write([]byte(`{"login":"tester"}`))
		return

	case r.Method == "GET" && path == "/api/repos/tester/saves-repo":
		if m.repoExists {
			w.WriteHeader(200)
			w.Write([]byte(`{}`))
			return
		}
		w.WriteHeader(404)
		w.Write([]byte(`{"message":"Not Found"}`))
		return

	case r.Method == "POST" && path == "/api/user/repos":
		m.repoExists = true
		m.createdRepo = true
		w.WriteHeader(201)
		w.Write([]byte(`{"id":1}`))
		return

	case r.Method == "GET" && strings.HasPrefix(path, "/api/repos/tester/saves-repo/releases/tags/"):
		if m.release == nil {
			w.WriteHeader(404)
			w.Write([]byte(`{"message":"Not Found"}`))
			return
		}
		json.NewEncoder(w).Encode(m.release)
		return

	case r.Method == "POST" && path == "/api/repos/tester/saves-repo/releases":
		m.release = &Release{ID: 1, TagName: "gssync"}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(m.release)
		return

	case r.Method == "POST" && strings.HasPrefix(path, "/uploads/repos/tester/saves-repo/releases/1/assets"):
		name := r.URL.Query().Get("name")
		data := readAll(r)
		// 同名资产不允许重复（与 GitHub 行为一致）
		for _, id := range m.order {
			if m.assets[id].name == name {
				w.WriteHeader(422)
				w.Write([]byte(`{"message":"already_exists"}`))
				return
			}
		}
		id := m.nextID
		m.nextID++
		m.assets[id] = &mockAsset{name: name, data: data}
		m.order = append(m.order, id)
		if m.release != nil {
			m.release.Assets = append(m.release.Assets, Asset{ID: id, Name: name, Size: int64(len(data))})
		}
		w.WriteHeader(201)
		fmt.Fprintf(w, `{"id":%d,"name":%q,"size":%d}`, id, name, len(data))
		return

	case r.Method == "DELETE" && strings.HasPrefix(path, "/api/repos/tester/saves-repo/releases/assets/"):
		idStr := strings.TrimPrefix(path, "/api/repos/tester/saves-repo/releases/assets/")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		if _, ok := m.assets[id]; !ok {
			w.WriteHeader(404)
			return
		}
		delete(m.assets, id)
		for i, oid := range m.order {
			if oid == id {
				m.order = append(m.order[:i], m.order[i+1:]...)
				break
			}
		}
		if m.release != nil {
			out := m.release.Assets[:0]
			for _, a := range m.release.Assets {
				if a.ID != id {
					out = append(out, a)
				}
			}
			m.release.Assets = out
		}
		w.WriteHeader(204)
		return

	case r.Method == "GET" && strings.HasPrefix(path, "/api/repos/tester/saves-repo/releases/assets/"):
		idStr := strings.TrimPrefix(path, "/api/repos/tester/saves-repo/releases/assets/")
		id, _ := strconv.ParseInt(idStr, 10, 64)
		a, ok := m.assets[id]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Write(a.data)
		return
	}
	w.WriteHeader(404)
	w.Write([]byte(`{"message":"no route: ` + path + `"}`))
}

func readAll(r *http.Request) []byte {
	var out []byte
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Body.Read(buf)
		out = append(out, buf[:n]...)
		if err != nil {
			break
		}
	}
	return out
}

// ---- 测试脚手架 ----

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

const descTmpl = `<?xml version="1.0" ?>
<SaveGameData z="1">
<characterName>%s</characterName>
<saveFileVersion>0.6</saveFileVersion>
<gameVersion>0.98a-RC8</gameVersion>
<saveDate z="3">%s</saveDate>
</SaveGameData>
`

func makeSave(t *testing.T, root, name, character, date, body string) {
	writeFile(t, filepath.Join(root, name, "descriptor.xml"),
		fmt.Sprintf(descTmpl, character, date))
	writeFile(t, filepath.Join(root, name, "campaign.xml"), body)
	writeFile(t, filepath.Join(root, name, "campaign.xml.bak"), body+"-bak")
}

func setupDevice(t *testing.T, mock *mockGH, home, gameRoot, device string) *Config {
	t.Helper()
	os.Setenv("GSSYNC_HOME", home)
	os.Setenv("GITHUB_TOKEN", "test-token")
	cfg := &Config{
		Device:      device,
		Repo:        "tester/saves-repo",
		ReleaseTag:  "gssync",
		PollSeconds: 10,
		HistoryKeep: 5,
		Games: []GameProfile{{
			Name:         "starsector",
			GameRoot:     gameRoot,
			SaveDir:      filepath.Join(gameRoot, "saves"),
			ProcessNames: []string{"nonexistent_game.exe"},
		}},
	}
	if err := saveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

var _ = struct{}{} // 保留占位，原 mock URL 变量改由环境变量传递

// ---- 端到端测试 ----

func TestEndToEndSync(t *testing.T) {
	mock := newMockGH()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	os.Setenv("GSSYNC_API_BASE", srv.URL+"/api")
	os.Setenv("GSSYNC_UPLOAD_BASE", srv.URL+"/uploads")

	base := t.TempDir() // 模拟"初始状态的同一个存档集"
	makeSave(t, base, "save_A", "CXJ", "2026-10-12 01:31:36.761 UTC", "A-v1")
	makeSave(t, base, "save_B", "CXJ", "2026-10-10 20:00:00.000 UTC", "B-v1")

	rootA := t.TempDir()
	copyGame(t, base, rootA)
	writeFile(t, filepath.Join(rootA, "vmparams"), "-Xmx4096m")
	writeFile(t, filepath.Join(rootA, "mods", "enabled_mods.json"), `{"enabledMods":["m1"]}`)
	writeFile(t, filepath.Join(rootA, "mods", "m1", "mod_info.json"), `{"id":"m1","version":"1.0"}`)

	homeA := t.TempDir()
	cfgA := setupDevice(t, mock, homeA, rootA, "PC-A")

	// ---- 1. 设备 A 首次同步：全部上传 ----
	sum, err := doSync(cfgA, SyncOpts{})
	if err != nil {
		t.Fatalf("A 首次同步失败: %v", err)
	}
	if len(sum.Uploaded) != 2 {
		t.Fatalf("A 首次应上传 2 个存档, 实际: %v", sum.Uploaded)
	}
	if mock.assetByName("manifest.json") == nil || mock.assetByName("save_A.zip") == nil ||
		mock.assetByName("save_B.zip") == nil || mock.assetByName("mods.json") == nil ||
		mock.assetByName("vmparams") == nil {
		t.Fatalf("云端缺少资产: %v", mock.snapshotNames())
	}

	// ---- 2. 无变化再同步：不应有任何上传 ----
	before := len(mock.snapshotNames())
	sum2, err := doSync(cfgA, SyncOpts{})
	if err != nil {
		t.Fatalf("A 二次同步失败: %v", err)
	}
	if !sum2.empty() {
		t.Fatalf("无变化时不应有动作: %+v", sum2)
	}
	if len(mock.snapshotNames()) != before {
		t.Fatalf("无变化时不应用资产: %v", mock.snapshotNames())
	}

	// ---- 3. 设备 B 接入：save_B 本机改动(较新) + 新增 save_C ----
	rootB := t.TempDir()
	copyGame(t, base, rootB)
	writeFile(t, filepath.Join(rootB, "vmparams"), "-Xmx4096m")
	writeFile(t, filepath.Join(rootB, "mods", "enabled_mods.json"), `{"enabledMods":["m1"]}`)
	writeFile(t, filepath.Join(rootB, "mods", "m1", "mod_info.json"), `{"id":"m1","version":"1.0"}`)
	makeSave(t, filepath.Join(rootB, "saves"), "save_B", "CXJ", "2026-10-13 10:00:00.000 UTC", "B-v2")
	makeSave(t, filepath.Join(rootB, "saves"), "save_C", "CXJ", "2026-10-13 11:00:00.000 UTC", "C-v1")

	homeB := t.TempDir()
	cfgB := setupDevice(t, mock, homeB, rootB, "PC-B")

	sumB, err := doSync(cfgB, SyncOpts{})
	if err != nil {
		t.Fatalf("B 首次同步失败: %v", err)
	}
	if len(sumB.Conflicts) == 0 {
		t.Fatalf("save_B 应判定冲突: %+v", sumB)
	}
	if len(sumB.Uploaded) != 2 { // save_B(冲突胜出) + save_C
		t.Fatalf("B 应上传 save_B/save_C: %+v", sumB)
	}
	if mock.assetByName("save_C.zip") == nil {
		t.Fatal("云端缺少 save_C.zip")
	}
	conflict := findAsset(mock, "save_B.conflict.")
	if conflict == nil {
		t.Fatal("云端缺少 save_B 冲突资产")
	}
	if !strings.Contains(string(conflict.data), "B-v1") {
		t.Fatal("冲突资产应是云端旧版本 B-v1")
	}
	// 校验云端清单里 save_B 已是 B 的较新版本
	cm := fetchMockManifest(t, mock)
	if cm.Saves["save_B"].UpdatedBy != "PC-B" {
		t.Fatalf("save_B 应由 PC-B 更新: %+v", cm.Saves["save_B"])
	}

	// ---- 4. 设备 A 再同步：下载 B 的新版本 + 取回新增的 save_C ----
	os.Setenv("GSSYNC_HOME", homeA)
	sumA2, err := doSync(cfgA, SyncOpts{})
	if err != nil {
		t.Fatalf("A 二轮同步失败: %v", err)
	}
	if len(sumA2.Downloaded) != 1 || len(sumA2.Restored) != 1 {
		t.Fatalf("A 应下载 save_B(下载) + save_C(取回): 下载=%v 取回=%v", sumA2.Downloaded, sumA2.Restored)
	}
	bBody, _ := os.ReadFile(filepath.Join(rootA, "saves", "save_B", "campaign.xml"))
	if string(bBody) != "B-v2" {
		t.Fatalf("A 的 save_B 应为 B-v2, 实际 %q", bBody)
	}
	if _, err := os.Stat(filepath.Join(rootA, "saves", "save_C")); err != nil {
		t.Fatal("A 缺少 save_C")
	}
	// A 的旧版本应进入本地历史
	if _, err := os.Stat(filepath.Join(homeA, "history", "starsector", "save_B")); err != nil {
		t.Fatal("A 的 save_B 旧版本未归档到历史")
	}

	// ---- 5. mod 清单：B 增加 m2 → A 同步时应警告 ----
	os.Setenv("GSSYNC_HOME", homeB)
	writeFile(t, filepath.Join(rootB, "mods", "enabled_mods.json"), `{"enabledMods":["m1","m2"]}`)
	if _, err := doSync(cfgB, SyncOpts{}); err != nil {
		t.Fatalf("B mod 上传失败: %v", err)
	}
	os.Setenv("GSSYNC_HOME", homeA)
	sumA3, err := doSync(cfgA, SyncOpts{})
	if err != nil {
		t.Fatalf("A mod 同步失败: %v", err)
	}
	foundModWarn := false
	for _, w := range sumA3.Warnings {
		if strings.Contains(w, "m2") {
			foundModWarn = true
		}
	}
	if !foundModWarn {
		t.Fatalf("A 应收到 mod 差异警告: %+v", sumA3.Warnings)
	}

	// ---- 6. vmparams：B 修改 → A 下载覆盖 ----
	os.Setenv("GSSYNC_HOME", homeB)
	writeFile(t, filepath.Join(rootB, "vmparams"), "-Xmx8192m")
	if _, err := doSync(cfgB, SyncOpts{}); err != nil {
		t.Fatal(err)
	}
	os.Setenv("GSSYNC_HOME", homeA)
	if _, err := doSync(cfgA, SyncOpts{}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(rootA, "vmparams"))
	if string(got) != "-Xmx8192m" {
		t.Fatalf("A 的 vmparams 未同步: %q", got)
	}

	// ---- 7. 删除：A 删除 save_C → 云端删除 → B 跟随删除 ----
	os.Setenv("GSSYNC_HOME", homeA)
	if err := cmdDelete([]string{"save_C"}); err != nil {
		t.Fatalf("A 删除 save_C 失败: %v", err)
	}
	if mock.assetByName("save_C.zip") != nil {
		t.Fatal("云端 save_C.zip 应被删除")
	}
	os.Setenv("GSSYNC_HOME", homeB)
	sumB2, err := doSync(cfgB, SyncOpts{})
	if err != nil {
		t.Fatalf("B 删除同步失败: %v", err)
	}
	if len(sumB2.Deleted) != 1 {
		t.Fatalf("B 应跟随删除 save_C: %+v", sumB2)
	}
	if _, err := os.Stat(filepath.Join(rootB, "saves", "save_C")); !os.IsNotExist(err) {
		t.Fatal("B 的 save_C 应已删除")
	}
	if _, err := os.Stat(filepath.Join(homeB, "history", "starsector", "save_C")); err != nil {
		t.Fatal("B 删除前未归档到历史")
	}

	// ---- 8. 保护性恢复：B 本机删掉 save_A（云端未变）→ 同步后自动恢复 ----
	os.RemoveAll(filepath.Join(rootB, "saves", "save_A"))
	sumB3, err := doSync(cfgB, SyncOpts{})
	if err != nil {
		t.Fatalf("B 恢复同步失败: %v", err)
	}
	if len(sumB3.Restored) != 1 {
		t.Fatalf("save_A 应被保护性恢复: %+v", sumB3)
	}
	if _, err := os.Stat(filepath.Join(rootB, "saves", "save_A")); err != nil {
		t.Fatal("save_A 未恢复")
	}
}

func copyGame(t *testing.T, src, dstRoot string) {
	t.Helper()
	if err := copyDir(src, filepath.Join(dstRoot, "saves")); err != nil {
		t.Fatal(err)
	}
}

func findAsset(m *mockGH, prefix string) *mockAsset {
	for _, name := range m.snapshotNames() {
		if strings.HasPrefix(name, prefix) {
			return m.assetByName(name)
		}
	}
	return nil
}

func fetchMockManifest(t *testing.T, m *mockGH) *CloudManifest {
	t.Helper()
	a := m.assetByName("manifest.json")
	if a == nil {
		t.Fatal("无云端清单")
	}
	cm := newCloudManifest()
	if err := json.Unmarshal(a.data, cm); err != nil {
		t.Fatal(err)
	}
	return cm
}

// 回归：本机没有 mods 目录时，不得把云端 mod 清单覆盖为空
func TestNoModsDirKeepsCloudMods(t *testing.T) {
	mock := newMockGH()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	os.Setenv("GSSYNC_API_BASE", srv.URL+"/api")
	os.Setenv("GSSYNC_UPLOAD_BASE", srv.URL+"/uploads")

	base := t.TempDir()
	makeSave(t, base, "save_A", "CXJ", "2026-10-12 01:31:36.761 UTC", "A-v1")

	// 设备 A：有 mods 目录，上传 mod 清单
	rootA := t.TempDir()
	copyGame(t, base, rootA)
	writeFile(t, filepath.Join(rootA, "mods", "enabled_mods.json"), `{"enabledMods":["m1"]}`)
	writeFile(t, filepath.Join(rootA, "mods", "m1", "mod_info.json"), `{"id":"m1","version":"1.0"}`)
	cfgA := setupDevice(t, mock, t.TempDir(), rootA, "PC-A")
	if _, err := doSync(cfgA, SyncOpts{}); err != nil {
		t.Fatalf("A 首次同步失败: %v", err)
	}
	cloudMods := mock.assetByName("mods.json")
	if cloudMods == nil {
		t.Fatal("云端应有 mods.json")
	}
	beforeMods := string(cloudMods.data)

	// 设备 B：没有 mods 目录（如游戏未装 mod / 路径不完整）
	rootB := t.TempDir()
	copyGame(t, base, rootB)
	cfgB := setupDevice(t, mock, t.TempDir(), rootB, "PC-B")
	sumB, err := doSync(cfgB, SyncOpts{})
	if err != nil {
		t.Fatalf("B 同步失败: %v", err)
	}

	// 云端 mod 清单必须保持 A 的内容，不能被 B 的空清单覆盖
	afterMods := mock.assetByName("mods.json")
	if afterMods == nil {
		t.Fatal("云端的 mods.json 不应被删除")
	}
	if string(afterMods.data) != beforeMods {
		t.Fatalf("云端 mod 清单被覆盖:\n before=%s\n after =%s", beforeMods, afterMods.data)
	}
	// 不应产生任何 mod 相关误报
	for _, w := range sumB.Warnings {
		if strings.Contains(w, "mod") || strings.Contains(w, "mods") {
			t.Fatalf("不应产生 mod 警告: %v", sumB.Warnings)
		}
	}
	// 存档仍应正常下载
	if _, err := os.Stat(filepath.Join(rootB, "saves", "save_A")); err != nil {
		t.Fatal("B 应下载 save_A")
	}
}

// 手动 push（以本机为准上传）/ pull（以云端为准覆盖本机）
func TestForcePushAndPull(t *testing.T) {
	mock := newMockGH()
	srv := httptest.NewServer(mock)
	defer srv.Close()
	os.Setenv("GSSYNC_API_BASE", srv.URL+"/api")
	os.Setenv("GSSYNC_UPLOAD_BASE", srv.URL+"/uploads")

	base := t.TempDir()
	makeSave(t, base, "save_A", "CXJ", "2026-10-12 01:00:00.000 UTC", "A-v1")

	// 设备 A：建立云端初始版本
	rootA := t.TempDir()
	copyGame(t, base, rootA)
	homeA := t.TempDir()
	cfgA := setupDevice(t, mock, homeA, rootA, "PC-A")
	if _, err := doSync(cfgA, SyncOpts{}); err != nil {
		t.Fatalf("A 首次同步失败: %v", err)
	}

	// 设备 B：save_A 内容更新（与云端不同，普通同步会判为冲突），并新增 save_C
	rootB := t.TempDir()
	copyGame(t, base, rootB)
	makeSave(t, filepath.Join(rootB, "saves"), "save_A", "CXJ", "2026-10-12 02:00:00.000 UTC", "A-v2")
	makeSave(t, filepath.Join(rootB, "saves"), "save_C", "CXJ", "2026-10-12 03:00:00.000 UTC", "C-v1")
	cfgB := setupDevice(t, mock, t.TempDir(), rootB, "PC-B")

	// ---- push：以本机为准，强制上传（不报冲突错） ----
	sumPush, err := doSync(cfgB, SyncOpts{ForceUpload: true})
	if err != nil {
		t.Fatalf("push 失败: %v", err)
	}
	if len(sumPush.Warnings) != 0 {
		t.Fatalf("push 不应产生警告: %v", sumPush.Warnings)
	}
	cm := fetchMockManifest(t, mock)
	if cm.Saves["save_A"] == nil || cm.Saves["save_A"].UpdatedBy != "PC-B" {
		t.Fatalf("push 后 save_A 应以 PC-B 为准: %+v", cm.Saves["save_A"])
	}
	if cm.Saves["save_C"] == nil {
		t.Fatal("push 后 save_C 应已上传")
	}
	a := mock.assetByName("save_A.zip")
	if a == nil {
		t.Fatal("云端缺少 save_A.zip")
	}

	// ---- pull：本机改成更新的版本，仍应以云端为准覆盖 ----
	os.Setenv("GSSYNC_HOME", homeA)
	makeSave(t, filepath.Join(rootA, "saves"), "save_A", "CXJ", "2026-10-12 23:00:00.000 UTC", "A-v99")
	sumPull, err := doSync(cfgA, SyncOpts{ForceDownload: true})
	if err != nil {
		t.Fatalf("pull 失败: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(rootA, "saves", "save_A", "campaign.xml"))
	if string(body) != "A-v2" {
		t.Fatalf("pull 后 save_A 应为云端内容 A-v2, 实际 %q", body)
	}
	if len(sumPull.Downloaded) == 0 {
		t.Fatalf("pull 应记录下载: %+v", sumPull)
	}
	// 本机被覆盖的较新版本应归档到历史
	if _, err := os.Stat(filepath.Join(homeA, "history", "starsector", "save_A")); err != nil {
		t.Fatal("pull 覆盖前应把本机旧版本归档到历史")
	}
	// 云端独有的 save_C 也应被拉到本机
	if _, err := os.Stat(filepath.Join(rootA, "saves", "save_C", "campaign.xml")); err != nil {
		t.Fatal("pull 应下载云端新增的 save_C")
	}
}
