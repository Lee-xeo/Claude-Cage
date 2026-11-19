// The Tailscale page. It talks to the workbench with the plugin page
// protocol and to the node through the node's own tools.
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
  if (message.method === "ui/themeChanged") { applyTheme(message.params); return; }
  if (message.method === "ui/visibilityChanged") { if (message.params?.visible) void refresh(); return; }
  const pending = waiting.get(message.id);
  if (!pending) return;
  waiting.delete(message.id);
  if (message.error) pending.reject(new Error(message.error.message));
  else pending.resolve(message.result);
});

function applyTheme(theme) {
  for (const [name, value] of Object.entries(theme?.tokens || {})) document.documentElement.style.setProperty("--" + name, value);
  document.documentElement.style.colorScheme = theme?.scheme === "dark" ? "dark" : "light";
}

// The node's tools answer with its state, or with what went wrong.
async function tool(name, args = {}) {
  const result = await request("tools/call", { name, arguments: args });
  const text = (result?.content || []).map(part => part.text || "").join("\n");
  if (result?.isError) throw new Error(text || "Tailscale 操作没有完成");
  return result?.structuredContent;
}

const labels = { Running: "已连接", NeedsLogin: "等待登录", NeedsMachineAuth: "等待管理员批准", Starting: "正在连接", Stopped: "已断开" };
const descriptions = {
  Stopped: "登录 Tailscale，让这个环境加入你的私网。其他设备加入同一网络后，会显示在这里。",
  NeedsLogin: "在内置浏览器中登录并授权这台设备。",
  NeedsMachineAuth: "登录已完成。请让此网络的管理员在 Tailscale 管理台批准这台设备。",
  Starting: "正在连接 Tailscale，设备清单会在连接成功后自动更新。",
};
const ui = { state: null, error: "", notice: "", busy: false, loginRequested: false, query: "", selected: "", port: "80", user: "", key: "", sshOpen: false, appsOpen: false };
const deviceName = peer => peer.hostname || peer.name?.split(".")[0] || peer.ip;
const connected = () => ui.state?.backend === "Running";
const address = () => ui.state?.ips?.find(ip => !ip.includes(":"));

function el(tag, props = {}, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(props)) {
    if (value === undefined || value === false || value === null) continue;
    if (key.startsWith("on")) node.addEventListener(key.slice(2).toLowerCase(), value);
    else if (key === "className") node.className = value;
    else if (key === "value") node.value = value;
    else node.setAttribute(key, value === true ? "" : value);
  }
  for (const child of children.flat()) if (child !== null && child !== undefined && child !== false) node.append(child);
  return node;
}

// A refresh redraws the page only when what it shows changed, so a click in
// progress is not lost to a redraw.
async function refresh() {
  const before = JSON.stringify([ui.state, ui.error]);
  try {
    const state = await tool("status");
    ui.state = state; ui.error = "";
    if (ui.loginRequested && state.auth_url) await openLogin(state.auth_url);
    if (state.backend === "Running") ui.loginRequested = false;
  } catch (error) { ui.error = error.message; }
  if (JSON.stringify([ui.state, ui.error]) !== before) render();
}

async function act(name, args) {
  if (ui.busy) return null;
  ui.busy = true; ui.error = ""; ui.notice = ""; render();
  try {
    const state = await tool(name, args);
    if (state) ui.state = state;
    return state;
  } catch (error) { ui.error = error.message; return null; }
  finally { ui.busy = false; render(); }
}

async function openLogin(url) {
  ui.loginRequested = false;
  try { await request("ui/openUrl", { url }); }
  catch { ui.error = "登录页面未能打开，请点击“打开登录页面”重试。"; render(); }
}

async function login() {
  ui.loginRequested = true;
  const state = await act(ui.state?.enabled ? "login" : "connect");
  if (state?.auth_url) await openLogin(state.auth_url);
  else if (!state || state.backend === "Running") ui.loginRequested = false;
}

async function copy(text, message = "已复制") {
  try { await request("ui/copy", { text }); ui.notice = message; }
  catch { ui.notice = "复制失败，请选中文本手动复制。"; }
  render();
}

