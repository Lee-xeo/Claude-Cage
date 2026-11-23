const COMMON_SITES = [
  { host: "youtube.com", url: "https://www.youtube.com/", title: "YouTube" },
  { host: "github.com", url: "https://github.com/", title: "GitHub" },
  { host: "google.com", url: "https://www.google.com/", title: "Google" },
  { host: "claude.ai", url: "https://claude.ai/", title: "Claude" },
  { host: "anthropic.com", url: "https://www.anthropic.com/", title: "Anthropic" },
  { host: "openai.com", url: "https://openai.com/", title: "OpenAI" },
  { host: "chatgpt.com", url: "https://chatgpt.com/", title: "ChatGPT" },
  { host: "deepseek.com", url: "https://www.deepseek.com/", title: "DeepSeek" },
  { host: "bilibili.com", url: "https://www.bilibili.com/", title: "Bilibili" },
  { host: "zhihu.com", url: "https://www.zhihu.com/", title: "Zhihu" },
  { host: "wikipedia.org", url: "https://www.wikipedia.org/", title: "Wikipedia" },
  { host: "reddit.com", url: "https://www.reddit.com/", title: "Reddit" },
  { host: "x.com", url: "https://x.com/", title: "X" },
  { host: "stackoverflow.com", url: "https://stackoverflow.com/", title: "Stack Overflow" },
  { host: "npmjs.com", url: "https://www.npmjs.com/", title: "npm" },
  { host: "pypi.org", url: "https://pypi.org/", title: "PyPI" },
  { host: "developer.mozilla.org", url: "https://developer.mozilla.org/", title: "MDN Web Docs" },
  { host: "cloudflare.com", url: "https://www.cloudflare.com/", title: "Cloudflare" },
  { host: "vercel.com", url: "https://vercel.com/", title: "Vercel" },
  { host: "huggingface.co", url: "https://huggingface.co/", title: "Hugging Face" },
  { host: "bing.com", url: "https://www.bing.com/", title: "Bing" },
  { host: "duckduckgo.com", url: "https://duckduckgo.com/", title: "DuckDuckGo" },
  { host: "gmail.com", url: "https://mail.google.com/", title: "Gmail" },
  { host: "docs.google.com", url: "https://docs.google.com/", title: "Google Docs" },
  { host: "drive.google.com", url: "https://drive.google.com/", title: "Google Drive" },
  { host: "figma.com", url: "https://www.figma.com/", title: "Figma" },
  { host: "notion.so", url: "https://www.notion.so/", title: "Notion" },
  { host: "linear.app", url: "https://linear.app/", title: "Linear" },
  { host: "discord.com", url: "https://discord.com/", title: "Discord" },
  { host: "twitch.tv", url: "https://www.twitch.tv/", title: "Twitch" },
  { host: "netflix.com", url: "https://www.netflix.com/", title: "Netflix" },
  { host: "amazon.com", url: "https://www.amazon.com/", title: "Amazon" },
  { host: "apple.com", url: "https://www.apple.com/", title: "Apple" },
  { host: "microsoft.com", url: "https://www.microsoft.com/", title: "Microsoft" },
  { host: "linkedin.com", url: "https://www.linkedin.com/", title: "LinkedIn" },
  { host: "weibo.com", url: "https://weibo.com/", title: "Weibo" },
  { host: "taobao.com", url: "https://www.taobao.com/", title: "Taobao" },
  { host: "jd.com", url: "https://www.jd.com/", title: "JD.com" },
  { host: "baidu.com", url: "https://www.baidu.com/", title: "Baidu" },
  { host: "douban.com", url: "https://www.douban.com/", title: "Douban" },
  { host: "v2ex.com", url: "https://www.v2ex.com/", title: "V2EX" },
  { host: "juejin.cn", url: "https://juejin.cn/", title: "Juejin" },
  { host: "gitee.com", url: "https://gitee.com/", title: "Gitee" },
  { host: "gitlab.com", url: "https://gitlab.com/", title: "GitLab" },
  { host: "docker.com", url: "https://www.docker.com/", title: "Docker" },
  { host: "arxiv.org", url: "https://arxiv.org/", title: "arXiv" },
  { host: "medium.com", url: "https://medium.com/", title: "Medium" },
  { host: "spotify.com", url: "https://open.spotify.com/", title: "Spotify" },
];

export { COMMON_SITES };

function isPrivateHost(host) {
  const name = host.toLowerCase().replace(/\.$/, "");
  if (!name || !name.includes(".") || /(^|\.)(localhost|local|internal|home\.arpa)$/.test(name)) {
    return true;
  }
  if (/^\d+\.\d+\.\d+\.\d+$/.test(name)) {
    const parts = name.split(".").map(Number);
    if (parts.some(n => n < 0 || n > 255)) return true;
    const [a, b] = parts;
    return a === 0 || a === 10 || a === 127 ||
      (a === 100 && b >= 64 && b <= 127) ||
      (a === 169 && b === 254) ||
      (a === 172 && b >= 16 && b <= 31) ||
      (a === 192 && b === 168) ||
      a >= 224;
  }
  return name.includes(":");
}

