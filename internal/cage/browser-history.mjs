import { mkdir, readFile, rename, writeFile } from "node:fs/promises";
import { join } from "node:path";

const validModes = new Set(["list", "delete", "clear"]);
const validRanges = new Map([
  ["hour", 60 * 60 * 1000],
  ["day", 24 * 60 * 60 * 1000],
  ["week", 7 * 24 * 60 * 60 * 1000],
  ["all", 0],
]);
const phoneHistoryLimit = 500;

export function isHistoryURL(value) {
  if (typeof value !== "string" || !value || value.length > 16384) return false;
  try {
    const parsed = new URL(value);
    if (!["http:", "https:"].includes(parsed.protocol) || parsed.username || parsed.password) return false;
    if (parsed.hostname === "127.0.0.1" && parsed.port === "18080") return false;
    return true;
  } catch {
    return false;
  }
}

export function normalizeHistoryInput(input = {}) {
  const mode = input?.mode || "list";
  if (!validModes.has(mode)) throw new Error("历史记录操作无效");
  if (mode === "list") {
    const query = typeof input?.query === "string" ? input.query.trim() : "";
    if (query.length > 500) throw new Error("搜索词过长，请缩短后重试");
    const limit = input?.limit == null || input.limit === 0 ? 100 : Number(input.limit);
    if (!Number.isInteger(limit) || limit < 1 || limit > 500) throw new Error("历史记录条数无效");
    const startTime = input?.startTime == null ? 0 : Number(input.startTime);
    if (!Number.isFinite(startTime) || startTime < 0) throw new Error("开始时间无效");
    const endTime = input?.endTime == null || input.endTime === 0 ? undefined : Number(input.endTime);
    if (endTime !== undefined && (!Number.isFinite(endTime) || endTime < 0)) throw new Error("结束时间无效");
    return { mode, query, limit, startTime, ...(endTime !== undefined ? { endTime } : {}) };
  }
  if (mode === "delete") {
    const url = typeof input?.url === "string" ? input.url.trim() : "";
    if (!isHistoryURL(url)) throw new Error("历史记录地址无效");
    return { mode, url };
  }
  const range = input?.range || "all";
  if (!validRanges.has(range)) throw new Error("清除范围无效");
  return { mode, range };
}

function sanitizeItems(raw, limit) {
  if (!Array.isArray(raw)) return [];
  const items = [];
  for (const item of raw) {
    if (!isHistoryURL(item?.url)) continue;
    items.push({
      id: String(item.id || item.url),
      url: item.url,
      title: String(item.title || "").slice(0, 300),
      lastVisitTime: Number.isFinite(Number(item.lastVisitTime)) ? Number(item.lastVisitTime) : 0,
      visitCount: Math.max(1, Number.isFinite(Number(item.visitCount)) ? Math.floor(Number(item.visitCount)) : 1),
    });
    if (items.length >= limit) break;
  }
  return items;
}

export class BrowserHistory {
  constructor({ cdp, control }) {
    this.cdp = cdp;
    this.control = control;
  }
  async evaluate(payload) {
    const run = async repair => this.cdp("Runtime.evaluate", {
      expression: `manageCageHistory(${JSON.stringify(payload)})`,
      awaitPromise: true,
      returnByValue: true,
    }, await this.control(repair), 15000);
    let response;
    try { response = await run(false); }
    catch { response = await run(true); }
    if (response.exceptionDetails && /not defined|context|session/i.test(response.exceptionDetails.exception?.description || response.exceptionDetails.text || "")) {
      response = await run(true);
    }
    if (response.exceptionDetails || !response.result?.value) {
      throw new Error("浏览历史暂时不可用，请重试");
    }
    return response.result.value;
  }
  async request(input) {
    const payload = normalizeHistoryInput(input);
    const result = await this.evaluate(payload);
    if (payload.mode === "list") {
      return { items: sanitizeItems(result.items, payload.limit) };
    }
    return { ok: true };
  }
}

