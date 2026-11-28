// One displayed page and private debugging pipe per active phone browser.
// The desktop process, its targets and its profile are never attached here.
import { spawn } from "node:child_process";
import { createHash, randomBytes } from "node:crypto";
import { existsSync } from "node:fs";
import { mkdir, mkdtemp, readFile, rename, rm, writeFile } from "node:fs/promises";
import { createConnection } from "node:net";
import { hostname, tmpdir } from "node:os";
import { join } from "node:path";
import { phoneError, startPhoneProxy, webAddress } from "./browser-phone-network.mjs";
import { PhoneHistoryStore } from "./browser-history.mjs";

const idleTimeout = 2 * 60 * 1000;
const maxPhoneBrowsers = 8;
const minDisplayNumber = 120;
const maxDisplayNumber = 199;
const validID = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const keys = new Map([
  ["Enter", 13], ["Tab", 9], ["Escape", 27], ["Backspace", 8], ["Delete", 46],
  ["ArrowLeft", 37], ["ArrowUp", 38], ["ArrowRight", 39], ["ArrowDown", 40],
]);

function xauthEntry(family, address, displayNumber, cookie) {
  const addr = Buffer.from(address, "utf8");
  const num = Buffer.from(String(displayNumber), "utf8");
  const name = Buffer.from("MIT-MAGIC-COOKIE-1", "ascii");
  const out = Buffer.alloc(10 + addr.length + num.length + name.length + cookie.length);
  let off = 0;
  out.writeUInt16BE(family, off); off += 2;
  out.writeUInt16BE(addr.length, off); off += 2;
  addr.copy(out, off); off += addr.length;
  out.writeUInt16BE(num.length, off); off += 2;
  num.copy(out, off); off += num.length;
  out.writeUInt16BE(name.length, off); off += 2;
  name.copy(out, off); off += name.length;
  out.writeUInt16BE(cookie.length, off); off += 2;
  cookie.copy(out, off);
  return out;
}

async function writeXauthority(file, displayNumber) {
  const cookie = randomBytes(16);
  const data = Buffer.concat([
    xauthEntry(0x0100, hostname(), displayNumber, cookie),
    xauthEntry(0xffff, "", displayNumber, cookie),
  ]);
  await writeFile(file, data, { mode: 0o600 });
}

function probeUnixSocket(socketPath) {
  return new Promise(resolve => {
    const socket = createConnection(socketPath);
    const done = ok => { socket.destroy(); resolve(ok); };
    socket.setTimeout(200, () => done(false));
    socket.once("connect", () => done(true));
    socket.once("error", () => done(false));
  });
}

async function startPhoneDisplay({ xserver, usedDisplays, renderNode }) {
  let num = -1;
  for (let candidate = minDisplayNumber; candidate <= maxDisplayNumber; candidate++) {
    if (usedDisplays.has(candidate)) continue;
    if (existsSync(`/tmp/.X11-unix/X${candidate}`) || existsSync(`/tmp/.X${candidate}-lock`)) continue;
    num = candidate;
    usedDisplays.add(candidate);
    break;
  }
  if (num < 0) return null;
  let runtimeDir;
  let proc;
  let closed = false;
  const cleanup = async () => {
    if (closed) return;
    closed = true;
    if (proc && proc.exitCode === null && !proc.killed) {
      try { proc.kill("SIGTERM"); } catch {}
      await new Promise(resolve => {
        const timer = setTimeout(() => { try { proc.kill("SIGKILL"); } catch {} resolve(); }, 1000);
        proc.once("exit", () => { clearTimeout(timer); resolve(); });
      });
    }
    if (runtimeDir) await rm(runtimeDir, { recursive: true, force: true }).catch(() => {});
    usedDisplays.delete(num);
  };
  try {
    runtimeDir = await mkdtemp(join(tmpdir(), "pdisp-"));
    const xauthority = join(runtimeDir, "xauth");
    const vncSocket = join(runtimeDir, "v");
    await writeXauthority(xauthority, num);
    const args = [
      `:${num}`,
      ...(renderNode ? ["-rendernode", renderNode] : []),
      "-geometry", "1920x1080",
      "-depth", "24",
      "-rfbport", "-1",
      "-rfbunixpath", vncSocket,
      "-rfbunixmode", "0600",
      "-nolisten", "tcp",
      "-auth", xauthority,
      "-SecurityTypes", "None",
    ];
    proc = spawn(xserver, args, { stdio: "ignore" });
    let exited = false;
    proc.once("error", () => { exited = true; });
    proc.once("exit", () => { exited = true; });
    const xSocket = `/tmp/.X11-unix/X${num}`;
    const deadline = Date.now() + 3000;
    while (!exited && Date.now() < deadline) {
      if (existsSync(xSocket) && await probeUnixSocket(xSocket)) {
        return { display: `:${num}`, xauthority, close: cleanup };
      }
      await new Promise(resolve => setTimeout(resolve, 25));
    }
  } catch {}
  await cleanup();
  return null;
}

