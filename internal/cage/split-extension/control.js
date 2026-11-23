// This extension page is opened as a hidden CDP target by the guest controller.
// The debugger permission maps CDP target IDs to Chrome tab IDs. We never
// attach a debugger through the extension or accept messages from webpages.
const targetsByTab = new Map();
globalThis.manageCageExtensions = async function (input) {
  const items = (await chrome.management.getAll()).filter(item => item.id !== chrome.runtime.id && item.type === 'extension');
  if (input.mode === 'list') return { items: items.map(({ id, name, description, version, enabled, mayDisable, optionsUrl, permissions, hostPermissions, installType }) =>
    ({ id, name, description, version, enabled, mayDisable, optionsUrl, permissions, hostPermissions, installType })) };
  const entry = items.find(item => item.id === input.id);
  if (!entry) throw new Error('扩展不存在');
  if (input.mode === 'options') return { url: entry.optionsUrl || '' };
  if (!entry.mayDisable) throw new Error('此扩展由浏览器管理');
  if (!['enable', 'disable'].includes(input.mode)) throw new Error('扩展操作无效');
  await chrome.management.setEnabled(input.id, input.mode === 'enable');
  return { ok: true };
};
function isHistoryURL(value) {
  if (typeof value !== 'string' || !value || value.length > 16384) return false;
  try {
    const parsed = new URL(value);
    if (!['http:', 'https:'].includes(parsed.protocol) || parsed.username || parsed.password) return false;
    if (parsed.hostname === '127.0.0.1' && parsed.port === '18080') return false;
    return true;
  } catch {
    return false;
  }
}
const historyRanges = {
  hour: 60 * 60 * 1000,
  day: 24 * 60 * 60 * 1000,
  week: 7 * 24 * 60 * 60 * 1000,
};
globalThis.manageCageHistory = async function (input) {
  const mode = input?.mode || 'list';
  if (mode === 'list') {
    const query = typeof input?.query === 'string' ? input.query.trim().slice(0, 500) : '';
    const limit = Math.max(1, Math.min(500, Number.isInteger(input?.limit) ? input.limit : 100));
    const startTime = Number.isFinite(input?.startTime) && input.startTime >= 0 ? input.startTime : 0;
    const params = { text: query, startTime, maxResults: Math.min(1000, limit + 80) };
    if (Number.isFinite(input?.endTime) && input.endTime > 0) params.endTime = input.endTime;
    const raw = await chrome.history.search(params);
    const items = raw
      .filter(item => isHistoryURL(item?.url))
      .sort((a, b) => (Number(b.lastVisitTime) || 0) - (Number(a.lastVisitTime) || 0))
      .slice(0, limit)
      .map(item => ({
        id: String(item.id || item.url),
        url: item.url,
        title: String(item.title || '').slice(0, 300),
        lastVisitTime: Number(item.lastVisitTime) || 0,
        visitCount: Math.max(1, Number(item.visitCount) || 1),
      }));
    return { items };
  }
  if (mode === 'delete') {
    if (!isHistoryURL(input?.url)) throw new Error('历史记录地址无效');
    await chrome.history.deleteUrl({ url: input.url });
    return { ok: true };
  }
  if (mode === 'clear') {
    const range = input?.range || 'all';
    if (range === 'all') {
      await chrome.history.deleteAll();
      return { ok: true };
    }
    const span = historyRanges[range];
    if (!span) throw new Error('清除范围无效');
    const endTime = Date.now();
    await chrome.history.deleteRange({ startTime: Math.max(0, endTime - span), endTime });
    return { ok: true };
  }
  throw new Error('历史记录操作无效');
};
let managedSplit;
const faviconVersions = new Map();
chrome.tabs.onUpdated.addListener((tabId, changes) => {
  if (!("favIconUrl" in changes || "url" in changes || changes.status === "complete")) return;
  faviconVersions.set(tabId, (faviconVersions.get(tabId) || 0) + 1);
  void chrome.debugger.getTargets().then(targets => {
    const target = targets.find(item => item.tabId === tabId);
    if (target) globalThis.cageFaviconChanged?.(target.id);
  }).catch(() => {});
});
chrome.tabs.onRemoved.addListener(tabId => faviconVersions.delete(tabId));

// Read Chromium's decoded favicon cache. The extension never requests a site's
// icon URL, so its permissions cannot bypass the webpage's image policy.
globalThis.readCageFavicon = async function (targetId) {
  try {
    const targets = await chrome.debugger.getTargets();
    const tabId = targets.find(target => target.id === targetId)?.tabId;
    if (tabId == null) return null;
    const tab = await chrome.tabs.get(tabId);
    const version = faviconVersions.get(tabId);
    if (!/^https?:/.test(tab.url || "") || !tab.favIconUrl) return { url: tab.url, favicon: "" };
    const source = new URL(chrome.runtime.getURL("/_favicon/"));
    source.searchParams.set("pageUrl", tab.url);
    source.searchParams.set("size", "32");
    source.searchParams.set("version", String(version || 0));
    const image = new Image();
    image.src = source.href;
    await image.decode();
    const current = await chrome.tabs.get(tabId);
    if (current.url !== tab.url || current.favIconUrl !== tab.favIconUrl || faviconVersions.get(tabId) !== version) return null;
    const canvas = document.createElement("canvas");
    canvas.width = canvas.height = 32;
    canvas.getContext("2d").drawImage(image, 0, 0, 32, 32);
    const favicon = canvas.toDataURL("image/png");
    return { url: tab.url, favicon: favicon.length <= 8192 ? favicon : "" };
  } catch {
    return null;
  }
};

chrome.tabs.onActivated.addListener(({ tabId }) => {
  const target = targetsByTab.get(tabId);
  if (target) globalThis.cagePaneActivated?.(target);
});

chrome.tabs.onUpdated.addListener((id, changes) => {
  const target = targetsByTab.get(id);
  if (target && "splitViewId" in changes && changes.splitViewId !== managedSplit) {
    targetsByTab.clear();
    managedSplit = null;
    globalThis.cagePaneSeparated?.(target);
  }
});

globalThis.setCageSplit = async function (left, right) {
  if (!chrome.tabs.createSplit) throw new Error("Chromium 155 or later is required");
  const targets = await chrome.debugger.getTargets();
  const findTab = id => targets.find(t => t.id === id)?.tabId;
  const pair = left ? await Promise.all([left, right].map(id => chrome.tabs.get(findTab(id)))) : [];
  if (pair.length && pair[0].incognito !== pair[1].incognito) throw new Error("Separate browser profiles");
  targetsByTab.clear();
  const affected = new Set(pair.map(tab => tab.splitViewId));
  if (managedSplit != null) affected.add(managedSplit);
  if (!pair.length && managedSplit == null) {
    // Chromium can restore the foreground split before Cage reconnects.
    const [active] = await chrome.tabs.query({ active: true, lastFocusedWindow: true });
    if (active) affected.add(active.splitViewId);
  }
  const open = await chrome.tabs.query({});
  for (const id of affected)
    if (id >= 0 && open.some(tab => tab.splitViewId === id)) await chrome.tabs.unsplit(id);
  managedSplit = null;
  if (!pair.length) return;
  const [a, b] = pair;
  const index = a.index + (b.windowId === a.windowId && b.index < a.index ? 0 : 1);
  if (a.windowId !== b.windowId || b.index !== index)
    await chrome.tabs.move(b.id, { windowId: a.windowId, index });
  managedSplit = await chrome.tabs.createSplit([a.id, b.id]);
  targetsByTab.set(a.id, left);
  targetsByTab.set(b.id, right);
};
