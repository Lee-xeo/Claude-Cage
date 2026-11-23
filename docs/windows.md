# Windows 安装、更新与发布

Cage 的 Windows 版本面向 Windows 10 22H2（内部版本 19045）和 Windows 11 的 x64 电脑，家庭版同样可用。安装后从开始菜单打开 Cage，导入自己的代理，再进入工作台。环境是一台 Debian 13 虚拟机，见[Debian 环境](guide/guest-systems.md)。

## 安装前准备

| 项目 | 要求 |
|---|---|
| 系统 | x64 Windows 10 22H2 或 Windows 11；不包含 Windows ARM64 与 32 位系统 |
| 虚拟化 | BIOS/UEFI 已启用处理器虚拟化，Windows 已启用“Windows 虚拟机监控程序平台” |
| 存储 | 使用本地磁盘，保留安装包、Linux 镜像、虚拟磁盘和更新备份所需空间；初始虚拟磁盘上限为 24 GiB |
| 代理 | 自己的 Clash/Mihomo YAML 来源或 HTTP/SOCKS5 节点；Cage 不提供代理服务 |

在“启用或关闭 Windows 功能”中启用“Windows 虚拟机监控程序平台”，按系统要求重启。也可在管理员终端执行：

```powershell
DISM /Online /Enable-Feature /FeatureName:HypervisorPlatform /All
```

这个系统功能需要一次管理员授权；Cage 日常运行和应用安装使用普通用户权限。企业电脑若禁止虚拟化，应由设备管理员启用。环境使用独立的 QEMU 虚拟机，不依赖 Hyper-V 管理工具、WSL 或 Docker。

## 安装并开始工作