export function isValidSuggestionURL(raw, { publicOnly = false } = {}) {
  if (typeof raw !== "string" || !raw || raw.length > 16384) return false;
  try {
    const u = new URL(raw);
    if (!["http:", "https:"].includes(u.protocol) || u.username || u.password) return false;
    if (u.hostname === "127.0.0.1" && u.port === "18080") return false;
    if (publicOnly && isPrivateHost(u.hostname)) return false;
    return true;
  } catch {
    return false;
  }
}

export function cleanDisplayURL(raw) {
  try {
    const u = new URL(raw);
    const host = u.host.replace(/^www\./i, "");
    const rest = u.pathname === "/" && !u.search && !u.hash ? "" : `${u.pathname}${u.search}${u.hash}`;
    return host + rest;
  } catch {
    return String(raw || "");
  }
}

function canonicalKey(raw) {
  try {
    const u = new URL(raw);
    const host = u.hostname.toLowerCase().replace(/^www\./, "");
    const port = u.port ? `:${u.port}` : "";
    const path = u.pathname === "/" && !u.search && !u.hash ? "" : u.pathname.replace(/\/+$/, "");
    return `${host}${port}${path}${u.search}${u.hash}`;
  } catch {
    return String(raw || "").toLowerCase();
  }
}

function stripQueryPrefix(raw) {
  return raw.replace(/^https?:\/\//i, "").replace(/^www\./i, "");
}

function matchScore(needle, host, full, title) {
  if (!needle) return 0;
  const hostLower = host.toLowerCase();
  const fullLower = full.toLowerCase();
  const titleLower = (title || "").toLowerCase();
  if (hostLower === needle) return 1000;
  if (hostLower.startsWith(needle)) return 860 - Math.min(80, hostLower.length - needle.length);
  if (fullLower.startsWith(needle)) return 760 - Math.min(80, fullLower.length - needle.length);
  const hostIndex = hostLower.indexOf(needle);
  if (hostIndex > 0 && (hostLower[hostIndex - 1] === "." || hostLower[hostIndex - 1] === "-")) {
    return 650 - hostIndex;
  }
  if (hostIndex > 0) return 520 - hostIndex;
  const fullIndex = fullLower.indexOf(needle);
  if (fullIndex >= 0) return 440 - Math.min(100, fullIndex);
  const titleIndex = titleLower.indexOf(needle);
  if (titleIndex === 0) return 420;
  if (titleIndex > 0) return 340 - Math.min(80, titleIndex);
  return 0;
}

export function looksLikeURLInput(raw, { publicOnly = false } = {}) {
  const value = raw.trim();
  if (!value || /\s/.test(value)) return null;
  if (/^https?:\/\//i.test(value)) {
    try {
      const u = new URL(value);
      return isValidSuggestionURL(u.href, { publicOnly }) ? u.href : null;
    } catch {
      return null;
    }
  }
  if (/^[a-z][a-z\d+.-]*:/i.test(value) && !/^localhost:\d+/i.test(value)) return null;
  if (value.includes(".") || (!publicOnly && /^localhost(?::\d+)?(?:\/|$)/i.test(value))) {
    try {
      const scheme = /^localhost|^127\./i.test(value) ? "http://" : "https://";
      const u = new URL(scheme + value);
      return isValidSuggestionURL(u.href, { publicOnly }) ? u.href : null;
    } catch {
      return null;
    }
  }
  return null;
}

export function completeDotCom(raw) {
  const value = String(raw || "").trim();
  if (!/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/i.test(value)) return null;
  return `https://www.${value.toLowerCase()}.com/`;
}

export function buildAddressSuggestions({
  query = "",
  tabs = [],
  favorites = [],
  history = [],
  activeTabId = "",
  allowSearch = true,
  publicOnly = false,
  limit = 8,
  now = Date.now(),
} = {}) {
  const trimmed = String(query || "").trim();
  if (!trimmed) return [];
  const needle = stripQueryPrefix(trimmed).toLowerCase();
  if (!needle) return [];

  const merged = new Map();
  const consider = (candidate) => {
    const key = candidate.kind === "tab" ? `tab:${candidate.tabId}` : canonicalKey(candidate.url);
    const existing = merged.get(key);
    if (!existing) {
      merged.set(key, candidate);
      return;
    }
    if (candidate.score > existing.score) {
      merged.set(key, {
        ...candidate,
        title: candidate.title || existing.title,
      });
    } else if (!existing.title && candidate.title) {
      existing.title = candidate.title;
    }
  };

  for (const tab of tabs) {
    if (!tab || tab.id === activeTabId || tab.kind !== "web") continue;
    if (!isValidSuggestionURL(tab.url, { publicOnly })) continue;
    const displayURL = cleanDisplayURL(tab.url);
    const host = new URL(tab.url).host.replace(/^www\./i, "");
    const base = matchScore(needle, host, displayURL, tab.title);
    if (base <= 0) continue;
    consider({
      id: `tab:${tab.id}`,
      kind: "tab",
      tabId: tab.id,
      title: tab.title || host,
      url: tab.url,
      displayURL,
      host,
      score: base + 140,
    });
  }

  for (const fav of favorites) {
    if (!fav || !isValidSuggestionURL(fav.url, { publicOnly })) continue;
    const displayURL = cleanDisplayURL(fav.url);
    const host = new URL(fav.url).host.replace(/^www\./i, "");
    const base = matchScore(needle, host, displayURL, fav.title);
    if (base <= 0) continue;
    consider({
      id: `favorite:${fav.id || fav.url}`,
      kind: "favorite",
      title: fav.title || host,
      url: fav.url,
      displayURL,
      host,
      score: base + 180,
    });
  }

  for (const item of history) {
    if (!item || !isValidSuggestionURL(item.url, { publicOnly })) continue;
    const displayURL = cleanDisplayURL(item.url);
    const host = new URL(item.url).host.replace(/^www\./i, "");
    const base = matchScore(needle, host, displayURL, item.title);
    if (base <= 0) continue;
    const visits = Math.max(1, Number(item.visitCount) || 1);
    const ageHours = Math.max(0, (now - (Number(item.lastVisitTime) || 0)) / 3600000);
    const recency = ageHours < 24 ? 70 : ageHours < 168 ? 40 : 15;
    const frequency = Math.min(60, Math.round(Math.log2(visits + 1) * 18));
    consider({
      id: `history:${item.id || item.url}`,
      kind: "history",
      title: item.title || host,
      url: item.url,
      displayURL,
      host,
      score: base + recency + frequency,
    });
  }

  for (const site of COMMON_SITES) {
    if (!isValidSuggestionURL(site.url, { publicOnly })) continue;
    const base = matchScore(needle, site.host, site.host, site.title);
    if (base <= 0) continue;
    // Domain-prefix suggestions shine when typing prefixes like "you" -> "youtube.com".
    const prefixBoost = site.host.startsWith(needle) ? 55 : 0;
    consider({
      id: `site:${site.host}`,
      kind: "site",
      title: site.title,
      url: site.url,
      displayURL: site.host,
      host: site.host,
      score: base + prefixBoost,
    });
  }

  const ranked = [...merged.values()].sort((a, b) => b.score - a.score);
  const results = [];
  const directURL = looksLikeURLInput(trimmed, { publicOnly });
  if (directURL) {
    const directKey = canonicalKey(directURL);
    const existingIndex = ranked.findIndex(item => item.kind !== "tab" && canonicalKey(item.url) === directKey);
    if (existingIndex === -1) {
      results.push({
        id: `url:${directURL}`,
        kind: "url",
        title: cleanDisplayURL(directURL),
        url: directURL,
        displayURL: cleanDisplayURL(directURL),
        host: new URL(directURL).host.replace(/^www\./i, ""),
        score: 2000,
      });
    }
  }

  const maxPrimary = allowSearch && !directURL ? Math.max(1, limit - 1) : limit;
  for (const item of ranked) {
    if (results.length >= maxPrimary) break;
    results.push(item);
  }

  if (allowSearch && !/^https?:\/\//i.test(trimmed) && !/^cage:\/\//i.test(trimmed)) {
    results.push({
      id: `search:${trimmed}`,
      kind: "search",
      title: trimmed,
      url: "https://www.google.com/search?q=" + encodeURIComponent(trimmed),
      displayURL: "",
      host: "",
      score: 1,
    });
  }

  return results.slice(0, limit);
}

export function findInlineCompletion(query, suggestions) {
  const raw = String(query || "");
  if (!raw || /\s/.test(raw) || raw.includes("?") || raw.includes("#")) return null;
  const schemeMatch = /^(https?:\/\/(?:www\.)?|www\.)/i.exec(raw);
  const prefix = schemeMatch ? schemeMatch[1] : "";
  const typed = raw.slice(prefix.length);
  if (!typed) return null;
  const typedLower = typed.toLowerCase();

  for (const item of suggestions || []) {
    if (!item || !["favorite", "history", "site"].includes(item.kind)) continue;
    const candidate = item.displayURL || item.host || "";
    if (!candidate) continue;
    const candidateLower = candidate.toLowerCase();
    if (candidateLower.startsWith(typedLower) && candidate.length > typed.length) {
      // Only inline-complete to host boundary unless user already typed a slash.
      let target = candidate;
      if (!typed.includes("/")) {
        const slash = candidate.indexOf("/");
        if (slash > 0) target = candidate.slice(0, slash);
        if (target.length <= typed.length) continue;
      }
      const suffix = target.slice(typed.length);
      return {
        text: raw + suffix,
        suffix,
        url: item.url,
      };
    }
  }
  return null;
}
