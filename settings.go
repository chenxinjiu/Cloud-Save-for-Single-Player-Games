package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ============ GUI「设置」页的纯逻辑（不依赖 walk，跨平台可编译可测）============
//
// 目标：让使用者在图形界面里填写 GitHub 仓库 / Token / 本地游戏目录 / 设备名即可完成配置，
// 不必运行命令行 `gssync init`。Token 只写系统凭据管理器，绝不写入 config.json。

// credentialReader/credentialWriter 是凭据存取的间接层，生产环境即 credRead/credWrite，
// 测试可替换为内存实现，避免污染真实的 Windows 凭据管理器。
var (
	credentialReader = credRead
	credentialWriter = credWrite
)

// Settings 表示设置表单里的一批值（GUI 读控件后填充，再交给这里的函数处理）
type Settings struct {
	Repo         string // owner/name（存储存档的私有仓库）
	Token        string // GitHub Token（可留空表示沿用已保存的）
	Preset       string // "starsector" | "generic"
	GameRoot     string // 游戏根目录，例如 E:\yxx\Starsector
	SaveDir      string // 存档目录（留空则按 根/saves 派生）
	SaveMode     string // subdir（每个子目录一个存档）| file（每个文件一个）| single（整个目录一个）
	Include      string // 匹配规则（作用于条目名的 glob，逗号分隔，如 *.sav）
	ProcessNames string // 游戏进程名，逗号分隔（用于判断游戏是否在运行）
	Device       string // 本机设备名
	ReleaseTag   string // 滚动 Release 的 tag，默认 gssync
	AppRepo      string // 应用自身仓库（自动更新用），留空用默认
}

// splitList 把逗号/分号/换行分隔的输入拆成去空白的列表
func splitList(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '\n' }) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// defaultConfig 返回一份骨架配置（首次使用、尚无配置文件时用）
func defaultConfig() *Config {
	return &Config{
		Device:      hostname(),
		ReleaseTag:  defaultReleaseTag,
		PollSeconds: 10,
		HistoryKeep: 5,
		Games: []GameProfile{{
			Name:         "starsector",
			ProcessNames: []string{"starsector.exe", "javaw.exe"},
		}},
	}
}

// loadOrDefault 读取配置；若尚未初始化则返回骨架配置（wasExisting=false），其它错误照常上抛。
// 这样首次打开 GUI 也能进入设置页填写，而不是直接报错退出。
func loadOrDefault() (cfg *Config, wasExisting bool, err error) {
	cfg, err = loadConfig()
	if err == nil {
		return cfg, true, nil
	}
	if errors.Is(err, errNotInitialized) {
		return defaultConfig(), false, nil
	}
	return nil, false, err
}

// normalizeSettings 去除首尾空白并补默认值（ReleaseTag / Device）
func normalizeSettings(s Settings) Settings {
	s.Repo = strings.TrimSpace(s.Repo)
	s.Token = strings.TrimSpace(s.Token)
	s.Preset = strings.TrimSpace(s.Preset)
	s.GameRoot = strings.TrimSpace(s.GameRoot)
	s.SaveDir = strings.TrimSpace(s.SaveDir)
	s.SaveMode = strings.TrimSpace(s.SaveMode)
	s.Include = strings.TrimSpace(s.Include)
	s.ProcessNames = strings.TrimSpace(s.ProcessNames)
	s.Device = strings.TrimSpace(s.Device)
	s.ReleaseTag = strings.TrimSpace(s.ReleaseTag)
	s.AppRepo = strings.TrimSpace(s.AppRepo)
	if s.ReleaseTag == "" {
		s.ReleaseTag = defaultReleaseTag
	}
	if s.Device == "" {
		s.Device = hostname()
	}
	if s.GameRoot != "" {
		s.GameRoot = filepath.Clean(s.GameRoot)
	}
	if s.SaveDir != "" {
		s.SaveDir = filepath.Clean(s.SaveDir)
	} else if s.GameRoot != "" {
		s.SaveDir = filepath.Join(s.GameRoot, "saves")
	}
	return s
}