async function stop(logout) {
  const answer = await request("ui/confirm", logout
    ? { title: "退出 Tailscale 登录？", message: "私网连接会中断，下次连接需要重新登录。已添加的 SSH 公钥保留。", confirmLabel: "退出登录", danger: true }
    : { title: "断开 Tailscale？", message: "正在使用的私网连接会中断，登录状态和 SSH 公钥保留。", confirmLabel: "断开连接" });
  if (!answer?.confirmed) return;
  ui.loginRequested = false;
  if (await act(logout ? "logout" : "disconnect")) { ui.selected = ""; ui.sshOpen = false; render(); }
}

function overview() {
  const state = ui.state;
  return el("section", { className: "overview" + (connected() ? " connected" : "") },
    el("div", { className: "state", role: "status" },
      el("div", { className: "mark", "aria-hidden": "true" }, "T"),
      el("div", { className: "text" }, el("strong", {}, state ? labels[state.backend] || "连接状态待确认" : "正在读取状态…"),
        connected() && el("span", {}, state.tailnet || "Tailscale 私网")),
      el("button", { className: "icon", "aria-label": "刷新 Tailscale 状态", disabled: ui.busy, onClick: refresh }, "刷新")),
    el("p", { className: "help" }, connected() ? "这个环境已加入私网。选择设备，查看地址并使用它的服务。"
      : state?.backend === "NeedsLogin" && !state.auth_url ? "正在向 Tailscale 获取登录链接。若长时间没有打开浏览器，请检查环境联网后重新发起登录。"
      : descriptions[state?.backend] || "正在读取这个环境的 Tailscale 状态。"),
    connected() && state.account && el("p", { className: "help" }, "登录账号 · " + state.account),
    state && el("div", { className: "actions" },
      !connected() && !["NeedsMachineAuth", "Starting"].includes(state.backend) && el("button", { className: "primary", disabled: ui.busy, onClick: login }, state.enabled ? "重新发起登录" : "连接并登录"),
      state.auth_url && !connected() && el("button", { disabled: ui.busy, onClick: () => openLogin(state.auth_url) }, "打开登录页面"),
      state.enabled && el("button", { disabled: ui.busy, onClick: () => stop(false) }, "断开连接"),
      connected() && el("button", { disabled: ui.busy, onClick: () => stop(true) }, "退出登录")));
}

function notices() {
  const state = ui.state;
  return [
    ui.error && el("p", { className: "error", role: "alert" }, ui.error),
    state?.health?.length > 0 && el("div", { className: "notice", role: "status" }, el("strong", {}, "网络需要留意"), state.health.map(text => el("p", {}, text))),
    state?.renamed_from && el("div", { className: "notice", role: "status" }, el("strong", {}, "设备已改名"),
      el("p", {}, `这台设备在 Tailscale 中的名称已从 ${state.renamed_from} 改为 ${state.hostname || state.name?.split(".")[0] || "新名称"}。如果你在 Tailscale 管理后台手动改过名称，以管理后台为准。`),
      el("button", { disabled: ui.busy, onClick: () => act("acknowledge_rename") }, "知道了")),
  ];
}

function self() {
  if (!connected()) return null;
  const state = ui.state, ip = address();
  return el("section", { className: "row", "aria-label": "当前设备" },
    el("div", { className: "grow" }, el("strong", {}, state.hostname || state.name?.split(".")[0] || "当前设备", el("span", { className: "badge" }, "当前环境")),
      el("span", { className: "sub" }, (ip || "正在分配地址…") + (state.os ? " · " + state.os : ""))),
    ip && el("button", { onClick: () => copy(ip, "已复制这个环境的私网地址") }, "复制地址"));
}

