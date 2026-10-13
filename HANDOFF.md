# 工作交接（HANDOFF）

> 最后更新：2026-10-13（A机：对外开源，迁移到新公开仓库 + 脱敏，v0.7.0）
> 当前版本：**v0.7.0**（游戏规则参数化 + 迁移到新公开仓库；版本历史见第 9 节）
> 阅读对象：在另一台电脑上接手本项目的 Trae 会话 / 开发者。读完本文即可继续干活。
> 说明：本文已**脱敏**——本机用户名、设备名、绝对路径一律以占位符表示（`A机` / `B机` / `%USERPROFILE%` / `<游戏安装目录>`）。

---

## 0. 迁移通告（2026-10-13）：各机要做的事

**背景**：代码仓库已从旧的私有仓库 `chenxinjiu/CXJ-gssync` 迁移到新的**公开**仓库 `chenxinjiu/Cloud-Save-for-Single-Player-Games`（v0.7.0）。旧仓库保留作**私有归档**，勿公开、勿再向它发布新版本。

**B机（设备名 B机）需要做的（只需一次）**：
1. 下载新仓库根目录的 `gssync-gui.exe`（需要命令行再下 `gssync.exe`）：<https://github.com/chenxinjiu/Cloud-Save-for-Single-Player-Games>
2. 覆盖到本机 `%USERPROFILE%\.local\bin\gssync-gui.exe`（覆盖前先退出正在运行的程序）。
3. 若 B机有源码克隆：把远端 `origin` 指向新仓库（或直接重新 clone），别再用旧仓库地址。
4. **无需重新 init**：仓库 / Token / 游戏目录 / 存档仓库都不变，凭据与配置完全兼容。

**验证**：打开 `gssync-gui.exe`，状态栏应显示"已是最新版本 v0.7.0"（或能连上新仓库）；此后自动更新即走新仓库。

> ⚠️ **不要把旧私有仓库 `CXJ-gssync` 改成公开**——它的历史里含本机用户名 / 路径。

---

## 1. 这是什么

`gssync` 是一个单机游戏云存档同步工具（Go，Windows 桌面），把存档存到 **GitHub 私有仓库的 Release 资产**里（滚动替换同名资产，仓库体积不膨胀）。当前服务的游戏是 **Starsector（远行星号）**，但存档/进程按 profile 配置，理论上可扩展其他游戏。

- 代码仓库（**公开**，新）：<https://github.com/chenxinjiu/Cloud-Save-for-Single-Player-Games.git>（旧私有仓库 `CXJ-gssync` 仅作归档，不再更新）
- 存档仓库（**私有**）：`chenxinjiu/<私有存档仓库>`，Release tag = `gssync`
  - 地址：<https://github.com/chenxinjiu/<私有存档仓库>/releases/tag/gssync>（仓库名见本机 `config.json` 的 `repo` 字段）
  - 截至本次交接共 **15 个资产**：`common.zip` + 11 个 `save_*.zip` + `manifest.json` + `mods.json` + `vmparams`
- 两个仓库都已把编译好的 exe 入库（见第 6 节），没有 Go 环境的机器可直接下载 exe 使用。

## 2. 当前状态（一句话）

**手动控制模式已上线**：不再后台自动同步，用户双击桌面快捷方式打开小窗口，按【上传存档】/【拉取存档】才同步。后台 `watch` 进程已停、开机自启已删。`watch` 自动模式代码仍保留，手动/自动共存。

本开发机（设备名 A机）实测：`push` / `pull` 正常（当前本地与云端已一致），GUI 双击正常开窗，全部单元测试 + `go vet` 通过。

另一台电脑（设备名 B机）已于同日接棒并完成 v0.2.0 部署（见下一节末），两台机器统一为手动模式。

**v0.3.0 起支持 MOD 同步**：窗口新增【上传MOD】【拉取MOD】按钮（CLI 为 `mods-push` / `mods-pull`），mod 本体按内容差异上传 / 拉取，启用列表随之对齐（两台机器都需 ≥v0.3.0）。

