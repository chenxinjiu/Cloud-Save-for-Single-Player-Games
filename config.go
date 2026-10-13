package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GameProfile 描述一个受管游戏。
// 其中存档"识别规则"可配置，从而支持任意单机游戏：
//   - Preset=starsector 时用远行星号的内置规则（与旧版行为完全一致）
//   - Preset=generic（或其它名称）时用通用规则，由 SaveMode/Include/Exclude 决定"什么算一个存档"
type GameProfile struct {
	Name     string   `json:"name"`
	Preset   string   `json:"preset,omitempty"`   // "starsector" | "generic"（空=按名称推断）
	GameRoot string   `json:"game_root"`          // 游戏根目录，如 E:\yxx\Starsector
	SaveDir  string   `json:"save_dir"`           // 存档目录
	SaveMode string   `json:"save_mode,omitempty"` // subdir（每个子目录一个存档）| file（每个文件一个）| single（整个目录一个）
	Include  []string `json:"include,omitempty"`  // 对"条目名"的 glob 白名单
	Exclude  []string `json:"exclude,omitempty"`  // 对"条目名"的 glob 黑名单

	SaveTimeSource string `json:"save_time_source,omitempty"` // mtime（文件修改时间）| descriptor（指定文件里的字段）
	DescriptorFile string `json:"descriptor_file,omitempty"`  // descriptor 模式下要解析的文件名

	ExtraFiles []string `json:"extra_files,omitempty"` // 相对 GameRoot，需随存档一起同步的配置（如远行星号的 vmparams）

	ModsDir         string `json:"mods_dir,omitempty"`          // 空 = 不做 mod 同步
	ModsMetaFile    string `json:"mods_meta_file,omitempty"`    // mod 版本元数据文件
	ModsEnabledFile string `json:"mods_enabled_file,omitempty"` // 启用列表文件名

	ProcessNames []string `json:"process_names"`
}

// 预设与识别模式常量
const (
	PresetStarsector = "starsector"
	PresetGeneric    = "generic"

	SaveModeSubdir = "subdir"
	SaveModeFile   = "file"
	SaveModeSingle = "single"

	TimeMTIME      = "mtime"
	TimeDescriptor = "descriptor"
)

// normalize 仅在字段为空时填默认值（用户显式值优先），可反复调用。
func (p *GameProfile) normalize() {
	if p.Preset == "" {
		// 老配置没有 preset 字段：名称是 starsector（旧 init/设置页写死此名）→ 远行星号预设
		if p.Name == "" || strings.EqualFold(p.Name, PresetStarsector) {
			p.Preset = PresetStarsector
		} else {
			p.Preset = PresetGeneric
		}
	}
	if p.Name == "" {
		p.Name = p.Preset
	}
	if p.SaveDir == "" && p.GameRoot != "" {
		p.SaveDir = filepath.Join(p.GameRoot, "saves")
	}
	if p.SaveMode == "" {
		p.SaveMode = SaveModeSubdir
	}
	if p.SaveTimeSource == "" {
		if p.Preset == PresetStarsector {
			p.SaveTimeSource = TimeDescriptor
		} else {
			p.SaveTimeSource = TimeMTIME
		}
	}
	if p.SaveTimeSource == TimeDescriptor && p.DescriptorFile == "" {
		p.DescriptorFile = "descriptor.xml"
	}

	if p.Preset == PresetStarsector {
		if len(p.Include) == 0 {
			p.Include = []string{"common", "save_*"}
		}
		if p.ExtraFiles == nil {
			p.ExtraFiles = []string{"vmparams"}
		}
		if p.ModsDir == "" && p.GameRoot != "" {
			p.ModsDir = filepath.Join(p.GameRoot, "mods")
		}
		if p.ModsMetaFile == "" {
			p.ModsMetaFile = "mod_info.json"
		}
		if p.ModsEnabledFile == "" {
			p.ModsEnabledFile = "enabled_mods.json"
		}
		if len(p.ProcessNames) == 0 {
			p.ProcessNames = []string{"starsector.exe", "javaw.exe"}
		}
		return
	}

	// 通用模式：不假设任何专有格式
	if len(p.Include) == 0 {
		p.Include = []string{"*"} // 空白名单兜底为"全部"
	}
}

// Config 全局配置（不含任何密钥，Token 存于系统凭据管理器）
type Config struct {
	Device      string        `json:"device"`
	Repo        string        `json:"repo"`     // owner/name（存档仓库）
	AppRepo     string        `json:"app_repo,omitempty"` // 应用自身仓库（自动更新用），留空用默认
	ReleaseTag  string        `json:"release_tag"`
	PollSeconds int           `json:"poll_seconds"`
	HistoryKeep int           `json:"history_keep"`
	Games       []GameProfile `json:"games"`
}

const defaultReleaseTag = "gssync"

// errNotInitialized 配置文件缺失（尚未初始化）。调用方可用 errors.Is 判断并兜底，
// 例如 GUI 首次打开时可回退到骨架配置进入设置页，而不是直接报错退出。
var errNotInitialized = errors.New("尚未初始化，请先运行: gssync init")