function dimensions({ width, height }) {
  if (!Number.isInteger(width) || width < 240 || width > 1440 ||
      !Number.isInteger(height) || height < 160 || height > 1600) throw new Error("手机视口大小无效");
  return { width, height };
}

class BrowserPipe {
  constructor(binary, args, env) {
    this.calls = new Map();
    this.sequence = 0;
    this.process = spawn(binary, args, {
      stdio: ["ignore", "ignore", "ignore", "pipe", "pipe"],
      ...(env ? { env } : {}),
    });
    this.closed = new Promise(resolve => this.process.once("close", resolve));
    let buffered = [];
    const disconnected = () => {
      this.dead = true;
      for (const call of this.calls.values()) call.reject(phoneError(410, "远程浏览器已关闭"));
      this.calls.clear();
      this.process.kill();
    };
    this.process.on("error", disconnected);
    this.process.once("exit", disconnected);
    for (const stream of this.process.stdio.slice(3)) stream.on("error", disconnected);
    this.process.stdio[4].on("data", data => {
      let end;
      while ((end = data.indexOf(0)) !== -1) {
        buffered.push(data.subarray(0, end));
        let event;
        try { event = JSON.parse(Buffer.concat(buffered)); } catch { disconnected(); return; }
        data = data.subarray(end + 1);
        buffered = [];
        if (!event.id) { this.onEvent?.(event); continue; }
        const call = this.calls.get(event.id);
        if (!call) continue;
        this.calls.delete(event.id);
        if (event.error) call.reject(new Error("Chromium 暂时无法完成此操作"));
        else call.resolve(event.result);
      }
      if (data.length) buffered.push(data);
    });
  }
  async send(method, params = {}, sessionId) {
    if (this.dead) throw phoneError(410, "远程浏览器已关闭");
    const id = ++this.sequence;
    let timer;
    try {
      return await new Promise((resolve, reject) => {
        // The first CDP command includes macOS Chrome's cold process startup.
        let timeout = method === "Page.navigate" || method === "Page.navigateToHistoryEntry" ? 12000 : 4500;
        if (method === "Target.getTargets" && process.platform === "darwin") timeout = 75000;
        timer = setTimeout(() => reject(new Error("远程浏览器操作超时")), timeout);
        this.calls.set(id, { resolve, reject });
        this.process.stdio[3].write(JSON.stringify({ id, method, params, sessionId }) + "\0");
      });
    } finally { clearTimeout(timer); this.calls.delete(id); }
  }
  async close() {
    if (!this.dead) void this.send("Browser.close").catch(() => {});
    const timeout = setTimeout(() => this.process.kill("SIGKILL"), 1800);
    try { await this.closed; } finally { clearTimeout(timeout); }
  }
}

// Executed in an anonymous isolated world in the current top-level document.
// A password only enters the page through Input.insertText; no field value is inspected.
const inspectFocus = `(() => {
  let node = document.activeElement;
  while (node?.shadowRoot?.activeElement) node = node.shadowRoot.activeElement;
  const input = node instanceof HTMLInputElement;
  const editable = node && !node.readOnly && !node.disabled &&
    (node.isContentEditable || node instanceof HTMLTextAreaElement ||
      (input && ["text","email","search","url","tel","password","number"].includes(node.type)));
  if (!editable) return { focus: "", password: false };
  const ids = globalThis.__focusIds ??= new WeakMap();
  if (!ids.has(node)) ids.set(node, String(globalThis.__focusSeq = (globalThis.__focusSeq || 0) + 1));
  return { focus: ids.get(node), password: input && node.type === "password" };
})()`;

