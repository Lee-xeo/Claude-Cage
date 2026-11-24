# Intel Mac 构建与验证

公开发行版提供 Intel Mac 安装包 `Cage-<版本>-macos-amd64.dmg`，与 Apple Silicon 一样经过 Developer ID 签名和公证，并支持应用内更新。Intel Mac 上的环境同样是 Debian 13（x86_64），沿用其他平台的工作台；主机最低版本为 macOS 14，并需要可用的硬件虚拟化（`sysctl kern.hv_support` 为 1）。本文说明源码构建、CI 与验收范围。

## 运行方式

Intel Mac 使用随包的 QEMU 和 Hypervisor.framework 硬件虚拟化运行环境，Apple Silicon 继续使用 Virtualization.framework。两者共用 Cage 的账户、工作台、项目共享和网络检查流程。

## 构建独立测试包

需要 Xcode、Go（根模块要求 1.26.10，`tools/tailnet` 要求 1.27.3，均为最低版本）、Node.js、Python 3.11+、Meson、Ninja、pkg-config，以及源码构建所需的 gettext、pixman、libffi。GLib 和 PCRE2 由构建脚本从固定版本源码构建。依赖工具需在 `PATH` 中；Go 工具链的联网下载、离线准备与构建前检查见[macOS 源码构建说明](../.github/CONTRIBUTING.md#macos-源码构建)。

QEMU 配置阶段（`mkvenv`）除源码归档自带的 `qemu.qmp` 外，还需要 `pip`、`setuptools`、`wheel` 及其运行依赖 `packaging` 四个纯 Python 构建包。`scripts/build-intel-macos-runtime.sh` 已固定这四项的版本与 SHA-256（与 Windows 构建 [dependencies.json](../packaging/windows/dependencies.json) 一致），下载校验后缓存在 `_output/intel-runtime/python-wheels/`，并在调用 QEMU `configure` 时自动设置 `PIP_NO_INDEX=1` 与指向该缓存目录的 `PIP_FIND_LINKS`（以 `file://` URI 传入，兼容含空格的路径），不再在配置阶段临时访问 PyPI。这些包仅用于构建，不进入安装包。

若当前网络无法直连 `files.pythonhosted.org` 或需要离线构建，可任选以下方式之一：

- 预先通过可用渠道获取上述四个固定版本 `.whl` 文件并核对 SHA-256，放入 `_output/intel-runtime/python-wheels/` 后再运行构建（脚本会直接复用并校验缓存，不触发下载），或通过 `PIP_FIND_LINKS` 指定本地 wheel 目录。
- 在 `CAGE_BUILD_PYTHON` 指向的独立 Python 环境中先运行 `python -m ensurepip --upgrade`，安装已校验的 `wheel` 与 QEMU 源码归档中 `python/wheels/qemu_qmp-0.0.6-py3-none-any.whl`，再在构建会话中设置 `PIP_NO_INDEX=1`。

```sh
bash scripts/build-app.sh --intel-macos-test
```

输出 `_output/Cage Intel.app`，可通过 `CAGE_BUILD_OUTPUT` 指定另一个绝对 `.app` 路径。包内包含独立运行时、许可证及所需源码材料，不包含系统盘、账户或代理配置。测试包使用独立的应用标识和 `~/.claude-cage-intel-test` 默认配置目录；更新使用新的测试包。

## GitHub Actions 产物

三平台检查在 `macos-15-intel` 上独立运行 Intel 构建，产物名为 `cage-macos-amd64-test`，与 `cage-macos-arm64-test` 并列。Intel 任务使用标准的 `scripts/build-app.sh` 构建完整 `Cage.app`，包含 QEMU/HVF、Linux 启动固件和相应源代码材料；本地独立测试包只是额外的应用标识与配置隔离选项。

上传前执行 Go、工作台、更新和账户检查，核对主程序与运行时的 x86_64 架构，并解压回验签名及可执行权限。GitHub 托管 runner 不启动嵌套虚拟机；环境首次安装和工作台验收仍需 Intel Mac 实机记录。

## 正式发行

发版流程（`release.yml`）在 `macos-15-intel` 上运行 `macos-intel-build.yml` 的 release 任务：从固定版本源码编译 QEMU 运行时（不读取缓存），以同一 Developer ID 证书签名并公证 App 与 DMG，产出 `Cage-<版本>-macos-amd64.dmg`。两个 macOS 安装包随附相同的 Mihomo，对应源码只由 Apple Silicon 任务发布一份。发布前要求 Apple Silicon、Intel、Windows、Linux 四个安装包全部通过校验；发布后官网构建会重新下载两个 DMG，逐个核对签名、公证、版本与架构。

应用内更新按处理器选择安装包：Intel 版只接受 `macos-amd64.dmg`，某个版本没有 Intel 安装包时不会改用 Apple Silicon 安装包。`--intel-macos-test` 测试包使用独立的应用标识，不进入更新通道。

## 验证范围

构建成功不代表工作台验收完成。提交贡献时分别记录以下证据及未验证的平台：

- 环境首次安装、断点继续和冷启动。
- 工作台终端、浏览器、编辑器及项目双向保存、执行、Git 写入和解除共享。
- 外网关闭、代理故障、网络检查和授权门槛。
- 停止、冷启动、磁盘持久性，以及脱离源码目录运行安装包。
- Apple Silicon 编译回归，以及共享代码的 Linux、Windows 检查。

实机验收覆盖的机型与 macOS 版本以发行说明为准；CI 通过不代表所有 Intel Mac 和 macOS 版本都已在实机上验证。
