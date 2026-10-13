//go:build windows

package main

import "syscall"

// initConsole 将控制台输出代码页设为 UTF-8，避免中文乱码
func initConsole() {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	p := k32.NewProc("SetConsoleOutputCP")
	_, _, _ = p.Call(65001)
}