func (c *Config) normalize() {
	if c.ReleaseTag == "" {
		c.ReleaseTag = defaultReleaseTag
	}
	if c.PollSeconds <= 0 {
		c.PollSeconds = 10
	}
	if c.HistoryKeep <= 0 {
		c.HistoryKeep = 5
	}
	for i := range c.Games {
		c.Games[i].normalize()
	}
}

func (c *Config) game() (*GameProfile, error) {
	if len(c.Games) == 0 {
		return nil, errors.New("配置中没有游戏档案，请重新运行 gssync init")
	}
	return &c.Games[0], nil
}

// getToken 优先环境变量，其次凭据管理器
func getToken() (string, error) {
	if t := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); t != "" {
		return t, nil
	}
	_, token, err := credRead()
	if err != nil {
		return "", errors.New("未找到 Token，请运行 gssync init 或设置环境变量 GITHUB_TOKEN")
	}
	return token, nil
}

// configDir 支持环境变量覆盖（测试用），默认 ~/.gssync
func configDir() (string, error) {
	if s := os.Getenv("GSSYNC_HOME"); s != "" {
		return s, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gssync"), nil
}

func configPath() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "config.json"), nil
}

func loadConfig() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, errNotInitialized
		}
		return nil, err
	}
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("配置文件损坏: %w", err)
	}
	cfg.normalize()
	return cfg, nil
}

func saveConfig(cfg *Config) error {
	d, err := configDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	cfg.normalize()
	data, _ := json.MarshalIndent(cfg, "", "  ")
	p := filepath.Join(d, "config.json")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p) // 原子替换，避免写一半被截断
}

func statePath() (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "state.json"), nil
}

// ---- init 命令 ----

func cmdInit(args []string) error {
	reader := bufio.NewReader(os.Stdin)

	fmt.Println("== gssync 初始化 ==")
	fmt.Println()
	fmt.Println("第一步：GitHub Token（将保存在 Windows 凭据管理器，加密存储，不写入任何明文文件）")
	fmt.Println("  打开 https://github.com/settings/tokens/new?scopes=repo&description=gssync")
	fmt.Println("  勾选 repo 权限即可（用于自动创建私有仓库并读写存档）")
	fmt.Println("  （也可用环境变量 GITHUB_TOKEN 提供后直接回车）")
	token := strings.TrimSpace(prompt(reader, "GitHub Token: "))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token == "" {
		return errors.New("未提供 Token")
	}

	gh := NewGH(token, "")
	login, err := gh.Login()
	if err != nil {
		return fmt.Errorf("Token 校验失败: %w", err)
	}
	fmt.Println("Token 有效，账号:", login)

	repoName := promptDefault(reader, "仓库名（自动创建为私有仓库）", "starsector-cloud-saves")
	gh.repo = login + "/" + repoName
	if err := gh.EnsureRepo(); err != nil {
		return fmt.Errorf("创建仓库失败: %w", err)
	}
	if _, err := gh.EnsureRelease(defaultReleaseTag); err != nil {
		return err
	}
	if err := credWrite(gh.repo, token); err != nil {
		return err
	}

	device := promptDefault(reader, "本机设备名（用于区分两台电脑）", hostname())
	fmt.Println()
	fmt.Println("第二步：游戏")
	preset := promptDefault(reader, "游戏类型（starsector=远行星号 / generic=通用）", PresetStarsector)
	if !strings.EqualFold(preset, PresetGeneric) {
		preset = PresetStarsector
	}
	rootPrompt, rootDefault := "远行星号游戏根目录", `E:\yxx\Starsector`
	if preset == PresetGeneric {
		rootPrompt, rootDefault = "游戏根目录", "."
	}
	gameRoot := filepath.Clean(promptDefault(reader, rootPrompt, rootDefault))
	saveDir := filepath.Join(gameRoot, "saves")
	if preset == PresetGeneric {
		saveDir = filepath.Clean(promptDefault(reader, "存档目录（含存档文件的目录）", saveDir))
	}
	if st, err := os.Stat(saveDir); err != nil || !st.IsDir() {
		fmt.Printf("警告: 未找到存档目录 %s，请确认路径\n", saveDir)
	}

	cfg := &Config{
		Device:      device,
		Repo:        gh.repo,
		ReleaseTag:  defaultReleaseTag,
		PollSeconds: 10,
		HistoryKeep: 5,
		Games: []GameProfile{{
			Name:     preset,
			Preset:   preset,
			GameRoot: gameRoot,
			SaveDir:  saveDir,
		}},
	}
	if err := saveConfig(cfg); err != nil {
		return err
	}
	p, _ := configPath()
	fmt.Println()
	fmt.Println("配置已保存:", p)
	fmt.Println("Token 已存入 Windows 凭据管理器（目标名: gssync）")
	fmt.Println("运行 gssync sync 进行首次同步，或 gssync watch 启动守护模式")
	return nil
}

// ---- 交互辅助 ----

func prompt(r *bufio.Reader, label string) string {
	fmt.Print(label)
	s, _ := r.ReadString('\n')
	return strings.TrimSpace(s)
}

func promptDefault(r *bufio.Reader, label, def string) string {
	v := prompt(r, label+" ["+def+"]: ")
	if v == "" {
		return def
	}
	return v
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil || h == "" {
		return "pc"
	}
	return h
}