// validateRepoFormat 校验仓库名必须为 owner/name（不触网）
func validateRepoFormat(repo string) error {
	parts := strings.SplitN(strings.TrimSpace(repo), "/", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return fmt.Errorf("仓库格式错误，应为 owner/name：%q", repo)
	}
	return nil
}

// validateSettings 纯本地校验，返回警告（不阻断保存）
func validateSettings(s Settings) []string {
	var warn []string
	if s.SaveDir == "" {
		warn = append(warn, "未填写存档目录")
	} else if st, err := os.Stat(s.SaveDir); err != nil || !st.IsDir() {
		warn = append(warn, fmt.Sprintf("未找到存档目录 %s，请确认路径", s.SaveDir))
	}
	if strings.TrimSpace(os.Getenv("GITHUB_TOKEN")) != "" {
		warn = append(warn, "检测到环境变量 GITHUB_TOKEN，它会覆盖此处保存的 Token")
	}
	return warn
}

// resolveToken 表单留空时回退到已保存在凭据管理器里的 Token
func resolveToken(formToken string) (string, error) {
	if t := strings.TrimSpace(formToken); t != "" {
		return t, nil
	}
	_, token, err := credentialReader()
	if err != nil || strings.TrimSpace(token) == "" {
		return "", errors.New("未提供 Token，且未找到已保存的 Token")
	}
	return token, nil
}

// applySettings 把表单值写入配置，仅改动对应字段（保留 PollSeconds/HistoryKeep 等）
func applySettings(cfg *Config, s Settings) {
	if s.Device != "" {
		cfg.Device = s.Device
	}
	cfg.Repo = s.Repo
	if s.ReleaseTag != "" {
		cfg.ReleaseTag = s.ReleaseTag
	}
	cfg.AppRepo = s.AppRepo // 空表示走默认应用仓库

	if len(cfg.Games) == 0 {
		cfg.Games = []GameProfile{{}}
	}
	g := &cfg.Games[0]

	// 现有档案的"有效预设"（老配置没有 preset 字段时按名称推断）
	oldPreset := g.Preset
	if oldPreset == "" {
		if g.Name == "" || strings.EqualFold(g.Name, PresetStarsector) {
			oldPreset = PresetStarsector
		} else {
			oldPreset = PresetGeneric
		}
	}
	preset := strings.TrimSpace(s.Preset)
	if preset == "" {
		preset = oldPreset // 表单没给类型时沿用现有预设，避免误丢规则
	}
	if oldPreset != preset {
		// 切换预设：清空上一个预设派生的字段，交由 normalize 按新预设重新填充
		g.SaveMode, g.SaveTimeSource, g.DescriptorFile = "", "", ""
		g.SaveDir, g.ModsDir, g.ModsMetaFile, g.ModsEnabledFile = "", "", "", ""
		g.Include, g.Exclude, g.ExtraFiles, g.ProcessNames = nil, nil, nil, nil
	}
	g.Preset = preset
	g.Name = preset
	g.GameRoot = s.GameRoot
	if s.SaveDir != "" {
		g.SaveDir = s.SaveDir
	} else if s.GameRoot != "" && g.SaveDir == "" {
		g.SaveDir = filepath.Join(s.GameRoot, "saves")
	}
	if s.SaveMode != "" {
		g.SaveMode = s.SaveMode
	}
	if inc := splitList(s.Include); len(inc) > 0 {
		g.Include = inc
	}
	if pn := splitList(s.ProcessNames); len(pn) > 0 {
		g.ProcessNames = pn
	}
	g.normalize()
}

// buildConfig 基于现有配置生成一份应用了表单值的新配置（不触网）：先归一化，再校验。
// 返回的配置与 base 不共享底层切片，可安全落盘。
func buildConfig(base *Config, s Settings) (*Config, []string, error) {
	if base == nil {
		base = defaultConfig()
	}
	s = normalizeSettings(s)
	if err := validateRepoFormat(s.Repo); err != nil {
		return nil, nil, err
	}
	warn := validateSettings(s)

	cfg := *base
	cfg.Games = append([]GameProfile(nil), base.Games...)
	applySettings(&cfg, s)
	cfg.normalize()
	return &cfg, warn, nil
}

