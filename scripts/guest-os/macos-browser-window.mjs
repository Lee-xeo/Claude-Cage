// The signed window helper is the only owner of the ordinary Chrome process.
import net from "node:net";
import http from "node:http";
import path from "node:path";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { setTimeout as delay } from "node:timers/promises";
import { randomBytes } from "node:crypto";
import { once } from "node:events";
import { lstat, readFile, realpath } from "node:fs/promises";

const tabID = "native-chrome";
const officialLoginHosts = new Set(["claude.ai", "claude.com", "console.anthropic.com", "platform.claude.com", "platform.anthropic.com"]);

export function failure(code, message) {
  return Object.assign(new Error(message), { code, safe: true });
}

export async function windowRequest(address, action, fields = {}, signal) {
  const info = await lstat(address);
  if (!info.isSocket() || info.uid !== process.getuid() || (info.mode & 0o077))
    throw failure("browser_unavailable", "请检查客机窗口服务");
  const socket = net.createConnection({ path: address, signal });
  try {
    socket.setTimeout(48000, () => socket.destroy(new Error("Window service timeout")));
    await once(socket, "connect");
    socket.write(JSON.stringify({ ...fields, surface: "browser", action: "browser-" + action }) + "\n");
    let data = "";
    for await (const chunk of socket) {
      data += chunk.toString();
      if (data.length > 65536) break;
      if (!data.includes("\n")) continue;
      const reply = JSON.parse(data);
      if (reply.error || (action === "status" && reply.surface !== "browser") || (action !== "status" && reply.ok !== true)) break;
      return reply;
    }
    throw failure("browser_unavailable", "请更新客机窗口组件并检查 Chrome");
  } finally { socket.destroy(); }
}

function webURL(value) {
  if (typeof value !== "string" || value.length > 8192) throw failure("invalid_url", "浏览器地址无效");
  const url = new URL(value);
  if (!["http:", "https:"].includes(url.protocol) || url.username || url.password)
    throw failure("invalid_url", "请输入 HTTP 或 HTTPS 地址");
  return url;
}

// A one-use loopback hand-off. Bind before opening the tab and authenticate
// both the Host and a random path before issuing an editor cookie or accepting a result.
export async function browserCallback(hostname, receive) {
  const pathname = "/" + randomBytes(24).toString("hex");
  let authority, resolveResult, rejectResult, timer, used = false;
  const result = new Promise((resolve, reject) => { resolveResult = resolve; rejectResult = reject; });
  result.catch(() => {}); // Launch may still be in progress when the callback expires.
  const server = http.createServer({ maxHeaderSize: 8192 }, (request, response) => {
    response.setHeader("Cache-Control", "no-store");
    response.setHeader("Referrer-Policy", "no-referrer");
    response.setHeader("X-Content-Type-Options", "nosniff");
    if (used || request.method !== "GET" || request.headers.host !== authority || request.url.split("?")[0] !== pathname) {
      response.writeHead(404).end();
      return;
    }
    used = true;
    try {
      const value = receive(new URL(request.url, "http://" + authority), response);
      resolveResult(value);
    } catch (error) { response.writeHead(400).end(); rejectResult(error); }
    finally { clearTimeout(timer); server.close(); }
  });
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  authority = hostname + ":" + server.address().port;
  const close = () => {
    clearTimeout(timer);
    server.close();
    if (!used) server.closeAllConnections();
    rejectResult(new Error("Browser callback closed"));
  };
  timer = setTimeout(close, 30000);
  timer.unref();
  return { url: "http://" + authority + pathname, result, close };
}

