# 插件

Cage 的核心路线是把 Claude 的运行外壳关在轻量化的本地隔离环境中，再通过标准 [MCP](https://modelcontextprotocol.io) 插件，让 Claude 安全、受控地使用本机或环境内能力（比直接暴露 SSH 更安全）。

在 Cage 中，**插件 = MCP + 扩展**，分别对应「给 Claude 的能力」与「定制 Cage 工作台」，一个插件可以两者兼有：

| 用途 | 做什么 | 清单里的写法 |
| --- | --- | --- |
| 给 Claude 的能力（MCP） | 提供工具，让环境里的 Claude 安全、受控地获得本机或环境内的能力 | `contributes.mcpServers` |
| 定制 Cage（扩展） | 在 Cage 侧栏加一个自己的沙箱页面，扩展工作台界面与工作流 | `contributes.views` |
| 运行桌面应用 | 在环境里安装并运行厂商的官方桌面应用，默认跟随最新版本 | `contributes.desktopApps` |

工具由标准的 [MCP](https://modelcontextprotocol.io) 服务提供，通过标准输入输出通信，用任何语言都能写。页面是普通的 HTML，运行在沙箱里。所有社区插件通过开放的插件市场 [cage-plugins](https://github.com/Lee-xeo/cage-plugins) 与 Cage 主仓库解耦协作：社区在市场持续贡献 MCP 工具与页面扩展，Cage 维护团队专注于隔离环境、网络与工作台本身。日常安装、权限管理与使用说明也见[插件与插件市场指南](guide/plugins.md)。

插件可以运行在两个地方，由清单的 `kind` 决定：

| `kind` | 运行位置 | 适合 |
| --- | --- | --- |
| `host` | 你的电脑 | 需要电脑本身的能力：屏幕、键鼠、本机文件和程序 |
| `guest` | 环境内 | 只在环境里做事：查看状态、处理环境里的文件、安装在环境里的服务 |

## 使用

### 安装

在侧栏打开「插件」，先打开总开关，再进入「插件市场」选择插件安装。市场的内容来自 [cage-plugins](https://github.com/Lee-xeo/cage-plugins) 仓库：应用读取其中的索引，下载压缩包并核对 SHA-256 后安装。读取和下载走电脑自己的网络，遵循系统代理设置。

也可以手动安装：把插件目录放进配置目录的 `plugins/` 下（默认 `~/.claude-cage/plugins/<插件名>/`），然后在插件页点击「重新加载」。

- **默认预装插件**：应用初始化时会自动部署随包附带的默认预装插件（`cage-plugin` 插件工坊与 `use-cage` 控制 Cage），状态保存在配置目录的 `default-plugins.json` 中（属于个人数据，升级与重装时保留）。默认预装插件支持停用和卸载；用户卸载后后续更新不再强制装回，可随时在插件设置中一键恢复。若未本地修改过文件，应用升级时会自动平滑更新默认预装插件（旧版保留于 `plugins-removed/`）。其中 `cage-plugin` 同步进环境时使用中性目录名并附带脱敏后的市场索引快照供 `search_plugins` 检索；`use-cage` 通过仅绑定本机回环地址的一次性随机令牌提供窄口白名单接口，不接触共享目录、网络出口、插件授权与密钥，且扩展启停/重载工具硬编码禁止「始终允许」。
- **草稿审核与安装**：通过 `cage-plugin` 提交的自定义插件草稿会冻结在环境内 `~/.local/share/plugin-submissions/` 中，不会直接写入宿主机 `plugins/`。用户可在工作台插件侧栏的「待审核草稿」中只读检查每个文件、确认运行位置（`guest` 或 `host`）后一键转译安装并重载；覆盖同名插件时旧版移入 `plugins-removed/`，并强制撤销该插件原有的「始终允许」授权。
- 卸载或更新时，原来的插件目录不会被删除，而是移到配置目录的 `plugins-removed/` 下。
- 每个插件可以单独停用，包括内置的命令插件。停用的插件不启动，工具不再提供给 Claude，页面也不再显示；文件保留在原处。
- 主机插件的程序启动时使用与终端相同的 `PATH`，清单里可以直接写 `python3`、`node`、`npx`、`uvx` 等命令；从桌面打开应用时也按终端的 `PATH` 查找这些命令。本机缺少 `uv`、`uvx`、`npx`、`node` 或 `python3` 等运行环境时，插件详情页会提示缺少的程序名称与安装入口，安装后点击「重新加载插件」即可生效。
- 环境内插件的文件在启动时同步到环境里的 `~/.local/share/plugins/<插件名>/`，程序在那里运行。清单 `plugin.json` 不会同步过去。
- 插件市场按分类展示插件，分类由插件在清单的 `categories` 里声明。
- 基于开源项目的插件，详情页的「上游项目」列出项目名、版本、许可证和项目地址；「信息」栏还会显示市场索引声明的运行环境（`runtime`）与支持的系统。这些信息来自市场索引，只用于展示。
- 市场里部分主机插件（如 `blender`）在第一次启动时由 `uv` 或 `npx` 获取上游程序的固定版本，因此需要先在本机安装 [uv](https://docs.astral.sh/uv/) 或 Node.js；文档与办公类等环境内插件直接复用环境内的 Python 3 或 Node.js，按锁定哈希校验依赖，无需在本机安装额外运行环境。

### 授权

主机插件以你的用户权限在电脑上运行，不受环境的隔离保护。是否放行由你决定，决定可以被记住：

- 调用前弹出确认窗口，显示插件、工具和参数。可以拒绝、只允许这一次，或选择「始终允许」。
- 「始终允许」按插件记录，覆盖该插件的全部工具；内置的命令插件按项目记录。记住之后不再询问，应用重启后依然有效。
- 授权可以在插件详情页的「授权与权限」里查看、授予和撤销，撤销后立即恢复逐次询问。从市场安装时也可以勾选「安装后始终允许」。
- 插件更新后如果声明了你当初没有同意过的权限，授权自动失效，会重新询问。卸载插件时授权一并清除。
- 插件自己的页面调用工具，与 Claude 调用工具用的是同一份授权。
- 环境内插件如果没有声明任何权限，调用时不询问：它只在 Claude 本来就能操作的环境里行动。
- Windows 的确认窗口只有允许和拒绝两个按钮，「始终允许」在插件详情页里设置；内置的命令插件在详情页按项目设置，只能选择已打开的项目。
- 确认窗口 2 分钟没有回答，按拒绝处理。
- 参数超过 4000 字节，或含有控制字符、不可见的格式字符时，不弹出确认窗口，直接拒绝，即使已经「始终允许」：这样的请求无法在窗口里完整显示。较长的内容请写进文件，再把文件路径传给工具。
- Linux 上弹出确认窗口需要 `zenity` 或 `kdialog`，两者都没有时，需要确认的调用一律被拒绝。
- 手动删除插件目录不会清除授权；之后装回同名插件，会沿用原来的授权。在插件页卸载才会一并清除。

授权记录保存在配置目录的 `host-grants.json`，属于个人数据，更新和重装应用时保留。

### 设置与密钥

插件可以在清单里声明需要你填写的设置，例如服务地址或访问令牌。声明了设置的插件，详情页多一个「设置」标签：

- 必填设置没有值时，插件照常列出，状态为「需要配置」，但不启动；填好并保存后自动启动。
- 保存后插件会重新启动，新值在下次启动时生效。值以环境变量交给插件的程序，不写在命令行里。
- 密钥类型的设置用你的 Cage 账户加密保存在配置目录的 `plugin-secrets.json`，保存后不再显示，只能替换或清除。其他设置保存在 `plugin-settings.json`。
- Cage 锁定时读不到密钥：用到密钥的插件会停止，解锁后自动重新启动；锁定期间也不能保存密钥。
- 环境内插件同样可以收到密钥，但密钥会离开这台电脑，环境内以同一用户运行的其他程序（包括 Claude）也可能读到它。每次为环境内插件保存密钥前，都需要你在确认窗口中确认这一风险。
- 在插件页卸载插件时，它的设置一并清除。手动删除插件目录不会清除设置。

## 开发

### 目录

```
my-plugin/
├── plugin.json      清单
├── i18n.json        多国语言说明（上架插件市场必需）
├── icon.svg         图标，可省略
├── server.py        MCP 服务
└── view/            页面，可省略
    ├── index.html
    ├── app.js
    └── style.css
```

目录里只能有普通文件和文件夹，不能有链接。从市场安装、同步到环境、页面读取文件和读取图标时都会检查；手动放进来的主机插件不检查这一点，但同样不应包含链接。

### 清单 `plugin.json`

```json
{
  "name": "system-status",
  "displayName": "System Status",
  "version": "0.1.0",
  "description": "Shows processor load, memory and disk use.",
  "icon": "icon.svg",
  "engines": { "cage": ">=0.2.0" },
  "kind": "guest",
  "permissions": [],
  "categories": ["system"],
  "contributes": {
    "mcpServers": [
      { "id": "status", "command": ["python3", "server.py"] }
    ],
    "views": [
      { "id": "status", "title": "System Status", "icon": "icon.svg", "entry": "view/index.html", "server": "status" }
    ]
  }
}
```

| 字段 | 必需 | 说明 |
| --- | --- | --- |
| `name` | 是 | 小写字母、数字和连字符，必须与目录名相同，不超过 40 个字符 |
| `displayName` | 否 | 界面上显示的名称，省略时用 `name`，不超过 80 个字符 |
| `version` | 是 | 版本号，写成 `1.2.3` 的形式。上架时由打包脚本检查格式 |
| `description` | 是 | 一句话说明，不能为空 |
| `icon` | 否 | 插件目录内的一张 PNG 或 SVG 图片，不超过 256 KB。文件不存在或超过大小时不报错，只是不显示 |
| `engines.cage` | 是 | 需要的应用版本：`>=X.Y.Z`，或 `^X.Y.Z`（该版本起、到下一个不兼容版本之前）。不满足时插件会列出，但不启动 |
| `kind` | 是 | `host` 或 `guest`，见上文 |
| `platforms` | 否 | 仅主机插件：支持的系统 `darwin`、`linux`、`windows`，省略表示都支持 |
| `permissions` | 是 | 插件会做的事，必须写明，没有就写空列表。可选值：`files`（读写文件）、`commands`（运行命令）、`screen`（查看屏幕）、`input`（控制键盘和鼠标）、`network`（访问网络）。环境内插件只需声明越出环境的能力 |
| `categories` | 否 | 插件在市场里所属的分类，最多 3 个，见下表。需要 `engines.cage` 至少为 `0.2.0`：更早的应用不认识这个字段，会拒绝整份清单 |
| `contributes` | 是 | 插件提供的内容，见下 |

清单里不能有其他字段，嵌套对象里也一样。字段名区分大小写。

`categories` 的可选值：

| 值 | 分类 |
| --- | --- |
| `desktop` | 桌面控制：屏幕、键鼠、窗口 |
| `developer` | 开发 |
| `system` | 系统与状态 |
| `network` | 网络 |
| `files` | 文件 |
| `productivity` | 效率 |
| `example` | 示例 |

应用遇到不认识的分类时保留但不显示，所以市场以后新增分类，不会让旧版应用拒绝插件。这只针对分类的值：`categories` 字段本身从 0.2.0 起才被认识，`engines.cage` 低于 `0.2.0` 的插件写了它，在更早的应用里会显示为兼容，安装时却因清单含未知字段而失败。上架到市场的插件只能使用上表中的值。

`contributes.mcpServers[]`：

| 字段 | 说明 |
| --- | --- |
| `id` | 小写字母、数字和连字符，插件内唯一，不超过 40 个字符 |
| `command` | 程序和参数。带路径的程序从插件目录算起；不带路径的，插件目录里有同名文件时用这个文件，否则按 `PATH` 查找。参数中可使用 `${PROJECT_DIR}` 占位符，启动时替换为当前打开的项目目录（主机插件为本机路径，环境内插件为环境内挂载路径；可执行文件名本身不替换）。切换项目时只在后台重新启动用到该占位符的插件，其他插件及其进行中的调用不受影响；当前项目不是从本机共享的文件夹（未打开项目、环境默认目录等）时，这类插件不启动并显示原因，不会改用其他项目的目录 |

目前每个插件最多一个 MCP 服务。写了多个服务的插件会列出并显示原因，但不启动。

主机插件的程序继承应用的环境变量，但不包括应用自己的变量；环境内插件的程序只拿到 `HOME`、`USER`、`LOGNAME`、`SHELL`、`PATH`、`LANG`、`LC_ALL`、`TZ`。两种插件都会另外拿到 `contributes.settings` 里设置的值。

`contributes.settings[]`（需要 `engines.cage` 至少为 `0.2.0`，最多 20 项）：

| 字段 | 必需 | 说明 |
| --- | --- | --- |
| `id` | 是 | 小写字母、数字和连字符，插件内唯一，不超过 40 个字符 |
| `title` | 是 | 表单里显示的名称，不超过 80 个字符 |
| `description` | 否 | 显示在名称下方的说明，不超过 500 个字符 |
| `type` | 是 | `string`（文本）、`secret`（密钥，加密保存、不再显示）、`boolean`（开关，值为 `true` 或 `false`）或 `choice`（从 `choices` 中选一个） |
| `env` | 是 | 交给程序的环境变量名：大写字母开头，只含大写字母、数字和下划线，不超过 64 个字符，插件内唯一。不能是 `HOME`、`USER`、`LOGNAME`、`SHELL`、`PATH`、`LANG`、`LC_ALL`、`TZ`，也不能以 `LD_` 或 `DYLD_` 开头 |
| `required` | 否 | 为 `true` 时，没有值的插件不启动 |
| `default` | 否 | 用户没有填写时使用的值。密钥不能有默认值；开关只能是 `true` 或 `false`，没有默认值时为 `false`；选择必须是 `choices` 之一 |
| `choices` | 选择类型必需 | 可选的值，1 到 20 个，各不相同，每个不超过 80 个字符 |

值不超过 8192 字节，不能含有换行或 NUL。没有值且不是必填的设置不设置环境变量。

```json
"settings": [
  { "id": "url", "title": "Server address", "type": "string", "env": "NOTES_URL", "required": true },
  { "id": "token", "title": "Access token", "type": "secret", "env": "NOTES_TOKEN", "required": true }
]
```

环境内插件的值在启动时经 SSH 标准输入写入环境里只有该用户能读的临时文件 `~/.local/share/plugins/<插件名>.env`，启动脚本读取后立即删除，不会出现在命令行或进程参数里。

`contributes.views[]`：

| 字段 | 必需 | 说明 |
| --- | --- | --- |
| `id` | 是 | 小写字母、数字和连字符，插件内唯一，不超过 40 个字符 |
| `title` | 是 | 侧栏和标题里显示的名称，不超过 80 个字符 |
| `entry` | 是 | 插件目录内的 HTML 文件。页面只能加载它所在文件夹里的文件 |
| `icon` | 否 | 格式同顶层 `icon`。目前侧栏显示的是插件的顶层图标，这个字段为以后预留 |
| `server` | 否 | 页面可以调用的 MCP 服务的 `id`。省略表示纯静态页面 |
| `openUrls` | 否 | 页面可以请求打开的地址模式，见下 |

清单里的文件路径不超过 200 个字符。

`openUrls` 的写法：

- 协议为 `https://` 加明确的主机名，或 `http://127.0.0.1`、`http://localhost`。`https://localhost` 不合法。
- 必须写路径，至少是 `/`：`https://example.com` 不合法，要写 `https://example.com/*`。
- 端口可以写数字或 `*`。不写端口只匹配默认端口，所以 `http://localhost/*` 只匹配 80 端口，本机服务通常要写 `http://localhost:*/*`。
- 路径只能在结尾用 `*`。匹配时会连同查询串一起比较，所以要放行带查询串的地址，路径要以 `*` 结尾。

`contributes.desktopApps[]`（只用于环境内插件，需要 `engines.cage` 至少为 `0.3.2`，最多 4 个）：

插件可以让 Cage 在环境里安装并运行厂商的官方桌面应用，像 Claude Desktop 一样在侧栏打开、显示在自己的画面中。插件只描述**去哪里取最新版本、怎样启动**；下载、验签、安装、更新、卸载这些需要管理员权限的步骤都由 Cage 完成，插件自己的程序不会获得管理员权限。插件不写版本号：安装和“更新应用”总是取厂商当时的最新版本，厂商改变发布方式时升级插件即可。

| 字段 | 必需 | 说明 |
| --- | --- | --- |
| `id` | 是 | 小写字母、数字和连字符，插件内唯一 |
| `title` | 是 | 侧栏和应用顶部显示的名称，不超过 80 个字符 |
| `icon` | 否 | 插件内的 PNG 或 SVG |
| `source` | 是 | 最新版本从哪里来，见下表 |
| `command` | 是 | 启动应用的程序和参数。`apt`、`deb` 来源写软件包安装的绝对路径（位于 `/usr` 或 `/opt` 下）；`archive` 来源写压缩包内的相对路径 |
| `schemes` | 否 | 厂商登录完成后返回应用使用的链接协议，最多 4 个。Cage 浏览器遇到这些链接时交给正在运行的应用。不能使用 `http`、`https`、`claude` 等保留协议，也不能与其他插件重复 |
| `packages` | 否 | 应用另外需要的 Debian 软件包，最多 40 个 |

`source` 的三种写法：

| `type` | 字段 | 安装与更新 |
| --- | --- | --- |
| `apt` | `repository`、`suite`、`components`、`key`、`fingerprint`、`package` | 下载 `key` 并核对 40 位大写的完整指纹 `fingerprint`，不符即停止；再从签名软件源安装 `package`。厂商的软件包安装时自己登记了同一个软件源的，以厂商的登记为准，避免 apt 因重复登记拒绝更新 |
| `deb` | `resolve`、`hosts` | 运行解析程序取得最新 `.deb` 的地址，下载后用 apt 安装 |
| `archive` | `resolve`、`hosts` | 运行解析程序取得最新 `.tar.gz` / `.tar.xz` 的地址，解压到新的版本目录，确认启动程序存在后再切换；保留上一个版本。压缩包里只有一个顶层文件夹时，`command` 从该文件夹内算起 |

解析程序 `resolve` 是插件内一个独立的 `.py` 或 `.sh` 文件（可带参数），以环境内的普通用户、干净的环境变量运行，第一个参数是处理器（`amd64` 或 `arm64`），须在 90 秒内向标准输出打印一个 JSON 对象：

```json
{ "version": "2.35.0", "url": "https://dl.example.com/2.35.0/linux-arm/App.tar.gz", "sha256": "可选，64 位十六进制" }
```

`url` 必须是 `https`，主机名必须是 `hosts` 中列出的官方下载站点之一；厂商发布了校验值时请一并给出 `sha256`。

应用以环境用户本人的主目录运行，可以使用已共享的项目；它的登录凭据保存在单独的密钥环里。应用使用 Cage 的剪贴板、文件选择、上传与输入法，打开的网页进入 Cage 浏览器。卸载只删除应用文件以及安装时登记的软件源和密钥，登录、设置和项目保留在环境中。

#### 旧格式

没有 `contributes` 的清单是旧格式，继续可用，等同于一个主机插件带一个 MCP 服务：

```json
{
  "name": "clock",
  "version": "0.1.1",
  "description": "Tells the current time on this computer.",
  "platforms": ["darwin", "linux", "windows"],
  "permissions": [],
  "command": ["python3", "server.py"],
  "icon": "icon.svg"
}
```

旧格式里不能写 `engines`、`kind`（`host` 除外）和 `activationEvents`，可以写 `categories`。旧格式无法声明版本要求，写了 `categories` 的旧格式清单在 0.2.0 之前的应用里无法加载；需要兼容这些版本时不要写分类。

### 多国语言说明 `i18n.json` 与作者归属

Cage 界面会按当前软件界面语言自动展示插件的名称与说明（精确匹配语言代码，再按主语言码回退，未命中时回退到 `plugin.json` 中的英文 `displayName` 与 `description`）。

多语言翻译放在插件目录内的 `i18n.json`（不写进 `plugin.json`，以兼容较早版本的 Cage）：

```json
{
  "zh-CN": {
    "displayName": "系统状态",
    "description": "在独立页面和 Claude 工具中展示当前环境的处理器负载、内存与磁盘使用情况。"
  },
  "zh-TW": {
    "displayName": "系統狀態",
    "description": "在獨立頁面與 Claude 工具中顯示目前環境的處理器負載、記憶體與磁碟使用情況。"
  }
}
```

- 每个语言条目支持 `displayName`（可选，最多 80 字符）和 `description`（必需，最多 600 字符），且不得包含保留词或控制字符。
- 提交到官方插件市场 [cage-plugins](https://github.com/Lee-xeo/cage-plugins) 时：
  - `i18n.json` 必须完整覆盖 Cage 支持的全部 15 种界面语言（`zh-CN`、`zh-TW`、`ru`、`ar`、`fa`、`ko`、`es`、`my`、`be`、`ps`、`prs`、`am`、`so`、`ti`、`fr`，与 `internal/uilocale/languages.json` 一致）。
  - 需在市场仓库根目录的 `authors.json` 中登记插件名与贡献者的 GitHub 用户名；市场客户端对官方维护的插件显示「Cage 官方」，对社区贡献插件直接显示 GitHub 用户名。

### 工具的命名和说明

工具的名称、标题和说明，以及参数和结果的名称与说明，都会发送给模型，所以只描述工具做什么。其中出现本产品的名字，或者「虚拟机」「客机」「VM」「guest」之类描述运行环境的词，插件会被拒绝加载。只给页面的工具也一样检查。工具返回的结果和错误信息同样会交给模型，请遵守同样的要求。

工具名不能与已有工具重复。`run_command` 和 `command_job` 由内置的命令插件使用。两个插件的工具重名时，按插件目录名排序在前的那个正常加载，另一个加载失败。

### 提示词与斜杠指令（MCP Prompts）

所有已启用插件的 MCP 能力统一通过名为 `computer` 的 MCP 服务接入环境内的 Claude Code、默认 Claude Desktop 以及多账号编号 Desktop 实例。

- **插件自带 Prompts**：如果插件的 MCP 服务声明了 `prompts` 能力（`prompts/list` 与 `prompts/get`），Cage 会自动聚合转发这些 Prompt。Prompt 的名称、标题、说明、参数说明以及运行时返回的内容同样适用上述保留词规则；`commands` 由内置命令插件保留，重名冲突时后加载的插件会加载失败。
- **自动斜杠指令**：对于向模型提供了工具但未自带同名 Prompt 的插件，Cage 会自动生成一个与插件同名的 MCP Prompt（带可选 `request` 参数），用于在对话中显式触发该插件的工具。仅提供页面工具（`visibility: ["app"]`）的插件不会生成面向模型的 Prompt。
- **调用方式**：在 Claude Code 中输入 `/mcp__computer__<指令名>`（例如 `/mcp__computer__clock` 或 `/mcp__computer__commands`），在 Claude Desktop 中通过输入框的 `/` 或 `+` 菜单选用 `computer` 下的指令即可显式触发；工具执行仍遵守原有的授权与审批边界。

### 工具给谁用

工具默认既提供给 Claude，也可以被插件自己的页面调用。只想给其中一方时，在工具的 `_meta.ui.visibility` 里写明（这是 MCP Apps 定义的字段）：

```json
{
  "name": "disconnect",
  "description": "Leave the network.",
  "inputSchema": { "type": "object", "properties": {} },
  "_meta": { "ui": { "visibility": ["app"] } }
}
```

| 写法 | 含义 |
| --- | --- |
| 不写，或 `["model", "app"]` | Claude 和页面都能用 |
| `["app"]` | 只给页面：不出现在 Claude 的工具列表里，Claude 也调用不了。适合只应由用户亲手点的操作 |
| `["model"]` | 只给 Claude：页面调用不了 |
| `[]` | 谁都不能用 |

这条限制由应用执行，不依赖插件自觉。只给页面的工具不占用工具名，不会与其他插件的工具重名冲突。

### 页面

页面显示在 Cage 的工作区里，入口在主侧栏。它运行在沙箱框架中：

- 可以运行脚本，但没有工作台的身份，拿不到任何凭据。
- 只能加载入口文件所在文件夹里的脚本、样式、图片和字体。脚本必须放在单独的文件里，不能写在 HTML 里；样式可以内联，图片可以用 `data:` 地址。
- 不能联网，不能提交表单，不能再嵌套框架，不能使用 Worker 和 `eval`。需要的数据通过调用本插件的工具获得。
- 页面没有自己的源，所以 `localStorage`、`alert`、`window.open` 等都不可用。要保存状态请用 `ui/setState`，要提示用户请用 `ui/notify` 或 `ui/confirm`，要打开网址请用 `ui/openUrl`。
- 文件按扩展名识别类型：`.html`、`.htm`、`.js`、`.mjs`、`.css`、`.json`、`.txt`、`.svg`、`.png`、`.jpg`、`.jpeg`、`.gif`、`.webp`、`.ico`、`.woff`、`.woff2`、`.ttf`、`.otf`、`.wasm`。其他扩展名的文件不会提供给页面。单个文件不超过 20 MiB。
- 离开页面时页面会被关闭，下次打开时重新加载。需要保留的内容请用 `ui/setState` 保存。
- 主机插件的页面只能在这台电脑上的应用窗口（或经 SSH 认证的桌面客户端）里调用工具；主机插件总开关关闭时，主机插件的页面不显示。

### 远程主机与自动同步

当 Cage 部署在远程主机（包括云端 Linux 容器与远程 VM 主机）并通过桌面端 SSH 连接时：

- **远程主机插件运行**：远程环境支持运行环境内插件（`guest`）、远程宿主机上的无头命令行/开发类主机插件（`host`）以及内置 `commands` 插件（仅作用于远程宿主机打开的项目目录，不触碰客户端反向共享目录）。
- **自动增量同步**：连上并解锁远程主机后，桌面端会自动把本机兼容的插件同步到远程（单向增量：只新增和升级兼容版本，不删除远程单独安装的插件；不支持目标系统或无头服务器无法使用的屏幕/键鼠类插件会自动跳过）。更新覆盖时旧版本移入远程的 `plugins-removed/` 保留；本机授权（`host-grants.json`）与密钥（`plugin-secrets.json`）不会自动同步。
- **审批弹窗回流本机**：远程主机需要确认时，审批请求经加密连接传回当前桌面端，在你本机弹出系统原生确认窗口，并明确标出远程主机名称；离线且未授「始终允许」的请求按拒绝处理。
- **本机插件反向共享**：对于只在你面前的电脑上可用的插件（如 `blender`、`desktop-control`），可在远程工作台的「插件设置 → 共享本机插件给当前远程」中按插件显式开启。开启后，远程环境内的 Claude 调用工具时会经 SSH 隧道传回你本机弹窗审批并执行；断开连接或关闭开关后立即收回，不留残留。
- **扩展页面代理**：远程插件的沙箱页面经本机网关代理并由本机强制施加严格 CSP 与 `sandbox allow-scripts`，不透传远端 Cookie 或策略头。

页面通过 `postMessage` 与工作台通信，消息是 JSON-RPC 2.0 对象。页面必须先调用 `ui/initialize`。

| 方法 | 参数 | 结果 |
| --- | --- | --- |
| `ui/initialize` | `{"protocolVersion":1}` | `{"protocolVersion":1,"plugin","view","theme":{"scheme":"light"或"dark","tokens":{…}},"locale","state"}`。目前 `locale` 固定为 `zh-CN` |
| `tools/list` | 无 | `{"tools":[{"name","description","inputSchema"}]}` |
| `tools/call` | `{"name","arguments":{…}}` | MCP 的调用结果：`{"content":[…],"structuredContent"?,"isError"?}`。工具名不超过 200 个字符 |
| `ui/openUrl` | `{"url"}` | `{}`。地址必须匹配清单的 `openUrls`，不超过 4096 个字符，在应用的内置浏览器里打开 |
| `ui/confirm` | `{"title","message","confirmLabel"?,"danger"?}` | `{"confirmed":true或false}`。标题不超过 200 个字符，正文不超过 2000，按钮文字不超过 40 |
| `ui/copy` | `{"text"}` | `{}`。不超过 64 KiB |
| `ui/notify` | `{"message","tone"?:"info"或"error"}` | `{}`。不超过 500 个字符 |
| `ui/setState` | `{"state":<任意 JSON>}` | `{}`。序列化后不超过 64 KiB，超过时返回 -32002。下次 `ui/initialize` 时在 `state` 里取回。状态保存在当前窗口，不跨设备同步 |

`theme.tokens` 是工作台当前主题的颜色，名称为 `paper`、`ink`、`strong`、`muted`、`line`、`chrome`、`hover`、`selected`、`accent`、`link`、`focus`、`input`、`button`、`button-ink`、`success`、`danger`。值是 CSS 颜色，主题未定义的名称不会出现。

工作台会向页面发两种通知：`ui/themeChanged`（`{"scheme","tokens"}`）和 `ui/visibilityChanged`（`{"visible"}`）。离开页面时页面会被关闭，所以 `ui/visibilityChanged` 只在整个窗口被隐藏或恢复时发出。

错误码：

| 代码 | 含义 |
| --- | --- |
| -32001 | 用户在确认窗口里拒绝了，或无法弹出确认窗口 |
| -32002 | 不允许：尚未初始化、调用的工具不提供给页面、地址不匹配 `openUrls`、超出限制、不在这台电脑的窗口里 |
| -32003 | 插件没有在运行，或页面没有绑定服务 |
| -32004 | 工具调用 60 秒没有结果。等待用户确认的时间不计在内 |
| -32005 | 工具执行出错，或地址无法打开 |
| -32700、-32600、-32601、-32602 | JSON-RPC 的标准错误：消息无法解析、不是有效请求、没有这个方法、参数不对 |

页面发出的单条消息不超过 1 MiB（按序列化后的字符数计算），同一页面同时进行的请求不超过 8 个。工具自己报告的失败不算协议错误，返回结果并带 `isError: true`。

最小的页面脚本。用 `<script type="module" src="app.js"></script>` 加载，因为其中用到了顶层 `await`：

```js
const waiting = new Map();
let nextId = 1;

function request(method, params) {
  return new Promise((resolve, reject) => {
    const id = nextId++;
    waiting.set(id, { resolve, reject });
    window.parent.postMessage({ jsonrpc: "2.0", id, method, params }, "*");
  });
}

window.addEventListener("message", event => {
  const message = event.data;
  if (event.source !== window.parent || !message || message.jsonrpc !== "2.0") return;
  const pending = waiting.get(message.id);
  if (!pending) return; // 通知：按 message.method 处理
  waiting.delete(message.id);
  if (message.error) pending.reject(new Error(message.error.message));
  else pending.resolve(message.result);
});

await request("ui/initialize", { protocolVersion: 1 });
const result = await request("tools/call", { name: "system_status", arguments: {} });
```

### 本地调试

1. 把插件目录放进配置目录的 `plugins/` 下，目录名与 `name` 相同。
2. 在插件页点击「重新加载」。清单有问题时，插件会列出并显示原因。
3. 修改文件后再次「重新加载」。环境内插件的文件会在这时重新同步。

MCP 服务可以脱离 Cage 单独测试：它只是一个读标准输入、写标准输出的程序。

### 上架到市场

市场的规则、打包方法和收录要求见 [cage-plugins](https://github.com/Lee-xeo/cage-plugins)。向市场提交新插件或界面/交互变更时，请按仓库的 PR 模板填写插件信息、权限、数据流向与实测范围，并在 PR 描述中直接拖入插件在 Cage 中的**真实运行效果图**（截图、GIF 或短录屏，上传前遮盖敏感信息，不要提交进插件目录或压缩包）。

应用安装市场里的插件时还会检查：压缩包解压后不超过 100 MiB、不超过 2000 个文件；索引里写的权限必须与插件清单完全一致。

索引条目可以带 `source`（说明上游开源项目：`project`、`version`、`license`、`url`）与 `runtime`（运行环境标识，如 `python3`、`node`、`npx`、`uv`、`powershell`）。格式不对时应用不显示它们，插件照常列出。它们由市场的打包脚本生成，不写进 `plugin.json`：较早的应用读取清单时不接受未知字段。

### 尚未提供

规范里已经预留、应用还没有实现的部分：

- `contributes.uriHandlers`（处理自定义协议的链接）和 `activationEvents`：字段名已预留，清单里可以写，但不生效，格式也还没有定下来，现在请不要使用。
- 一个插件多个 MCP 服务。
- MCP 服务主动发出的通知不会转给页面，页面需要自己定时调用工具取最新状态。

## 示例

- [`clock`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/clock)：最小的主机插件，旧格式，一个工具。
- [`desktop-control`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/desktop-control)：主机插件，查看屏幕并使用键鼠，声明了 `screen` 和 `input` 权限。
- [`system-status`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/system-status)：环境内插件，一个页面加一个工具。
- [`browser`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/browser)：环境内插件，用环境里的 Chromium 浏览网页，基于 microsoft/playwright-mcp。
- [`excel-workbooks`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/excel-workbooks)、[`word-documents`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/word-documents)、[`powerpoint-slides`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/powerpoint-slides)：环境内插件，读写 Office 文件，不需要安装 Office 或本机 `uv`，通过 `launcher.py` 按 `pylock.json` SHA-256 锁定校验启动。
- [`blender`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/blender)：主机插件，操作正在运行的 Blender，基于 ahujasid/blender-mcp。

向市场提交插件的方法见 [cage-plugins](https://github.com/Lee-xeo/cage-plugins)。
