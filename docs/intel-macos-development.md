# Intel Mac 本机 macOS 客机开发构建

这是实验性的 Intel 本机路线，公开发行版的支持范围仍以发行说明为准。主机与客机均需 macOS 15.4 或更新版本；不要求升级到 26.4。当前不提供 Intel Mac 上的 Linux 客机。

## 运行方式

Intel 使用 QEMU 的 Hypervisor.framework 硬件虚拟化和 Apple Paravirtualized Graphics。Apple Silicon 继续使用 Virtualization.framework。两者共用 Cage 的账户、首次授权、工作台、项目共享和网络检查流程。

安装器从 Apple 下载完整 Intel InstallAssistant 和签名恢复环境，校验安装包签名及逐块摘要，在新的虚拟磁盘上安装。优先匹配主机版本；Apple 不再提供该补丁版本时，显示并选用同一主版本的兼容更新。不会复制主机账户、应用数据或登录状态。

系统盘初始容量上限为 64 GiB，实际占用随安装和使用增长。首次安装还需要保存安装材料和应用缓存：已缓存系统材料时至少预留 64 GiB，尚未下载时在此基础上加上界面显示的下载量。安装过程会多次重启，中断后保留原磁盘和安装身份。

整个安装和运行阶段沿用实例独立的受控网络，不添加临时 NAT、主机目录盘或其他网卡。首次主机公钥通过虚拟串口独立取得，再用于 SSH 校验。普通客机账户用于日常工作。

项目共享使用 Cage 的受限 SFTP 服务与 macFUSE FSKit 后端。macOS 15.4 至 26.3 的 FSKit 不支持卷级只读挂载，首次检查改由只读 SFTP 服务拒绝写入。文件系统扩展、屏幕录制和辅助功能仍需在客机内按 Cage 引导确认。

Intel 首次设置的文件系统步骤会先打开已校验的官方 macFUSE 安装器。完成安装后，继续按 Cage 提示打开文件系统扩展并授权。macFUSE 不随系统附加产品在后台安装，以免 Apple 安装事务因文件系统权限检查失败。

## 构建独立测试包

需要 Xcode、Go（版本见 `go.mod`）、Node.js、Python 3.11+、Meson、Ninja、pkg-config，以及源码构建所需的 gettext、pixman、libffi。GLib 和 PCRE2 由构建脚本从固定版本源码构建。依赖工具需在 `PATH` 中。

```sh
bash scripts/build-app.sh --intel-macos-test
```

输出 `_output/Cage Intel.app`，可通过 `CAGE_BUILD_OUTPUT` 指定另一个绝对 `.app` 路径。包内包含独立运行时、客机安装工具、许可证及所需源码材料，不包含系统盘、账户或代理配置。测试包使用独立的应用标识和 `~/.claude-cage-intel-test` 默认配置目录；更新使用新的测试包。

## 验证范围

构建成功与系统启动不代表工作台验收完成。提交贡献时分别记录以下证据及未验证的平台：

- 空白磁盘首次安装、断点继续、可信公钥取得及加速显示切换。
- 普通账户、首次系统授权和临时管理员收尾。
- 工作台终端、浏览器、编辑器及项目双向保存、执行、Git 写入和解除共享。
- 外网关闭、代理故障、网络检查和授权门槛。
- 停止、冷启动、磁盘与权限持久性，以及脱离源码目录运行安装包。
- Apple Silicon 编译回归，以及共享代码的 Linux、Windows 检查。

本路线目前没有通过所有受支持 Intel Mac 和 macOS 版本的硬件验收，不应据此推断官方 Claude 登录、业务会话或嵌套虚拟化已经通过测试。