export async function auditBrowser(input, launch) {
  if (typeof input.timezone !== "string" || input.timezone.length > 128)
    throw failure("invalid_input", "诊断时区无效");
  const zone = input.timezone;
  new Intl.DateTimeFormat("en", { timeZone: zone });
  const callback = await browserCallback("127.0.0.1", (url, res) => {
    const observation = JSON.parse(url.searchParams.get("result"));
    if (!observation || ["language", "timezone", "expected_timezone", "platform"].some(k => typeof observation[k] !== "string" || !observation[k] || observation[k].length > 128) ||
        (input.network && !Number.isInteger(observation.network?.status))) throw Error("Missing browser observation");
    res.setHeader("Content-Type", "text/html; charset=utf-8");
    res.end('<!doctype html><meta charset="utf-8"><title>浏览器检测完成</title><p>检测完成，可以关闭此标签页。</p><script>window.close()</script>');
    return observation;
  });
  let localPage;
  try {
    // The synthetic gateway serves the same fixed page only while checking
    // network policy. Routine language/timezone checks stay entirely local.
    if (!input.network) {
      const html = await readFile(new URL("./browser-check.html", import.meta.url));
      localPage = await browserCallback("127.0.0.1", (_, res) => {
        res.setHeader("Content-Type", "text/html; charset=utf-8"); res.end(html);
      });
    }
    const url = new URL(localPage?.url ?? "http://198.18.0.254/browser-check");
    url.hash = new URLSearchParams({ report: callback.url, zone, network: input.network ? "1" : "0" });
    await launch(url.href);
    return await callback.result;
  } finally { localPage?.close(); callback.close(); }
}

const run = promisify(execFile);

// The full check samples a real page. Background monitoring compares the
// signed Chrome process, its active profile, and the preferences that supply
// language and timezone. It does not claim to sample a new page.
export async function browserSettingsInputs(info, profile) {
  if (!Number.isSafeInteger(info.profile_pid) || info.profile_pid <= 0 || !Number.isSafeInteger(info.profile_started) || info.profile_started <= 0)
    throw failure("browser_recheck_needed", "请打开 Chrome 并重新检测");
  const local = JSON.parse(await readFile(path.join(profile, "Local State"), "utf8"));
  const current = local.profile?.last_used || "Default";
  const active = local.profile?.last_active_profiles ?? [current];
  if (!Array.isArray(active) || active.length > 1 || (active.length && active[0] !== current) ||
      !/^(?:Default|Profile [0-9]+)$/.test(current)) throw failure("browser_recheck_needed", "请在单个 Chrome 资料中完成检测");
  const prefs = JSON.parse(await readFile(path.join(profile, current, "Preferences"), "utf8"));
  const [zone, languages, locale] = await Promise.all([
    realpath("/etc/localtime"),
    run("/usr/bin/defaults", ["read", "-g", "AppleLanguages"], { timeout: 2500 }),
    run("/usr/bin/defaults", ["read", "-g", "AppleLocale"], { timeout: 2500 }),
  ]);
  return JSON.stringify([info.profile_pid, info.profile_started, current, info.version, zone, languages.stdout, locale.stdout,
    prefs.intl ?? null, local.intl ?? null]);
}

