# gssync

**单机游戏云存档同步工具** · Cloud save sync for single-player games

用你自己的 GitHub **私有仓库**当存储，在**多台电脑**之间同步游戏存档：
这边玩完点【上传】，换台电脑点【拉取】，存档自动对齐。
不依赖任何第三方网盘或服务器，只用你自己的 GitHub 账号；仓库私有，别人看不到。

> 支持**任意单机游戏**；对《远行星号（Starsector）》有内置规则
> （存档 + MOD + `vmparams` 启动参数一起同步）。

## 下载

- 普通用户：下载本仓库根目录的 **`gssync-gui.exe`**，双击即用（绿色免安装，无需 Go / Java）。
- 需要命令行的：再下载 `gssync.exe`。
- 详细教程：[使用教程.txt](使用教程.txt)（中文）· [User-Guide.txt](User-Guide.txt)（English）。

## 特性

- **手动可控**：只在点【上传】/【拉取】/【查看状态】时才同步，不偷偷跑后台进程。
- **防坏档**：检测到游戏正在运行会拒绝【拉取】，避免覆盖正在写入的存档。
- **MOD 同步**（远行星号）：mod 本体按内容增量上传 / 拉取，启用列表自动对齐。
- **冲突保护**：被覆盖的旧存档自动归档到本地历史，可随时找回。
- **自动更新**：打开程序自动检查新版本并替换（依赖本仓库根目录的 `version.txt`）。
- **安全**：GitHub Token 只存 Windows 凭据管理器（DPAPI 加密），配置文件不含任何密钥。
- **通用**：存档识别规则可配置（`preset` / `save_mode` / `include` / `exclude` / 时间来源 / 额外文件 / MOD 目录）。

## 快速开始

1. 下载 `gssync-gui.exe`，双击打开。
2. 切到【设置】页：
   - **GitHub 仓库**：填仓库名即可（保存时自动补成 `账号/仓库名`；不存在会自动创建私有仓库）。
   - **Token**：到 [这里新建](https://github.com/settings/tokens/new?scopes=repo&description=gssync)，勾选 `repo`，粘贴进来。
   - **游戏类型 / 游戏根目录 / 存档目录**：按提示填写（远行星号选 `starsector`；其它游戏选 `generic` 并指定存档目录）。
3. 点【测试连接】→【保存设置】。
4. 回到【同步】页：玩完点【上传存档】，换电脑点【拉取存档】。

多台电脑互通的前提：**同一个仓库 + 同一个 Token**（同一账号）；各机设备名可以不同。
工具发给别人时，建议让对方用**他自己的**账号 / 仓库 / Token，各用各的。

## 工作原理

- 存档存放在 GitHub 仓库的 **Release 资产**里（滚动替换同名资产，仓库体积保持恒定）。
- 本地状态文件记录同步基线，避免重复传输；内容按 SHA256 校验。
- 与云端冲突时以存档时间较新者为准，输家自动归档。

## 从源码构建

需要 Go 1.27+（Windows）：

```powershell
# CLI 版（带控制台）
go build -ldflags "-s -w" -o gssync.exe .

# GUI 版（无控制台；无参数启动即打开窗口）
go build -ldflags "-s -w -H=windowsgui -X main.defaultMode=gui" -o gssync-gui.exe .
```

> **发布新版本清单**（自更新依赖，务必照做）：
> ① 改 `main.go` 的 `version`；② 把仓库根 `version.txt` 改成同一版本号；
> ③ 重新构建两个 exe；④ 与 `version.txt` 一起提交并推送。

## 许可证

[MIT](LICENSE)

---

## English

**gssync** is a Windows tool that syncs single-player game saves across multiple
PCs, using your own **private GitHub repository** as storage. Upload here, pull
there — that's it. No third-party cloud; only your GitHub account, and the repo
stays private.

Works for **any single-player game**; Starsector has a built-in rule
(saves + mods + `vmparams` synced together).

- **Download**: grab **`gssync-gui.exe`** from this repo root and double-click to
  run (portable — no Go/Java needed). CLI users: also grab `gssync.exe`.
- **Docs**: [User-Guide.txt](User-Guide.txt) (English) ·
  [使用教程.txt](使用教程.txt) (中文).
- **Setup**: open the app → **Settings** tab → fill GitHub repo + token
  (`repo` scope) + game root / save folder → **Test connection** → **Save**.
- **Multi-PC**: use the **same repo and same token** on every PC.
- **Build**: `go build -ldflags "-s -w -H=windowsgui -X main.defaultMode=gui" -o gssync-gui.exe .`
- **License**: MIT.

## Repository layout

| Path | Description |
|---|---|
| `gssync-gui.exe` / `gssync.exe` | Prebuilt executables (for direct download / auto-update) |
| `version.txt` | Currently published version (used by auto-update) |
| `使用教程.txt` / `User-Guide.txt` | End-user guides (Chinese / English) |
| `*.go` | Source code (single `package main`) |
| `CHANGELOG.md` | Changelog |
