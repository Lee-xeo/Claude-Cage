# 插件

插件用来扩展 Cage，分两种用途，一个插件可以两者兼有：

| 用途 | 做什么 | 清单里的写法 |
| --- | --- | --- |
| 给 Claude 的能力 | 提供工具，让环境里的 Claude 多一项本事 | `contributes.mcpServers` |
| 定制 Cage | 在 Cage 里加一个自己的页面 | `contributes.views` |

工具由标准的 [MCP](https://modelcontextprotocol.io) 服务提供，通过标准输入输出通信，用任何语言都能写。页面是普通的 HTML，运行在沙箱里。

插件可以运行在两个地方，由清单的 `kind` 决定：

| `kind` | 运行位置 | 适合 |
| --- | --- | --- |
| `host` | 你的电脑 | 需要电脑本身的能力：屏幕、键鼠、本机文件和程序 |
| `guest` | 环境内 | 只在环境里做事：查看状态、处理环境里的文件、安装在环境里的服务 |

## 使用

### 安装

在侧栏打开「插件」，先打开总开关，再进入「插件市场」选择插件安装。市场的内容来自 [cage-plugins](https://github.com/Lee-xeo/cage-plugins) 仓库：应用读取其中的索引，下载压缩包并核对 SHA-256 后安装。读取和下载走电脑自己的网络，遵循系统代理设置。

也可以手动安装：把插件目录放进配置目录的 `plugins/` 下（默认 `~/.claude-cage/plugins/<插件名>/`），然后在插件页点击「重新加载」。

- 卸载或更新时，原来的插件目录不会被删除，而是移到配置目录的 `plugins-removed/` 下。
- 每个插件可以单独停用，包括内置的命令插件。停用的插件不启动，工具不再提供给 Claude，页面也不再显示；文件保留在原处。
- 主机插件的程序启动时使用与终端相同的 `PATH`，清单里可以直接写 `python3`、`node` 等命令。
- 环境内插件的文件在启动时同步到环境里的 `~/.local/share/plugins/<插件名>/`，程序在那里运行。清单 `plugin.json` 不会同步过去。
- 插件市场按分类展示插件，分类由插件在清单的 `categories` 里声明。

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

## 开发

### 目录

```
my-plugin/
├── plugin.json      清单
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
  "engines": { "cage": ">=0.1.8" },
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
| `categories` | 否 | 插件在市场里所属的分类，最多 3 个，见下表 |
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

应用遇到不认识的分类时保留但不显示，所以市场以后新增分类，不会让旧版应用拒绝插件。上架到市场的插件只能使用上表中的值。

`contributes.mcpServers[]`：

| 字段 | 说明 |
| --- | --- |
| `id` | 小写字母、数字和连字符，插件内唯一，不超过 40 个字符 |
| `command` | 程序和参数。带路径的程序从插件目录算起；不带路径的，插件目录里有同名文件时用这个文件，否则按 `PATH` 查找 |

目前每个插件最多一个 MCP 服务。写了多个服务的插件会列出并显示原因，但不启动。

主机插件的程序继承应用的环境变量，但不包括应用自己的变量；环境内插件的程序只拿到 `HOME`、`USER`、`LOGNAME`、`SHELL`、`PATH`、`LANG`、`LC_ALL`、`TZ`。

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

旧格式里不能写 `engines`、`kind`（`host` 除外）和 `activationEvents`，可以写 `categories`。

### 工具的命名和说明

工具的名称、标题和说明，以及参数和结果的名称与说明，都会发送给模型，所以只描述工具做什么。其中出现本产品的名字，或者「虚拟机」「客机」「VM」「guest」之类描述运行环境的词，插件会被拒绝加载。只给页面的工具也一样检查。工具返回的结果和错误信息同样会交给模型，请遵守同样的要求。

工具名不能与已有工具重复。`run_command` 和 `command_job` 由内置的命令插件使用。两个插件的工具重名时，按插件目录名排序在前的那个正常加载，另一个加载失败。

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
- 主机插件的页面只能在这台电脑上的应用窗口里调用工具；主机插件总开关关闭时，主机插件的页面不显示。

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

市场的规则、打包方法和收录要求见 [cage-plugins](https://github.com/Lee-xeo/cage-plugins)。应用安装市场里的插件时还会检查：压缩包解压后不超过 100 MiB、不超过 2000 个文件；索引里写的权限必须与插件清单完全一致。

### 尚未提供

规范里已经预留、应用还没有实现的部分：

- `contributes.uriHandlers`（处理自定义协议的链接）和 `activationEvents`：字段名已预留，清单里可以写，但不生效，格式也还没有定下来，现在请不要使用。
- 一个插件多个 MCP 服务。
- MCP 服务主动发出的通知不会转给页面，页面需要自己定时调用工具取最新状态。

## 示例

- [`clock`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/clock)：最小的主机插件，旧格式，一个工具。
- [`desktop-control`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/desktop-control)：主机插件，查看屏幕并使用键鼠，声明了 `screen` 和 `input` 权限。
- [`system-status`](https://github.com/Lee-xeo/cage-plugins/tree/main/plugins/system-status)：环境内插件，一个页面加一个工具。

向市场提交插件的方法见 [cage-plugins](https://github.com/Lee-xeo/cage-plugins)。
