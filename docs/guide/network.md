---
title: 网络与代理
description: 导入节点与订阅、配置多次中转，理解联网检查、时区和出口变化规则。
group: 开始使用
order: 2
keywords: Clash Mihomo YAML VLESS REALITY ML-KEM SOCKS5 SSH 私钥 住宅 IP DNS DoH 暂停 断网 代理链 订阅
---
<figure class="guide-shot">
  <a href="../../www/public/docs/screenshots/proxy-subscription.jpg" target="_blank" rel="noopener" aria-label="查看原图：添加订阅链接与首次主机下载选项"><img src="../../www/public/docs/screenshots/proxy-subscription.jpg" alt="添加订阅链接与首次主机下载选项" width="2560" height="1764" loading="lazy" decoding="async" /></a>
  <figcaption>添加订阅链接与首次主机下载选项。截图版本：0.1.6-alpha-preview（开发构建） · macOS 原生应用 · 2026-11-07。<a href="../../www/public/docs/screenshots/proxy-subscription.jpg" target="_blank" rel="noopener">查看原图（2560 × 1764） ↗</a></figcaption>
</figure>

## 从本机代理软件导入

首次安装的代理页，以及本机“设置 → 网络与代理”，会自动查找当前用户配置目录里的 Clash Verge Rev、Clash Verge、Clash/ClashX、Mihomo、FlClash 和 SS/SSR 配置。找到后会显示软件名称、配置名称和可导入节点数。点击“导入此配置”，再在下方选出口、点击“应用代理”。无需启动原软件，也无需填写它的本地端口。

Cage 会复制节点及认证参数，由内置核心独立连接；原软件的配置、分组、分流规则、系统代理和监听端口保持原样。Clash Verge 的订阅记录如果带有 HTTPS 链接，也会保留，可在 Cage 的“订阅”页手动更新；导入本身读取本地副本，不下载订阅。当前生效配置会包含已缓存的 provider 节点；未缓存或不兼容的节点会提示，Cage 不执行原软件的配置脚本。

自动查找限于软件常用的数据目录。便携版、自定义目录、加密配置或其他格式可能无法识别；可从原软件导出含节点的 Clash/Mihomo 配置，再通过“添加来源 → 本地 YAML”导入。远程连接不读取服务器上的代理软件配置；请手动上传要使用的配置。

## 导入自己的代理

入口是首页“配置网络与代理”，或“设置 → 网络与代理”。点击“添加来源”，选择导入方式：

| 来源 | 怎么填 |
| --- | --- |
| 订阅链接 | 输入名称和 HTTPS 节点订阅地址。首次没有可用节点时，可明确勾选“通过主机网络下载这份订阅”。 |
| 本地 YAML | 拖入或点击选择导出的 YAML、YML、JSON 文件（单个不超过 8 MiB），也可直接粘贴 Clash/Mihomo YAML 内容。读取后可编辑，点击“导入并验证”完成导入。 |
| 手动节点 | 选择 HTTP、SOCKS5 或 SSH，填写服务器、端口和认证信息。SSH 默认端口为 22，用户名必填；可选择密码或私钥认证。私钥认证须粘贴完整私钥内容，加密私钥还需填写私钥口令。 |

点击“导入并验证”，进入“节点”，选最终出口，再点“应用代理”。“保存设置”用于其他设置，不能替代应用代理。未应用的选择只是草稿。

“导入并验证”检查节点格式和核心配置，成功后仍需验证实际连接与出口：