async function saveLanguage(profile, language) {
  const directory = join(profile, "Default");
  await mkdir(directory, { recursive: true, mode: 0o700 });
  const file = join(directory, "Preferences");
  let settings = {};
  try { settings = JSON.parse(await readFile(file, "utf8")); }
  catch (error) { if (error.code !== "ENOENT") throw new Error("手机浏览器资料暂时无法读取"); }
  settings.intl ??= {};
  settings.intl.accept_languages = language;
  settings.intl.selected_languages = language;
  const next = file + ".phone-next";
  await writeFile(next, JSON.stringify(settings), { mode: 0o600 });
  await rename(next, file);
}

export class PhoneBrowser {
  constructor({ binary = "/usr/bin/chromium", directory = "/home/cage-browser/phone-profiles",
                language = process.env.CAGE_LANGUAGE || "en-US", connect, pipeFactory,
                xserver = process.platform === "linux" ? "/usr/bin/Xtigervnc" : "",
                displayFactory, maxSessions = maxPhoneBrowsers } = {}) {
    this.binary = binary;
    this.directory = directory;
    this.language = language;
    this.connect = connect;
    this.customPipeFactory = Boolean(pipeFactory);
    this.pipeFactory = pipeFactory ?? ((binary, args, env) => new BrowserPipe(binary, args, env));
    this.xserver = xserver;
    this.displayFactory = displayFactory;
    this.maxSessions = maxSessions;
    this.historyStore = new PhoneHistoryStore({ directory: this.directory, validateURL: webAddress });
    this.sessions = new Map(); // profile -> session
    this.sessionById = new Map(); // id -> profile
    this.opening = new Set(); // profiles currently launching
    this.lanes = new Map(); // profile -> { queue, pending }
    this.usedDisplays = new Set();
  }
  get session() {
    let last = null;
    for (const s of this.sessions.values()) last = s;
    return last;
  }
  async history(input) {
    if (!input || typeof input.profile !== "string" || !/^[a-f0-9]{64}$/.test(input.profile)) {
      throw new Error("手机浏览器资料无效");
    }
    return this.historyStore.request(input.profile, input);
  }
  enqueue(profile, fn) {
    let lane = this.lanes.get(profile);
    if (!lane) {
      lane = { queue: Promise.resolve(), pending: 0 };
      this.lanes.set(profile, lane);
    }
    if (lane.pending >= 12) return Promise.reject(new Error("手机浏览器正在处理上一项操作"));
    lane.pending++;
    const task = lane.queue.then(fn);
    lane.queue = task.catch(() => {}).finally(() => {
      lane.pending--;
      if (lane.pending === 0 && !this.sessions.has(profile)) this.lanes.delete(profile);
    });
    return task;
  }
  run(operation, input, signal) {
    if (!input || !validID.test(input.id)) return Promise.reject(new Error("手机浏览器会话无效"));
    if (operation === "open") {
      if (typeof input.profile !== "string" || !/^[a-f0-9]{64}$/.test(input.profile)) {
        return Promise.reject(new Error("手机浏览器资料无效"));
      }
      const existingOwner = this.sessionById.get(input.id);
      if (existingOwner && existingOwner !== input.profile) {
        return Promise.reject(phoneError(409, "手机浏览器会话冲突"));
      }
      return this.enqueue(input.profile, () => this.perform(operation, input, input.profile, signal));
    }
    const profile = this.sessionById.get(input.id);
    if (!profile) return Promise.reject(phoneError(410, "手机浏览器会话已结束，请重新打开"));
    return this.enqueue(profile, () => this.perform(operation, input, profile, signal));
  }
  async perform(operation, input, profile, signal) {
    if (operation === "open") {
      const size = dimensions(input);
      const url = input.url ? webAddress(input.url) : "";
      const existingOwner = this.sessionById.get(input.id);
      if (existingOwner && existingOwner !== profile) throw phoneError(409, "手机浏览器会话冲突");
      if (!this.sessions.has(profile)) {
        if (this.sessions.size + this.opening.size >= this.maxSessions) {
          throw phoneError(409, "同时打开的远程浏览器数量已达上限");
        }
        this.opening.add(profile);
        try { await this.open(input.id, profile, size, url, signal); }
        catch (error) {
          if (error.status === 403 || error.status === 409) throw error;
          throw phoneError(503, "客机浏览器暂时无法启动，请稍后重新打开");
        } finally {
          this.opening.delete(profile);
        }
      }
    }
    const s = this.sessions.get(profile);
    if (!s || s.id !== input.id) throw phoneError(410, "手机浏览器会话已结束，请重新打开");
    clearTimeout(s.timer);
    s.timer = setTimeout(() => { void this.run("close", { id: s.id }).catch(() => {}); }, idleTimeout).unref();
    if (operation === "open") return { id: s.id };
    if (operation === "frame") return this.frame(s, input.after);
    if (operation === "action") {
      await this.action(s, input);
      if (input.return_frame === true) {
        // Navigation can invalidate the screenshot context after the input was
        // delivered. Acknowledge the input so the phone retries only the frame.
        try { return await this.frame(s); } catch { return { id: s.id }; }
      }
    } else if (operation === "close") await this.dispose(s);
    else throw new Error("未知的手机浏览器操作");
    return { id: input.id };
  }
  async open(id, profile, size, url, signal) {
    const data = join(this.directory, profile);
    await saveLanguage(data, this.language);
    const proxy = await startPhoneProxy(this.connect);
    const baseArgs = [
      "--disable-extensions", "--disable-quic", "--disable-background-networking",
      "--no-first-run", "--no-default-browser-check", "--disable-session-crashed-bubble",
      "--disable-sync", "--disable-dev-shm-usage", "--remote-debugging-pipe",
      "--disable-blink-features=AutomationControlled",
      "--force-webrtc-ip-handling-policy=disable_non_proxied_udp",
      "--proxy-server=socks5://127.0.0.1:" + proxy.port, "--proxy-bypass-list=<-loopback>",
      "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1",
      "--lang=" + this.language, "--accept-lang=" + this.language, "--user-data-dir=" + data, "about:blank",
    ];
    let display = null;
    if (this.displayFactory) {
      display = await this.displayFactory({ renderNode: baseArgs.includes("--use-angle=gl") ? "/dev/dri/renderD128" : "" });
    } else if (process.platform === "linux" && !this.customPipeFactory && this.xserver && existsSync(this.xserver)) {
      const renderNode = baseArgs.includes("--use-angle=gl") && existsSync("/dev/dri/renderD128") ? "/dev/dri/renderD128" : "";
      display = await startPhoneDisplay({ xserver: this.xserver, usedDisplays: this.usedDisplays, renderNode });
    }
    const args = display ? baseArgs : ["--headless=new", ...baseArgs];
    const env = display ? { ...process.env, DISPLAY: display.display, XAUTHORITY: display.xauthority } : undefined;
    let pipe;
    try { pipe = await this.pipeFactory(this.binary, args, env); }
    catch (error) { proxy.close(); await display?.close(); throw error; }
    const s = { id, profile, ...size, pipe, proxy, display, focusWorld: null, timer: null };
    this.sessions.set(profile, s);
    this.sessionById.set(id, profile);
    let stopping;
    const stop = () => { stopping ??= this.dispose(s); };
    signal?.addEventListener("abort", stop, { once: true });
    pipe.closed.then(() => {
      proxy.close();
      void display?.close();
      if (this.sessions.get(profile) === s) {
        this.sessions.delete(profile);
        clearTimeout(s.timer);
      }
      if (this.sessionById.get(id) === profile) this.sessionById.delete(id);
    });
    try {
      signal?.throwIfAborted();
      const { targetInfos } = await pipe.send("Target.getTargets");
      const target = targetInfos.find(t => t.type === "page" && t.url === "about:blank");
      if (!target) throw phoneError(503, "客机浏览器尚未就绪，请稍后重新打开");
      s.targetId = target.targetId;
      const { sessionId } = await pipe.send("Target.attachToTarget", { targetId: target.targetId, flatten: true });
      s.send = (method, params) => pipe.send(method, params, sessionId);
      pipe.onEvent = ({ method, params, sessionId: src }) => {
        if (method === "Target.targetCreated" && params.targetInfo.type === "page" && params.targetInfo.targetId !== target.targetId)
          void pipe.send("Target.closeTarget", { targetId: params.targetInfo.targetId }).catch(() => {});
        if (src === sessionId && method === "Page.javascriptDialogOpening")
          void s.send("Page.handleJavaScriptDialog", { accept: false }).catch(() => {});
      };
      await pipe.send("Target.setDiscoverTargets", { discover: true });
      await pipe.send("Browser.setDownloadBehavior", { behavior: "deny" });
      await s.send("Page.enable");
      if (!display) {
        // Headless fallback only: remove HeadlessChrome from UA while keeping desktop Client Hints coherent.
        const { userAgent } = await pipe.send("Browser.getVersion");
        if (typeof userAgent === "string" && userAgent.includes("HeadlessChrome/")) {
          const platform = process.platform === "darwin" ? "macOS" : process.platform === "win32" ? "Windows" : "Linux";
          const architecture = process.arch === "arm64" ? "arm" : "x86";
          await s.send("Emulation.setUserAgentOverride", {
            userAgent: userAgent.replace(/HeadlessChrome\//g, "Chrome/"),
            userAgentMetadata: { platform, platformVersion: "", architecture, model: "", mobile: false, formFactors: ["Desktop"] },
          });
        }
      }
      await s.send("Page.setInterceptFileChooserDialog", { enabled: true });
      await this.resize(s, size);
      if (url) await s.send("Page.navigate", { url });
    } catch (error) { await (stopping ?? this.dispose(s)); throw error; }
    finally { signal?.removeEventListener("abort", stop); }
  }
  async dispose(s) {
    if (!s) {
      await Promise.all([...this.sessions.values()].map(active => this.dispose(active)));
      return;
    }
    clearTimeout(s.timer);
    if (this.sessions.get(s.profile) === s) this.sessions.delete(s.profile);
    if (this.sessionById.get(s.id) === s.profile) this.sessionById.delete(s.id);
    s.proxy.close();
    await s.pipe.close();
    await s.display?.close();
  }
  async resize(s, size) {
    // Keep the page in phone CSS pixels with crisp raster density while avoiding
    // DevTools mobile-device emulation signals that conflict with desktop Chromium.
    const density = Math.max(1, Math.min(2, Math.sqrt(2_000_000 / (size.width * size.height))));
    await s.send("Emulation.setDeviceMetricsOverride", { ...size, deviceScaleFactor: density, mobile: false });
    Object.assign(s, size);
  }
  async document(s) {
    const [{ frameTree: { frame } }, { cssVisualViewport: viewport }] = await Promise.all([
      s.send("Page.getFrameTree"), s.send("Page.getLayoutMetrics"),
    ]);
    const failed = frame.url === "chrome-error://chromewebdata/";
    const url = frame.url === "about:blank" ? "" : webAddress(failed ? frame.unreachableUrl : frame.url + (frame.urlFragment || ""));
    const token = createHash("sha256").update(JSON.stringify([frame.loaderId, url, s.width, s.height,
      viewport.scale, viewport.offsetX, viewport.offsetY])).digest("hex");
    return { url, token, failed, scale: viewport.scale, frameId: frame.id, loaderId: frame.loaderId };
  }
  async focus(s, { frameId, loaderId }) {
    const evaluate = async () => {
      if (!s.focusWorld || s.focusWorld.frameId !== frameId || s.focusWorld.loaderId !== loaderId) {
        const { executionContextId } = await s.send("Page.createIsolatedWorld", { frameId, worldName: "" });
        s.focusWorld = { frameId, loaderId, executionContextId };
      }
      return s.send("Runtime.evaluate", {
        expression: inspectFocus,
        contextId: s.focusWorld.executionContextId,
        returnByValue: true,
      });
    };
    let result;
    try { result = await evaluate(); }
    catch {
      s.focusWorld = null;
      result = await evaluate();
    }
    const field = result.result.value;
    if (!field?.focus) return { focus: "", password: false };
    // Opening the phone keyboard changes the frame geometry, but the same
    // DOM input and its draft remain valid until the document or focus changes.
    const token = createHash("sha256").update(JSON.stringify([frameId, loaderId, field.focus])).digest("hex");
    return { focus: token, password: field.password };
  }
  async frame(s, previousTag) {
    const before = await this.document(s);
    const [picture, info, focus] = await Promise.all([
      s.send("Page.captureScreenshot", { format: "jpeg", quality: 70, captureBeyondViewport: false }),
      s.pipe.send("Target.getTargetInfo", { targetId: s.targetId }),
      before.failed ? { focus: "", password: false } : this.focus(s, before),
    ]);
    const after = await this.document(s);
    if (before.token !== after.token) throw phoneError(409, "页面正在切换，请重新读取画面");
    if (!picture.data || picture.data.length > 2500000) throw new Error("远程画面过大，请减小视口");
    const title = info.targetInfo.title.slice(0, 512);
    if (!after.failed && after.url && (s.lastRecordedURL !== after.url || (title && s.lastRecordedTitle !== title))) {
      s.lastRecordedURL = after.url;
      s.lastRecordedTitle = title;
      void this.historyStore.record(s.profile, after.url, title).catch(() => {});
    }
    const metadata = { id: s.id, url: after.url, title, width: s.width,
      height: s.height, document: after.token, ...focus };
    const tag = createHash("sha256").update(JSON.stringify(metadata)).update(picture.data, "ascii").digest("hex").slice(0, 32);
    if (previousTag === tag) return { id: s.id, frame_tag: tag, unchanged: true };
    return { ...metadata, frame_tag: tag, image: picture.data };
  }
  async action(s, input) {
    const { type } = input;
    if (type === "resize") return this.resize(s, dimensions(input));
    if (type === "navigate") return s.send("Page.navigate", { url: webAddress(input.url) });
    const doc = await this.document(s);
    if (doc.token !== input.document) throw phoneError(409, "页面已切换，请重新读取画面");
    if (type === "back" || type === "forward") {
      const history = await s.send("Page.getNavigationHistory");
      const entry = history.entries[history.currentIndex + (type === "back" ? -1 : 1)];
      if (entry) { if (entry.url !== "about:blank") webAddress(entry.url); await s.send("Page.navigateToHistoryEntry", { entryId: entry.id }); }
      return;
    }
    if (type === "reload") return s.send("Page.reload");
    if (doc.failed) throw new Error("网页加载失败，请检查地址或重新载入");
    if (type === "text" || type === "key") {
      if (type === "text" || input.key === "Enter" || input.focus) {
        const actual = await this.focus(s, doc);
        if (!actual.focus || actual.focus !== input.focus) throw phoneError(409, "输入框已改变，请重新点选");
      }
      if (type === "text") {
        if (typeof input.text !== "string" || !input.text || Buffer.byteLength(input.text) > 8192) throw new Error("输入文字无效");
        return s.send("Input.insertText", { text: input.text });
      }
      const code = keys.get(input.key);
      if (!code) throw new Error("按键不受支持");
      const key = { key: input.key, code: input.key, windowsVirtualKeyCode: code };
      await s.send("Input.dispatchKeyEvent", { ...key, type: "keyDown", ...(input.key === "Enter" ? { text: "\r" } : {}) });
      await s.send("Input.dispatchKeyEvent", { ...key, type: "keyUp" });
      return;
    }
    if (!Number.isFinite(input.x) || !Number.isFinite(input.y) ||
        input.x < 0 || input.y < 0 || input.x >= s.width || input.y >= s.height) throw new Error("触点不在当前画面内");
    const x = input.x / doc.scale, y = input.y / doc.scale;
    if (type === "tap") {
      await s.send("Input.dispatchMouseEvent", { type: "mouseMoved", x, y });
      await s.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", buttons: 1, clickCount: 1 });
      await s.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", buttons: 0, clickCount: 1 });
    } else if (type === "scroll") {
      if (![input.dx, input.dy].every(n => Number.isFinite(n) && Math.abs(n) <= 3200)) throw new Error("滚动距离无效");
      await s.send("Input.dispatchMouseEvent", { type: "mouseWheel", x, y, deltaX: input.dx / doc.scale, deltaY: input.dy / doc.scale });
    } else throw new Error("未知的浏览器动作");
  }
}