**v0.4.0 起支持自动更新**：每次打开 GUI 自动检查云端版本（仓库根 `version.txt`），更高就自动下载替换、状态栏提示重启生效；A机手动升级到 v0.4.0 之后，后续版本即可自动更新。

**v0.5.0 起支持 GUI 内配置**：窗口改为「同步 / 设置」两个页签，「设置」页可直接填 GitHub 仓库 / Token / 游戏目录 / 设备名并一键保存（等价于命令行 `init`，`init` 保留）；**首次无配置文件也能开窗并自动停在设置页**（此前会直接报错退出）。

**v0.6.0 起支持任意单机游戏**：游戏规则参数化——`preset`（starsector / generic）、`save_mode`（subdir / file / single）、`include`/`exclude`（条目名 glob）、`save_time_source`（descriptor / mtime）、`extra_files`（随存档同步的配置文件）、`mods_dir`（留空=不做 mod 同步）。远行星号仍是内置预设，**老配置零改动继续工作**；【设置】页可切换"游戏类型"并填存档目录 / 识别模式 / 匹配规则 / 进程名。

## 3. 环境与路径（接手机器需按实际调整）

| 项 | 值 |
|---|---|
| 源码目录（本机） | Trae 工作区下的 `GameSaveSync`（路径随工作区迁移而变；换机直接 `git clone` 到任意目录即可） |
| 部署的 exe | `%USERPROFILE%\.local\bin\gssync.exe`、`gssync-gui.exe`（各约 10.5 MiB，与仓库内提交的 exe 一致） |
| 桌面快捷方式 | 桌面 `gssync 云存档.lnk` → `gssync-gui.exe` |
| 配置文件 | `%USERPROFILE%\.gssync\config.json`（设备名 A机） |
| GitHub Token | Windows 凭据管理器（credential target 名硬编码 `gssync`，DPAPI 加密）；也支持 `GITHUB_TOKEN` 环境变量 |
| 游戏目录 | `<游戏安装目录>`（存档在 `saves\`，12 个：common + 11 个 save_xx） |
| Go 工具链（免安装便携版） | `%USERPROFILE%\.local\share\go-portable\go\bin\go.exe`，go1.27.1 windows/amd64 |

> **目录迁移注意**：本机 Trae 工作区曾从 `...\TRAE SOLO CN\...` 迁移到 `...\Trae CN\...`（旧目录只剩 `TRAE_PROJECT_MOVED_*.md` 指针）。换电脑后直接 `git clone` 到任意目录即可，别用旧路径。

### 另一台电脑环境（设备名 B机，2026-10-12 接棒部署 v0.2.0）

| 项 | 值 |
|---|---|
| 源码目录 | B机 Trae 工作区下的 `GameSaveSync` |
| 部署的 exe | `%USERPROFILE%\.local\bin\gssync.exe`、`gssync-gui.exe`（约 10.5 MiB，与仓库一致） |
| 桌面快捷方式 | 桌面 `gssync 云存档.lnk` → `gssync-gui.exe`（该机桌面为自定义位置） |
| 配置文件 | `%USERPROFILE%\.gssync\config.json`（设备名 B机） |
| 游戏目录 | `<游戏安装目录>`（存档在 `saves\`，12 个：common + 11 个 save_xx） |
| Go 工具链 | 系统安装 go1.27.1（`go` 已在 PATH）；构建前设 `$env:GOPROXY="https://goproxy.cn,direct"`、`$env:GOSUMDB="sum.golang.google.cn"` |
| 部署验证 | 新 exe 直接可用：`status` 12 项全部一致；`go vet` + 全部单元测试通过；**无需重新 init**（旧配置与凭据完全兼容） |

### 构建必需的环境变量（A机，PowerShell）

```powershell
$env:GOROOT="$env:USERPROFILE\.local\share\go-portable\go"
$env:GOPATH="$env:USERPROFILE\.local\share\gopath"
$env:GOCACHE="$env:USERPROFILE\.local\share\gocache"
$env:GOPROXY="https://goproxy.cn,direct"
$env:GOSUMDB="sum.golang.google.cn"
```

## 4. 源码地图

单包 `package main`，模块名 `gssync`（go 1.27）。

| 文件 | 职责 |
|---|---|
| `main.go` | 入口、命令分发（init/status/sync/**push/pull/gui**/watch/history/delete/update/version）、版本号 `0.6.0`、`defaultMode`（构建期注入，GUI 版无参数启动直接开窗） |
| `config.go` | `~/.gssync/config.json` 结构与加载、`GameProfile`（含识别规则字段）、**`GameProfile.normalize()` 预设填充**、`errNotInitialized` 哨兵、`saveConfig` 原子写 |
| `settings.go` | **v0.5.0 GUI 设置页逻辑**（不依赖 walk）：`Settings`、`loadOrDefault`（无配置回退骨架）、`validateRepoFormat/validateSettings`、`resolveToken`、`applySettings/buildConfig`（只改对应字段）、`testConnection`（只读校验）、`SaveSettings`（联网校验→可选建库→写凭据与配置）；`credentialReader/Writer` 为可测替换点 |
| `gh.go` | GitHub Release API：EnsureRelease / 资产上传下载删除 / 清单 |
| `local.go` | 本地扫描：`hashDir`/`hashFileEntry`/`scanEntryMeta`、zip 打包解包、`parseDescriptor`（descriptor.xml 解析）、`modsInfo(prof)`（按 `ModsDir` 参数化） |
| `rule.go` | **v0.6.0 存档识别规则**：`saveEntry`、`selectSaveEntries`（subdir/file/single）、`entryFor`、`matchAny`（glob，大小写不敏感）、`resolveSaveTime`（descriptor/mtime）、`extraAssetName` |
| `sync.go` | 同步核心。`SyncOpts{UploadOnly, DownloadOnly, ResetCloud, ForceUpload, ForceDownload}`；`syncOne/syncMods/syncExtraFiles`；冲突按存档时间取新，输家归档 |
| `history.go` | 本地历史版本归档 / list / restore / clean |
| `mods.go` | **v0.3.0 MOD 同步**：`mods-push`（按规范化哈希只传新增/改动）、`mods-pull`（以云端为准覆盖 + 重写启用列表）；云端注册表 `mods_files.json`；资产名 `mod_<目录名sha256前16位>.zip`（纯 ASCII）；`uploadModsList` 顺带同步 mods.json 与 manifest 的 mods 字段 |
| `watch.go` | 守护轮询模式（游戏运行中只上传不下载） |
| `update.go` | **v0.4.0 自更新**：`runSelfUpdate`（打开 GUI 自动检查 + `gssync update [--check]`）、git blob 完整性校验、`.old-*` 改名替换、启动时 `cleanupOldExes`；云端版本来自仓库根 `version.txt` |
| `version.txt` | 仓库根目录：**当前已发布的版本号**（发布新版本时必须与 `main.go` 的 `version` 同步更新，见第 6 节） |
| `gui_windows.go` | GUI：`github.com/lxn/walk` 声明式窗口，**v0.5.0 起为「同步 / 设置」两页签**（`TabWidget`），输出框与状态栏两页共用；设置页含仓库/Token/目录/设备名/Release Tag/应用仓库输入 + 【测试连接】【保存设置】【浏览…】；`runAsync` 用 goroutine + `mw.Synchronize` 防阻塞；`captureOutput` 用 `os.Pipe` 捕获命令输出（仅同步页命令） |
| `gui_other.go` | 非 Windows 下 `gui` 命令报"仅支持 Windows" |
| `console_windows.go` / `console_other.go` | 控制台初始化（隐藏/分配控制台的平台差异） |
| `cred_windows.go` / `cred_other.go` | 凭据存取（Win 用 wincred/DPAPI，其他平台 stub） |
| `app.manifest` + `rsrc.syso` | 嵌入 Common Controls v6 清单（PerMonitorV2 DPI aware）。**没有它 walk 会报 `TTM_ADDTOOL failed` 崩溃**。syso 由 rsrc 生成（见第 6 节） |
| `sync_test.go` | EndToEnd、NoModsDirKeepsCloudMods、**TestForcePushAndPull**；`mockGH` 含仓库存在性/建库路由 |
| `settings_test.go` | **v0.5.0**：字段保留、仓库格式校验、保存往返、`config.json` 不含 Token、自动建库、越权拒绝、测试连接（用 `swapCredStore` 替换凭据读写，避免污染真实凭据） |
| `save_rule_test.go` | **v0.6.0**：subdir/file/single 识别、include/exclude 通配、descriptor vs mtime 时间、通用模式 file+额外文件端到端、老配置（无 preset）兼容 |
| `mods_test.go` | **TestModsPushPull**（两台设备往返，含中文目录名 mod）、TestModsPullNoData |
| `update_test.go` | **TestSelfUpdate**（版本比较 / 替换 / `.old` 备份清理 / `--check` / 无 version.txt 容错） |
| `gui_windows_test.go` | TestCaptureOutput / OnError |
| `real_scan_test.go` | 真实目录扫描测试 |

依赖（go.mod）：`github.com/lxn/walk`、`lxn/win`、`govaluate`（GUI）、`github.com/danieljoos/wincred`（凭据）、`golang.org/x/sys`。

### push/pull 的强制语义（v0.2.0 核心改动，在 sync.go）

- **push（ForceUpload）**：本机有的存档无条件 `applyUpload`（与云端 SHA 相同则跳过传输），不做新旧比较、不报冲突；防呆检查"云端清单空 + 基线非空"对 ForceUpload 放行。
- **pull（ForceDownload）**：云端有的存档无条件 `applyDownload`，被覆盖的本机旧版自动归档到 `history/`（结果记 Downloaded）；强制按云端重算 mods/vmparams 基线，且视为游戏未运行。
- 本机有/云端无（或反向）时保守不删除，避免误删。
- `pull` 在 CLI 层先调 `gameRunning(prof.ProcessNames)`，游戏在运行直接拒绝（防坏档）；`push` 不拦。

## 5. 命令用法

```text
gssync init              初始配置（游戏目录 / GitHub Token / 私有仓库）
gssync gui               打开图形界面（上传 / 拉取 / 查看状态）
gssync push              手动上传：以本机为准，强制覆盖云端
gssync pull              手动拉取：以云端为准，强制覆盖本机（旧版归档；游戏运行中拒绝）
gssync mods-push         上传 MOD：把本机新增/改动的 mod 打包上传云端
gssync mods-pull         拉取 MOD：以云端为准下载 mod 覆盖本机（游戏运行中拒绝）
gssync update            检查并下载更新（打开 GUI 时也会自动检查；--check 只检查）
gssync version           显示版本号
gssync status            查看本地与云端差异
gssync sync              双向同步一次（--upload-only / --download-only / --reset-cloud）
gssync watch             守护模式：持续自动同步（游戏运行中只上传）
gssync history list [存档名]
gssync history restore <存档名> <时间戳>
gssync history clean <存档名|all>
gssync delete <存档名>
```

## 6. 构建与发布

```powershell
# 设好第 3 节的 GOROOT/GOPATH/GOCACHE/GOPROXY/GOSUMDB 后，在源码目录：

