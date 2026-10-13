//go:build windows

package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// GUI 通过捕获命令的标准输出来显示结果，这里验证该机制
func TestCaptureOutput(t *testing.T) {
	got, err := captureOutput(func() error {
		fmt.Println("hello 世界")
		return nil
	})
	if err != nil {
		t.Fatalf("不应报错: %v", err)
	}
	if !strings.Contains(got, "hello 世界") {
		t.Fatalf("未捕获到输出: %q", got)
	}
}

func TestCaptureOutputOnError(t *testing.T) {
	want := errors.New("boom")
	got, err := captureOutput(func() error {
		fmt.Println("partial")
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("应透传错误, 实际: %v", err)
	}
	if !strings.Contains(got, "partial") {
		t.Fatalf("出错路径下也应捕获已产生的输出: %q", got)
	}
}