export function nativeBrowserDispatch({ window, editor, tailnet, settingsInputs, profile }) {
  let homepage, observed, focusSequence = 0, sequence = 1;
  let lastEventStatus, sampledAt = 0;
  const capabilities = { nativeWindow: true, login: true, editor: true };
  async function status(signal) {
    const info = await window("status", {}, signal);
    if (info.surface !== "browser" || !info.installed) throw failure("browser_unavailable", "请安装客机 Chrome 并更新窗口组件");
    return info;
  }
  function snapshot(info) {
    return { active: tabID, tabs: [{ id: tabID, kind: "web", native_window: true, title: info.login ? "账号授权" : "Google Chrome", login: Boolean(info.login), url: "" }], capabilities, uploads: [], downloads: [] };
  }
  return async ({ method, path, input = {}, operation }, { signal } = {}) => {
    signal?.throwIfAborted();
    if (operation === "configure") return { ok: true };
    if (method === "POST" && path === "/profile" && profile) { await profile(input); return { ok: true }; }
    if (method === "POST" && path === "/tailnet") return tailnet.request(input);
    if (method === "POST" && path === "/homepage") {
      homepage = input.url === "about:blank" ? undefined : webURL(input.url).href;
      return { ok: true };
    }
    if (method === "GET" && path === "/events") {
      // The SSE relay polls every 300 ms. Refresh periodically for a window
      // closed natively and immediately after an explicit login transition.
      if (!lastEventStatus || Date.now() - sampledAt >= 4000) {
        const current = await status(signal);
        if (lastEventStatus && current.login !== lastEventStatus.login) sequence++;
        lastEventStatus = current;
        sampledAt = Date.now();
      }
      if (input.onlyChanges && input.after === sequence) return { sequence, events: [] };
      const state = snapshot(lastEventStatus);
      if (lastEventStatus.login && focusSequence > (input.after || 0)) state.focus = true;
      return { sequence, events: [], state };
    }
    const info = await status(signal);
    if (method === "GET") {
      if (path === "/health") return { ready: true, native: true, capabilities };
      if (path === "/tabs") return snapshot(info);
    }
    if (method !== "POST") throw failure("invalid_input", "请使用 Chrome 窗口操作");
    const launch = url => window("launch", url ? { url } : {}, signal);
    if (path === "/audit") {
      if (info.login) throw failure("login_active", "请完成当前账号授权后重新检测");
      observed = undefined;
      let before;
      if (info.profile_pid) {
        try { before = await settingsInputs(info); }
        catch (error) { if (error.code !== "ENOENT") throw error; }
      }
      const value = await auditBrowser(input, launch);
      // A newly created Chrome profile can finish writing Local State and
      // Preferences a few seconds after the first page finishes rendering.
      let key;
      for (let attempt = 0; attempt < 12; attempt++) {
        try { key = await settingsInputs(await status(signal)); break; }
        catch (error) {
          if (error.code !== "ENOENT" || attempt === 11) throw error;
          await delay(250, undefined, { signal });
        }
      }
      if (before && before !== key) throw failure("browser_recheck_needed", "检测期间 Chrome 设置发生变化，请重新检测");
      observed = { key, zone: input.timezone, language: value.language };
      return value;
    }
    if (path === "/audit-recheck") {
      if (!observed || observed.zone !== input.timezone || observed.language.toLowerCase() !== String(input.language).toLowerCase() ||
          await settingsInputs(info) !== observed.key) throw failure("browser_recheck_needed", "Chrome 进程或浏览器设置发生变化，请重新检测");
      return { unchanged: true };
    }
    if (path === "/editor" || path === "/editor-restart") {
      await editor(input.workspace, path === "/editor-restart", launch);
      return { id: tabID };
    }
    if (path === "/login-open" || path === "/login-finish") {
      if (!/^[a-f0-9-]{16,64}$/.test(input.login || "")) throw failure("invalid_input", "登录任务无效");
      if (path === "/login-finish") {
        await window("finish-login", { login: input.login }, signal);
        sequence++;
        lastEventStatus = undefined;
        return { ok: true };
      }
      const url = webURL(input.url);
      if (url.protocol !== "https:" || !officialLoginHosts.has(url.host)) throw failure("invalid_input", "仅可打开官方登录地址");
      await window("launch", { login: input.login, url: url.href }, signal);
      focusSequence = ++sequence;
      lastEventStatus = undefined;
      return { id: tabID };
    }
    if (path === "/close") return { ok: true, closed: true };
    if (path === "/new" && (input.incognito || input.private)) throw failure("native_window_required", "请在 Chrome 菜单选择“打开新的无痕式窗口”");
    if (path === "/new" || path === "/navigate" || path === "/activate") {
      const url = input.url && input.url !== "about:blank" ? webURL(input.url).href : undefined;
      await launch(url ?? (path === "/new" && !input.url ? homepage : undefined));
      return { id: tabID };
    }
    if (path === "/visibility" || path === "/resize") return { ok: true };
    throw failure("native_window_required", "请在 Chrome 窗口中执行此操作");
  };
}
