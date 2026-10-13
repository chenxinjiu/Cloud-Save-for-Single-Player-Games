//go:build !windows

package main

import "errors"

// 非 Windows 平台不支持凭据管理器，退化为环境变量
func credWrite(repo, token string) error {
	return errors.New("仅 Windows 支持凭据管理器存储，请设置环境变量 GITHUB_TOKEN")
}
func credRead() (string, string, error) {
	return "", "", errors.New("请设置环境变量 GITHUB_TOKEN")
}
func credDelete() error { return nil }
