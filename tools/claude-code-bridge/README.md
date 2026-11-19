# 从 Codex 询问远端 Claude Code

在 Codex 里说「问 Claude……」，就能和 Cage 客机上已登录的 Claude Code 对话。在同一个 Codex 聊天里接着问，Claude 会记得上文。新聊天有独立的上下文。Cage 桌面窗口可以保持最小化。

## 接入

本机需要 Node.js 20+ 和 OpenSSH 客户端。本工具面向通过密钥连接的 Cage Linux 云主机，使用该主机既有的 Claude Code 安装和项目目录。先在 PowerShell 查看已配置的主机：

~~~powershell
(Get-Content -Raw "$env:USERPROFILE/.claude-cage/desktop-entry/remote-hosts.json" | ConvertFrom-Json).hosts | Select-Object id,host
~~~

在 Codex 的用户级 config.toml 注册 stdio MCP，把示例路径和主机 ID 换成本机的值：

~~~toml
[mcp_servers.cage_claude]
command = "node"
args = ["D:/src/Claude-Cage/tools/claude-code-bridge/server.mjs", "--host-id", "YOUR_CAGE_HOST_ID"]
tool_timeout_sec = 120
~~~

使用独立 Cage 配置目录时可追加 --profile-dir 和目录路径。主机密钥和身份文件沿用本机 SSH 与 Cage 配置。客机 CLI 使用 /home/cage/.claude 下的登录状态，工作目录是 /home/ubuntu/.claude-cage-workspace。

## 聊天规则

在使用者的 AGENTS.md 添加下面一段，把其中的后备脚本路径替换为实际绝对路径：

> 用户说「问 Claude」「跟 Claude 聊」时，用 cage_claude.ask 原样转发问题，完整转回回答。用户指定的本机附件用 files 传绝对路径。同一 Codex 聊天自动续聊。用户要求重开时传 newSession。升级前已开始的对话，首次追问时传本聊天已有的 sessionId。收到 needsSessionId 时，之后的追问也带上它。返回 running 时，持续用 ask_result 和同一 jobId 等待最终回答。返回 busy 表示本轮尚未发送，先取完上一条再发送本轮。旧窗口缺少 ask_result 时用本机 ask.ps1，指定 -HostId，提问用 -Text，取结果用 -JobId。

**续聊。** MCP 从每次请求的 `_meta.threadId` 识别 Codex 聊天。PowerShell 脚本在 Codex 终端里使用 `CODEX_THREAD_ID`，并兼容 `CODEX_SESSION_ID`。普通终端或未传聊天编号的 MCP 客户端会收到 `needsSessionId: true`，用上次返回的 `sessionId` 续聊。

~~~powershell
$reply = ./ask.ps1 -HostId $cageHostId -Text '帮我看这张图' -Files 'D:\demo\图.png' | ConvertFrom-Json
while ($reply.status -eq 'running') {
    $reply = ./ask.ps1 -HostId $cageHostId -JobId $reply.jobId | ConvertFrom-Json
}
if ($reply.status -eq 'completed') { $reply.text } else { $reply.message }
~~~

**长回复。** `ask` 和兼容入口 `send` 默认等 30 秒，可用 `waitSeconds` 调整为 0 到 60 秒。云端任务在 SSH 断开后继续运行。用同一任务号调用 `ask_result` 可重复领取结果。完成时返回 `status: completed`、`sessionId` 和完整的 `text`。连接中断时如果有任务号，先用它查进度。客机关机导致任务中断时，查询会返回失败原因。

**本地附件。** `files` 接受本次提问选定的绝对文件路径。文件经现有 SSH 复制到 Cage 客机，Claude 可以读文档和图片。每次最多 8 个文件，单个 10 MiB，合计 20 MiB。目录和常见凭据文件不支持。任务结果与上传的附件保存在客机用户的 `~/.cache/cage-claude-bridge/`，以便稍后取回回复或继续讨论附件。