export class PhoneHistoryStore {
  constructor({ directory, validateURL = isHistoryURL, now = () => Date.now() }) {
    this.directory = directory;
    this.validateURL = validateURL;
    this.now = now;
    this.queues = new Map();
  }
  file(profile) {
    if (typeof profile !== "string" || !/^[a-f0-9]{64}$/.test(profile)) {
      throw new Error("手机浏览器资料无效");
    }
    return join(this.directory, profile, "history.json");
  }
  serialize(profile, fn) {
    const previous = this.queues.get(profile) || Promise.resolve();
    const task = previous.then(fn);
    const tail = task.catch(() => {});
    this.queues.set(profile, tail);
    tail.finally(() => {
      if (this.queues.get(profile) === tail) this.queues.delete(profile);
    });
    return task;
  }
  async read(profile) {
    const path = this.file(profile);
    try {
      const parsed = JSON.parse(await readFile(path, "utf8"));
      return sanitizeItems(parsed?.items, phoneHistoryLimit).sort((a, b) => b.lastVisitTime - a.lastVisitTime);
    } catch (error) {
      if (error.code === "ENOENT") return [];
      return [];
    }
  }
  async write(profile, items) {
    const path = this.file(profile);
    const dir = join(this.directory, profile);
    await mkdir(dir, { recursive: true, mode: 0o700 });
    const next = path + ".phone-next";
    await writeFile(next, JSON.stringify({ items }), { mode: 0o600 });
    await rename(next, path);
  }
  validPublicURL(url) {
    try {
      const checked = this.validateURL(url);
      if (typeof checked === "string") return checked;
      return checked ? url : "";
    } catch {
      return "";
    }
  }
  record(profile, rawURL, rawTitle = "") {
    const url = this.validPublicURL(rawURL);
    if (!url) return Promise.resolve();
    const title = String(rawTitle || "").trim().slice(0, 300);
    return this.serialize(profile, async () => {
      const items = await this.read(profile);
      const timestamp = this.now();
      const index = items.findIndex(item => item.url === url);
      if (index >= 0) {
        const existing = items[index];
        // Update title without inflating visitCount on rapid consecutive frames.
        const sameRecentVisit = timestamp - existing.lastVisitTime < 5000;
        items[index] = {
          ...existing,
          title: title || existing.title,
          lastVisitTime: Math.max(existing.lastVisitTime, timestamp),
          visitCount: sameRecentVisit ? existing.visitCount : existing.visitCount + 1,
        };
      } else {
        items.push({
          id: url,
          url,
          title,
          lastVisitTime: timestamp,
          visitCount: 1,
        });
      }
      items.sort((a, b) => b.lastVisitTime - a.lastVisitTime);
      await this.write(profile, items.slice(0, phoneHistoryLimit));
    });
  }
  request(profile, input) {
    this.file(profile);
    const payload = normalizeHistoryInput(input);
    return this.serialize(profile, async () => {
      const items = await this.read(profile);
      if (payload.mode === "list") {
        const needle = payload.query.toLowerCase();
        const filtered = items.filter(item => {
          if (payload.startTime && item.lastVisitTime < payload.startTime) return false;
          if (payload.endTime && item.lastVisitTime > payload.endTime) return false;
          if (!needle) return true;
          return `${item.title} ${item.url}`.toLowerCase().includes(needle);
        });
        return { items: filtered.slice(0, payload.limit) };
      }
      if (payload.mode === "delete") {
        const next = items.filter(item => item.url !== payload.url);
        if (next.length !== items.length) await this.write(profile, next);
        return { ok: true };
      }
      if (payload.range === "all") {
        await this.write(profile, []);
        return { ok: true };
      }
      const cutoff = Math.max(0, this.now() - validRanges.get(payload.range));
      const next = items.filter(item => item.lastVisitTime < cutoff);
      await this.write(profile, next);
      return { ok: true };
    });
  }
}