function peerDetail(peer) {
  const state = ui.state;
  const port = Number(ui.port), validPort = Number.isInteger(port) && port >= 1 && port <= 65535;
  const user = ui.user.trim(), validUser = /^[a-zA-Z0-9_.@-]+$/.test(user);
  const portInput = el("input", { type: "number", min: "1", max: "65535", inputmode: "numeric", "aria-label": "服务端口", value: ui.port,
    onInput: event => { ui.port = event.target.value; render(); } });
  const userInput = el("input", { "aria-label": "设备的系统用户名", placeholder: "设备的系统用户名", autocomplete: "off", maxlength: "128", value: ui.user,
    onInput: event => { ui.user = event.target.value; render(); } });
  return el("div", { className: "detail", id: "device-" + peer.ip },
    peer.name && el("p", { className: "help" }, peer.name),
    !peer.online && el("p", { className: "help" }, "设备当前离线，请先在该设备上连接 Tailscale。"),
    el("strong", {}, "访问设备上的服务"),
    el("p", { className: "help" }, "填写服务端口，为这个环境里的浏览器、数据库客户端或其他应用创建本地连接地址。"),
    el("div", { className: "form" }, portInput, el("button", { disabled: ui.busy || !validPort, onClick: async () => {
      if (await act("open_service", { host: peer.ip, port })) { ui.notice = "本地连接地址已就绪，可以在这个环境里使用。"; render(); }
    } }, "创建连接地址")),
    (state.forwards || []).filter(entry => entry.host === peer.ip).map(entry => el("div", { className: "box" },
      el("span", {}, el("strong", {}, "端口 " + entry.port), " ", el("code", {}, entry.address)),
      el("button", { className: "icon", "aria-label": `复制端口 ${entry.port} 的连接地址`, onClick: () => copy(entry.address, "已复制这个环境里可用的连接地址") }, "复制"),
      el("button", { onClick: async () => {
        try { await request("ui/openUrl", { url: `http://${entry.address}/` }); } catch { ui.error = "服务页面未能打开，请重试。"; render(); }
      } }, "打开 HTTP 页面"))),
    el("strong", {}, "SSH 连接"),
    el("p", { className: "help" }, "填写这台设备的系统用户名。目标设备需开启 SSH，并允许当前账号登录。"),
    el("div", { className: "form" }, userInput, el("button", { disabled: !user || !validUser,
      onClick: () => copy(`ssh -l '${user}' ${peer.ip}`, "已复制 SSH 命令，请在终端运行") }, "复制 SSH 命令")),
    el("p", { className: "help" }, "SSH、SFTP 与使用 OpenSSH 的工具可直接使用以上私网地址。其他应用的接入方式见下方“应用访问私网”。"));
}

function devices() {
  const peers = [...(ui.state?.peers || [])].sort((a, b) => Number(b.online) - Number(a.online) || deviceName(a).localeCompare(deviceName(b)));
  const needle = ui.query.trim().toLowerCase();
  const visible = peers.filter(peer => [deviceName(peer), peer.name, peer.ip, peer.os].some(value => value?.toLowerCase().includes(needle)));
  const search = el("input", { type: "search", className: "search", "aria-label": "搜索设备", placeholder: "搜索设备名称或地址", value: ui.query,
    onInput: event => { ui.query = event.target.value; render(); } });
  return el("section", { className: "section", "aria-label": "网络中的设备" },
    el("h2", {}, "网络中的设备", connected() && el("small", {}, `${peers.length} 台 · ${peers.filter(peer => peer.online).length} 台在线`)),
    connected() && peers.length > 0 && search,
    !connected() ? el("div", { className: "empty" }, el("strong", {}, "连接后查看你的设备"), "在浏览器完成登录，设备清单会自动出现。")
      : !peers.length ? el("div", { className: "empty" }, el("strong", {}, "私网已连接，还没有发现其他设备"), "在电脑或手机安装 Tailscale，并加入同一网络。这里只显示此网络允许这台设备发现的设备。")
      : !visible.length ? el("p", { className: "help" }, "没有匹配的设备，请尝试其他名称或地址。")
      : el("div", {}, visible.map(peer => el("article", { className: "peer" },
        el("div", { className: "row" },
          el("div", { className: "grow" }, el("strong", {}, deviceName(peer)), el("span", { className: "sub" }, peer.ip + (peer.os ? " · " + peer.os : ""))),
          el("span", { className: "presence" + (peer.online ? " online" : "") }, el("i"), peer.online ? "在线" : "离线"),
          el("button", { className: "icon", "aria-label": `复制 ${deviceName(peer)} 的地址`, onClick: () => copy(peer.ip, "已复制设备私网地址") }, "复制"),
          el("button", { "aria-expanded": String(ui.selected === peer.ip), "aria-controls": "device-" + peer.ip, onClick: () => {
            ui.selected = ui.selected === peer.ip ? "" : peer.ip; ui.user = ""; ui.port = "80"; render();
          } }, "使用设备")),
        ui.selected === peer.ip && peerDetail(peer)))));
}

