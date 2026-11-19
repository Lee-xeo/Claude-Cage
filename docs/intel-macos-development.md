# Intel Mac 开发构建

这是实验性的 Intel Mac 路线，公开发行版不包含 Intel 安装包，支持范围以发行说明为准。Intel Mac 上的环境同样是 Debian 13（x86_64），沿用其他平台的工作台；主机最低版本为 macOS 14。

## 运行方式

Intel Mac 使用随包的 QEMU 和 Hypervisor.framework 硬件虚拟化运行环境，Apple Silicon 继续使用 Virtualization.framework。两者共用 Cage 的账户、工作台、项目共享和网络检查流程。

## 构建独立测试包

需要 Xcode、Go（根模块要求 1.26.10，`tools/tailnet` 要求 1.27.3，均为最低版本）、Node.js、Python 3.11+、Meson、Ninja、pkg-config，以及源码构建所需的 gettext、pixman、libffi。GLib 和 PCRE2 由构建脚本从固定版本源码构建。依赖工具需在 `PATH` 中；Go 工具链的联网下载、离线准备与构建前检查见[macOS 源码构建说明](../.github/CONTRIBUTING.md#macos-源码构建)。

```sh
bash scripts/build-app.sh --intel-macos-test
```

输出 `_output/Cage Intel.app`，可通过 `CAGE_BUILD_OUTPUT` 指定另一个绝对 `.app` 路径。包内包含独立运行时、许可证及所需源码材料，不包含系统盘、账户或代理配置。测试包使用独立的应用标识和 `~/.claude-cage-intel-test` 默认配置目录；更新使用新的测试包。

## GitHub Actions 产物

三平台检查在 `macos-15-intel` 上独立运行 Intel 构建，产物名为 `cage-macos-amd64-test`，与 `cage-macos-arm64-test` 并列。Intel 任务使用标准的 `scripts/build-app.sh` 构建完整 `Cage.app`，包含 QEMU/HVF、Linux 启动固件和相应源代码材料；本地独立测试包只是额外的应用标识与配置隔离选项。

上传前执行 Go、工作台、更新和账户检查，核对主程序与运行时的 x86_64 架构，并解压回验签名及可执行权限。GitHub 托管 runner 不启动嵌套虚拟机；环境首次安装和工作台验收仍需 Intel Mac 实机记录。

## 验证范围

构建成功不代表工作台验收完成。提交贡献时分别记录以下证据及未验证的平台：

- 环境首次安装、断点继续和冷启动。
- 工作台终端、浏览器、编辑器及项目双向保存、执行、Git 写入和解除共享。
- 外网关闭、代理故障、网络检查和授权门槛。
- 停止、冷启动、磁盘持久性，以及脱离源码目录运行安装包。
- Apple Silicon 编译回归，以及共享代码的 Linux、Windows 检查。

本路线目前没有通过所有受支持 Intel Mac 和 macOS 版本的硬件验收，不应据此推断官方 Claude 登录、业务会话或嵌套虚拟化已经通过测试。
