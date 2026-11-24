# Linux 安装、使用与验收

Cage 的 Linux 桌面版面向 Debian 12、Ubuntu 22.04/24.04 的 x86_64 电脑，使用 GTK 3 与 WebKitGTK 显示工作台。项目、终端、浏览器、编辑器和内置代理沿用其他平台的操作方式。选择本机时，Cage 创建一台持久的 Debian 13 虚拟机，所有本机项目共用这个环境与其中的 Linux 工具链；主机发行版不需要与它相同。见[Debian 环境](guide/guest-systems.md)。

同一个桌面应用也可以从侧栏 SSH 连接已部署 Cage 的服务器。使用远程工作台只需本机图形依赖，本机模式另需 KVM 和虚拟机工具。云服务器的无 KVM 容器部署见[云端使用指南](guide/cloud.md)和[部署说明](../deploy/cloud/README.md)。

Ubuntu 22.04 的桌面包采用兼容该系统的构建基线，已在隔离容器验证真实窗口、浏览器启动、重装与卸载数据保留。完整 KVM 环境安装、物理桌面输入法和 Claude 登录尚未实测，不能把容器验证视为这些操作通过。

## 支持范围

官方发布的 Linux 桌面包只有 x86_64 的 `.deb`，面向 Debian 12、Ubuntu 22.04 和 Ubuntu 24.04；升级与卸载由 `apt`/`dpkg` 管理。Arch、Fedora 等非 Debian 发行版和 Linux ARM64 桌面目前没有官方安装包，也未经项目验证。社区维护的转换包（例如 AUR 中由 `.deb` 解包的包）不由本项目发布，依赖、升级与卸载以其维护者说明为准。

在其他 x86_64 发行版上从源码构建时，`scripts/build-linux.sh` 需要 GTK 3、json-glib、WebKitGTK 4.1（2.40 或更新）的开发文件以及 Go、Node.js、uv，产物位于 `_output/linux-amd64/app`；`scripts/package-linux.sh` 只生成 `.deb`。运行本机环境还需要 QEMU（`qemu-system-x86_64`、`qemu-img`）、OVMF 固件、OpenSSH 客户端、iproute2、systemd 用户会话与 D-Bus 用户会话，包名按发行版自行对应。

Linux ARM64 服务器可以按[部署说明](../deploy/cloud/README.md)用 `deploy/cloud/build-guest.sh` 在本机原生构建云端工作镜像；桌面端的一键部署仍只面向 x86_64 服务器。

## 安装前准备

Ubuntu 22.04 需要启用 `universe`、`jammy-updates` 与 `jammy-security`，并安装更新后的 WebKitGTK 4.1（至少 2.40）。系统初始镜像中的旧 WebKit 无法运行当前工作台。

电脑需要图形桌面、可用的 KVM 硬件虚拟化、足够的磁盘空间和自己的代理节点。默认虚拟磁盘上限为 24 GiB。云主机需要服务商开放嵌套虚拟化；没有 `/dev/kvm` 的服务器不能按普通桌面流程启动 Cage 环境。

检查当前用户可读写 `/dev/kvm`：

```sh
test -r /dev/kvm && test -w /dev/kvm
```

设备不存在时，检查 BIOS/UEFI 虚拟化和内核 KVM 模块。设备存在但权限不足时，可由管理员将用户加入 `kvm` 组，再退出桌面并重新登录：

```sh
sudo usermod -aG kvm "$USER"
```

Cage 日常以普通用户运行。不要用 `sudo cage-desktop` 启动工作台。

## 安装并开始工作

