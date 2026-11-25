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

const cdpMethodPattern = /\b(Target|Page|Browser|Storage|Runtime|Emulation|Input|Network|Extensions|Security|DOM)\.([A-Za-z][A-Za-z0-9]*)\b/g;

// Rewrite fixed CDP Domain.method tokens so diagnostic host redaction keeps
// the exact failing protocol method instead of collapsing it to "[host]".
export function formatCDPDiagnostic(error) {
  const message = String(error?.message || error || "unknown error");
  return message.replace(cdpMethodPattern, "$1/$2");
}

// A login controls only its own targets. Even in a regular profile, redirects
// and popup URLs must never expose codes or state to the controller.
export class LoginBrowser {
  constructor(cdp, cookies, { commandTimeout = 12000, navigateTimeout = 30000, navigateWait = 3000 } = {}) {
    this.cookies = cookies;
    this.cdp = cdp;
    this.commandTimeout = commandTimeout;
    this.navigateTimeout = navigateTimeout;
    this.navigateWait = navigateWait;
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
    // Reuse the regular browser for the default route. A bound account can
    // restore only the selected account's cookies.
    const key = account || route;
    if (key && !/^[a-f0-9]{24}$/.test(key)) throw new Error("授权账号无效");
    if (route && !/^[a-f0-9]{24}$/.test(route)) throw new Error("账号出口无效");
    let stage = "finish-previous";
    let profile, proxy, createdProfile = false;
    const profileKey = key ? (route || "shared") + ":" + key : "";
    try {
      await this.finish(login);
      profile = !privateLogin && profileKey ? this.accountProfiles.get(profileKey) : undefined;
      if (!profile && (privateLogin || profileKey)) {
        if (route) {
          stage = "account-proxy";
          proxy = await accountProxy(route);
        }
        stage = "create-context";
        const { browserContextId } = await this.cdp("Target.createBrowserContext", { disposeOnDetach: true, ...proxy?.options }, undefined, this.commandTimeout);
        profile = { browserContextId, proxy, users: 0 };
        createdProfile = true;
        if (!privateLogin && key) {
          stage = "load-cookies";
          const cookies = await this.cookies.load(key);
          if (cookies.length) await this.cdp("Storage.setCookies", { browserContextId, cookies }, undefined, this.commandTimeout);
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
      const context = profile ? { browserContextId: profile.browserContextId } : {};
      stage = "create-target";
      const { targetId } = await this.cdp("Target.createTarget", { ...context, url: "about:blank", newWindow: true }, undefined, this.commandTimeout);
      entry.targets.add(targetId);
      this.targets.set(targetId, entry);
      stage = "activate-target";
      const sessionId = await activate({ targetId, ...context, type: "page", url: url.href });
      stage = "navigate";
      // Page.navigate waits for response headers/commit, which on a slow proxy
      // can exceed a short command timeout. Once the prepared tab is active and
      // navigation has been dispatched, wait briefly for immediate CDP errors
      // without destroying the tab if the remote server is still responding.
      const navigation = Promise.resolve(this.cdp("Page.navigate", { url: url.href }, sessionId, this.navigateTimeout));
      let deferred = false;
      let waitTimer;
      try {
        await Promise.race([
          navigation,
          new Promise(resolve => {
            waitTimer = setTimeout(() => {
              deferred = true;
              resolve();
            }, this.navigateWait);
            waitTimer.unref?.();
          }),
        ]);
      } finally {
        clearTimeout(waitTimer);
      }
      if (deferred) {
        void navigation.catch(error => {
          console.error(`Login browser navigation failed after handoff (stage=navigate): ${formatCDPDiagnostic(error)}`);
        });
      }
      return { id: targetId, incognito: privateLogin };
    } catch (error) {
      console.error(`Login browser open failed (stage=${stage}): ${formatCDPDiagnostic(error)}`);
      if (this.logins.has(login)) {
        await this.finish(login).catch(() => {});
      } else if (createdProfile && profile) {
        if (!privateLogin && profileKey && this.accountProfiles.get(profileKey) === profile) {
          this.accountProfiles.delete(profileKey);
        }
        await this.cdp("Target.disposeBrowserContext", { browserContextId: profile.browserContextId }).catch(() => {});
        proxy?.close();
      } else {
        proxy?.close();
      }
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
