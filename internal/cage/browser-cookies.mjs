import { join } from "node:path";
import { link, mkdir, readFile, rename, unlink, writeFile } from "node:fs/promises";
import { randomUUID, randomBytes, createCipheriv, createDecipheriv, createHash } from "node:crypto";

const key = cookie => createHash("sha256").update(JSON.stringify([
  cookie.name, cookie.domain, cookie.path, cookie.partitionKey || null, Boolean(cookie.partitionKeyOpaque),
])).digest("hex");
const forHost = (cookie, hostname) => cookie.domain.startsWith(".")
  ? hostname === cookie.domain.slice(1) || hostname.endsWith(cookie.domain)
  : hostname === cookie.domain;

export async function browserCookies(cdp, session, tab, input) {
  if (!tab || tab.login) throw new Error("当前标签不可管理 Cookie");
  if (input.url !== undefined && input.url !== tab.url) throw new Error("网页已切换，请重新打开 Cookie 管理");
  const { mode = "list", scope = "site", query = "", page = 0 } = input;
  if (!["list", "details", "delete", "delete-selected", "clear"].includes(mode) || !["site", "all"].includes(scope) ||
      typeof query !== "string" || query.length > 200 || !Number.isInteger(page) || page < 0)
    throw new Error("Cookie 操作无效");
  if (mode === "delete-selected" && (!Array.isArray(input.cookieKeys) || !input.cookieKeys.length ||
      !input.cookieKeys.every(value => typeof value === "string" && /^[a-f0-9]{64}$/.test(value))))
    throw new Error("请选择要删除的 Cookie");
  const matches = cookie => `${cookie.domain}\n${cookie.name}`.toLowerCase().includes(query.toLowerCase());
  let hostname = "";
  if (scope === "site") {
    const url = new URL(tab.url);
    if (!["http:", "https:"].includes(url.protocol)) throw new Error("当前页面没有网站 Cookie，请选择所有网站");
    hostname = url.hostname;
  }
  const context = tab.browserContextId ? { browserContextId: tab.browserContextId } : {};
  const read = async () => {
    const { cookies } = await cdp("Storage.getCookies", context);
    return cookies.filter(cookie => !hostname || forHost(cookie, hostname));
  };
  let cookies = await read();
  const remove = async cookie => {
    // Opaque partition keys cannot be addressed exactly by Network.deleteCookies.
    if (cookie.partitionKeyOpaque) throw new Error("此 Cookie 的分区不可单独定位，请使用清空所有网站 Cookie");
    await cdp("Network.deleteCookies", {
      name: cookie.name, domain: cookie.domain, path: cookie.path,
      ...(cookie.partitionKey ? { partitionKey: cookie.partitionKey } : {}),
    }, await session(tab.targetId));
  };
  if (mode === "delete" || mode === "details") {
    const cookie = cookies.find(cookie => key(cookie) === input.cookieKey);
    if (!cookie) throw new Error("Cookie 已变化，请刷新列表");
    if (mode === "details") return { value: cookie.value };
    await remove(cookie);
    cookies = await read();
  } else if (mode === "delete-selected") {
    const available = new Map(cookies.filter(matches).map(cookie => [key(cookie), cookie]));
    const selected = [...new Set(input.cookieKeys)].map(value => available.get(value));
    if (selected.some(cookie => !cookie)) throw new Error("所选 Cookie 已变化，请刷新列表后重新选择");
    if (selected.some(cookie => cookie.partitionKeyOpaque)) throw new Error("所选 Cookie 含不可单独定位的分区，请使用清空所有网站 Cookie");
    try {
      for (const cookie of selected) await remove(cookie);
    } catch (error) {
      throw new Error("批量删除未完成，部分 Cookie 可能已删除；请刷新列表后重新选择。", { cause: error });
    }
    cookies = await read();
  } else if (mode === "clear") {
    if (scope === "all") await cdp("Storage.clearCookies", context);
    else {
      if (cookies.some(cookie => cookie.partitionKeyOpaque)) throw new Error("此网站含不可单独定位的分区 Cookie，请使用清空所有网站 Cookie");
      for (const cookie of cookies) await remove(cookie);
    }
    cookies = await read();
  }
  cookies = cookies.filter(matches)
    .sort((a, b) => a.domain.localeCompare(b.domain) || a.name.localeCompare(b.name) || a.path.localeCompare(b.path));
  const current = Math.min(page, Math.max(0, Math.ceil(cookies.length / 50) - 1));
  return { total: cookies.length, page: current, hostname, incognito: Boolean(tab.incognito),
    selectableKeys: cookies.filter(cookie => !cookie.partitionKeyOpaque).map(key),
    cookies: cookies.slice(current * 50, (current + 1) * 50).map(({ value, ...cookie }) => ({
      key: key(cookie), name: cookie.name, domain: cookie.domain, path: cookie.path,
      expires: cookie.expires, session: cookie.session, secure: cookie.secure, httpOnly: cookie.httpOnly,
      sameSite: cookie.sameSite || "", partitionKey: cookie.partitionKey,
      partitionKeyOpaque: Boolean(cookie.partitionKeyOpaque), size: cookie.size,
    })),
  };
}

// The browser process keeps opted-in sign-in cookies in the guest user's
// private profile. The account ID authenticates each encrypted record, so a
// record moved to another account cannot be restored there.
export class AccountLoginCookies {
  constructor(directory) { this.directory = directory; }

  async file(account) {
    if (!/^[a-f0-9]{24}$/.test(account)) throw new Error("授权账号无效");
    await mkdir(this.directory, {recursive: true, mode: 0o700});
    return join(this.directory, account);
  }

  async key() {
    const filename = join(this.directory, ".key");
    try { return await readFile(filename); }
    catch (error) { if (error.code !== "ENOENT") throw error; }
    const key = randomBytes(32), temporary = filename + "." + randomUUID();
    await writeFile(temporary, key, {flag: "wx", mode: 0o600});
    try {
      await link(temporary, filename);
      return key;
    } catch (error) {
      if (error.code !== "EEXIST") throw error;
      return readFile(filename);
    } finally { await unlink(temporary); }
  }

  async load(account) {
    const file = await this.file(account);
    let envelope;
    try { envelope = await readFile(file); }
    catch (error) { if (error.code === "ENOENT") return []; throw error; }
    const decipher = createDecipheriv("aes-256-gcm", await this.key(), envelope.subarray(0,12));
    decipher.setAAD(Buffer.from(account)); decipher.setAuthTag(envelope.subarray(12,28));
    return JSON.parse(Buffer.concat([decipher.update(envelope.subarray(28)),decipher.final()]).toString("utf8"));
  }

  async save(account, cookies) {
    const file = await this.file(account);
    const iv = randomBytes(12), cipher = createCipheriv("aes-256-gcm", await this.key(), iv);
    cipher.setAAD(Buffer.from(account));
    const body = Buffer.concat([cipher.update(JSON.stringify(cookies)), cipher.final()]);
    const temporary = file + "." + randomUUID();
    try {
      await writeFile(temporary, Buffer.concat([iv,cipher.getAuthTag(),body]), {flag:"wx", mode:0o600});
      await rename(temporary, file);
    } finally { await unlink(temporary).catch(() => {}); }
  }
}
