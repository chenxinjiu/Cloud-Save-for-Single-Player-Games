package main

import (
	"fmt"
	"os"
	"testing"
)

// TestRealScan 用真实游戏目录做只读冒烟验证（默认跳过）。
// 运行: GSSYNC_REAL_GAME_DIR=D:\Games\Starsector go test -run TestRealScan -v
func TestRealScan(t *testing.T) {
	gameRoot := os.Getenv("GSSYNC_REAL_GAME_DIR")
	if gameRoot == "" {
		t.Skip("未设置 GSSYNC_REAL_GAME_DIR，跳过真实数据测试")
	}
	s := &Syncer{prof: &GameProfile{
		Name: "starsector", GameRoot: gameRoot, SaveDir: gameRoot + `\saves`,
	}, state: &StateFile{}}
	ls, err := s.scanLocal()
	if err != nil {
		t.Fatalf("扫描失败: %v", err)
	}
	for name, m := range ls.Saves {
		fmt.Printf("%-40s 角色=%-10s 版本=%-10s 时间=%s 大字节数=%d\n",
			name, m.Character, m.GameVersion, m.SaveDate, m.Size)
	}
	fmt.Printf("mod: 已安装 %d, 已启用 %d\n", len(ls.Mods.Installed), len(ls.Mods.Enabled))
	if ls.VmSHA256 != "" {
		fmt.Println("vmparams: 存在")
	}
}