1. 应用节点后，在“设置 → 网络与代理”点击“验证出口”。该检查通过所选代理发起真实 HTTPS 请求，确认能取得出口 IP 和位置，并与预期出口核对；主机上其他代理客户端能够访问外网，不能代替这一步。
2. 启动工作环境，查看底部网络状态及检查结果。宿主、环境终端和浏览器的出口采样及其他关键检查通过后，才会显示“已联网”。失败项的处理见[为什么应用了节点仍然离线](#为什么应用了节点仍然离线)。

订阅或本地配置必须包含 `proxies` 或 `payload` 节点。Base64 列表、单个 `vless://` 等分享链接、Xray/v2rayN 原生 JSON、只引用远程 provider 的配置暂不支持。只有分享链接时，先从原客户端导出 Clash/Mihomo YAML；更改文件后缀不能完成转换。导入只采用支持的节点与代理链，不采用订阅中的主机系统代理、TUN、监听端口或直连规则。

普通节点参数保留并交给 Cage 随附的 Mihomo 核心校验。例如，从 FlClash 复制的 VLESS 节点可以保留 `xudp: true`，无需手动改成其他字段。内核可能忽略未知字段；配置校验通过后，仍需应用节点并检查实际出口。环境公网 UDP 的阻断规则不受这些协议选项影响。

VMess、VLESS 和 Trojan 的 WebSocket 节点可以导入旧字段 `ws-path`、`ws-headers`。节点须明确设置 `network: ws`，Cage 会将路径和请求头转换为 `ws-opts.path`、`ws-opts.headers`，已保存的旧节点在应用代理时也会转换。新旧字段可同时存在，路径须一致，请求头按名称合并并忽略名称的字母大小写。相同请求头的值冲突、字段写法重复或旧字段用于其他传输方式时，来源会显示具体原因，需要修改该节点后重新导入。

Hysteria2 节点可使用 `mport` 指定端口跳跃范围、`pinSHA256` 指定证书指纹，导入时会转换为核心支持的字段并保留功能。若节点使用不支持的协议、引用本机文件或网卡、管理字段重复、别名参数冲突或代理链不可用，会跳过该节点，并在来源下显示导入数量、跳过数量和原因。点击「查看原因」可展开具体节点；依赖失败节点的代理链也会跳过。若所有节点均被跳过，导入失败；更新影响当前出口或中转节点时保留原配置，请先应用其他节点再更新。其余节点还须整体通过核心配置校验，校验失败时原配置保持不变，可在设置的「日志与诊断」中导出诊断包查看校验记录。

手动添加 SSH 节点时，可在“服务器公钥”中填写可信公钥，每行一条；留空时不校验服务器身份。

SSH 节点（`type: ssh`）可以把自己的 VPS 直接作为固定出口，服务器上只需开启 SSH，无需另外部署代理服务。可用 `password` 或 `private-key` 认证；`private-key` 须直接填写 PEM 私钥内容（含 `-----BEGIN ... PRIVATE KEY-----`），引用本机私钥文件路径的节点会被跳过，加密私钥可配合 `private-key-passphrase`。建议用 `host-key` 固定服务器公钥（`ssh-keyscan` 输出中的 `ssh-ed25519 AAAA...` 部分），未填写时核心不校验服务器身份。SSH 节点仅转发 TCP。示例：

```yaml
proxies:
  - name: my-vps
    type: ssh
    server: 203.0.113.10
    port: 22
    username: root
    private-key: |
      -----BEGIN OPENSSH PRIVATE KEY-----
      ...
      -----END OPENSSH PRIVATE KEY-----
    host-key:
      - "ssh-ed25519 AAAA..."
```

同一个订阅地址可能按请求的客户端身份返回不同格式。Cage 下载时使用 Clash 客户端身份，服务商返回 YAML 时可以导入；若返回 Base64 或分享链接列表，仍需向服务商获取 Clash/Mihomo YAML。

<figure class="guide-shot">
  <a href="../../www/public/docs/screenshots/proxy-manual.jpg" target="_blank" rel="noopener" aria-label="查看原图：手动填写 HTTP / SOCKS5 节点"><img src="../../www/public/docs/screenshots/proxy-manual.jpg" alt="手动填写 HTTP / SOCKS5 节点" width="2560" height="1764" loading="lazy" decoding="async" /></a>
  <figcaption>手动填写 HTTP / SOCKS5 节点。截图版本：0.1.6-alpha-preview（开发构建） · macOS 原生应用 · 2026-11-07。<a href="../../www/public/docs/screenshots/proxy-manual.jpg" target="_blank" rel="noopener">查看原图（2560 × 1764） ↗</a></figcaption>
</figure>

## 通过本机代理客户端转发

节点依赖 Xray 等客户端支持的传输或握手参数、转换为 Mihomo YAML 后仍无法连接时，可将该客户端的本机 SOCKS5 监听作为 Cage 的上游。此时连接顺序为 `Cage → 本机 SOCKS5 监听 → 原客户端所选节点 → 目标网站`；原客户端须保持运行，并让这些请求经过预期节点，避免其直连规则或自动回退改变出口。

1. 在原客户端启用只监听本机的 SOCKS5 端口，记录实际地址、端口及认证信息。此处的本机指运行 Cage 控制服务的主机；连接远程 Cage 时，`127.0.0.1` 指远程主机，无法使用你电脑上的监听。
2. 在 Cage 手动添加 SOCKS5 节点，或导入以下完整 YAML。示例端口 `10808` 须替换为客户端实际的 SOCKS5 端口；启用认证时，补上该监听要求的 `username` 和 `password`。

```yaml
proxies:
  - name: local-client
    type: socks5
    server: 127.0.0.1
    port: 10808
    udp: false
```

3. 选择并应用该节点，在“设置 → 网络与代理”点击“验证出口”，与原客户端的预期出口核对；随后完成环境联网检查。客户端选中其他节点、规则变化或停止运行都会影响此路径，须重新验证。

Cage 仍通过受管核心转发环境出站，原客户端负责连接上游 VLESS 等服务；这条路径不会开放环境到主机的任意直连，也不放宽出口规则或公网 UDP 限制。各主机上的监听能否被核心实际访问，以“验证出口”和环境检查结果为准。

## 通过中转连接住宅出口

如果住宅节点无法直接连接，在“节点”页切换到“链式代理”。按连接顺序选择中转，将住宅节点设为“最终出口”；需要多次中转时点“添加中转”。节点可以来自不同来源。

应用前检查预览，例如：`Cage → 中转节点 → 住宅代理 → 目标网站`。目标网站看到的是最终出口。订阅更新不会覆盖已保存的组合；使用中的节点被移除时会保留原配置并显示错误，需要你重新选择。

<figure class="guide-shot">
  <a href="../../www/public/docs/screenshots/network-settings.jpg" target="_blank" rel="noopener" aria-label="查看原图：网络与代理页面及出口变化规则"><img src="../../www/public/docs/screenshots/network-settings.jpg" alt="网络与代理页面及出口变化规则" width="2560" height="1764" loading="lazy" decoding="async" /></a>
  <figcaption>网络与代理页面及出口变化规则。截图版本：0.1.6-alpha-preview（开发构建） · macOS 原生应用 · 2026-11-07。<a href="../../www/public/docs/screenshots/network-settings.jpg" target="_blank" rel="noopener">查看原图（2560 × 1764） ↗</a></figcaption>
</figure>

## 为什么应用了节点仍然离线

代理核心就绪和工作环境获准联网是两个步骤。环境启动后先保持业务外网关闭，执行出口、DNS、系统与浏览器配置、共享目录、宿主管理接口和网络阻断等关键检查。DNS 项同时要求解析器只指向环境内地址，且到公共 DNS 的 UDP/53、TCP/53 与 DoT/853 直连未绕过网关；宿主管理接口项还探测链路本地元数据地址和已配置的用户代理端口。IPv6 项要求环境的全局 IPv6 地址为零，并核对路由或拒绝记录。缺少探针结果时显示未知；任一关键项失败或证据缺失都会继续关闭外网。

macOS 主机只从 `networksetup` 报告的硬件端口中选择具有可用 IPv4 地址的物理出口；`utun`、`bridge`、`vmen`、`vmenet`、`vmnet`、`tap`、`lima`、`awdl`、`llw`、`p2p`、`gif`、`stf`、`anpi`、`ap` 等隧道、桥接和虚拟机接口即使带有地址或默认路由，也不会被绑定为代理出口。

出口检测通过所选代理读取公网 IPv4 地址，依次使用 `https://ipv4.icanhazip.com/` 和备用服务 `https://api.ipify.org/`。双栈节点使用其 IPv4 出口完成检查；宿主、环境终端和浏览器使用相同的检测方式。启动检查无法取得有效地址时，业务外网保持关闭，可稍后重试或检查节点。

点击底部网络状态查看失败项，修复后重新检查。核心运行但环境未启动时会显示“待启动环境”。关键检查通过、联网授权有效时才显示“已联网”。

“出口采样不完整”会列出未取得结果的路径：宿主、虚拟机终端或浏览器，以及超时、检测通道返回的 HTTP 状态或缺少地址等原因。终端代理检查通过只证明该次终端采样成功，宿主和浏览器仍须分别取得结果。动态 IP 规则决定哪些出口地址可以接受，采样失败时仍须重新检测。

浏览器的出口采样没有响应或返回 HTTP 503 时，Cage 会等待一秒并重试一次。重试期间外网保持关闭；持续失败时保留最后一次检测结果。浏览器配置不符或响应缺少出口地址时，须先处理对应失败项。环境已经就绪、仅联网检查未通过时，页面会显示“环境已启动，联网检查未通过”，可直接重检网络。

离线时仍可使用终端、共享文件及环境内的本地服务。浏览器地址栏中的 `localhost` 指当前 Cage 环境；开发服务应在 Cage 终端里启动。

<figure class="guide-shot">
  <a href="../../www/public/docs/screenshots/region-settings.jpg" target="_blank" rel="noopener" aria-label="查看原图：跟随出口 IP 的时区与语言设置"><img src="../../www/public/docs/screenshots/region-settings.jpg" alt="跟随出口 IP 的时区与语言设置" width="2560" height="1764" loading="lazy" decoding="async" /></a>
  <figcaption>跟随出口 IP 的时区与语言设置。截图版本：0.1.6-alpha-preview（开发构建） · macOS 原生应用 · 2026-11-07。<a href="../../www/public/docs/screenshots/region-settings.jpg" target="_blank" rel="noopener">查看原图（2560 × 1764） ↗</a></figcaption>
</figure>

## 系统镜像的下载网络

系统镜像使用主机网络下载，与 Cage 环境的代理节点分开。Clash 等软件只开启“系统代理”也可以接管这些下载，无需开启 TUN；下载连接不绑定物理网卡，也不会修改代理软件的节点或规则。下载网站若被主机代理规则设为直连，Cage 遵循该规则；主机未配置代理时使用直连。

- macOS：读取系统 HTTP、HTTPS、SOCKS 代理及 PAC，每次请求和重定向按目标地址解析。
- Windows：读取当前用户的系统代理；PAC 与自动检测通过 WinHTTP 按目标地址解析。
- Linux：支持代理环境变量、GNOME 手动桌面代理及主机 TUN。此构建遇到 GNOME 自动 PAC 会停止并提示改用 TUN 或代理环境变量；其他桌面环境请设置代理环境变量或开启 TUN。

从终端启动时，目标协议对应的 `HTTP_PROXY`、`HTTPS_PROXY` 优先于系统设置，`ALL_PROXY` 作为缺省代理，并遵循对应的 `NO_PROXY` 例外。这些主机设置不会传入环境。代理连不上或已配置的 PAC 解析失败时，下载停止，不因失败切换直连；修复主机代理后，在初始化界面继续安装，已有下载会尝试续传。

系统镜像和开发工具优先使用官方源。Debian 镜像下载失败后尝试提供同一固定版本的国内镜像；Debian 软件源和 Linux 工具在官方源不可用时尝试备用镜像。各下载路径保留原有的摘要、软件包签名或应用签名检查，自定义 Debian 软件源保持原样。

环境内的进程和在环境内下载的开发工具继续走 Cage 的独立代理。系统镜像下载成功不代表网络检查通过，进入工作台仍需完成环境的网络与隔离检查。

## 时区、语言与出口规则

在“设置 → 时区与语言”选择跟随出口或手动设置。自动模式通过代理查询出口位置，不使用主机位置替代。位置服务失败时可重试或切换节点；手动设置时区和语言仍须确认出口国家或地区。

网络页的“IP 变化规则”按最终出口节点保存，支持静态 IP，以及动态 IP 的同一城市、同一州／省、同一国家／地区或不限地区。城市模式还检查国家与州省。切换最终出口时自动恢复该节点的规则；首次使用的节点沿用当前的静态或动态模式及允许范围，通过代理检测并确认非中国大陆出口后绑定自己的 IP 或地区。仅更换中转、刷新订阅、重新检查或重启会保留该节点的基准。

例如，住宅节点001与节点003分别绑定各自的固定 IP，来回切换会自动使用对应地址；同一住宅节点意外更换 IP 时仍会暂停外网，需要确认后明确修改。旧配置未记录出口基准所属的节点时，原规则持续生效，直到明确应用节点后为其检测并建立绑定。

中国大陆出口禁止放行。检测地址命中内置大陆网段库，或定位结果为中国大陆时，业务外网保持关闭；静态 IP 和“不限地区”均不能豁免。无法确认出口国家或地区时也保持关闭，离线终端和文件仍可使用。地区定位和网段库可能存在误差；后台按周期采样，无法保证第三方代理每条连接都使用同一出口。

保存影响系统环境的配置可能要求重启。先保存文件；运行中不会因后台发现位置变化而静默重启任务。

## 暂停、恢复和网络切换

点击底部状态可手动暂停外网。暂停会切断已有外部连接，保留本地任务和文件操作；关闭、重开 Cage 或刷新订阅不会解除手动暂停。

后台会定期核对出口与环境配置。两个出口检测服务均超时时，Cage 在现有联网许可到期前继续重试，不延长许可；到期仍未确认时关闭外网，并尝试完整复检。发现出口或环境配置不符合规则时立即暂停。主机切换网络后会尝试恢复并完整复检；原先人为暂停的状态仍需主动恢复。控制服务失联后，已有网络授权到期关闭。固定出口还需要代理服务本身保证，周期采样不能证明所有连接都走同一 IP。

自动完整复检首次在 15 秒后尝试，失败后依次等待 30、60、120、240 秒，之后每次等待 300 秒；实际开始时间还取决于环境就绪、当前操作和内存压力。网络状态会显示剩余等待时间或等待原因。重新检查必须取得全部关键证据，自动恢复不会修改已保存的出口规则。

## DNS 与诊断

业务 DNS 通过所选代理转发。节点域名的启动解析属于准备通信，与环境业务 DNS 的设置分开。Cage 不修改主机系统代理或 TUN，也不改变本机其他代理软件的节点。

代理页“诊断”可检查核心状态、重启 Cage 核心或选择独立监听端口。端口占用时选择未占用端口即可，修改后按提示重启环境。具体症状见[故障排查](troubleshooting.md#节点已应用但无法联网)。

### VLESS REALITY 握手失败

从 V2rayN 等客户端迁入 VLESS + TCP + REALITY 节点后，可能出现配置校验通过、真实 HTTPS 请求失败，核心日志报 `REALITY authentication failed` 的情况。先逐项对照原客户端与服务端参数，包括服务器、端口、UUID、传输方式、SNI、客户端指纹、flow、公钥和 short-id；配置语法正确仍可能存在握手兼容问题。

参数核对一致后，可按 [Mihomo 官方 TLS 文档](https://wiki.metacubex.one/config/proxies/tls/#reality-optssupport-x25519mlkem768)检查 `reality-opts.support-x25519mlkem768`，该选项用于支持 X25519-MLKEM768 密钥交换。对于需要此兼容选项的节点，在该节点现有的 `reality-opts` 中保留原公钥和 short-id，并添加：

```yaml
reality-opts:
  public-key: <原节点公钥>
  short-id: <原节点 short-id>
  support-x25519mlkem768: true
```

这是节点内的配置片段，需放回完整的 Clash/Mihomo YAML 后重新导入、应用。保留原配置，每次只调整这一项，再重复相同的 HTTPS 与出口检查。部分节点在启用此项后可以恢复连接；请按原节点与服务端要求使用，不要对所有 REALITY 节点无条件添加或重写参数。若仍失败，保留日志继续排查，不能仅凭该错误判断服务端版本或设置，也不要通过关闭证书校验、放宽出口规则或启用直连回退处理。

## 通过 Tailscale 私网使用 SSH

从侧栏「浏览器」下方打开「Tailscale」，在 Cage 浏览器授权后，可让本机与 Cage 环境通过私网双向 SSH。连接步骤、公钥和本机 SSH 服务要求见 [Tailscale 与 SSH 私网](tailscale.md)。

私网访问由用户主动启用，并受设备和 SSH 授权约束。外层通信继续经过 Cage 受控出口，暂停外网或代理中断会影响连接。这个入口与默认阻断的主机、局域网直连分别处理，不开放主机公网监听或子网路由。
