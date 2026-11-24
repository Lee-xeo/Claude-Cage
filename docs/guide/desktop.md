---
title: Claude Desktop
description: 在 Cage 内按需安装官方桌面应用，调整画面、传递项目与附件，并处理常见限制。
group: 日常工作
order: 6
keywords: Desktop Chat Code Cowork 画质 缩放 安装 上传
---
## 安装与登录

Claude Desktop 运行在 Cage 的 Debian 环境中，使用 Anthropic 官方的 Linux 版。工作台统一提供安装、打开、显示和关闭入口，应用数据留在环境里。

在侧栏点击“Claude Desktop”，首次选择“下载并安装”。先启动环境并确认代理可用，Cage 从 Anthropic 官方源安装；下载失败可查看进度并重试。安装维护下载可在业务外网尚未放行时进行，应用内联网仍需要关键检查通过。

首次登录在 Cage 内完成，不导入主机上的 Desktop 账号。登录和外部网页会在 Cage 浏览器打开，回调完成后返回 Desktop。若仍停留在浏览器，可回到侧栏应用入口查看。

Chat 和 Code 在同一个官方应用内使用。需要额外虚拟机的 Cowork 暂不支持。手机可以接续电脑上的同一个 Desktop 窗口，两端共用账号与应用数据，见[手机操作 Claude Desktop](mobile.md#在手机操作-claude-desktop)。

<figure class="guide-shot">
  <a href="../../www/public/docs/screenshots/cloud-desktop-welcome.png" target="_blank" rel="noopener" aria-label="查看原图：云端 Linux 环境中的 Claude Desktop 欢迎页"><img src="../../www/public/docs/screenshots/cloud-desktop-welcome.png" alt="云端 Linux 环境中的 Claude Desktop 欢迎页" width="1192" height="875" loading="lazy" decoding="async" /></a>
  <figcaption>云端 Linux 环境中的 Claude Desktop 欢迎页。截图版本：Claude Desktop 4.16336.0 · 真实 Linux 云端环境 · 2026-11-06 · Cage 版本未记录；未登录 Claude。<a href="../../www/public/docs/screenshots/cloud-desktop-welcome.png" target="_blank" rel="noopener">查看原图（1192 × 875） ↗</a></figcaption>
</figure>

## 调整文字与画面

窗口、侧栏变化时画面自动适配；提示“画面尺寸未能同步”时点“重试调整”。需要放大或缩小界面文字和控件时，可在 Claude Desktop 内直接使用 `Ctrl`/`Cmd` + `+`、`-`、`0` 缩放。

Linux 环境的 Desktop 画面在无人操作时自动降低刷新率，操作时立即恢复 60 Hz，清晰度不变。画面按所在屏幕的像素密度渲染。详见[浏览器说明](browser.md#打开网页与浏览器设置)。

滚动卡顿时到“设置 → 通用”开启“优先流畅”，保存并在下次启动环境后生效。它在高分屏上降低绘图像素密度，控件逻辑大小保留。Linux 环境同时让 Claude Desktop 减少动效：动画播放期间应用每一帧都会重绘整块消息区域，关掉后画面只在内容变化时更新。性能还受主机 CPU、内存以及远程网络影响。

## 复制与粘贴

Cage 桌面应用在环境运行期间共享本机、Cage 浏览器和 Claude Desktop 的剪贴板。点击 Claude 消息的复制按钮，或选中文字后按复制快捷键，可直接在本机应用粘贴；本机复制的文字、图片、文件和文件夹也可在 Cage 内粘贴。切换到其他画面或本机窗口后继续同步，账户锁定、切换环境或退出工作台时结束旧连接。

文字最多 2 MB，图片最多 20 MB、4000 万像素；一次最多选择 100 个文件或文件夹，每个普通文件最多 2 GB。本机复制的文件和文件夹不会在后台读取，在 Cage 画面内按粘贴快捷键时才读取并传入环境，同一次复制只传一次；传入前环境内的剪贴板为空。文件传递保留原名与目录结构，来源文件继续保留，跨环境的文件剪切按复制处理。接收文件或图片仍需要目标应用支持该类粘贴。普通网页入口受浏览器权限限制，无法自动共享时可使用原有复制、粘贴和文本传递入口。

## 选择项目与传文件

Desktop Code 请求选择目录时，Cage 显示“选择工作项目”。选择已授权项目并点“使用此项目”，或用“添加本机项目…”明确新增目录。项目改动会直接作用于共享原件。

网页或 Desktop 上传附件时，点“选择本机文件”调用系统窗口。也可将文件拖入支持附件的区域，或在选中附件输入位置后粘贴。详细数量、大小及保留规则见[上传与附件](workbench.md#上传下载与附件)。

本机模式下，文件打开与定位只接受已授权项目或已保存下载记录中的文件。没有主机保存成功记录时，先核对环境中的文件与操作详情。云端文件需要按远端下载流程保存到本机。

## 关闭、更新与恢复

切换到终端或浏览器时 Desktop 保持运行。在左侧栏的 Desktop 项上点击关闭按钮即可释放其运行资源，登录资料继续保留。

Claude Desktop 由 Anthropic 官方 apt 软件源更新，应用内的更新入口可能打开官方说明页。在“设置 → 环境与恢复”的版本与组件中点击“更新 Claude Desktop”，保存工作后确认，Cage 会关闭应用、安装软件源中的可用版本并重新打开。关闭应用后也可选择“更新 / 修复安装”。需要通过终端操作时，先关闭 Desktop，再在 Cage 终端运行：

```sh
sudo apt update
sudo apt install --only-upgrade claude-desktop
```

安装完成后从侧栏重新打开 Desktop。使用相同软件源重复更新会保留当前已安装版本。Linux 更新方式见 [Anthropic 安装说明](https://support.claude.com/en/articles/10065433-install-claude-desktop)。

Cage 为内部系统密钥环创建和解锁所需凭据，不必再设一份密钥环密码。修改 Cage 密码会保留 Desktop 登录数据。

Desktop 的 Code 授权通过内部系统密钥环加密保存，关闭并重新打开应用后继续使用。若旧授权已经失效，仍需在 Desktop 中重新登录一次；官方要求重新授权时，应用也会显示登录提示。

更新 Cage 或环境组件后，先保存工作、关闭并重新打开 Desktop，让文件选择、缩放及其他组件生效。画面或上传仍异常时，查[故障排查](troubleshooting.md#浏览器desktop-或文件上传异常)。
