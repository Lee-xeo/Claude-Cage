---
title: 安装与首次启动
description: 安装适合电脑的 Cage，选择数据盘，完成第一次环境启动。
group: 开始使用
order: 1
keywords: 下载 安装 环境 macOS Windows Linux Debian Apple Silicon WHPX KVM 账户 密码 镜像 继续安装
---
## 选择安装包

从[官网下载区](https://claude-cage.com/#download)选择平台。公开附件、签名和校验文件以 [GitHub Releases](https://github.com/Lee-xeo/Claude-Cage/releases)为准。源码压缩包用于开发，不能直接安装。

| 平台 | 要求与安装方式 |
| --- | --- |
| macOS | Apple Silicon、macOS 14+。打开 `.dmg`，将 Cage 拖入“应用程序”，再从那里启动。Intel Mac 暂不支持。 |
| Windows | Windows 10 22H2 / 11、x64。运行 `.exe` 安装器，启用“Windows 虚拟机监控程序平台”，并在系统要求时重启。未签名预览包会明确标注。 |
| Linux | Debian 12 / Ubuntu 22.04 / 24.04、x86_64。需要 KVM 与当前用户的访问权限。执行 `sudo apt install ./Cage-<版本>-linux-amd64.deb` 补齐应用依赖，再从应用菜单打开。 |

Windows ARM64 和 Linux ARM64 暂不支持。平台依赖详情见 [Windows 指南](https://github.com/Lee-xeo/Claude-Cage/blob/main/docs/windows.md)与 [Linux 指南](https://github.com/Lee-xeo/Claude-Cage/blob/main/docs/linux.md)。

这里选择的是运行 Cage 的电脑平台。三个平台上的环境都是 Debian 13，无需另外选择系统，见[Debian 环境](guest-systems.md)。

<figure class="guide-shot">
  <a href="../../www/public/docs/screenshots/account-setup.jpg" target="_blank" rel="noopener" aria-label="查看原图：首次创建 Cage 账户"><img src="../../www/public/docs/screenshots/account-setup.jpg" alt="首次创建 Cage 账户" width="2560" height="1764" loading="lazy" decoding="async" /></a>
  <figcaption>首次创建 Cage 账户。截图版本：0.1.6-alpha-preview（开发构建） · macOS 原生应用 · 2026-11-07。<a href="../../www/public/docs/screenshots/account-setup.jpg" target="_blank" rel="noopener">查看原图（2560 × 1764） ↗</a></figcaption>
</figure>

## 创建 Cage 账户

选择用户名和密码登录时，设置用户名和至少 8 个字符的密码。这个账户用于解锁当前 Cage，Claude 订阅需要另外在环境里登录。注册后请保存恢复密钥，忘记密码时可在登录页使用它重置；保存与补设步骤见[账户恢复](settings.md#保存恢复密钥)。

包含本机信任登录的较新桌面构建还可选择“信任此电脑并进入”，也可选“使用用户名和密码”进入下图所示的流程。已有密码账户保持原来的登录方式；本机信任的适用范围见[受信任的本机登录](https://github.com/Lee-xeo/Claude-Cage/blob/main/docs/getting-started.md#受信任的本机登录)。

完整桌面版在创建密码账户时默认勾选“记住我”，可以取消；已有账户可在登录页或“设置 → 账户”调整。macOS 另可开启 Touch ID。手动锁定后仍需验证，退出账户会撤销快捷登录授权。账户密码不会加密整个项目目录或虚拟磁盘。

若以前已有账户，现在再次出现创建页面，先停止操作并查[数据目录问题](troubleshooting.md#原账户与数据看不见了)。

## 初始化系统与存储

首次登录后，Cage 显示独立的初始化界面，第一步“系统与存储”用于确定环境的存储位置。环境固定为 Debian 13；以后启动沿用同一环境和磁盘，更新 Cage 不会替换它。

“存储目录”已填入默认位置。需要更换时直接编辑，或点击“选择文件夹”选定本地磁盘上的专用空文件夹；点击“下一步：配置代理”时保存，目录无效时留在当前页提示修改。Windows 安装器里的程序目录与环境数据目录分开；例如应用装在 C 盘，环境可放在 `D:\CageData`。

Mac、Linux 使用默认配置时，环境数据放在用户主目录的 `.claude-cage/state`。Windows 正式安装版在首次初始化时，默认填入安装目录下的 `CageData`，例如应用装在 `D:\Cage`，环境数据就放在 `D:\Cage\CageData`；没有写入权限时，在当前字段选择其他有权限的本地文件夹。已有环境继续使用原存储位置，不会自动迁移。更新或覆盖安装时，安装目录下的 `CageData` 保持原位，不随程序文件移动，虚拟磁盘路径不变。账户与配置仍保存在用户目录的 `.claude-cage`。

初始化页显示本机完整路径。系统镜像、后续镜像下载缓存与持久虚拟磁盘使用同一存储位置；旧版本已留在系统缓存目录的文件不自动移动或删除。更新、重装与卸载应用保留环境数据目录，重新安装后可以继续使用已有数据。不要手动删除这个目录。

选择支持权限管理与稀疏文件的磁盘：Windows 可用 NTFS，Linux 可用 ext4 / XFS / Btrfs，macOS 使用已启用所有权的 APFS。不要选择磁盘根目录、网络盘、已有项目或应用安装目录。已有环境换盘请按[存储迁移](maintenance.md#迁移环境到另一块磁盘)操作。

## 导入代理并启动

1. 点击“下一步：配置代理”。在“从已有代理软件导入”中，找到已有配置并点击“导入此配置”，无需自己查找文件。未找到时，可点击“添加来源”，使用订阅链接、配置文件或手动节点。详细步骤见[网络与代理](network.md)。
2. 选择最终出口，点击“应用代理”。Cage 使用独立核心，不会自动采用本机 Clash 当前所选节点。
3. 点击“开始安装”。
4. 留在初始化界面查看下载、校验、系统安装和工具配置进度。这些步骤自动完成，需要你操作时会显示具体指引。失败时按当前步骤的提示重试；“操作详情”显示具体错误与检查结果，完整记录可在“日志与诊断”导出。已完成系统安装的环境无需重新下载或安装。
5. 网络与隔离检查通过后点击“进入工作台”。以后打开 Cage 直接进入原工作台，环境停止时可以直接启动使用。只需连接远程服务器时，初始化界面的“只连接 SSH 主机”可跳过本机安装。

安装、启动与项目共享不要求修改主机全局 Git 配置，也不要求为裸仓库保护升级 Git。

## 第一次启动需要等待什么

首次安装会下载 Debian 13 基础镜像（约 320–325 MiB），再安装浏览器、编辑器和开发工具。镜像来源与存储要求见[下载量与磁盘空间](guest-systems.md#下载量与磁盘空间)。

系统镜像通过主机网络下载，遵循主机代理设置；未配置代理时直连官方源。使用 Clash 等软件时，可开启其系统代理或 TUN，实际出口由该软件对下载网站的规则决定。已选择的主机代理连接失败、明确配置的 PAC 解析失败时，下载会报错，不自动回退直连。环境内的进程和下载任务使用 Cage 的独立代理，官方源与备用镜像的选择及平台设置见[系统镜像的下载网络](network.md#系统镜像的下载网络)。

初始虚拟磁盘容量上限为 24 GiB，实际占用随安装和使用增加。Claude Desktop 按需单独下载。随后启动会复用工具、文件与登录资料。

应用内准备有固定摘要的开发工具包时，剩余下载量达到 8 MiB 会尝试最多 4 连接分段下载。小文件、无法取得文件长度或服务器不支持分段时使用单连接。所有连接沿用对应下载任务的代理设置，收齐后校验整份文件，校验成功才用于安装。

Claude Code 和 Claude Desktop 在实际下载安装时查询官方最新版本；已安装的更新版本会保留。环境版本页显示这一下载策略，环境中的版本号以实际安装结果为准。

下载中断时查看错误，修复网络或磁盘空间后点“继续安装”。已有连续进度和已保存的分段会用于续传；即使剩余不足 8 MiB，也会继续复用分段。摘要不符或服务器不支持续传时，从头下载该文件。Claude Desktop 的官方最新安装包没有预先固定的摘要，中断后可能需要重新下载整个安装包。不要通过清空 `.claude-cage` 或重建账户解决安装失败。

下载时显示已接收字节、可取得的总量与采样速度；校验单独显示处理进度。工具安装没有可测总量时显示阶段。继续安装沿用原安装记录和已创建磁盘。

<figure class="guide-shot">
  <a href="../../www/public/docs/screenshots/windows-native-download.png" target="_blank" rel="noopener" aria-label="查看原图：Windows 原生应用首次下载镜像的进度"><img src="../../www/public/docs/screenshots/windows-native-download.png" alt="Windows 原生应用首次下载镜像的进度" width="750" height="320" loading="lazy" decoding="async" /></a>
  <figcaption>Windows 原生应用首次下载镜像的进度。截图版本：0.1.5-alpha.2（测试构建） · Windows 11 x64 原生应用 · 2026-11-06。<a href="../../www/public/docs/screenshots/windows-native-download.png" target="_blank" rel="noopener">查看原图（750 × 320） ↗</a></figcaption>
</figure>

## 完成第一项工作

点项目旁加号打开自己的目录，用“新建 Claude 终端”完成环境内的授权。先让 Claude 读取一个文件或完成小修改，在主机检查结果后再投入正式任务。文件、终端和上传操作见[项目、终端与文件](workbench.md)。
