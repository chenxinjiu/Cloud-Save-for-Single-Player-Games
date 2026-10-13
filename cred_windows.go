//go:build windows

package main

import (
	"fmt"

	"github.com/danieljoos/wincred"
)

// Windows 凭据管理器存储 GitHub Token（DPAPI 加密，按用户绑定，不落明文）

const credTarget = "gssync"

func credWrite(repo, token string) error {
	cred := wincred.NewGenericCredential(credTarget)
	cred.UserName = repo
	cred.CredentialBlob = []byte(token)
	if err := cred.Write(); err != nil {
		return fmt.Errorf("写入凭据管理器失败: %w", err)
	}
	return nil
}

func credRead() (repo string, token string, err error) {
	c, err := wincred.GetGenericCredential(credTarget)
	if err != nil {
		return "", "", fmt.Errorf("读取凭据管理器失败: %w", err)
	}
	return c.UserName, string(c.CredentialBlob), nil
}

func credDelete() error {
	c, err := wincred.GetGenericCredential(credTarget)
	if err != nil {
		return nil // 本来就不存在
	}
	if err := c.Delete(); err != nil {
		return fmt.Errorf("删除凭据失败: %w", err)
	}
	return nil
}
