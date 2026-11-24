import { translate } from "./message-catalog.mjs";

const JOB_NAMES = new Set([
  "启动环境",
  "停止环境",
  "准备环境",
  "应用设置",
  "连接项目目录",
  "迁移环境目录",
  "恢复配置",
  "应用资源配置",
  "检查联网条件",
  "恢复联网",
  "应用代理节点",
  "恢复代理连接",
  "更新环境组件",
  "更新环境工具",
  "安装 Claude Desktop",
  "打开 Claude Desktop",
  "关闭 Claude Desktop",
  "重置环境与换机",
  "恢复旧环境",
  "本次操作",
]);

export const backendMessageKeys = [
  // Issue #749: local proxy node validation & migration
  "本机代理端口不能用作节点。请导入原客户端的具体节点，在 Cage 中按中转到最终出口的顺序重新配置并应用",
  "原代理链包含本机代理端口。请导入各跳的具体节点后重建代理链",
  "节点 {0} 需要迁移：{1}",
  "节点 {0}：{1}",
  // Proxy state labels
  "尚未配置",
  "尚未应用",
  "代理已停止",
  "待启动环境",
  "外网未放行",
  "正在检查",
  "已联网",
  "需要迁移代理节点",
  // Proxy import, validation & skipped-node reasons
  "配置为空或超过 8 MiB",
  "无法读取订阅内容：需要 Clash/Mihomo YAML、分享链接列表或其 Base64 编码",
  "配置需要包含 1–3000 个 proxies 节点；仅含远程 provider 的配置请改为导入节点订阅链接",
  "节点名称为空或过长",
  "节点名称重复，无法确定代理链引用",
  "节点协议暂不支持",
  "Clash Verge Ninja 专用的 ninja 协议，Cage 随附的标准 Mihomo 核心无法解析，请向服务商获取同一出口的标准 Mihomo 节点后重新导入",
  "节点服务器缺失或过长",
  "SSH 节点的 private-key 必须直接填写私钥内容，不能引用本机文件",
  "节点不能引用本机文件、网络接口或额外路由",
  "代理链必须引用同一来源中成功导入的具体节点，不能引用 DIRECT 或选择分组",
  "代理链存在循环",
  "代理链依赖的中转节点不可用或存在循环",
  "没有可导入的节点，已跳过 {0} 个；{1}",
  "第 {0} 个节点 {1}：{2}",
  "第 {0} 个节点：{1}",
  "inline provider 设置了 override 或过滤规则，暂不支持；请展开为顶层 proxies 后再导入",
  "节点字段 {0} 存在重复写法，请仅保留一种",
  "Hysteria2 字段 {0} 无效，未忽略该字段",
  "Hysteria2 字段 {0} 与 {1} 冲突",
  "旧 WebSocket 字段仅适用于 VMess、VLESS 或 Trojan 节点",
  "旧 WebSocket 字段需要显式设置 network: ws，请核对节点传输方式",
  "WebSocket 字段 ws-opts 无效，未忽略旧字段",
  "WebSocket 字段 ws-path 无效，未忽略该字段",
  "WebSocket 字段 ws-path 与 ws-opts.path 冲突",
  "WebSocket 字段 ws-headers 与 ws-opts.headers 冲突",
  "WebSocket 请求头必须为字符串映射，未忽略该字段",
  "WebSocket 请求头无效，未忽略该字段",
  "WebSocket 请求头存在大小写不同的冲突写法",
  // Share-link errors
  "分享链接的协议暂不支持；请改用 Clash/Mihomo YAML",
  "分享链接格式无效，缺少服务器、端口或凭据",
  "分享链接的伪装头类型暂不支持；请改用 Clash/Mihomo YAML",
  "分享链接的传输方式暂不支持；请改用 Clash/Mihomo YAML",
  "Trojan 分享链接的安全层暂不支持；请改用 Clash/Mihomo YAML",
  "VLESS 分享链接的 encryption 暂不支持；请改用 Clash/Mihomo YAML",
  "VLESS Reality 分享链接缺少公钥",
  "VLESS 分享链接的安全层暂不支持；请改用 Clash/Mihomo YAML",
  "VMess 分享链接不是 v2rayN 格式；请改用 Clash/Mihomo YAML",
  "VMess 分享链接的安全层暂不支持；请改用 Clash/Mihomo YAML",
  "Shadowsocks 分享链接的 obfs 模式无效",
  "Shadowsocks 分享链接的插件暂不支持；请改用 Clash/Mihomo YAML",
  "Hysteria2 分享链接的混淆方式暂不支持",
  "TUIC 分享链接需要 uuid:password；TUIC v4 token 链接暂不支持",
  // Source DNS errors
  "订阅的节点 DNS 需要填写 HTTPS DoH 地址列表",
  "订阅的节点 DNS 最多支持 4 个 HTTPS DoH 地址",
  "订阅的第 {0} 个节点 DNS 需要远程 HTTPS DoH 地址，不支持用户名、密码或 # 路由参数",
  "订阅的第 {0} 个节点 DNS 端口无效",
  "多个来源对同一节点域名指定了不同 DNS，无法隔离解析；请统一这些来源的节点 DNS 或移除冲突来源，原配置已保留",
  "使用订阅节点 DNS 时，节点服务器必须是具体域名或 IP，不能包含通配符或匹配规则",
  // Proxy fetch, store, core, delay & workbench errors
  "尚未选择代理节点，请在网络与代理中添加来源并选择节点",
  "代理节点尚未应用或配置不一致，请重新应用节点；外网保持关闭",
  "订阅重定向被拒绝",
  "订阅服务器必须使用公网地址",
  "代理基础设施域名无效",
  "代理基础设施 DNS 均不可用或未返回公网 IPv4；请检查主机网络后重试，未使用系统 DNS 回退",
  "代理基础设施域名解析失败",
  "代理基础设施 DNS 响应无效",
  "代理基础设施域名没有可用公网 IPv4",
  "订阅需要完整的 HTTPS 网址，不支持地址内用户名和密码",
  "订阅网址无效",
  "订阅下载失败，请检查下载路径和服务地址",
  "订阅服务器返回 HTTP {0}",
  "订阅下载中断或超过 8 MiB",
  "Cage 代理配置无效，请恢复私有配置备份",
  "代理来源配置总大小超过 32 MiB，请减少来源或节点；原配置已保留",
  "请填写 1–100 字节的来源名称",
  "更新间隔应为 0–168 小时，0 表示手动更新",
  "节点未包含在更新中",
  "订阅更新移除或跳过了当前出口或中转节点 {0}：{1}；原配置已保留，请先应用其他节点再更新",
  "请先选择有效的最终出口",
  "代理链与最终出口不一致，请重新配置代理",
  "代理链中的节点已被移除，请重新选择；原配置已保留",
  "代理链不能重复使用同一个节点",
  "导入的代理链存在循环或缺失节点，请检查来源配置",
  "账号绑定的代理节点已移除，请重新选择",
  "请至少选择一个最终出口",
  "所选节点已不存在，请刷新列表",
  "代理端口应为 1024–65535",
  "订阅来源不存在",
  "该来源包含当前代理链中的节点，请先选择并应用其他来源的节点",
  "订阅节点会在更新订阅时被覆盖，请在服务商处修改；只能编辑本地导入的节点",
  "服务器地址无效，请填写 IP 地址或域名",
  "节点端口应为 1–65535",
  "节点不存在",
  "名称或更新间隔无效",
  "订阅需要完整的 HTTPS 网址",
  "本地来源需要提供新的配置内容才能更新",
  "订阅来源从订阅链接更新，不能用粘贴的内容替换",
  "正在下载节点订阅…",
  "正在验证节点配置…",
  "正在应用内置代理节点…",
  "节点已应用，正在重新检查环境出口…",
  "节点已应用；启动 Cage 环境后完成联网检查",
  "代理节点已保存，外网尚未恢复；请重新检查联网条件：{0}",
  "随附的 Mihomo 核心未通过配置校验，请核对节点协议和参数；可在设置的“日志与诊断”中导出诊断包查看校验记录；原配置已保留",
  "Cage 代理端口已被占用，请更换独立端口",
  "无法启动 Cage 自带代理核心",
  "代理核心启动失败，请查看私有代理日志",
  "代理核心启动超时，请查看私有代理日志",
  "节点未载入核心；请先应用节点以载入当前列表",
  "测速超时：节点已载入核心，但 5 秒内没有收到测速地址的响应；请检查节点或上游代理",
  "测速失败：节点已载入核心，但连接节点或上游代理时出错；请检查节点或上游代理",
  "测速失败：代理核心返回 HTTP {0}",
  "测速失败：{0}",
  "代理核心未运行或无法连接；请先应用节点。{0}",
  "一次最多测速 {0} 个节点",
  "测速列表包含重复节点",
  "测速已取消",
  "Cage 代理核心操作失败（{0}）",
  "未找到可用的物理 IPv4 网卡；Cage 外网保持关闭，请连接网络后重试",
  "未找到带 IPv4 默认路由的物理或云主机网卡；Cage 外网保持关闭，请连接网络后重试",
  "未找到可用的物理 IPv4 网卡。请连接以太网或 Wi-Fi；仅有虚拟网卡的 Windows 虚拟机和云主机暂不可用。Cage 外网保持关闭",
  "内置代理未就绪；外网保持关闭",
  "正在识别物理网卡并重新绑定代理…",
  "这份配置已经更新或不在原位置，请点击“重新检测”后再导入",
  "请在这台电脑的 Cage 窗口中导入本机代理配置；其他连接可手动上传配置文件",
  // Local proxy discovery & Surge
  "当前生效配置",
  "已保存的配置",
  "加密配置",
  "本机配置",
  "配置文件使用 age 加密，Cage 不会解密私有配置，无法直接导入。请从原客户端或服务商导出明文 Clash/Mihomo YAML 后通过“添加来源”导入，或手动添加节点",
  "无法读取当前用户的配置目录，请重新检测或手动导入文件",
  "无法读取这份配置，请检查文件权限或改为手动导入",
  "这份配置没有可导入的节点，或格式暂不支持；请从原软件导出 Clash/Mihomo 配置",
  "这份配置没有可导入的节点；请从原软件导出含节点的 Clash/Mihomo 配置",
  "{0}。请在原软件中把节点 DNS（dns.proxy-server-nameserver）改为远程 HTTPS DoH 地址后重新导出，或在 Cage 中手动添加节点",
  "有 {0} 份节点列表未能从本机缓存读取，这些节点不会导入。可先在原软件中更新配置，再重新检测。",
  "这份 Surge 配置没有可导入的节点",
  "WireGuard 暂不支持",
  "缺少服务器或端口",
  "端口无效",
  "ShadowTLS 只支持与 Shadowsocks 搭配",
  "同时使用 obfs 与 ShadowTLS，暂不支持",
  "ShadowTLS v{0} 暂不支持（支持 v1～v3）",
  "Snell v{0} 暂不支持（支持 v1～v3）",
  "SSH 使用 Surge 钥匙串中的私钥，无法读取；请在 Cage 中手动添加 SSH 节点",
  "协议 {0} 暂不支持",
  "有 {0} 处引用了远程配置，未下载；这些节点不会导入。可在 Surge 中导出完整配置后再导入",
  "以下节点未导入：{0}",
  "{0}（{1} 个）",
  // SSH config import
  "SSH 配置已变化，请重新打开导入预览",
  "请选择 1 至 100 个 SSH 主机",
  "SSH 主机选择无效，请重新打开导入预览",
  "SSH 配置参数数量无效",
  "依赖代理、跳板机或主机名规范化，请手动配置",
  "Cage 无法保留此连接设置：{0}",
  "PreferredAuthentications 未包含 publickey，请手动选择认证方式",
  "包含暂不支持的连接选项：{0}",
  "IdentitiesOnly 无法通过导入保留，请手动选择认证方式",
  "主机名包含不支持的变量",
  "SSH 端口无效",
  "IdentityAgent 不能为空，请手动选择认证方式",
  "IdentityAgent 不支持变量展开，请填写完整的本机路径",
  "IdentityAgent 需要使用本机 Windows 命名管道",
  "IdentityAgent 需要使用符合系统长度限制的 Unix 套接字绝对路径",
  "主机名、用户名或端口不符合 Cage 连接要求",
  "最多保存 100 台远程主机，现有配置未修改",
  // Jobs & usererror
  "另一个操作仍在进行，请稍候",
  "后台正在更新，请重新打开 Cage。",
  "另一个操作仍在进行，请稍候：当前正在{0}。请等待完成，或先停止当前操作。",
  "正在准备…",
  "已取消",
  "设置已保存，后续操作已取消",
  "{0}失败",
  "环境已启动，联网检查未通过",
  "设置已保存，等待环境恢复",
  "已保存待验证的静态 IP，外网保持关闭",
  "设置已保存，环境已启动，联网检查未通过",
  "已取消：已被{0}替换",
  "设置已保存，后续操作已被{0}取消",
  "设置已保存，{0}",
  "准备环境",
  "应用设置",
  "迁移环境目录",
  "恢复配置",
  "恢复联网",
  "应用代理节点",
  "恢复代理连接",
  "安装 Claude Desktop",
  "打开 Claude Desktop",
  "关闭 Claude Desktop",
  "重置环境与换机",
  "本次操作",
  "{0}失败：{1}。{2}",
  "{0}失败：{1}。",
  "未能取得可识别的错误原因",
  "等待响应超时",
  "无法解析服务器域名",
  "服务器证书验证未通过",
  "服务器没有返回有效的加密连接响应",
  "系统拒绝访问所需文件或目录",
  "所需文件或程序不存在",
  "磁盘剩余空间不足",
  "目标磁盘为只读，无法写入",
  "目标服务拒绝了连接",
  "连接在返回完整结果前中断",
  "当前网络无法到达目标服务",
  "程序提前结束（退出码 {0}），未返回可识别的原因",
  "打开“操作详情”查看失败记录；仍无法处理时，请在设置中导出诊断包。",
  "请重新检测；仍失败时导出诊断包。",
  "连接恢复后会自动重试，也可手动重新检测。",
  // Common workbench HTTP & native errors
  "本地账户尚未初始化",
  "请先解锁 Cage",
  "请先启动 Cage 环境",
  "请先启动环境",
  "环境尚未就绪",
  "环境正在执行其他操作，请稍后重试",
  "上传窗口已关闭，请重新点击网页上传按钮",
  "每个文件不能超过 2 GB",
  "文件已移动、删除或暂时无法读取",
  "文件尚未下载完成或已不可用",
  "所选本机扩展已不可用，请刷新清单",
  "Claude Desktop 尚未启动",
];

