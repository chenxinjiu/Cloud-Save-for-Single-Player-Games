//go:build !windows

package main

import "errors"

// cmdGUI 图形界面仅支持 Windows
func cmdGUI(args []string) error {
	return errors.New("图形界面仅支持 Windows，请使用 gssync push / gssync pull")
}
