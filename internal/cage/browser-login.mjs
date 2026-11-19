import net from "node:net";

const official = new Set(["claude.ai", "claude.com", "console.anthropic.com", "platform.claude.com", "platform.anthropic.com"]);
export function isClaudeSignIn(address, explicitLogin = false) {
  let url;
  try { url = new URL(address); } catch { return false; }
  if (url.protocol !== "https:" || url.username || url.password || url.port) return false;
  if (official.has(url.hostname)) return /^\/(?:cai\/)?(?:login|auth|oauth|sso|sso-callback|magic-link|desktop\/(?:authorize|callback)|api\/auth)(?:\/|$)/.test(url.pathname);
  const provider = (url.hostname === "api.workos.com" && url.pathname.startsWith("/sso/")) ||
    (url.hostname === "accounts.google.com" && /^\/o\/oauth2\//.test(url.pathname));
  if (!provider || explicitLogin) return provider;
  const redirects = url.searchParams.getAll("redirect_uri");
  if (redirects.length !== 1) return false;
  try {
    const callback = new URL(redirects[0]);
    return callback.protocol === "https:" && !callback.username && !callback.password &&
      !callback.port && official.has(callback.hostname);
  } catch { return false; }
}

// Chrome accepts TCP proxy URLs. The only upstream of this loopback listener
// is the selected account's Unix socket; the relay resolves every name there.
async function accountProxy(route) {
  if (!/^[a-f0-9]{24}$/.test(route)) throw new Error("账号出口无效");
  const sockets = new Set();
  const server = net.createServer(client => {
    const remote = net.connect({ path: `/run/account-routes/${route}/browser.sock` });
    for (const [socket, peer] of [[client, remote], [remote, client]]) {
      sockets.add(socket);
      socket.on("error", () => socket.destroy());
      socket.on("close", () => { sockets.delete(socket); peer.destroy(); });
    }
    client.pipe(remote).pipe(client);
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", resolve);
  });
  return {
    options: { proxyServer: "socks5://127.0.0.1:" + server.address().port, proxyBypassList: "<-loopback>" },
    close() { server.close(); for (const socket of sockets) socket.destroy(); },
  };
}

// A login controls only its own targets. Even in a regular profile, redirects
// and popup URLs must never expose codes or state to the controller.
export class LoginBrowser {
  constructor(cdp, cookies) {
    this.cookies = cookies;
    this.cdp = cdp;
    this.logins = new Map();
    this.contexts = new Map();
    this.targets = new Map();
    this.accountProfiles = new Map();
  }

  tab(raw, previous) {
    const entry = this.targets.get(raw.targetId) || this.targets.get(raw.openerId) || this.contexts.get(raw.browserContextId);
    if (!entry && !previous?.login) return raw;
    if (entry && raw.targetId) {
      this.targets.set(raw.targetId, entry);
      entry.targets.add(raw.targetId);
    }
    if (entry?.external) return { ...raw, routeScoped: true };
    let origin = "about:blank";
    try {
      const url = new URL(raw.url);
      if (["https:", "http:"].includes(url.protocol)) origin = url.origin;
    } catch {}
    return { ...raw, login: true, incognito: entry?.private ?? previous?.incognito, url: origin, title: "账号授权" };
  }

  async open(login, address, activate, { private: privateLogin = true, route, account, external = false } = {}) {
    if (typeof login !== "string" || !/^[a-f0-9-]{16,64}$/.test(login)) throw new Error("登录任务无效");
    const url = new URL(address);
    if (url.protocol !== "https:" || url.username || url.password || url.port ||
        !(external && route || official.has(url.hostname) || isClaudeSignIn(address, true))) throw new Error("仅可打开官方登录地址");
    await this.finish(login);
    // Reuse the regular browser for the default route. A bound account can
    // restore only the selected account's cookies.
    const key = account || route;
    if (key && !/^[a-f0-9]{24}$/.test(key)) throw new Error("授权账号无效");
    const profileKey = key ? (route || "shared") + ":" + key : "";
    let profile = !privateLogin && profileKey ? this.accountProfiles.get(profileKey) : undefined;
    if (!profile && (privateLogin || profileKey)) {
      const proxy = route ? await accountProxy(route) : undefined;
      try {
        const { browserContextId } = await this.cdp("Target.createBrowserContext", { disposeOnDetach: true, ...proxy?.options });
        profile = { browserContextId, proxy, users: 0 };
        if (!privateLogin && key) {
          const cookies = await this.cookies.load(key);
          if (cookies.length) await this.cdp("Storage.setCookies", { browserContextId, cookies });
        }
      } catch (e) {
        if (profile) await this.cdp("Target.disposeBrowserContext", {browserContextId: profile.browserContextId}).catch(() => {});
        proxy?.close(); throw e;
      }
      if (!privateLogin && profileKey) this.accountProfiles.set(profileKey, profile);
    }
    if (profile) profile.users++;
    const entry = { login, key, profileKey, external, private: privateLogin, profile, targets: new Set() };
    this.logins.set(login, entry);
    if (privateLogin) this.contexts.set(profile.browserContextId, entry);
    if (!external) {
      entry.expiry = setTimeout(() => this.finish(login).catch(() => {}), 15 * 60_000);
      entry.expiry.unref();
    }
    try {
      const context = profile ? { browserContextId: profile.browserContextId } : {};
      const { targetId } = await this.cdp("Target.createTarget", { ...context, url: "about:blank", newWindow: true });
      entry.targets.add(targetId);
      this.targets.set(targetId, entry);
      const sessionId = await activate({ targetId, ...context, type: "page", url: url.href });
      await this.cdp("Page.navigate", { url: url.href }, sessionId);
      return { id: targetId, incognito: privateLogin };
    } catch {
      await this.finish(login);
      throw new Error("授权页面未能打开");
    }
  }

  async targetClosed(id) {
    const entry = this.targets.get(id);
    if (!entry) return;
    this.targets.delete(id);
    entry.targets.delete(id);
    if (!entry.targets.size) await this.finish(entry.login);
  }

  async finish(login) {
    const entry = this.logins.get(login);
    if (!entry) return;
    this.logins.delete(login);
    clearTimeout(entry.expiry);
    const profile = entry.profile;
    const dispose = profile && --profile.users === 0;
    if (dispose) {
      this.contexts.delete(profile.browserContextId);
      this.accountProfiles.delete(entry.profileKey);
    }
    for (const id of entry.targets) this.targets.delete(id);
    try {
      if (profile && !entry.private) {
        const {cookies} = await this.cdp("Storage.getCookies", {browserContextId:profile.browserContextId});
        await this.cookies.save(entry.key,cookies.filter(cookie=>!cookie.partitionKeyOpaque).map(cookie=>{
          const {size,session,partitionKeyOpaque,...params} = cookie;
          if (session) delete params.expires;
          if (!cookie.domain.startsWith(".")) {
            // Setting only a domain can widen a host-only cookie to subdomains.
            params.url = (cookie.secure ? "https://" : "http://") + cookie.domain + "/";
            delete params.domain;
          }
          return params;
        }));
      }
    } finally {
      if (dispose) {
        try { await this.cdp("Target.disposeBrowserContext", {browserContextId:profile.browserContextId}); }
        finally { profile.proxy?.close(); }
      } else await Promise.all([...entry.targets].map(targetId=>this.cdp("Target.closeTarget", {targetId}).catch(()=>{})));
    }
  }

  async shutdown() {
    for (const login of [...this.logins.keys()]) await this.finish(login);
  }
}