// testConnection 只读校验：确认 Token 有效并检查仓库是否可访问（不会创建任何东西）。
// repo 允许只写仓库名（无斜杠时按登录名补全为 owner/name）。
func testConnection(token, repo string) (login string, exists bool, err error) {
	if strings.TrimSpace(token) == "" {
		return "", false, errors.New("未提供 Token")
	}
	g := newGHShortTimeout(token, "")
	login, err = g.Login()
	if err != nil {
		return "", false, fmt.Errorf("Token 校验失败: %w", err)
	}
	full := strings.TrimSpace(repo)
	if full == "" {
		return login, false, nil
	}
	if !strings.Contains(full, "/") {
		full = login + "/" + full
	}
	g.repo = full
	exists, err = g.repoExists()
	if err != nil {
		return login, false, err
	}
	return login, exists, nil
}

// applyAndSave 保存：Token 写入凭据管理器（UserName 同步为仓库名），配置写入 config.json
func applyAndSave(token string, cfg *Config) error {
	if err := credentialWriter(cfg.Repo, token); err != nil {
		return err
	}
	return saveConfig(cfg)
}

// newGHShortTimeout 构造 GitHub 客户端并覆盖为较短超时（默认 15 分钟，界面交互会像卡死）
func newGHShortTimeout(token, repo string) *GH {
	g := NewGH(token, repo)
	g.hc = &http.Client{Timeout: 30 * time.Second}
	return g
}

// SaveSettings 执行「保存设置」的完整流程（可在后台 goroutine 调用）：
// 解析 Token → 校验 Token 并补全 owner → 必要时创建私有仓库 → 写入凭据与配置。
func SaveSettings(s Settings, createIfMissing bool) (string, error) {
	base, _, err := loadOrDefault()
	if err != nil {
		return "", err
	}
	s = normalizeSettings(s)

	token, err := resolveToken(s.Token)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(s.Repo) == "" {
		return "", errors.New("请填写 GitHub 仓库（owner/name，或仅填仓库名）")
	}

	g := newGHShortTimeout(token, "")
	login, err := g.Login()
	if err != nil {
		return "", fmt.Errorf("Token 校验失败（请检查网络或代理是否开启）：%w", err)
	}
	if !strings.Contains(s.Repo, "/") {
		s.Repo = login + "/" + s.Repo // 只填了仓库名 → 补成 账号/仓库名
	}

	cfg, warn, err := buildConfig(base, s)
	if err != nil {
		return "", err
	}
	g.repo = cfg.Repo

	exists, err := g.repoExists()
	if err != nil {
		return "", err
	}
	if !exists {
		owner := strings.SplitN(cfg.Repo, "/", 2)[0]
		if !strings.EqualFold(owner, login) {
			return "", fmt.Errorf("无法访问仓库 %s：它不属于当前账号 %s；为避免误建，不会替你在别的账号下创建", cfg.Repo, login)
		}
		if !createIfMissing {
			return "", fmt.Errorf("仓库 %s 不存在。可勾选「仓库不存在时自动创建私有仓库」后重试", cfg.Repo)
		}
		if err := g.EnsureRepo(); err != nil {
			return "", fmt.Errorf("创建仓库失败: %w", err)
		}
		if _, err := g.EnsureRelease(cfg.ReleaseTag); err != nil {
			return "", err
		}
	}

	if err := applyAndSave(token, cfg); err != nil {
		return "", err
	}

	var b strings.Builder
	fmt.Fprintf(&b, "设置已保存。\n仓库: %s\n设备: %s\n游戏目录: %s\n存档目录: %s\n",
		cfg.Repo, cfg.Device, cfg.Games[0].GameRoot, cfg.Games[0].SaveDir)
	if len(warn) > 0 {
		b.WriteString("\n注意:\n")
		for _, w := range warn {
			b.WriteString("· " + w + "\n")
		}
	}
	b.WriteString("\n现在可以切到「同步」页使用。")
	return b.String(), nil
}
