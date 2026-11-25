import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { formatMessage } from "./message-catalog.mjs";
import {
  buildSettingsPayload,
  cleanFallbackLines,
  dnsDraftEqual,
  dnsEndpointStatusText,
  dnsHostSummary,
  isInstallDNSFailure,
} from "./initialization-dns-state.mjs";

test("isInstallDNSFailure detects APT and guest DNS resolution errors without matching unrelated failures", () => {
  assert.equal(isInstallDNSFailure("APT 无法解析下载服务器域名（deb.debian.org）。请核对加密 DNS 设置。"), true);
  assert.equal(isInstallDNSFailure("Err:1 http://deb.debian.org/debian bookworm InRelease\n  No address associated with hostname"), true);
  assert.equal(isInstallDNSFailure("Something wicked happened resolving 'deb.debian.org:http' (-5)"), true);
  assert.equal(isInstallDNSFailure("Guest DNS could not resolve deb.debian.org"), true);
  assert.equal(isInstallDNSFailure("Temporary failure resolving 'security.debian.org'"), true);
  assert.equal(isInstallDNSFailure("请先选择并应用代理节点。"), false);
  assert.equal(isInstallDNSFailure("磁盘空间不足"), false);
  assert.equal(isInstallDNSFailure(""), false);
});

test("cleanFallbackLines trims, skips blanks, and preserves first-seen order", () => {
  assert.deepEqual(
    cleanFallbackLines(["  https://dns.quad9.net/dns-query ", "", "   ", "https://dns.quad9.net/dns-query", "https://dns.adguard-dns.com/dns-query"]),
    ["https://dns.quad9.net/dns-query", "https://dns.adguard-dns.com/dns-query"],
  );
  assert.deepEqual(cleanFallbackLines(null), []);
});

test("dnsHostSummary returns only host without URL path or query parameters", () => {
  assert.equal(dnsHostSummary("https://dns.nextdns.io/secret123?token=abc"), "dns.nextdns.io");
  assert.equal(dnsHostSummary("https://1.1.1.1/dns-query"), "1.1.1.1");
  assert.equal(dnsHostSummary("dns.quad9.net/dns-query"), "dns.quad9.net");
  assert.equal(dnsHostSummary(""), "");
});

test("dnsDraftEqual and buildSettingsPayload preserve non-DNS settings and normalize DNS fields", () => {
  const saved = {
    dns_url: "https://1.1.1.1/dns-query",
    dns_fallback_urls: ["https://dns.quad9.net/dns-query"],
    dns_public_fallback: false,
    keyboard: "mac",
    timezone: "UTC",
  };
  assert.equal(dnsDraftEqual(saved, {
    dns_url: "  https://1.1.1.1/dns-query ",
    dns_fallback_urls: ["https://dns.quad9.net/dns-query", ""],
    dns_public_fallback: false,
  }), true);
  assert.equal(dnsDraftEqual(saved, {
    dns_url: "https://dns.quad9.net/dns-query",
    dns_fallback_urls: ["https://dns.quad9.net/dns-query"],
    dns_public_fallback: false,
  }), false);

  const payload = buildSettingsPayload(saved, {
    dns_url: " https://dns.quad9.net/dns-query ",
    dns_fallback_urls: [" https://dns.adguard-dns.com/dns-query ", "", "https://dns.adguard-dns.com/dns-query"],
    dns_public_fallback: true,
  });
  assert.deepEqual(payload, {
    dns_url: "https://dns.quad9.net/dns-query",
    dns_fallback_urls: ["https://dns.adguard-dns.com/dns-query"],
    dns_public_fallback: true,
    keyboard: "mac",
    timezone: "UTC",
  });
});

test("dnsEndpointStatusText formats every status and localizes cleanly across all 14 non-zh-CN locales", () => {
  const identity = (source, ...values) => formatMessage(source, ...values);
  assert.equal(dnsEndpointStatusText({ status: "ok", addresses: ["151.101.2.132"] }, identity), "可用 · 151.101.2.132");
  assert.equal(dnsEndpointStatusText({ status: "connection", detail: "tls" }, identity), "TLS 握手失败");
  assert.equal(dnsEndpointStatusText({ status: "connection", detail: "timeout" }, identity), "连接超时");
  assert.equal(dnsEndpointStatusText({ status: "connection", detail: "connection" }, identity), "连接失败");
  assert.equal(dnsEndpointStatusText({ status: "response", detail: "REFUSED" }, identity), "返回错误 · REFUSED");
  assert.equal(dnsEndpointStatusText({ status: "no_address" }, identity), "未返回可用地址");

  const languages = JSON.parse(readFileSync(new URL("../../internal/uilocale/languages.json", import.meta.url), "utf8")).slice(1);
  const samples = [
    { status: "ok", addresses: ["151.101.2.132"] },
    { status: "connection", detail: "tls" },
    { status: "connection", detail: "timeout" },
    { status: "connection", detail: "connection" },
    { status: "response", detail: "HTTP 503" },
    { status: "no_address" },
  ];
  for (const { id } of languages) {
    const catalog = JSON.parse(readFileSync(new URL(`./locales/${id}.json`, import.meta.url), "utf8"));
    const tr = (source, ...values) => formatMessage(Object.hasOwn(catalog, source) ? catalog[source] : source, ...values);
    for (const sample of samples) {
      const rendered = dnsEndpointStatusText(sample, tr);
      assert.ok(rendered.trim(), `${id}: empty status text for ${JSON.stringify(sample)}`);
      if (id !== "zh-TW") {
        assert.doesNotMatch(rendered, /\p{Script=Han}/u, `${id}: Han leak in "${rendered}"`);
      }
    }
  }
});