# CLI 版（有控制台）
& "$env:GOROOT\bin\go.exe" build -ldflags "-s -w" -o gssync.exe .

# GUI 版（无控制台黑窗；无参数启动即 gui）
& "$env:GOROOT\bin\go.exe" build -ldflags "-s -w -H=windowsgui -X main.defaultMode=gui" -o gssync-gui.exe .
```

- **发布新版本清单（自更新依赖，务必照做）**：① 改 `main.go` 的 `version`；② 更新仓库根目录 `version.txt` 为同一版本号；③ `go vet` + `go test`；④ 构建两个 exe；⑤ 提交并推送（两个 exe 与 `version.txt` 一起）。
- 两个 exe 都约 10.5 MiB。`rsrc.syso` 已入库，正常情况**不用**重新生成；若改了 `app.manifest`，用 `%GOPATH%\bin\rsrc.exe -manifest app.manifest -arch amd64 -o rsrc.syso`（rsrc 已 `go install github.com/akavel/rsrc@latest` 装过）。
- 发布到另一台机器：exe 已提交进代码仓库，直接从 GitHub 下载 `gssync-gui.exe`（需要 CLI 就两个都下）覆盖到目标机器，再 `gssync init` 一次（Token 需交互粘贴，见第 8 节）。
- Go 构建时 stderr 常被 PowerShell 当 NativeCommandError 标红，**EXIT=0 就是成功**，不用慌。

## 7. 测试

```powershell
& "$env:GOROOT\bin\go.exe" vet ./...
& "$env:GOROOT\bin\go.exe" test ./...
```

- 全部通过：`TestEndToEndSync`、`TestNoModsDirKeepsCloudMods`、`TestForcePushAndPull`、`TestCaptureOutput*`。
- 测试里切换设备家目录必须 `os.Setenv("GSSYNC_HOME", homeA)`。
- 真实云端基准（核对用）：`mods.json` = 1177 字节，digest `sha256:23519c22891616d7b13935ce9d058c2da22c116b8d7729105dfacfac1c4cafc4`；`vmparams` digest 前缀 `d07307c9...`。

## 8. 已知坑 / 操作经验（重要，避免重踩）

1. **Token**：`init` 必须由用户在终端**交互粘贴**；禁止在命令行用 `$env:GITHUB_TOKEN=ghp_...` 传真实 token（会被安全策略拦截，也会留痕）。文档里也不要写 token。
2. **推送到 main** 可能被沙箱安全策略拦截；需先取得用户明确授权，再用 `dangerouslyDisableSandbox: true` 执行 `git push`。
3. **PowerShell 是 5.1**：不支持 `&&` / heredoc；多 `-m` 拼多行 commit；`.ps1` 脚本默认被执行策略拦（用 `.cmd`，如 `npm.cmd`）；不要用 cmd.exe 语法。
4. **BOM 坑**：`Set-Content -Encoding UTF8` 会写 BOM，Go 解析 JSON 会吃到 `﻿` 报错。写无 BOM UTF-8 用：
   `[System.IO.File]::WriteAllText($p,$c,(New-Object System.Text.UTF8Encoding($false)))`
5. **CRLF**：仓库 `core.autocrlf=true`，无 `.gitattributes`。目录迁移后可能出现"8 个 .go 显示 modified 但 diff 为空、blob hash 完全一致"的幻象（stat cache 问题），`git add -u` 刷新索引即恢复干净，**不要**当真实改动提交。
6. 沙箱对 `~/.gssync`、`Startup` 启动目录、桌面等写操作会被拦；建/删快捷方式走 COM（WScript.Shell / Scripting.FileSystemObject），或经用户授权后关沙箱执行。
7. walk GUI 必须随 exe 嵌入 v6 清单（即 rsrc.syso），否则 tooltip 初始化崩溃。
8. GitHub MCP 工具（`get_release_by_tag` 等）只读核对很好用；响应过大时结果会落盘成 txt，用正则/ConvertFrom-Json 解析，注意内容是转义过的 JSON。
9. **网络 / 代理约定（全局，跨机器）**：用户在中国大陆，直连 `github.com` 经常超时或被重置。**`git fetch/pull/push` 或访问 GitHub 失败时，第一时间请用户开启全局代理**，不要反复硬重试，也不要绕路（如 `--filter=blob:none` 部分克隆、稀疏检出——这些会在对象库留下缺失 blob，导致后续 `pull` 报 `unable to read sha1 file` 假象）。用户随时在线。直连失败的典型报错：`Failed to connect to github.com port 443`、`Empty reply from server`、`Recv failure: Connection was reset`、`early EOF`。代理开启后普通 `git pull` / `git push` 即可完成。

## 9. 版本历史（摘要，详见 CHANGELOG.md）

- **v0.7.0**（2026-10-13）：**对外开源**——迁移到公开仓库 `chenxinjiu/Cloud-Save-for-Single-Player-Games`（`defaultAppRepo` 改指新仓库；代码/文档脱敏；新增 README + LICENSE + 中英使用教程）。功能同 v0.6.0。
- **v0.6.0**（2026-10-13）：**支持任意单机游戏**（`preset`/`save_mode`/`include`/`exclude`/`save_time_source`/`extra_files`/`mods_dir` 参数化；远行星号为内置预设，老配置零改动；`extras` 取代硬编码 `vmparams`）。
- **v0.5.0**（2026-10-13）：**GUI「设置」页**（窗口改「同步 / 设置」两页签，可直接填仓库/Token/游戏目录/设备名并保存；【测试连接】只读校验；无配置也能开窗并停在设置页；`config.json` 原子写）。
- **v0.4.0**（2026-10-12）：**自动更新**（打开 GUI 自动检查云端 `version.txt`，仅更高版本才下载替换；`gssync update [--check]`；git blob 校验 + `.old-*` 改名替换 + 启动清理）。
- **v0.3.0**（2026-10-12）：**MOD 同步**（`mods-push` / `mods-pull` + GUI 两按钮 + 云端注册表 `mods_files.json`，资产名纯 ASCII；首次全传约 200MB，之后增量；pull 游戏运行中拒绝）。
- **v0.2.0**（2026-10-12，`31602c1`）：手动 push/pull + GUI 小窗口 + `gssync-gui.exe`；pull 游戏运行中拒绝；关 watch/开机自启。
- **v0.1.1**（`8a831e7`）：① mods 目录缺失时返回 nil，不再上传空 mods.json 覆盖云端；② mods 哈希一致时静默对齐基线，消除首同步误报；加回归测试。第二台电脑（A机）12/12 存档继承 + 逐文件 SHA256 全部通过。
- **v0.1.0**（`932cbbe` 起）：首版，Release 资产双向同步、mod 校验、vmparams、watch 防坏档、冲突取新 + 双向归档、凭据进凭据管理器。

## 10. 待办 / 下次可继续

- [ ] ⏳ **B机待办**：迁移到 v0.7.0 / 新公开仓库——下载新仓库的 `gssync-gui.exe` 覆盖 `%USERPROFILE%\.local\bin\`（或把 `config.json` 的 `app_repo` 指向 `chenxinjiu/Cloud-Save-for-Single-Player-Games`）；**无需重新 init**。详见第 0 节。
- [ ] 已向用户提议、**等拍板**：给【拉取】加二次确认弹窗；窗口显示"上次同步时间"。
- [x] ✅ A机已于 2026-10-13 部署到 **v0.5.0**（exe 覆盖到 `.local\bin`，GUI「设置」页可用）；此后新版本由应用自动更新，无需手动换 exe。
- [ ] 给别人使用 / 换其它游戏时：下载 `gssync-gui.exe`（如需命令行再加 `gssync.exe`）→ 打开 → 在「设置」页填**自己的**仓库 / Token / 游戏根目录（或存档目录）→ 选**游戏类型**（远行星号 / 通用）与**识别模式**、必要时填**匹配规则**（如 `*.sav`）与**进程名** → 保存（会按需在自己账号下创建私有仓库）。
- [ ] 首次【上传MOD】需全量上传（约 236MB，压缩后约 200MB），之后只传新增 / 改动。
- [x] ✅ 另一台电脑（B机）已于 2026-10-12 完成 v0.2.0 部署：exe 覆到 `%USERPROFILE%\.local\bin\`、桌面建 `gssync 云存档.lnk`、`status` 验证通过（配置/凭据兼容，**无需 init**）。
- [ ] 若想恢复全自动：启动 `gssync watch` 并把快捷方式放回 Startup（代码仍支持）。
- [x] 工作树干净、无未跟踪文件（旧的 `启动存档同步.cmd` 已于 2026-10-12 删除）；HEAD = `origin/main`。
