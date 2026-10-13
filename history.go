package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func historyRoot(cfg *Config, prof *GameProfile) (string, error) {
	d, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "history", prof.Name), nil
}

// archiveSaveToHistory 把一个存档条目复制进本地历史，并按 HistoryKeep 修剪
func archiveSaveToHistory(cfg *Config, prof *GameProfile, name string) (string, error) {
	return archiveEntryToHistory(cfg, prof, name, prof.entryFor(name).Path)
}

// archiveEntryToHistory 归档任意路径（目录或单文件）到历史
func archiveEntryToHistory(cfg *Config, prof *GameProfile, name, src string) (string, error) {
	if _, err := os.Stat(src); err != nil {
		return "", fmt.Errorf("存档不存在: %s", name)
	}
	root, err := historyRoot(cfg, prof)
	if err != nil {
		return "", err
	}
	ts := time.Now().Format("20060102-150405")
	dst := filepath.Join(root, name, ts)
	if err := copyDir(src, dst); err != nil {
		return "", err
	}
	pruneHistory(root, name, cfg.HistoryKeep)
	return ts, nil
}

func pruneHistory(root, name string, keep int) {
	dir := filepath.Join(root, name)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var tss []string
	for _, e := range ents {
		if e.IsDir() {
			tss = append(tss, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(tss)))
	for i := keep; i < len(tss); i++ {
		os.RemoveAll(filepath.Join(dir, tss[i]))
	}
}

// ---- history 命令 ----

func cmdHistory(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	prof, err := cfg.game()
	if err != nil {
		return err
	}
	root, err := historyRoot(cfg, prof)
	if err != nil {
		return err
	}
	if len(args) == 0 || args[0] == "list" {
		filter := ""
		if len(args) > 1 {
			filter = args[1]
		}
		ents, err := os.ReadDir(root)
		if err != nil {
			fmt.Println("暂无历史版本")
			return nil
		}
		found := false
		for _, e := range ents {
			if filter != "" && e.Name() != filter {
				continue
			}
			vers, _ := os.ReadDir(filepath.Join(root, e.Name()))
			for _, v := range vers {
				found = true
				size := dirSize(filepath.Join(root, e.Name(), v.Name()))
				fmt.Printf("%s  %s  %.1fMB\n", pad(e.Name(), 40), v.Name(), float64(size)/1048576)
			}
		}
		if !found {
			fmt.Println("暂无历史版本")
		}
		return nil
	}
	switch args[0] {
	case "restore":
		if len(args) < 3 {
			return fmt.Errorf("用法: gssync history restore <存档名> <时间戳>")
		}
		name, ts := args[1], args[2]
		src := filepath.Join(root, name, ts)
		if _, err := os.Stat(src); err != nil {
			return fmt.Errorf("历史版本不存在: %s %s", name, ts)
		}
		target := filepath.Join(prof.SaveDir, name)
		if _, err := os.Stat(target); err == nil {
			backup, err := archiveSaveToHistory(cfg, prof, name)
			if err != nil {
				return err
			}
			fmt.Println("当前版本已备份到历史:", backup)
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
		if err := copyDir(src, target); err != nil {
			return err
		}
		fmt.Println("已恢复:", name, ts)
		fmt.Println("注意: 下次 sync 时若云端有更早版本，可能会再次覆盖；建议先 sync 再决定")
		return nil
	case "clean":
		if len(args) < 2 {
			return fmt.Errorf("用法: gssync history clean <存档名|all>")
		}
		if args[1] == "all" {
			return os.RemoveAll(root)
		}
		return os.RemoveAll(filepath.Join(root, args[1]))
	default:
		return fmt.Errorf("未知子命令: %s (可用: list/restore/clean)", args[0])
	}
}

// ---- delete 命令 ----

func cmdDelete(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("用法: gssync delete <存档名>")
	}
	name := args[0]
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	prof, err := cfg.game()
	if err != nil {
		return err
	}

	// 1. 本机：归档到历史后删除
	target := filepath.Join(prof.SaveDir, name)
	if _, err := os.Stat(target); err == nil {
		ts, err := archiveSaveToHistory(cfg, prof, name)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return err
		}
		fmt.Println("本机已删除并归档到历史:", ts)
	} else {
		fmt.Println("本机无此存档")
	}

	// 2. 云端：删除资产与清单条目
	token, err := getToken()
	if err != nil {
		return err
	}
	gh := NewGH(token, cfg.Repo)
	rel, err := gh.EnsureRelease(cfg.ReleaseTag)
	if err != nil {
		return err
	}
	cm := newCloudManifest()
	if a := gh.AssetByName(rel, "manifest.json"); a != nil {
		data, err := gh.DownloadAsset(*a)
		if err != nil {
			return fmt.Errorf("下载云端清单失败: %w", err)
		}
		if json.Unmarshal(data, cm) == nil && cm.Saves == nil {
			cm.Saves = map[string]*CloudSave{}
		}
	}
	if cs, ok := cm.Saves[name]; ok {
		if a := gh.AssetByName(rel, cs.Asset); a != nil {
			if err := gh.DeleteAsset(a.ID); err != nil {
				return fmt.Errorf("删除云端资产失败: %w", err)
			}
		}
		delete(cm.Saves, name)
		cm.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		cm.UpdatedBy = cfg.Device
		data, _ := json.MarshalIndent(cm, "", "  ")
		if err := gh.ReplaceAsset(rel, "manifest.json", data); err != nil {
			return err
		}
		fmt.Println("云端已删除:", name)
	} else {
		fmt.Println("云端无此存档")
	}

	// 3. 更新本地状态
	st := loadState()
	delete(st.Baseline.Saves, name)
	if st.Local != nil {
		delete(st.Local.Saves, name)
	}
	return saveState(st)
}

// ---- 工具 ----

func dirSize(path string) int64 {
	var total int64
	filepath.WalkDir(path, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}
