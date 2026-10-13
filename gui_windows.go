//go:build windows

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
)

// cmdGUI 打开图形界面：分「同步」「设置」两个页签。
// 同步页：上传/拉取/状态/上传MOD/拉取MOD（手动控制）；设置页：填写仓库/Token/目录并保存。
func cmdGUI(args []string) error {
	_, wasExisting, err := loadOrDefault()
	if err != nil {
		return err
	}

	var mw *walk.MainWindow
	var tabs *walk.TabWidget
	var out *walk.TextEdit
	var statusLbl *walk.Label
	var btnPush, btnPull, btnStatus, btnModsPush, btnModsPull *walk.PushButton

	// 同步页信息标签
	var lblDev, lblRepo, lblRoot *walk.Label

	// 设置页控件
	var edRepo, edRoot, edSaveDir, edInclude, edProcess, edDevice, edTag, edAppRepo *walk.LineEdit
	var edToken *walk.LineEdit // 密码模式
	var cbPreset, cbMode *walk.ComboBox
	var lblTokenState, lblHint *walk.Label
	var chkCreate *walk.CheckBox
	var btnTest, btnSave, btnBrowse *walk.PushButton

	running := false
	syncAvailable := wasExisting

	setBusy := func(b bool) {
		running = b
		en := !b && syncAvailable
		btnPush.SetEnabled(en)
		btnPull.SetEnabled(en)
		btnStatus.SetEnabled(en)
		btnModsPush.SetEnabled(en)
		btnModsPull.SetEnabled(en)
		btnTest.SetEnabled(!b)
		btnSave.SetEnabled(!b)
		btnBrowse.SetEnabled(!b)
	}

	// runAsync 在后台执行一次操作，避免阻塞界面；after 在 UI 更新后回调（可刷新界面）
	runAsync := func(name string, fn func() (string, error), after func()) {
		if running {
			return
		}
		setBusy(true)
		statusLbl.SetText(name + " 进行中，请稍候...")
		out.SetText("")
		go func() {
			text, ferr := fn()
			mw.Synchronize(func() {
				setBusy(false)
				if ferr != nil {
					statusLbl.SetText(name + " 失败")
					out.SetText(text + "\n【失败】" + ferr.Error())
				} else {
					statusLbl.SetText(name + " 完成")
					if text == "" {
						text = "（无变化）"
					}
					out.SetText(text)
				}
				if after != nil {
					after()
				}
			})
		}()
	}

	// readForm 在 UI 线程读取设置页控件值
	readForm := func() Settings {
		return Settings{
			Repo:         edRepo.Text(),
			Token:        edToken.Text(),
			Preset:       cbPreset.Text(),
			GameRoot:     edRoot.Text(),
			SaveDir:      edSaveDir.Text(),
			SaveMode:     cbMode.Text(),
			Include:      edInclude.Text(),
			ProcessNames: edProcess.Text(),
			Device:       edDevice.Text(),
			ReleaseTag:   edTag.Text(),
			AppRepo:      edAppRepo.Text(),
		}
	}

	// setCombo 选中下拉框里与 cur 匹配的项（无匹配则选第一项）
	setCombo := func(cb *walk.ComboBox, items []string, cur string) {
		for i, it := range items {
			if it == cur {
				cb.SetCurrentIndex(i)
				return
			}
		}
		cb.SetCurrentIndex(0)
	}

	refreshTokenState := func() {
		repo, tok, err := credRead()
		if err == nil && strings.TrimSpace(tok) != "" {
			lblTokenState.SetText("已保存（仓库 " + repo + "）")
			return
		}
		lblTokenState.SetText("未配置")
	}

	// refreshInfo 从磁盘重新读取配置，刷新两页的展示
	refreshInfo := func() {
		cfg, existed, err := loadOrDefault()
		if err != nil {
			statusLbl.SetText("读取配置失败: " + err.Error())
			return
		}
		syncAvailable = existed
		setBusy(false)

		lblDev.SetText(cfg.Device)
		lblRepo.SetText(cfg.Repo)
		var p *GameProfile
		if gp, e := cfg.game(); e == nil {
			p = gp
		}
		if p != nil {
			lblRoot.SetText(p.SaveDir)
		} else {
			lblRoot.SetText("")
		}

		edRepo.SetText(cfg.Repo)
		edDevice.SetText(cfg.Device)
		edTag.SetText(cfg.ReleaseTag)
		edAppRepo.SetText(cfg.AppRepo)
		if p != nil {
			edRoot.SetText(p.GameRoot)
			edSaveDir.SetText(p.SaveDir)
			edInclude.SetText(strings.Join(p.Include, ", "))
			edProcess.SetText(strings.Join(p.ProcessNames, ", "))
			setCombo(cbPreset, []string{PresetStarsector, PresetGeneric}, p.Preset)
			setCombo(cbMode, []string{SaveModeSubdir, SaveModeFile, SaveModeSingle}, p.SaveMode)
		}
		refreshTokenState()

		if !syncAvailable {
			statusLbl.SetText("尚未配置：请在「设置」页填写仓库、Token 与游戏目录后点【保存设置】。")
		}
	}

	onSave := func() {
		s := readForm()
		createIfMissing := chkCreate.Checked()
		runAsync("保存设置", func() (string, error) {
			return SaveSettings(s, createIfMissing)
		}, refreshInfo)
	}

	onTest := func() {
		s := readForm()
		runAsync("测试连接", func() (string, error) {
			token, err := resolveToken(s.Token)
			if err != nil {
				return "", err
			}
			login, exists, err := testConnection(token, s.Repo)
			if err != nil {
				return "", err
			}
			var b strings.Builder
			fmt.Fprintf(&b, "Token 有效，账号: %s\n", login)
			if strings.TrimSpace(s.Repo) == "" {
				b.WriteString("仓库: （未填写，可只填仓库名，保存时自动补为 账号/仓库名）")
				return b.String(), nil
			}
			if exists {
				fmt.Fprintf(&b, "仓库 %s 可访问 ✅", s.Repo)
			} else {
				fmt.Fprintf(&b, "仓库 %s 不存在或无权访问（保存时可勾选自动创建）", s.Repo)
			}
			return b.String(), nil
		}, nil)
	}

	onBrowse := func() {
		dlg := new(walk.FileDialog)
		dlg.Title = "选择存档目录（游戏保存存档的位置）"
		dlg.InitialDirPath = edSaveDir.Text()
		ok, err := dlg.ShowBrowseFolder(mw)
		if err != nil {
			statusLbl.SetText("选择目录失败: " + err.Error())
			return
		}
		if !ok {
			return
		}
		edSaveDir.SetText(dlg.FilePath)
	}

	ui := MainWindow{
		AssignTo: &mw,
		Title:    "gssync 云存档同步",
		MinSize:  Size{Width: 800, Height: 700},
		Size:     Size{Width: 800, Height: 700},
		Layout:   VBox{},
		Children: []Widget{
			TabWidget{
				AssignTo: &tabs,
				Pages: []TabPage{
					// ---------------- 同步页 ----------------
					{
						Title:  "同步",
						Layout: VBox{},
						Children: []Widget{
							Composite{
								Layout: Grid{Columns: 2},
								Children: []Widget{
									Label{Text: "设备:"},
									Label{AssignTo: &lblDev},
									Label{Text: "仓库:"},
									Label{AssignTo: &lblRepo},
									Label{Text: "存档目录:"},
									Label{AssignTo: &lblRoot},
								},
							},
							Composite{
								Layout: HBox{},
								Children: []Widget{
									PushButton{
										AssignTo: &btnPush,
										Text:     "上传存档（以本机为准）",
										OnClicked: func() {
											runAsync("上传", func() (string, error) {
												return captureOutput(func() error { return cmdPush(nil) })
											}, nil)
										},
									},
									PushButton{
										AssignTo: &btnPull,
										Text:     "拉取存档（覆盖本机）",
										OnClicked: func() {
											runAsync("拉取", func() (string, error) {
												return captureOutput(func() error { return cmdPull(nil) })
											}, nil)
										},
									},
									PushButton{
										AssignTo: &btnStatus,
										Text:     "查看状态",
										OnClicked: func() {
											runAsync("状态", func() (string, error) {
												return captureOutput(func() error { return cmdStatus(nil) })
											}, nil)
										},
									},
								},
							},
							Composite{
								Layout: HBox{},
								Children: []Widget{
									PushButton{
										AssignTo: &btnModsPush,
										Text:     "上传MOD（以本机为准）",
										OnClicked: func() {
											runAsync("上传MOD", func() (string, error) {
												return captureOutput(func() error { return cmdModsPush(nil) })
											}, nil)
										},
									},
									PushButton{
										AssignTo: &btnModsPull,
										Text:     "拉取MOD（覆盖本机）",
										OnClicked: func() {
											runAsync("拉取MOD", func() (string, error) {
												return captureOutput(func() error { return cmdModsPull(nil) })
											}, nil)
										},
									},
								},
							},
						},
					},
					// ---------------- 设置页 ----------------
					{
						Title:  "设置",
						Layout: VBox{},
						Children: []Widget{
							Composite{
								Layout: Grid{Columns: 2},
								Children: []Widget{
									Label{Text: "GitHub 仓库:"},
									LineEdit{AssignTo: &edRepo, CueBanner: "owner/name，或只填仓库名（保存时自动补 owner）"},
									Label{Text: "Token:"},
									LineEdit{AssignTo: &edToken, PasswordMode: true},
									Label{Text: "Token 状态:"},
									Label{AssignTo: &lblTokenState},
									Label{Text: "游戏类型:"},
									ComboBox{AssignTo: &cbPreset, Model: []string{PresetStarsector, PresetGeneric}, ToolTipText: "starsector=远行星号，generic=通用其它游戏"},
									Label{Text: "游戏根目录:"},
									LineEdit{AssignTo: &edRoot, CueBanner: `可留空，如 E:\yxx\Starsector`},
									Label{Text: "存档目录:"},
									Composite{
										Layout: HBox{},
										Children: []Widget{
											LineEdit{AssignTo: &edSaveDir, CueBanner: `存档所在目录；留空=跟随游戏根目录/saves`},
											PushButton{AssignTo: &btnBrowse, Text: "浏览…", OnClicked: onBrowse},
										},
									},
									Label{Text: "识别模式:"},
									ComboBox{AssignTo: &cbMode, Model: []string{SaveModeSubdir, SaveModeFile, SaveModeSingle}, ToolTipText: "subdir=每个子目录一个存档；file=每个文件一个；single=整个目录一个"},
									Label{Text: "匹配规则:"},
									LineEdit{AssignTo: &edInclude, CueBanner: "逗号分隔，如 *.sav（留空=全部）"},
									Label{Text: "进程名:"},
									LineEdit{AssignTo: &edProcess, CueBanner: "逗号分隔，如 game.exe（用于判断游戏是否在运行）"},
									Label{Text: "设备名:"},
									LineEdit{AssignTo: &edDevice, CueBanner: "区分多台电脑，默认取本机名"},
									Label{Text: "Release Tag:"},
									LineEdit{AssignTo: &edTag, CueBanner: "默认 gssync"},
									Label{Text: "应用仓库:"},
									LineEdit{AssignTo: &edAppRepo, CueBanner: "自动更新用，可留空"},
								},
							},
							Composite{
								Layout: HBox{},
								Children: []Widget{
									PushButton{AssignTo: &btnTest, Text: "测试连接", OnClicked: onTest},
									PushButton{AssignTo: &btnSave, Text: "保存设置", OnClicked: onSave},
									CheckBox{AssignTo: &chkCreate, Text: "仓库不存在时自动创建私有仓库", Checked: true},
								},
							},
							Label{AssignTo: &lblHint, Text: "Token 仅存 Windows 凭据管理器，不写入配置文件；Token 留空=沿用已保存的 Token。"},
						},
					},
				},
			},
			TextEdit{AssignTo: &out, ReadOnly: true, VScroll: true},
			Label{AssignTo: &statusLbl, Text: "就绪：存档与 MOD 都点【上传】保存到云端、点【拉取】同步到本机。"},
		},
	}

	if err := ui.Create(); err != nil {
		return err
	}

	refreshInfo()
	if !syncAvailable && tabs != nil {
		tabs.SetCurrentIndex(1) // 首次无配置 → 直接停在设置页
	}

	// 启动时自动检查更新（后台执行，不阻塞界面；失败不影响使用）
	go func() {
		time.Sleep(700 * time.Millisecond) // 等窗口就绪
		exe, err := os.Executable()
		if err != nil {
			return
		}
		dir := filepath.Dir(exe)
		cleanupOldExes(dir)
		cfg, _, cerr := loadOrDefault()
		if cerr != nil {
			return
		}
		res, uerr := runSelfUpdate(cfg, false, dir, func(string, ...any) {})
		mw.Synchronize(func() {
			if uerr != nil {
				statusLbl.SetText("更新检查失败（不影响使用）: " + uerr.Error())
				return
			}
			if res.Updated {
				statusLbl.SetText(fmt.Sprintf("✅ 已更新到 v%s：关闭窗口再打开即生效", res.RemoteVersion))
				return
			}
			statusLbl.SetText(fmt.Sprintf("已是最新版本 v%s", res.LocalVersion))
		})
	}()

	mw.Run()
	return nil
}

// captureOutput 捕获一次命令的标准输出（命令内部用 fmt.Println 输出结果）
func captureOutput(f func() error) (string, error) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", f()
	}
	os.Stdout = w

	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	ferr := f()
	_ = w.Close()
	<-done
	_ = r.Close()
	os.Stdout = old

	return buf.String(), ferr
}
