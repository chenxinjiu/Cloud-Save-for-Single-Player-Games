package main

import (
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// memCred 内存凭据存储，替换真实 Windows 凭据管理器，避免污染用户凭据
type memCred struct{ repo, token string }

func swapCredStore(t *testing.T) *memCred {
	t.Helper()
	m := &memCred{}
	oldR, oldW := credentialReader, credentialWriter
	credentialReader = func() (string, string, error) {
		if m.repo == "" && m.token == "" {
			return "", "", errors.New("no credential")
		}
		return m.repo, m.token, nil
	}
	credentialWriter = func(repo, token string) error {
		m.repo, m.token = repo, token
		return nil
	}
	t.Cleanup(func() { credentialReader, credentialWriter = oldR, oldW })
	return m
}

// setupSettingsMock 启动 mock GitHub 并设好环境变量
func setupSettingsMock(t *testing.T, repoExists bool) (*mockGH, string) {
	t.Helper()
	mock := newMockGH()
	mock.repoExists = repoExists
	srv := httptest.NewServer(mock)
	t.Cleanup(srv.Close)
	home := t.TempDir()
	t.Setenv("GSSYNC_API_BASE", srv.URL+"/api")
	t.Setenv("GSSYNC_UPLOAD_BASE", srv.URL+"/uploads")
	t.Setenv("GSSYNC_HOME", home)
	t.Setenv("GITHUB_TOKEN", "") // 强制走凭据/表单，而非环境变量
	return mock, home
}

func TestValidateRepoFormat(t *testing.T) {
	bad := []string{"", "abc", "/name", "owner/", "  /  ", "   "}
	for _, s := range bad {
		if err := validateRepoFormat(s); err == nil {
			t.Errorf("validateRepoFormat(%q) 应为错误", s)
		}
	}
	good := []string{"owner/name", "a/b", "chenxinjiu/saves"}
	for _, s := range good {
		if err := validateRepoFormat(s); err != nil {
			t.Errorf("validateRepoFormat(%q) 不应报错: %v", s, err)
		}
	}
}

func TestBuildConfigPreservesFields(t *testing.T) {
	base := &Config{
		Device:      "old",
		Repo:        "a/b",
		ReleaseTag:  "gssync",
		PollSeconds: 99,
		HistoryKeep: 7,
		Games: []GameProfile{
			{Name: "starsector", GameRoot: "X", SaveDir: "X/saves", ProcessNames: []string{"g.exe", "java.exe"}},
			{Name: "other", GameRoot: "Y"},
		},
	}
	cfg, _, err := buildConfig(base, Settings{
		Repo: "tester/newrepo", Token: "t", GameRoot: "E:\\g", Device: "dev2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollSeconds != 99 || cfg.HistoryKeep != 7 {
		t.Fatalf("默认/自定义字段被覆盖: %+v", cfg)
	}
	if len(cfg.Games) != 2 {
		t.Fatalf("多游戏档案丢失: %+v", cfg.Games)
	}
	if len(cfg.Games[0].ProcessNames) != 2 || cfg.Games[0].ProcessNames[0] != "g.exe" {
		t.Fatalf("ProcessNames 丢失: %+v", cfg.Games[0])
	}
	if cfg.Games[0].GameRoot != "E:\\g" || cfg.Games[0].SaveDir != filepath.Join("E:\\g", "saves") {
		t.Fatalf("游戏目录未更新: %+v", cfg.Games[0])
	}
	if cfg.Repo != "tester/newrepo" || cfg.Device != "dev2" {
		t.Fatalf("仓库/设备未更新: %+v", cfg)
	}
	if base.Repo != "a/b" || base.Games[0].GameRoot != "X" {
		t.Fatalf("原始配置被就地修改: %+v", base)
	}
}

func TestSaveSettingsSavesConfigWithoutToken(t *testing.T) {
	_, home := setupSettingsMock(t, true)
	mem := swapCredStore(t)

	root := filepath.Join(t.TempDir(), "Starsector")
	if err := os.MkdirAll(filepath.Join(root, "saves"), 0o755); err != nil {
		t.Fatal(err)
	}

	msg, err := SaveSettings(Settings{
		Repo: "saves-repo", Token: "test-token", GameRoot: root, Device: "dev1",
	}, true)
	if err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if !strings.Contains(msg, "设置已保存") {
		t.Fatalf("消息异常: %q", msg)
	}

	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Repo != "tester/saves-repo" { // 只填仓库名 → 自动补 owner
		t.Fatalf("仓库未补全: %q", cfg.Repo)
	}
	if cfg.Device != "dev1" {
		t.Fatalf("设备名未保存: %q", cfg.Device)
	}
	if cfg.PollSeconds != 10 || cfg.HistoryKeep != 5 {
		t.Fatalf("默认项丢失: %+v", cfg)
	}
	if len(cfg.Games) != 1 || cfg.Games[0].SaveDir != filepath.Join(root, "saves") {
		t.Fatalf("游戏档案异常: %+v", cfg.Games)
	}
	if mem.repo != "tester/saves-repo" || mem.token != "test-token" {
		t.Fatalf("凭据写入异常: %+v", mem)
	}

	raw, err := os.ReadFile(filepath.Join(home, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "test-token") {
		t.Fatalf("config.json 泄漏了 Token: %s", raw)
	}
}

func TestSaveSettingsCreatesRepoWhenMissing(t *testing.T) {
	mock, _ := setupSettingsMock(t, false) // 仓库不存在
	swapCredStore(t)

	_, err := SaveSettings(Settings{
		Repo: "tester/saves-repo", Token: "tok", GameRoot: t.TempDir(), Device: "d",
	}, true)
	if err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	if !mock.createdRepo {
		t.Fatal("勾选自动创建时，应发出 POST /api/user/repos")
	}
	if mock.release == nil {
		t.Fatal("创建仓库后应同时创建 Release")
	}
}

func TestSaveSettingsRefusesForeignOwner(t *testing.T) {
	mock, _ := setupSettingsMock(t, false)
	swapCredStore(t)

	_, err := SaveSettings(Settings{
		Repo: "someone/saves-repo", Token: "tok", GameRoot: t.TempDir(), Device: "d",
	}, true)
	if err == nil {
		t.Fatal("仓库属于其它账号时应报错")
	}
	if mock.createdRepo {
		t.Fatal("不得替其它账号创建仓库")
	}
}

func TestTestConnection(t *testing.T) {
	mock, _ := setupSettingsMock(t, true)

	login, exists, err := testConnection("tok", "tester/saves-repo")
	if err != nil {
		t.Fatal(err)
	}
	if login != "tester" || !exists {
		t.Fatalf("login=%q exists=%v", login, exists)
	}

	// 只填仓库名也要能测
	login, exists, err = testConnection("tok", "saves-repo")
	if err != nil {
		t.Fatal(err)
	}
	if login != "tester" || !exists {
		t.Fatalf("name-only: login=%q exists=%v", login, exists)
	}

	// 不存在的仓库 → exists=false 且不报错
	mock.repoExists = false
	_, exists, err = testConnection("tok", "tester/saves-repo")
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Fatal("仓库不存在时应返回 exists=false")
	}
}
