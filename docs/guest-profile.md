# 创建客机时配置名称与设备

首次设置的“系统与存储”步骤中，展开“客机名称与初始设备配置”。可以直接填写，也可以留空让 Cage 随机生成；“重新随机生成名称和尺寸”用于预览一组新值。预览不会创建磁盘或保存环境身份。

开始安装时，配置保存到当前独立配置目录的 `guest-profile.json`。继续中断的安装、重启、重连和更新组件都会复用它。安装已经开始后不能通过首次设置替换这些值，以免使账户、文件权限、SSH 和浏览器数据失去对应关系。需要另一组身份时创建独立的新环境。

## 可配置项

| 设置 | 默认行为 | 手动填写与适用范围 |
| --- | --- | --- |
| 主机名 | 使用真实 PC 型号简称或常见 PC 命名样式随机生成 | 1–15 位 ASCII 字母、数字、连字符；字母开头，字母或数字结尾。适用于新建 Linux、macOS 和 Windows 客机 |
| Linux 工作账户 | 常见名字加两位数字，如 `alex73` | 1–24 位小写字母、数字、下划线，以字母开头；不使用系统账户 |
| Linux 浏览器账户 | 另一独立随机账户 | 同上，必须与工作账户不同，保留两个账户之间的权限边界 |
| 两个 Linux 主目录 | 分别使用 `/home/<账户>` | 可各自填写 `/home/` 下独立的单层目录；目录名以字母开头，最长 63 位，仅含 ASCII 字母、数字、点、连字符、下划线。不能共用目录或使用基础镜像保留的 `cage`、`cage-browser` 目录 |
| Linux 音频设备 | 从 12 个真实 USB 音频产品型号中自动选择 | 型号描述与设备名保持对应，设备名加独立随机后缀；首次设置不需要手填。已有自定义音频配置继续保留 |
| Linux 虚拟显示 | 从 1920×1080、1920×1200、2560×1440、2560×1600、2880×1800、3840×2160 中随机选择 | 宽 800–3840、高 600–2160，宽高一起填写 |
| 浏览器初始窗口 | 从适合显示尺寸的常见窗口大小中随机选择 | 同上；Linux 初始窗口不能大于虚拟显示。实际工作台视窗、全屏、像素密度和用户后续调整仍会影响最终可见尺寸 |
| Linux 控制扩展名称 | 从普通标签/窗口管理名称中随机选择 | 1–64 位普通 ASCII 文字；扩展安装路径与由路径派生的扩展 ID 同时按环境随机化 |
| WebGL | 保持原生行为 | 可选择关闭网页 3D 图形能力；Linux Chromium、Windows Chromium 和 macOS Chrome 均在启动进程时应用 |
| Canvas 图像读取 | 保持原生行为 | 可选择阻止网页读取 Canvas 图像，覆盖同一浏览器进程的普通窗口和授权窗口 |
| 浏览器图形后端 | 自动使用客机可用设备 | 按客机系统选择 ANGLE 的 Metal、D3D11、Vulkan、OpenGL，或 SwiftShader 软件兼容模式 |
| Linux QEMU 虚拟 GPU | 保持现有设备 | Linux 主机可选择实验性的 VirGL + DRI3，需宿主 GPU、支持 OpenGL 的 QEMU 及支持 `-rendernode` 的 TigerVNC；容器不提供此选项 |

名称、设备描述和扩展名称不接受控制字符、引号或脚本片段。尺寸有明确上限。不能通过这些设置注入任意浏览器参数、修改代理、关闭沙箱或开放调试端口。

建议选择自然、易记、与你以往环境身份不同的名称。账户、设备名称和尺寸形成一套持久配置；同一环境重启或重连后不重新抽取。

音频库随程序嵌入，包含 Focusrite Scarlett 第四代、Behringer U-PHORIA UMC202HD、Steinberg UR-C、Yamaha AG MK2 的 12 款产品，来源链接随记录保存在 `internal/guestprofile/audio-models.json`。这些名称是 Linux 虚拟输出的命名素材，不会把空输出设备变成实际 USB 声卡，也不伪造 USB VID/PID 或实体设备序列号。macOS、Windows 原生音频仍使用各自实际驱动报告的设备。

## 平台行为

Linux 的账户配置同时应用于 Lima 初始化、容器首次启动、服务运行账户、sudo 规则、浏览器主目录、音频采集和附件路径校验。剪贴板协议中的工作/浏览器角色与内部服务名称继续保持稳定；它们不作为实际 Linux 登录名使用。容器从镜像的初始账户改名时只操作本环境的文件系统，并验证工作账户的 UID 和浏览器账户的独立性。

Lima 从基础模板派生工作实例时使用本环境已经选定的主机名和账户，UUID 与 MAC 仍独立生成。没有 Cage 创建配置的普通 Lima 克隆保留原有的随机主机名行为。

Windows 正式安装会把选定的主机名写入新系统的应答文件。独立运行 `new-windows-test-vm.ps1` 或 `start-windows-evaluation-setup.ps1` 时，可以通过 `-ComputerName MY-PC` 手填；不指定则使用 `*`，由 Windows Setup 生成名称。不会重命名导入的已有 Windows 系统或更改宿主 Windows 的计算机名。