1. 在[项目主页](https://claude-cage.com/#download)点击“下载应用”并选择 Windows，核对对应发行条目的 `SHA256SUMS`。签名安装包名为 `Cage-<版本>-windows-amd64-setup.exe`；单独标明未签名的开发预览包名为 `Cage-<版本>-windows-amd64-setup-unsigned.exe`。未签名包可能触发未知发布者提示，需手动安装和更新。
2. 运行安装程序，选择安装位置。首次安装时，默认选择本地 D 盘的 `D:\Cage`；没有本地 D 盘时使用当前用户的 `%LOCALAPPDATA%\Programs\Cage`。已有安装沿用原目录，也可以手动选择其他位置。安装器随附 .NET 桌面运行组件、WebView2 安装程序、QEMU、OpenSSH 客户端和代理核心，无需另装 Go、Node.js 或 Clash。
3. 在“快捷方式”页选择“添加桌面快捷方式”和“添加到开始菜单”。首次安装默认勾选两项，覆盖安装沿用上次选择，旧安装按已有快捷方式恢复选择。卸载会清理 Cage 创建的快捷方式。
4. 从选中的快捷方式启动 Cage。如果 C 盘空间有限，在初始化界面的“存储目录”中选择其他磁盘的专用空文件夹，再导入代理并启动环境；已有环境也可以从设置迁移。见[选盘与迁移步骤](getting-started.md#选择环境存储位置)。

安装器不设置系统代理，不安装 SSH 服务，不把随附工具加入系统 PATH。WebView2 是 Microsoft 提供的系统浏览器运行时，其安装与维护由 Microsoft 安装程序负责。

安装器逐文件解压到安装位置所在磁盘的暂存目录，成功后替换应用，避免在系统临时目录生成整包解压副本。Windows 临时目录仍可能保存少量安装器辅助文件；缺少 WebView2 时，Microsoft 安装程序会管理其共享运行组件的位置。

“打开项目”接受 Windows 文件夹，例如 `C:\Users\姓名\Projects\demo` 在环境内对应 `/c/Users/姓名/Projects/demo`。项目文件仍保存在原来的主机文件夹中，编辑和删除直接影响原文件；项目内的 `.git` 可读写，可在 Cage 中暂存、提交和切换分支。项目共享只限明确打开的目录，下载使用独立的暂存目录。

Windows 快捷键以 `Ctrl` 为主：`Ctrl+O` 打开项目、`Ctrl+T` 新建网页、`Ctrl+L` 定位地址栏、`Ctrl+W` 关闭标签，`Ctrl+Shift+Enter` 新建终端。终端使用 `Ctrl+Shift+C/V` 复制粘贴，`Ctrl+C` 中断命令。

网页输入框短暂失焦时，工作台会尝试恢复同一标签和窗格的键盘输入。恢复期间的点击、按键、切换标签、打开设置或切换窗口会取消这次自动恢复，保留用户选择的焦点。如果网页仍不接收按键，可重新点击输入框；尚未确认的输入法组合文字会在失焦时取消。

窗口仅保留系统标题栏，工作台直接占据下方区域。文件、项目和设置操作使用工作台入口与快捷键；检查应用更新和版本信息可从托盘图标的右键菜单打开。窗口首次打开时按当前屏幕的可用区域居中，并适配 Windows 显示缩放。手动调整后的窗口尺寸和最大化状态会保存；换屏或降低分辨率时，窗口保持在当前屏幕内。后台启动与版本检测不显示命令行窗口。

从开始菜单打开 Cage 时，完整版和轻量客户端的后台服务都会静默启动。关闭工作台窗口会保留环境与后台任务，可从托盘图标重新打开。要退出桌面程序，在托盘菜单选择“退出 Cage”。本机环境仍在运行时，可以选择保留任务或停止环境；停止环境会结束其中的终端。

浏览器上传通过本机文件选择窗口选取文件；下载完成可用 Windows“另存为”选择位置。侧栏“下载”提供打开文件、在资源管理器中显示与再次另存为，取消保存窗口时保留默认 Downloads 中的文件。操作与限制见[浏览器上传与下载](getting-started.md#浏览器上传与下载)。

## 更新与卸载

应用内更新从固定的公开 GitHub Releases 来源选择 Windows x64 安装器，并沿用预发行与正式版通道规则。检查与下载优先使用主机网络，遵循系统代理与主机代理环境变量；连接失败或等待响应超时后尝试 Cage 所选代理。下载安装器后校验 SHA-256、版本、产品名称和 Windows 信任的 Authenticode 签名。新安装器与当前应用的发布者公钥相同，或证书主体的国家与组织均相同，才允许自动安装；后一个条件允许发布者续期证书时更换密钥。通过 Cage 安装程序安装的未签名源码构建（安装目录包含官方 `Cage.install-id`）也会按内置的官方发布者身份校验官方签名安装包，并在用户确认后原地切换为官方签名发行版（设置 `CAGE_DISABLE_OFFICIAL_UPGRADE=1` 可禁用）；直接从开发构建目录运行的未签名程序保持手动更新。

没有新版时隐藏更新按钮；有新版时显示金色圆形按钮。用户先保存文件并结束运行中的任务，再点击一次，按钮显示环形下载进度和百分比。校验通过后自动安装，不再二次确认。更新器先备份应用，再通过工作台统一入口停止环境、代理和控制服务，等待当前窗口退出后执行静默安装。新应用完成工作台启动确认后才认定成功；安装或启动失败时尝试恢复旧程序，保留失败原因和必要备份。磁盘空间不足、文件被其他软件占用等情况可能阻止自动恢复，应按错误信息处理。

手动更新前也应保存文件、停止环境并退出 Cage，再运行新版安装器。旧后台仍运行时安装器会拒绝覆盖。环境工具在“设置 → 环境与恢复”中单独更新，替换应用会保留原环境与数据。

卸载前停止环境并退出，在 Windows“设置 → 应用”中卸载 Cage。卸载保留用户数据：默认配置、代理和环境数据根目录为 `%USERPROFILE%\.claude-cage`，另选的环境存储位置继续保留；下载暂存通常位于 `%USERPROFILE%\.claude-cage-downloads\Downloads`，原 `%USERPROFILE%\.claude-cage-workspace` 中的文件也保留，自行打开的项目仍在原目录。WebView2 可能由其他程序共用，不随 Cage 自动卸载。需要另行删除数据时先备份，删除虚拟磁盘会丢失只保存在环境中的文件与登录状态。

## 常见启动问题

Windows Hypervisor Platform 已启用时，QEMU 仍可能因嵌套虚拟化初始化失败而退出，错误码为 `80370302`。当前开发构建在 Windows x64 的默认 WHPX 参数中使用 `kernel-irqchip=off`，由 QEMU 实现中断控制器，保留 WHPX 的 CPU 加速。正常桌面入口直接使用该参数，无需外部 PowerShell 兼容脚本；显式设置的 QEMU machine 参数仍优先。此设置改变中断控制器实现，具体性能取决于硬件及虚拟化能力。启动失败时，“操作详情”显示经过脱敏的 QEMU stderr 摘要，完整脱敏诊断包可从设置导出。

当前开发构建在升级或强制结束后会自动检查旧连接记录及控制锁。旧锁空闲时修复其所有者和私有权限，再由新后台重建连接记录；真实后台持有的锁继续阻止接管。权限无法迁移时显示具体原因。请保留个人配置目录，不要通过删除账户或虚拟磁盘解决连接问题。点右上角 × 隐藏窗口并保留托盘，结束应用请使用“退出 Cage”；运行中的环境可选择保留运行或停止后退出。

Cage 支持含中文的安装目录。启动时优先读取随附 QEMU 的 UEFI 固件，无需在用户目录另存副本。在默认虚拟 CPU 配置下，Cage 会向 Windows 虚拟机监控程序平台查询可用指令；Claude Code 需要虚拟 CPU 提供 SSE4.2。自行指定了虚拟 CPU 型号的环境仍沿用该设置。

- 提示虚拟化不可用：检查 BIOS/UEFI 的虚拟化开关及 Windows 虚拟机监控程序平台，重启后再试。远程或嵌套虚拟机还需要外层宿主允许嵌套虚拟化。
- 提示缺少 WebView2：重新运行完整安装程序。不要从第三方站点下载同名 DLL。
- 提示 Windows 应用程序控制或签名校验阻止了 QEMU：检查 Windows 安全中心和 CodeIntegrity/Operational 事件。使用带可信签名的完整发布包，保留原来的环境存储位置和虚拟磁盘。
- 工作台能打开，环境不能联网：检查 Cage 内是否已选择节点及网络检查结果。Windows 浏览器能联网不表示 Cage 节点已配置。
- 提示权限或路径不可用：使用当前用户有权限访问的本地目录，不把 Cage 安装目录、私有配置或磁盘根目录作为共享项目。更新器拒绝含符号链接或目录联接的安装路径。

反馈问题时提供 Cage 版本、Windows 版本与内部版本号、CPU 架构、失败步骤和错误原文。不要附带代理密码、订阅链接、会话地址或未脱敏的配置文件。

## Windows 源码构建

在 Windows x64 上从仓库根目录运行以下步骤。构建脚本需要 PowerShell 7，可用 `pwsh` 打开终端。安装命令行工具后重新打开终端，或在当前会话中更新 `PATH`。

| 工具 | 版本与用途 |
| --- | --- |
| [PowerShell 7](https://learn.microsoft.com/powershell/scripting/install/install-powershell-on-windows) | 用 `pwsh --version` 检查。系统自带的 `powershell.exe` 是 5.1。 |
| [Git](https://git-scm.com/install/windows)、[Node.js](https://nodejs.org/en/download) | Git 用于获取源码，CI 使用 Node.js 24 和随附的 npm 构建工作台。建议使用 24.x 的最新补丁版。若旧版 Node 以 `0xC0000409` 退出，可切换到较新的 Node 24。Node 24.22.1 已在相同的 Windows 检出目录通过复验。[#301](https://github.com/Lee-xeo/Claude-Cage/issues/301) |
| [Go](https://go.dev/dl/) | 核对根目录 [go.mod](../go.mod) 和 [tools/tailnet/go.mod](../tools/tailnet/go.mod)。当前最低版本分别为 1.26.10 和 1.27.3。安装 Go 1.27.3 可同时满足两者。使用 `GOTOOLCHAIN=auto` 时可联网下载所需工具链。 |
| [.NET SDK](https://dotnet.microsoft.com/download/dotnet/10.0) | 在仓库根目录运行 `dotnet --version`，应输出 [global.json](../global.json) 锁定的 10.0.404。构建还会核对 [dependencies.json](../packaging/windows/dependencies.json) 中的 .NET 运行时 10.0.15。 |
| [uv](https://docs.astral.sh/uv/getting-started/installation/)、[7-Zip](https://www.7-zip.org/) | 工作台构建需要 `uv` 运行 noVNC 补丁脚本。Windows 构建需要完整的 7-Zip（`7z` 或 `7zz`）解包依赖，其中须包含 PE/NSIS 格式支持；构建开始时会用 `7z i` 检查，精简版即使同名也会被拒绝。可用 `uv --version` 和 `Get-Command 7z, 7zz -ErrorAction SilentlyContinue` 检查。 |
| [WebView2 Evergreen 运行时](https://developer.microsoft.com/microsoft-edge/webview2/) | 本机需安装此运行时，构建完成后会启动工作台进行自检。 |
| [MSYS2](https://www.msys2.org/)、NSIS | MSYS2 的 UCRT64 环境用于编译 QEMU。生成安装包还需要 NSIS。 |

把 MSYS2 安装到简短、只含 ASCII 且无空格的本地路径，例如 `C:\msys64`。打开 **MSYS2 UCRT64**，运行 `pacman -Syu` 更新。若更新时终端关闭，重新打开 UCRT64 再运行一次。若 `pacman` 长时间没有进度，日志反复出现 `gpg: waiting for lock` 或 `starting migration from earlier GnuPG versions`，可能是 gpg-agent 无法建立本机套接字（目前见于部分 Windows 11 预览版本）。结束 MSYS2 中残留的 `gpg`、`gpg-agent`、`dirmngr` 和 `pacman` 进程，删除 `/etc/pacman.d/gnupg/` 下的 `.#lk*`、`*.lock` 和 `S.gpg-agent*` 后重试；仍然卡住时先排查 Windows 更新或安全软件对 AF_UNIX 套接字的影响，不建议关闭软件包签名校验。更新完成后安装下列依赖，包清单与 CI 一致。[Windows CI 工具配置](../.github/actions/windows-toolchain/action.yml)

```bash
pacman -S --needed make patch diffutils git tar gzip findutils curl \
  mingw-w64-ucrt-x86_64-{gcc,glib2,pixman,libslirp,dtc,ninja,python,pkgconf}
```

回到普通用户的 PowerShell 7，从仓库根目录构建。按实际安装位置设置 `CAGE_MSYS2_ROOT`。

```powershell
$env:CAGE_MSYS2_ROOT = 'C:\msys64'
.\scripts\build-windows.ps1
```

首次构建会下载并校验 QEMU 源码，再编译 `qemu-system-x86_64.exe` 和 `qemu-img.exe`。QEMU 配置阶段所需的 pip、setuptools 和 wheel 由 [dependencies.json](../packaging/windows/dependencies.json) 固定版本与 SHA-256，下载校验后放在依赖缓存的 `qemu-python-wheels` 目录，配置时不访问 PyPI。这三个包只用于构建，不随安装包分发。源码优先使用项目发行页上的归档，下载失败时回退到 Git。两种方式都校验源码 tar 文件的 SHA-256。MSYS2 的 tar 会将归档里的相对链接展开为普通文件或目录，普通账户即可解包。QEMU 在独立的短路径目录下编译，默认尝试 MSYS2 的 `tmp`，Cage 仓库路径可以有空格和中文。如果该目录不可写，可设置 `$env:CAGE_QEMU_BUILD_ROOT = 'D:\CageBuildTemp'`，使用普通用户可写、无空格且仅含 ASCII 字符的路径。这个目录还要足够短：QEMU 配置阶段由 pip 在其下创建很深的临时路径，目录过长时会报 `The filename or extension is too long`，脚本会提前给出警告。用 `SUBST` 映射的短盘符无效，Python 会展开为原来的物理路径，须直接使用短的物理目录。

QEMU 下载前会分别编译、链接并运行最小 C 和 C++ 程序。默认 GCC 和 G++ 的 `-B` 指向 MSYS2 的 `ucrt64/bin`，使汇编器和链接器从同一目录加载工具链 DLL。这能避开 `System32` 内旧版 `zlib1.dll` 与 GCC 默认子工具的冲突。如果设置了 `CC` 或 `CXX`，构建沿用对应的显式配置。预检失败时查看编译器输出。配置失败时，脚本会显示 `config.log` 和 `meson-logs/meson-log.txt` 所在目录。

依赖默认缓存到 `_output/windows-deps`，可给构建和打包命令传入相同的 `-DependencyCache` 复用缓存。QEMU 按源码、构建脚本和 `CC`/`CXX` 分目录缓存。完整运行时通过加载及哈希校验后才写入缓存，失败后可以直接重试。云部署包的 Linux Mihomo 归档缓存在 `_output/linux-deps`，下载完成并通过 SHA-256 校验后才写入缓存。也可先运行 `.\scripts\fetch-windows-dependencies.ps1` 预取。若公司网络要求代理，在当前构建会话按需设置 `HTTPS_PROXY`、`HTTP_PROXY` 和获准使用的 `GOPROXY`，再重试 NuGet、Go 或构建依赖下载。保留原有证书与哈希校验。

NuGet 报 `Central Directory corrupt` 或 `Received an unexpected EOF or 0 bytes from the transport stream` 时，检查代理是否截断了运行时包的下载。已完成的 QEMU、前端和 NuGet 全局包缓存可以保留。从官方来源下载完整的同版本 `.nupkg`，与 NuGet 官方目录记录的 `packageHash` 核对一致后放入本地目录，再保留 `--locked-mode` 重试还原。命令行同时指定官方源和本地目录时，把 HTTPS 源写在前面，例如 `dotnet restore .\desktop\windows\Cage.Windows.csproj --locked-mode -r win-x64 -p:Configuration=Release --source https://api.nuget.org/v3/index.json --source C:\CageNuGetFeed`；本地目录在前时，部分 SDK 会把后面的地址当作文件路径并报 `NU1301`。也可通过 `NuGet.Config` 配置本地包源。

应用输出到 `_output/windows-amd64/app`。本地默认自检目录是仓库内的 `_output/workbench-smoke`。源码如果检出到 AppData 或重解析到其他私有目录，构建会在开始前提示把 `RUNNER_TEMP` 指向 AppData 外的普通本地目录，例如 `D:\CageBuildTemp`。若自检失败，构建输出会显示已识别的共享目录错误、候选应用目录和诊断路径。可查看 `smoke.json` 和 `profile/logs/events.jsonl`。`prepareStatus=prepared` 是自检结束时后台的停止确认。

生成未签名安装包时，用仓库脚本安装校验过的 NSIS，再打包刚构建的应用。

```powershell
$tools = Join-Path (Get-Location).Path '_output/windows-tools'
.\scripts\install-nsis.ps1 -Destination $tools
$env:Path = "$(Join-Path $tools 'nsis-3.13');$env:Path"
.\scripts\package-windows.ps1 -SkipBuild
```

安装包及 SHA-256 校验文件位于 `_output/releases/<版本>/windows`。

正式发布还会检查随包 QEMU 两个 EXE 及其 DLL 的发布者签名、时间戳和文件哈希。签名后的运行时路径与对应源码归档必须来自同一次构建。发布验收应在开启智能应用控制的 Windows 11 测试机上，用正式签名的安装包升级并启动已有 Linux 环境。普通的构建自检只检查工作台、QEMU 加载及固件路径。

## 静默安装选项

静默安装可同时传入 `/DESKTOPSHORTCUT=0|1` 和 `/STARTMENUSHORTCUT=0|1`。若指定安装目录，`/D=` 必须是最后一项，只出现一次。目录要用绝对盘符和反斜杠，路径中有空格也无需加引号。未提供快捷方式参数的静默更新沿用上次选择。
