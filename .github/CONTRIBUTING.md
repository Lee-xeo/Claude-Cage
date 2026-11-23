# 贡献指南

Cage 采用与开放插件市场解耦的协作方式：

- **为 Claude 新增 MCP 能力，或为 Cage 制作自定义扩展页面**：请直接前往开放插件市场仓库 [cage-plugins](https://github.com/Lee-xeo/cage-plugins) 提交插件（开发规范见 [docs/plugins.md](../docs/plugins.md)），无需在 Cage 主仓库提 Issue。
- **改进 Cage 本体（隔离环境、网络、工作台、跨平台桌面端与文档）**：采用“贡献 Issue 审核 → 邀请成为协作者 → 提交 PR → 审查合入”的流程。仓库仅允许协作者创建 PR；修复代码、改进功能和修改文档都从明确的贡献方案开始。

## 提交方案并等待审核

在[新建 Issue 页面](https://github.com/Lee-xeo/Claude-Cage/issues/new/choose)选择“方案贡献（Contribution）”。说明要解决的问题、基于的版本或提交、适用系统、解决思路、验证结果和影响范围。有代码时附上 Fork 分支、具体提交、diff 或补丁；尚未验证的部分请明确注明。

只有问题描述时使用“问题反馈”；尚无具体方案的功能需求使用“功能建议”。提交前先搜索已有 Issue，已有相关讨论时在贡献 Issue 中关联它。

通过 GitHub CLI 或 API 创建时，请保留标题开头的 `[Contribution]`。这类请求无法带上表单标签，**Label contribution issues** 工作流会按标题前缀补上 `contribution` 标签；修改标题后同样生效。

仓库管理员或具有 `maintain` 权限的维护者审核方案、确认愿意接纳本次贡献后，为仍处于打开状态的 `contribution` Issue 添加 `accepted` 标签。通过审核表示可以进入实现和 PR 审查阶段，最终合入仍取决于代码审查与验证结果。

## 系统标签

Issue 按涉及的主机系统带有三个标签：`macOS 主机`、`Windows 主机`、`Linux 主机`。主机是运行 Cage 的电脑；Cage 内的环境固定为 Debian 13，不单独打标签。一条 Issue 可以同时带多个标签。各平台的维护者按标签筛选并认领自己负责的 Issue。

新建 Issue 时，**Label issues by system** 工作流读取正文里标题含“主机”的字段并添加对应标签。通过 GitHub CLI 或 API 创建时沿用表单的字段标题即可，例如 `### 主机系统` 下写 `macOS`。

工作流只添加标签，维护者手动调整的标签会保留。正文修改后需要重新识别时，在 **Actions → Label issues by system** 选择 **Run workflow** 并填写 Issue 编号；识别有误或正文没有系统信息时由维护者手动修正。

## 接受协作者邀请

添加 `accepted` 标签会触发 **Invite approved contributors** 工作流，向该 Issue 的作者发送协作者邀请，并在 Issue 中说明后续步骤。已有写权限的协作者可直接提交 PR；待接受的邀请会被复用。

贡献者需要通过 GitHub 通知或[仓库邀请页面](https://github.com/Lee-xeo/Claude-Cage/invitations)接受邀请。邀请过期或流程失败时，请在原 Issue 中联系维护者处理。

协作者拥有整个仓库的写权限，该权限不限于这一条 Issue。`main` 受分支保护，协作者通过独立分支和 PR 交付变更，由代码所有者 `@Lee-xeo` 审核后合入；新增提交后需要重新审核。关闭 Issue 或移除 `accepted` 标签不会自动取消邀请或撤销已有权限；权限调整由仓库管理员在仓库设置中处理。

## 提交 PR

1. 接受邀请后，将本地基线更新到仓库最新的 `main`。
2. 为本次贡献创建独立分支，命名为“开始日期-GitHub用户名-工作内容”，例如 `261112-alice-fix-startup`。保留无关的在途改动，提交范围与审核通过的方案保持一致；扩大范围时先在原 Issue 中讨论。
3. 完成实现并运行与改动相关的检查。涉及共享功能时核对 macOS、Windows 和 Linux；说明实际验证的平台、结果及未验证范围。
4. 推送分支并按 [PR 模板](PULL_REQUEST_TEMPLATE.md) 创建指向 `main` 的 PR，在正文中关联贡献 Issue（例如 `Closes #123`），说明用户可见的变化、验证结果、未验证范围和已知限制。
5. 根据评审意见更新 PR。通过审查与所需检查后由维护者合入，随后删除已合入的分支并关闭对应 Issue。

源代码、配置、日志和附件中均不得包含密码、令牌、订阅链接等敏感信息。引用第三方代码时注明来源并保留所需许可。

## 工作台构建

macOS、Linux 和 Windows 的工作台构建都通过 `PATH` 上的 `uv` 运行 noVNC 补丁脚本。[uv 安装说明](https://docs.astral.sh/uv/getting-started/installation/) · [Windows 源码构建](../docs/windows.md#windows-源码构建)

## macOS 源码构建

macOS 应用的构建入口是 `bash scripts/build-app.sh`，Apple Silicon 与 Intel 都会通过 `make cage` 构建嵌入式 Tailscale 环境组件。完整构建需要同时满足两个 Go 模块的最低版本：

| 模块 | 最低 Go 版本 | 版本来源 |
| --- | --- | --- |
| 工作台与控制服务 | 1.26.10 | 根目录 [go.mod](../go.mod) |
| Tailscale 环境组件 | 1.27.3 | [tools/tailnet/go.mod](../tools/tailnet/go.mod) |

版本以各模块的 `go.mod` 为准。在仓库根目录先检查两个模块，再开始下载材料和编译：

```sh
go version
go list -m
(cd tools/tailnet && GOFLAGS= go version && GOFLAGS= go list -m)
bash scripts/build-app.sh
```

`GOTOOLCHAIN=auto` 会在需要时查找或下载满足模块要求的工具链；未在本地找到时，需要能访问 Go 工具链下载服务。macOS CI 的 `setup-go` 读取根 `go.mod`，构建子模块时也依赖这一选择机制。规则见 [Go 工具链说明](https://go.dev/doc/toolchain)。

使用 `GOTOOLCHAIN=local` 时，先让 `PATH` 中的 Go 本身满足两个模块的要求，例如 Go 1.27.3 或更新版本；仅准备 Go 1.26.10 会在构建 `tools/tailnet` 时失败。离线构建还需预先准备 Go 模块、前端依赖和构建脚本所需的下载材料。Windows 与 Linux 构建也会使用该子模块，须核对同一版本要求。

`build-app.sh` 会先获取随附的代理核心 Mihomo。单独运行 `make cage` 只生成 `_output/bin/cage`，不含代理核心，此时代理导入会直接报“随附代理核心缺失”；在 macOS 或 Linux x86_64 上运行 `make cage-core`，它会校验并放到 `_output/proxy-core/mihomo`，`cage` 运行时从这里或同目录加载。

### macOS 构建排查

构建开始时会先链接一个最小 C 程序并让 Swift 导入 Cocoa，工具链有问题时在编译前报出原因。

| 现象 | 处理 |
| --- | --- |
| cgo 链接报 `unknown architecture`（例如 `arm64e.x1`） | 默认 SDK 比 clang 新。未设置 `SDKROOT` 时，脚本会改用本机已安装、能成功链接的最新 SDK 并提示所用路径；都不能链接时请更新 Xcode 或 Command Line Tools，或手动设置 `SDKROOT`。 |
| Swift 报 `redefinition of module 'SwiftBridging'` | Command Line Tools 升级后残留了旧的模块映射文件，`usr/include/swift/module.modulemap` 与 `bridging.modulemap` 重复定义。重新安装 Command Line Tools，或用 `xcode-select` 选择完整 Xcode。脚本不修改系统工具链。 |
| 图标生成阶段出现与 Swift 无关的报错 | `PATH` 上可能有同名的非 Apple `swift`。构建脚本通过 `xcrun` 调用 Swift，若仍失败，检查 `xcode-select -p`。 |
| 签名报 `resource fork, Finder information, or similar detritus not allowed` | 仓库位于 iCloud Drive 等文件同步目录时，文件会带上扩展属性。脚本在签名前清除应用包内的扩展属性；无法清除时，把仓库移到不受同步管理的目录再构建。 |
| 只允许写入工作区的沙箱中 npm、uv 或 Swift 模块缓存报 `Operation not permitted` | 脚本发现默认缓存目录不可写时，改用 `_output/cache` 下的 npm、uv 和 Swift 模块缓存。`iconutil` 在部分沙箱中仍会报 `Failed to generate ICNS`，这一步需要在沙箱外运行构建。 |
| 检出路径经过 `/tmp` 等符号链接 | 构建脚本按真实路径判断入口，可以在符号链接目录中构建。 |

## 维护者：配置自动邀请

自动邀请使用 `contribution-invites` Environment 中的 Secret `CONTRIBUTION_INVITE_TOKEN`。使用只授权 `Lee-xeo/Claude-Cage` 的 fine-grained personal access token，并授予仓库权限 **Administration: Read and write**；其余权限保持最小范围。令牌应设置有效期，并在到期前更新。

在仓库 **Settings → Environments → contribution-invites** 中，只允许 `main` 分支部署，并将该令牌保存为 Environment Secret。邀请令牌不得放进普通仓库 Secret，否则具有写权限的协作者可以在其他分支的工作流中使用它。GitHub Actions 自带的 `GITHUB_TOKEN` 用于检查审核者权限和写入提醒，无法单独完成协作者邀请。

`main` 的分支保护要求 PR、至少一次代码所有者审核及新提交后的重新审核，禁止强制推送和删除；`.github/CODEOWNERS` 将所有路径的代码所有者设为 `@Lee-xeo`。管理员保留处理自有改动的权限。这些保护与仅限 `main` 的 Environment 共同阻止普通协作者在未审核的分支中取得邀请令牌。

日常审核步骤：

1. 确认 Issue 有 `contribution` 标签，并完成方案与贡献者的审核。标题没有 `[Contribution]` 前缀的贡献不会自动获得标签，确认属于贡献后手动添加。
2. 由管理员或具有 `maintain` 权限的维护者添加 `accepted` 标签。普通 `write`、`triage` 和 `read` 权限不会触发授权成功。
3. 查看 **Actions → Invite approved contributors** 的结果。只有邀请成功、已有待接受邀请或作者已有写权限时，工作流才会在 Issue 中发布后续步骤。
4. 如凭据缺失、过期、权限不足或邀请发送失败，先修复原因，再在该工作流页面选择 **Run workflow**，使用 `main` 分支并填写原 Issue 编号重试。重试同样检查当前审核者权限、Issue 状态及两个标签，并复用已有提醒。

自动化读取默认分支中的脚本，不执行申请者的代码。它只负责审核后的邀请和提醒；方案审核、接受邀请、实现代码和 PR 审查由对应参与者完成。

GitHub 参考：[邀请协作者 API](https://docs.github.com/en/rest/collaborators/collaborators#add-a-repository-collaborator)、[工作流令牌的权限边界](https://docs.github.com/en/actions/tutorials/authenticate-with-github_token#granting-additional-permissions)。