function unquoteGo(raw) {
  try {
    return JSON.parse(raw);
  } catch {
    return raw.slice(1, -1);
  }
}

function translateTemplate(localize, template, ...values) {
  if (localize(template) === template) return null;
  return localize(template, ...values);
}

function localizeSkippedDetail(item, localize) {
  const match = item.match(/^第 (\d+) 个节点 ("(?:[^"\\]|\\.)*")：([\s\S]+)$/);
  if (!match) return localizeBackendMessage(item, localize);
  const name = unquoteGo(match[2]);
  const reason = localizeBackendMessage(match[3], localize);
  if (!name) {
    return translateTemplate(localize, "第 {0} 个节点：{1}", match[1], reason) ?? item;
  }
  return translateTemplate(localize, "第 {0} 个节点 {1}：{2}", match[1], name, reason) ?? item;
}

export function localizeBackendMessage(message, localize = translate) {
  if (typeof message !== "string" || !message) return message || "";
  const trimmed = message.trim();
  if (!trimmed) return message;
  const direct = localize(trimmed);
  if (direct !== trimmed) return direct;

  let m;
  if ((m = trimmed.match(/^节点 ("(?:[^"\\]|\\.)*") 需要迁移：([\s\S]+)$/))) {
    return translateTemplate(localize, "节点 {0} 需要迁移：{1}", unquoteGo(m[1]), localizeBackendMessage(m[2], localize)) ?? trimmed;
  }
  if ((m = trimmed.match(/^节点 ("(?:[^"\\]|\\.)*")：([\s\S]+)$/))) {
    return translateTemplate(localize, "节点 {0}：{1}", unquoteGo(m[1]), localizeBackendMessage(m[2], localize)) ?? trimmed;
  }
  if ((m = trimmed.match(/^没有可导入的节点，已跳过 (\d+) 个；([\s\S]+)$/))) {
    const details = m[2].split("；").map((part) => localizeSkippedDetail(part, localize)).join("; ");
    return translateTemplate(localize, "没有可导入的节点，已跳过 {0} 个；{1}", m[1], details) ?? trimmed;
  }
  if ((m = trimmed.match(/^订阅更新移除或跳过了当前出口或中转节点 ("(?:[^"\\]|\\.)*")：([\s\S]+)；原配置已保留，请先应用其他节点再更新$/))) {
    return translateTemplate(
      localize,
      "订阅更新移除或跳过了当前出口或中转节点 {0}：{1}；原配置已保留，请先应用其他节点再更新",
      unquoteGo(m[1]),
      localizeBackendMessage(m[2], localize),
    ) ?? trimmed;
  }
  if ((m = trimmed.match(/^代理节点已保存，外网尚未恢复；请重新检查联网条件：([\s\S]+)$/))) {
    return translateTemplate(localize, "代理节点已保存，外网尚未恢复；请重新检查联网条件：{0}", localizeBackendMessage(m[1], localize)) ?? trimmed;
  }
  if ((m = trimmed.match(/^订阅服务器返回 HTTP (\d+)$/))) {
    return translateTemplate(localize, "订阅服务器返回 HTTP {0}", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^测速失败：代理核心返回 HTTP (\d+)$/))) {
    return translateTemplate(localize, "测速失败：代理核心返回 HTTP {0}", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^测速失败：([\s\S]+)$/))) {
    return translateTemplate(localize, "测速失败：{0}", localizeBackendMessage(m[1], localize)) ?? trimmed;
  }
  if ((m = trimmed.match(/^代理核心未运行或无法连接；请先应用节点。([\s\S]+)$/))) {
    return translateTemplate(localize, "代理核心未运行或无法连接；请先应用节点。{0}", localizeBackendMessage(m[1], localize)) ?? trimmed;
  }
  if ((m = trimmed.match(/^一次最多测速 (\d+) 个节点$/))) {
    return translateTemplate(localize, "一次最多测速 {0} 个节点", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^Cage 代理核心操作失败（([^）]+)）$/))) {
    return translateTemplate(localize, "Cage 代理核心操作失败（{0}）", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^节点字段 ([^\s]+) 存在重复写法，请仅保留一种$/))) {
    return translateTemplate(localize, "节点字段 {0} 存在重复写法，请仅保留一种", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^Hysteria2 字段 ([^\s]+) 无效，未忽略该字段$/))) {
    return translateTemplate(localize, "Hysteria2 字段 {0} 无效，未忽略该字段", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^Hysteria2 字段 ([^\s]+) 与 ([^\s]+) 冲突$/))) {
    return translateTemplate(localize, "Hysteria2 字段 {0} 与 {1} 冲突", m[1], m[2]) ?? trimmed;
  }
  if ((m = trimmed.match(/^订阅的第 (\d+) 个节点 DNS 需要远程 HTTPS DoH 地址，不支持用户名、密码或 # 路由参数$/))) {
    return translateTemplate(localize, "订阅的第 {0} 个节点 DNS 需要远程 HTTPS DoH 地址，不支持用户名、密码或 # 路由参数", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^订阅的第 (\d+) 个节点 DNS 端口无效$/))) {
    return translateTemplate(localize, "订阅的第 {0} 个节点 DNS 端口无效", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^([\s\S]+)。请在原软件中把节点 DNS（dns\.proxy-server-nameserver）改为远程 HTTPS DoH 地址后重新导出，或在 Cage 中手动添加节点$/))) {
    return translateTemplate(
      localize,
      "{0}。请在原软件中把节点 DNS（dns.proxy-server-nameserver）改为远程 HTTPS DoH 地址后重新导出，或在 Cage 中手动添加节点",
      localizeBackendMessage(m[1], localize),
    ) ?? trimmed;
  }
  if ((m = trimmed.match(/^有 (\d+) 份节点列表未能从本机缓存读取，这些节点不会导入。可先在原软件中更新配置，再重新检测。$/))) {
    return translateTemplate(localize, "有 {0} 份节点列表未能从本机缓存读取，这些节点不会导入。可先在原软件中更新配置，再重新检测。", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^ShadowTLS v(\d+) 暂不支持（支持 v1～v3）$/))) {
    return translateTemplate(localize, "ShadowTLS v{0} 暂不支持（支持 v1～v3）", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^Snell v(\d+) 暂不支持（支持 v1～v3）$/))) {
    return translateTemplate(localize, "Snell v{0} 暂不支持（支持 v1～v3）", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^协议 ([^\s]+) 暂不支持$/))) {
    return translateTemplate(localize, "协议 {0} 暂不支持", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^有 (\d+) 处引用了远程配置，未下载；这些节点不会导入。可在 Surge 中导出完整配置后再导入$/))) {
    return translateTemplate(localize, "有 {0} 处引用了远程配置，未下载；这些节点不会导入。可在 Surge 中导出完整配置后再导入", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^以下节点未导入：([\s\S]+)$/))) {
    const items = m[1].split("；").map((part) => {
      const counted = part.match(/^([\s\S]+)（(\d+) 个）$/);
      if (!counted) return localizeBackendMessage(part, localize);
      return translateTemplate(localize, "{0}（{1} 个）", localizeBackendMessage(counted[1], localize), counted[2]) ?? part;
    }).join("; ");
    return translateTemplate(localize, "以下节点未导入：{0}", items) ?? trimmed;
  }
  if ((m = trimmed.match(/^Cage 无法保留此连接设置：([a-z0-9]+)$/))) {
    return translateTemplate(localize, "Cage 无法保留此连接设置：{0}", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^包含暂不支持的连接选项：([a-z0-9]+)$/))) {
    return translateTemplate(localize, "包含暂不支持的连接选项：{0}", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^另一个操作仍在进行，请稍候：当前正在([^。]+)。请等待完成，或先停止当前操作。$/))) {
    return translateTemplate(localize, "另一个操作仍在进行，请稍候：当前正在{0}。请等待完成，或先停止当前操作。", localize(m[1])) ?? trimmed;
  }
  if ((m = trimmed.match(/^已取消：已被(.+)替换$/))) {
    return translateTemplate(localize, "已取消：已被{0}替换", localize(m[1])) ?? trimmed;
  }
  if ((m = trimmed.match(/^设置已保存，后续操作已被(.+)取消$/))) {
    return translateTemplate(localize, "设置已保存，后续操作已被{0}取消", localize(m[1])) ?? trimmed;
  }
  if ((m = trimmed.match(/^设置已保存，([\s\S]+)$/))) {
    return translateTemplate(localize, "设置已保存，{0}", localizeBackendMessage(m[1], localize)) ?? trimmed;
  }
  if ((m = trimmed.match(/^程序提前结束（退出码 (-?\d+)），未返回可识别的原因$/))) {
    return translateTemplate(localize, "程序提前结束（退出码 {0}），未返回可识别的原因", m[1]) ?? trimmed;
  }
  if ((m = trimmed.match(/^(.+)失败$/)) && JOB_NAMES.has(m[1])) {
    return translateTemplate(localize, "{0}失败", localize(m[1])) ?? trimmed;
  }
  if ((m = trimmed.match(/^([^：。]+)失败：([^。]+)。([\s\S]*)$/))) {
    const [, action, reason, next] = m;
    const translatedAction = localize(action);
    const translatedReason = localizeBackendMessage(reason, localize);
    const translatedNext = next ? localizeBackendMessage(next, localize) : "";
    if (translatedAction !== action || translatedReason !== reason || (next && translatedNext !== next)) {
      if (next) {
        return translateTemplate(localize, "{0}失败：{1}。{2}", translatedAction, translatedReason, translatedNext) ?? trimmed;
      }
      return translateTemplate(localize, "{0}失败：{1}。", translatedAction, translatedReason) ?? trimmed;
    }
  }
  return trimmed;
}
