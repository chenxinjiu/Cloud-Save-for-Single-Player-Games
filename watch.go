package main

import (
	"fmt"
	"time"
)

// cmdWatch 守护模式：周期性双向同步。
// 引擎内部保证：游戏运行中只上传不下载，避免坏档。
func cmdWatch(args []string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	prof, err := cfg.game()
	if err != nil {
		return err
	}
	fmt.Printf("gssync 守护模式已启动（Ctrl+C 退出）\n")
	fmt.Printf("设备: %s | 仓库: %s | 游戏: %s | 轮询: %ds\n",
		cfg.Device, cfg.Repo, prof.Name, cfg.PollSeconds)

	failCount := 0
	for {
		start := time.Now()
		summary, err := doSync(cfg, SyncOpts{Quiet: true})
		if err != nil {
			failCount++
			fmt.Printf("[%s] 同步出错(连续第 %d 次): %v\n",
				time.Now().Format("15:04:05"), failCount, err)
		} else {
			failCount = 0
			if summary != nil && !summary.empty() {
				summary.print(cfg.Device)
			}
		}

		wait := time.Duration(cfg.PollSeconds) * time.Second
		if failCount > 0 {
			backoff := wait * time.Duration(1<<min(failCount, 5))
			if backoff > 5*time.Minute {
				backoff = 5 * time.Minute
			}
			wait = backoff
		}
		if elapsed := time.Since(start); elapsed < wait {
			time.Sleep(wait - elapsed)
		}
	}
}