function inbound() {
  if (!connected()) return null;
  const state = ui.state, ip = address();
  const key = el("textarea", { rows: "3", placeholder: "ssh-ed25519 AAAA…", maxlength: "8192", spellcheck: "false", "aria-label": "连接设备的 SSH 公钥", value: ui.key,
    onInput: event => { ui.key = event.target.value; render(); } });
  return el("section", { className: "section" },
    el("button", { className: "disclosure", "aria-expanded": String(ui.sshOpen), onClick: () => { ui.sshOpen = !ui.sshOpen; render(); } }, el("span", {}, "从其他设备 SSH 到这个环境"), el("span", { "aria-hidden": "true" }, ui.sshOpen ? "▴" : "▾")),
    ui.sshOpen && [
      el("p", { className: "help" }, "仅在需要远程终端、SFTP 或编辑器连接时配置。将连接设备的 SSH 公钥添加到这里，私钥保留在原设备。"),
      ip && el("div", { className: "box" }, el("code", {}, `ssh ${state.user}@${ip}`),
        el("button", { className: "icon", "aria-label": "复制连接命令", onClick: () => copy(`ssh ${state.user}@${ip}`) }, "复制")),
      key,
      el("div", {}, el("button", { disabled: ui.busy || !ui.key.trim(), onClick: async () => {
        if (await act("add_key", { key: ui.key.trim() })) { ui.key = ""; render(); }
      } }, "添加公钥")),
      (state.keys || []).map(entry => el("div", { className: "box key" }, el("code", { title: entry }, entry),
        el("button", { className: "icon", disabled: ui.busy, "aria-label": "移除 SSH 公钥", onClick: () => act("remove_key", { key: entry }) }, "移除"))),
    ]);
}

function apps() {
  if (!connected() || !ui.state.proxy_url) return null;
  return el("section", { className: "section" },
    el("button", { className: "disclosure", "aria-expanded": String(ui.appsOpen), onClick: () => { ui.appsOpen = !ui.appsOpen; render(); } }, el("span", {}, "应用访问私网"), el("span", { "aria-hidden": "true" }, ui.appsOpen ? "▴" : "▾")),
    ui.appsOpen && [
      el("p", { className: "help" }, "需要直接使用设备域名、HTTPS 或多个端口时，在这个环境里的应用中配置 HTTP 代理。仅允许访问此私网中可见的设备，公网继续使用原有出口。"),
      el("div", {}, el("button", { onClick: () => copy(ui.state.proxy_url, "已复制私网 HTTP 代理地址（含本地访问凭据）") }, "复制私网代理地址")),
      el("p", { className: "help" }, "代理地址含本地访问凭据，请仅用于这个环境里的应用。此接入方式支持 TCP 服务；UDP、系统 VPN 和子网路由未启用。"),
    ]);
}

function render() {
  const panel = document.getElementById("panel");
  // Keep the caret where it was in whichever field is being typed in.
  const active = document.activeElement, label = active?.getAttribute?.("aria-label"), caret = active?.selectionStart;
  panel.replaceChildren(overview(), ...notices().filter(Boolean), self() || "", devices(), inbound() || "", apps() || "",
    el("p", { className: "help footer" }, "其他设备需安装 Tailscale 并加入同一网络。设备访问受网络权限控制；暂停联网或停止环境会中断私网连接。"),
    ui.notice ? el("p", { className: "help", role: "status" }, "✓ " + ui.notice) : "");
  if (label) {
    const again = panel.querySelector(`[aria-label="${CSS.escape(label)}"]`);
    if (again && again !== active) {
      again.focus();
      try { if (caret !== null && caret !== undefined) again.setSelectionRange(caret, caret); } catch { /* number fields have no selection */ }
    }
  }
}

const start = await request("ui/initialize", { protocolVersion: 1 });
applyTheme(start.theme);
render();
await refresh();
setInterval(() => { if (!document.hidden && !ui.busy) void refresh(); }, 3000);