在[项目主页](https://claude-cage.com/#download)点击“下载应用”并选择 Linux。从对应 Release 下载 `Cage-<版本>-linux-amd64.deb` 与 `SHA256SUMS`，核对该文件的 SHA-256 后安装。只有对应 Release 提供安装包时，才执行下面的安装命令：

```sh
sudo apt install ./Cage-<版本>-linux-amd64.deb
```

安装器随附 Cage 后台、工作台和 Mihomo 代理核心。APT 安装 WebKitGTK 等图形依赖。只连接 SSH 主机时可直接打开应用。要使用本机环境，请再运行 `sudo apt install qemu-system-x86 qemu-utils ovmf openssh-client iproute2 git` 并确认 KVM 可用。首次启动本机环境时会下载 Debian 镜像与环境工具。

安装和启动 Cage 不要求启用主机 Git 裸仓库保护，也不因这项设置要求升级 Git。Cage 不修改主机的全局 Git 配置。

作为手机远程主机时，Cage 通过系统的 `systemd-inhibit` 和 logind 申请保持系统运行，屏幕仍可熄灭。完整安装包依赖 systemd；服务或用户权限不满足时，“设置 → 远程连接”会显示防休眠失败原因，不会自动改动系统电源策略。

从应用菜单打开 **Cage**，也可运行 `cage-desktop`。系统包名和后台命令为 `claude-cage`，避免与发行版自带的同名 Wayland 程序冲突。导入自己的代理、选择节点，再按[开始使用](getting-started.md#2-导入并选择代理)打开项目。项目路径例如 `/home/alice/Projects/demo`，项目共享只限明确选择的目录，下载使用独立的暂存目录；项目中的修改和删除会同步到主机。已授权项目中的 Git 元数据可读写，环境内的提交直接更新主机仓库；共享范围仍限于授权目录。

Linux 使用 Ctrl 快捷键：`Ctrl+O` 打开项目，`Ctrl+T` 新建网页，`Ctrl+L` 定位地址栏，`Ctrl+W` 关闭标签，`Ctrl+Shift+Enter` 新建终端。终端用 `Ctrl+Shift+C/V` 复制粘贴，`Ctrl+C` 中断命令。

关闭窗口保留后台环境与任务，再次打开 Cage 可重新连接。在应用内停止环境后退出，才能结束后台任务。Linux 本地账户使用密码解锁；设备快速解锁暂不提供。

浏览器上传使用本机文件选择窗口，下载可通过 GTK“另存为”选择保存位置。侧栏“下载”提供打开文件、打开所在文件夹和再次另存为；文件按当前桌面关联的应用打开。操作与限制见[浏览器上传与下载](getting-started.md#浏览器上传与下载)。

## 更新与卸载

Linux 版本通过系统安装包手动更新。应用内更新入口显示更新方式，不自动替换系统文件。先保存文件、停止环境并退出 Cage，再安装新版 `.deb`。安装器发现已安装的 Cage、控制服务或代理进程仍运行时，会拒绝覆盖并提示退出。

更新和卸载保留 `~/.claude-cage` 中的配置、代理与虚拟磁盘，下载暂存、原 `~/.claude-cage-workspace` 及自行打开的项目目录也保留。卸载命令为：

```sh
sudo apt remove claude-cage
```

需要删除用户数据时应先自行备份。只保存在虚拟磁盘中的文件和登录状态会随虚拟磁盘删除而丢失。

## 常见问题

- 工作台无法显示：确认在普通用户的图形桌面会话中启动，且系统 WebKitGTK 安装完整。不要关闭 WebKit 沙箱来绕过桌面配置问题。若主机安装了 NVIDIA GStreamer 插件（`libgstnvcodec.so`）或 gst-libav（`libgstlibav.so`，会导致 WebKit 渲染进程反复崩溃），Cage 会自动为内置 WebKit 建立私有插件目录与注册表缓存并排除这些插件，内置 WebKit 因此不使用 NVIDIA 与 FFmpeg 解码，工作台不受影响；检测到 NVIDIA 专有驱动（`/proc/driver/nvidia/version` 或 `/sys/module/nvidia`）时还会关闭 WebKit DMA-BUF 渲染，其他显卡保持 WebKit 默认；也可自行设置 `WEBKIT_DISABLE_DMABUF_RENDERER=1`（关闭）或 `=0`（保持开启），显式设置始终优先，并在启动阶段渲染进程崩溃时自动回退到软件渲染重试一次。若仍失败，请查看 `~/.claude-cage/logs/events.jsonl` 中的 WebKit 退出诊断记录。
- Ubuntu 24.04 上窗口出现前报 `bwrap: setting up uid map: Permission denied` 或 `Failed to fully launch dbus-proxy`：系统默认限制非特权用户命名空间，WebKitGTK 的 bubblewrap 沙箱因此无法启动。保留沙箱，为 Cage 桌面程序添加应用专属的 AppArmor 配置，例如以管理员身份创建 `/etc/apparmor.d/claude-cage`：

  ```text
  abi <abi/4.0>,
  include <tunables/global>
  profile claude-cage /usr/lib/claude-cage/cage-desktop flags=(unconfined) {
    userns,
    include if exists <local/claude-cage>
  }
  ```

  然后运行 `sudo apparmor_parser -r /etc/apparmor.d/claude-cage` 并重新打开 Cage。不建议用 `kernel.apparmor_restrict_unprivileged_userns=0` 放开整台机器的限制。背景见 [Ubuntu 24.04 发行说明](https://documentation.ubuntu.com/release-notes/24.04/)。
- 提示 KVM 不可用：按安装前准备检查设备和权限。普通阿里云实例未必提供嵌套虚拟化。
- 提示找不到物理网卡：内置代理需要可识别的主机网络接口。Docker 容器的 veth、VPN 隧道和桥接接口不作为物理出口；桌面主机与容器测试的网络条件不同。
- 能打开工作台但无法联网：在 Cage 内确认已选择代理节点并查看网络检查。主机浏览器能联网不能证明 Cage 代理已可用。

反馈时提供发行版、CPU 架构、Cage 版本、桌面环境以及失败步骤和错误原文。避免附带代理密码、订阅链接、账户文件和带会话信息的地址。
