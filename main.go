package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const version = "0.7.0"

// defaultMode 由构建时注入（GUI 版设为 "gui"）：无参数启动时的默认命令，
// 这样双击 gssync-gui.exe 即可直接打开窗口。
var defaultMode string

func main() {
	initConsole()
	args := os.Args[1:]
	if len(args) == 0 {
		if defaultMode == "" {
			usage()
			os.Exit(2)
		}
		args = []string{defaultMode}
	}
	var err error
	switch args[0] {
	case "init":
		err = cmdInit(args[1:])
	case "status":
		err = cmdStatus(args[1:])
	case "sync":
		err = cmdSync(args[1:])
	case "push":
		err = cmdPush(args[1:])
	case "pull":
		err = cmdPull(args[1:])
	case "mods-push":
		err = cmdModsPush(args[1:])
	case "mods-pull":
		err = cmdModsPull(args[1:])
	case "update":
		err = cmdUpdate(args[1:])
	case "version":
		fmt.Printf("gssync v%s\n", version)
	case "gui":
		err = cmdGUI(args[1:])
	case "watch":
		err = cmdWatch(args[1:])
	case "history":
		err = cmdHistory(args[1:])
	case "delete":
		err = cmdDelete(args[1:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Printf("未知命令: %s\n\n", args[0])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
}

func hasFlag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func cmdSync(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	_, err = doSync(cfg, SyncOpts{
		UploadOnly:   hasFlag(args, "--upload-only"),
		DownloadOnly: hasFlag(args, "--download-only"),
		ResetCloud:   hasFlag(args, "--reset-cloud"),
	})
	return err
}

// cmdPush 手动上传：以本机为准，把本地存档与配置强制上传覆盖云端
func cmdPush(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	fmt.Println("正在上传（以本机为准）...")
	_, err = doSync(cfg, SyncOpts{ForceUpload: true})
	return err
}

// cmdPull 手动拉取：以云端为准，强制下载覆盖本机（旧版本自动归档）
func cmdPull(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	prof, err := cfg.game()
	if err != nil {
		return err
	}
	if gameRunning(prof.ProcessNames) {
		return errors.New("检测到游戏正在运行，请先退出游戏再拉取，避免覆盖正在写入的存档导致坏档")
	}
	fmt.Println("正在拉取（以云端为准）...")
	_, err = doSync(cfg, SyncOpts{ForceDownload: true})
	return err
}

// cmdUpdate 检查并应用更新（--check 只检查不下载）
func cmdUpdate(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	cleanupOldExes(dir)
	_, err = runSelfUpdate(cfg, hasFlag(args, "--check"), dir, func(f string, a ...any) {
		fmt.Printf(f+"\n", a...)
	})
	return err
}

func cmdStatus(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	prof, err := cfg.game()
	if err != nil {
		return err
	}
	token, err := getToken()
	if err != nil {
		return err
	}
	gh := NewGH(token, cfg.Repo)
	rel, err := gh.EnsureRelease(cfg.ReleaseTag)
	if err != nil {
		return fmt.Errorf("连接云端失败: %w", err)
	}
	// 云端清单
	cm := newCloudManifest()
	if a := gh.AssetByName(rel, "manifest.json"); a != nil {
		data, err := gh.DownloadAsset(*a)
		if err != nil {
			return fmt.Errorf("下载云端清单失败: %w", err)
		}
		if json.Unmarshal(data, cm) == nil {
			if cm.Saves == nil {
				cm.Saves = map[string]*CloudSave{}
			}
			cm.seedExtras()
		}
	}
	// 本地扫描（不落状态）
	s := &Syncer{cfg: cfg, prof: prof, gh: gh, rel: rel, state: loadState()}
	local, err := s.scanLocal()
	if err != nil {
		return err
	}

	fmt.Printf("设备: %s | 仓库: %s | 游戏: %s\n", cfg.Device, cfg.Repo, prof.Name)
	fmt.Println()
	fmt.Println("存档:")
	fmt.Printf("  %-42s %-12s %-12s %s\n", "存档", "本机时间", "云端时间", "状态")
	union := map[string]bool{}
	for k := range local.Saves {
		union[k] = true
	}
	for k := range cm.Saves {
		union[k] = true
	}
	for _, name := range sortedKeys(union) {
		l, c := local.Saves[name], cm.Saves[name]
		var ld, cd, status string
		switch {
		case l == nil:
			cd = shortTime(c.SaveDate)
			status = "仅云端(待下载)"
		case c == nil:
			ld = shortTime(l.SaveDate)
			status = "仅本机(待上传)"
		case l.SHA256 == c.SaveSHA:
			ld = shortTime(l.SaveDate)
			cd = shortTime(c.SaveDate)
			status = "一致"
		case !parseTimeOrZero(c.SaveDate).After(parseTimeOrZero(l.SaveDate)):
			ld = shortTime(l.SaveDate)
			cd = shortTime(c.SaveDate)
			status = "本机较新"
		default:
			ld = shortTime(l.SaveDate)
			cd = shortTime(c.SaveDate)
			status = "云端较新"
		}
		fmt.Printf("  %-42s %-12s %-12s %s\n", name, ld, cd, status)
	}
	fmt.Println()
	if local.Mods != nil {
		fmt.Printf("mod: 已安装 %d 个，已启用 %d 个", len(local.Mods.Installed), len(local.Mods.Enabled))
		if cm.Mods != nil && !modsEqual(cm.Mods, local.Mods) {
			fmt.Print("  ⚠ 与云端不一致")
		}
		fmt.Println()
	}
	for _, rel := range prof.ExtraFiles {
		lsha := local.Extras[rel]
		if lsha == "" {
			continue
		}
		vs := "一致"
		if cs := cm.Extras[rel]; cs != "" && cs != lsha {
			vs = "⚠ 与云端不一致"
		}
		fmt.Printf("%s: %s\n", rel, vs)
	}
	return nil
}

func usage() {
	fmt.Printf(`gssync v%s - 单机游戏云存档同步（基于 GitHub 私有仓库）

用法:
  gssync init              初始配置（游戏目录 / GitHub Token / 私有仓库）
  gssync gui               打开图形界面（上传 / 拉取 / 查看状态 按钮）
  gssync push              手动上传：以本机为准，强制上传本地存档覆盖云端
  gssync pull              手动拉取：以云端为准，强制下载覆盖本机（旧版归档）
  gssync mods-push         上传 MOD：把本机新增/改动的 mod 打包上传云端
  gssync mods-pull         拉取 MOD：以云端为准下载 mod 覆盖本机（游戏运行中拒绝）
  gssync update            检查并下载更新（打开 GUI 时也会自动检查）
      --check              只检查不下载
  gssync version           显示版本号
  gssync status            查看本地与云端差异
  gssync sync              双向同步一次
      --upload-only        只上传
      --download-only      只下载
      --reset-cloud        以本机为准重建云端（灾难恢复）
  gssync watch             守护模式：持续自动同步（游戏运行中只上传不下载）
  gssync history list [存档名]                查看本地历史版本
  gssync history restore <存档名> <时间戳>    恢复历史版本
  gssync history clean <存档名|all>           清理历史
  gssync delete <存档名>   删除存档（本机归档后删除，云端同步删除）
`, version)
}
