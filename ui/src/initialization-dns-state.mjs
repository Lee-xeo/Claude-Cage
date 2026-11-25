export function isInstallDNSFailure(message = "") {
  const text = String(message || "");
  if (!text) return false;
  return (
    text.includes("APT 无法解析下载服务器域名") ||
    text.includes("加密 DNS") ||
    /No address associated with hostname|Something wicked happened resolving|Could not resolve host|Temporary failure resolving|Guest DNS could not resolve/i.test(text)
  );
}

export function cleanFallbackLines(lines) {
  if (!Array.isArray(lines)) return [];
  const seen = new Set();
  const out = [];
  for (const raw of lines) {
    const item = String(raw || "").trim();
    if (item && !seen.has(item)) {
      seen.add(item);
      out.push(item);
    }
  }
  return out;
}

export function dnsHostSummary(raw = "") {
  const text = String(raw || "").trim();
  if (!text) return "";
  try {
    return new URL(text).host;
  } catch {
    return text.replace(/^https?:\/\//i, "").split(/[/?#]/)[0];
  }
}

export function dnsEndpointStatusText(endpoint, tr) {
  if (!endpoint) return "";
  if (endpoint.status === "ok") {
    const address = Array.isArray(endpoint.addresses) && endpoint.addresses.length > 0 ? endpoint.addresses[0] : "";
    return tr("可用 · {0}", address);
  }
  if (endpoint.status === "connection") {
    if (endpoint.detail === "tls") return tr("TLS 握手失败");
    if (endpoint.detail === "timeout") return tr("连接超时");
    return tr("连接失败");
  }
  if (endpoint.status === "response") {
    return tr("返回错误 · {0}", endpoint.detail || "ERROR");
  }
  if (endpoint.status === "no_address") {
    return tr("未返回可用地址");
  }
  return tr("连接失败");
}

export function dnsDraftEqual(a, b) {
  if (!a || !b) return true;
  if ((a.dns_url || "").trim() !== (b.dns_url || "").trim()) return false;
  if (Boolean(a.dns_public_fallback) !== Boolean(b.dns_public_fallback)) return false;
  const fa = cleanFallbackLines(a.dns_fallback_urls);
  const fb = cleanFallbackLines(b.dns_fallback_urls);
  return fa.length === fb.length && fa.every((value, i) => value === fb[i]);
}

export function buildSettingsPayload(saved, draft) {
  return {
    ...saved,
    dns_url: (draft.dns_url || "").trim(),
    dns_fallback_urls: cleanFallbackLines(draft.dns_fallback_urls),
    dns_public_fallback: Boolean(draft.dns_public_fallback),
  };
}