macOS Chrome 和 Windows 浏览器保存本客机用户的公开浏览器创建设置。它们应用于新启动的浏览器进程；已有窗口不会仅因重连而关闭。macOS 的手动主机名仍通过 Lima 的客机初始化配置应用。

没有 `guest-profile.json` 的旧环境沿用已有账户和设备设置。配置文件损坏、不完整或包含未知字段时会报错，不会自动替换已有账户或磁盘。

## 图形选项的含义

虚拟 GPU 的 `Apple Paravirtual device` 名称来自实际图形驱动。ANGLE 的 Metal 后端改变浏览器的图形 API 路径，不能移除 Apple Virtualization 的虚拟设备，也不代表物理 GPU 直通。Windows 的 D3D11 和 Linux 的 OpenGL/Vulkan 同样依赖客机实际可用的驱动。SwiftShader 明确作为软件模式提供，检测会区分软件回退和虚拟 GPU。

| 客机 | 可选浏览器后端 |
| --- | --- |
| macOS | 自动、Metal、OpenGL、SwiftShader |
| Windows | 自动、D3D11、OpenGL、SwiftShader |
| Linux | 自动、Vulkan、OpenGL、SwiftShader |

Linux 主机的新建 Linux 虚拟机可额外选择 VirGL：使用 QEMU `egl-headless` 和加速版 virtio GPU，客机通过 Mesa、TigerVNC 的 DRI3 render node 提供 OpenGL 渲染。创建前检查当前用户是否可打开宿主 DRM render 节点；置备时检查客机 `/dev/dri/renderD128`、VNC 参数支持和浏览器账户访问权限，缺少条件会明确失败。不会修改宿主驱动、设备权限或防火墙。该路径未接入 Venus/Gfxstream，不能与 Vulkan 选项组合。驱动仍会报告 VirGL；这不是从显卡型号库中任选一款物理 GPU 的实现。

浏览器保持实际安装版本的原生 UA 与 Client Hints，不拼接随机浏览器版本、操作系统或显卡型号。桌面浏览器的 UA 通常不包含实体主板型号，修改字符串不能提供与该硬件一致的渲染行为。

关闭 WebGL 使用 Chromium 自身的 `--disable-3d-apis` 和 `--disable-webgl`；阻止 Canvas 读取使用 `--disable-reading-from-canvas`。这些选项控制浏览器能力，不往图像中添加随机噪声。

依赖 3D 渲染、图片编辑、Canvas 导出或验证码的网站可能受到影响，因此这两项默认保留原生行为。关闭功能本身也可被网页观察到。其他浏览器、应用、内核、字体、GPU、软件版本和 Cage 内部服务仍可能存在共同特征；这些设置不保证环境无法被识别。

浏览器运行所必需的受控网络、沙箱和管理参数仍由 Cage 管理。相关参数的差异来自用户选择的图形能力、窗口尺寸和独立扩展，而非无意义地打乱启动参数顺序。

## 检测实际生效情况

环境启动后，在安全检查面板点击“检测浏览器”或“检测 Claude Desktop”；Desktop 工具栏也提供“检测环境”。

- 浏览器在本地诊断文档核对 UA 与 Client Hints 的主版本和平台、显示信息、Canvas 已知颜色与字体样本校验值、WebGL 着色器编译及参考像素，并区分软件渲染、虚拟 GPU 和驱动报告的渲染器。
- Linux 同时核对实际主机名、工作账户、主目录和音频输出描述。浏览器音频标签受权限限制；检测不申请麦克风权限，缺少标签时显示“未观测”。诊断文档视口不等于用户当前窗口尺寸，也不用于验证初始窗口大小。
- Claude Desktop 核对可获得的安装、版本和运行状态，以及 Linux 系统配置。官方应用没有受支持的应用内渲染自检接口，因此其内部渲染器明确显示“未观测”；不会把浏览器探针结果当作 Desktop 内部证据。

检测不读取登录资料、设备 ID 或序列号，不为 Desktop 注入代码或开启调试端口，不进行外部指纹查询。结果用于发现配置不一致、渲染错误和软件回退，不提供“真实物理设备”或匿名性认证。相同 Canvas 校验值也不单独判为失败。

VirGL、macOS Metal 和 Windows D3D11 仍需在对应平台实际启动客机验收；本次 Linux 开发机未完成这些硬件路径的端到端验证。

参考：[QEMU virtio-gpu 后端](https://www.qemu.org/docs/master/system/devices/virtio/virtio-gpu.html)、[TigerVNC rendernode](https://github.com/TigerVNC/tigervnc/blob/master/unix/xserver/hw/vnc/Xvnc.man)、[Chromium 官方开关定义](https://chromium.googlesource.com/chromium/src/+/HEAD/content/public/common/content_switches.cc)、[Windows ComputerName](https://learn.microsoft.com/en-us/windows-hardware/customize/desktop/unattend/microsoft-windows-shell-setup-computername)、[机器身份](guest-identity.md)。
