import test from "node:test";
import assert from "node:assert/strict";
import { createPluginViewHost, matchesUrlPattern, ERRORS, CONCURRENCY_LIMIT } from "./plugin-view-host.mjs";

test("url patterns name the scheme and host outright and allow a port or path wildcard", () => {
  const login = "https://login.tailscale.com/a/*";
  assert.equal(matchesUrlPattern("https://login.tailscale.com/a/abc123", login), true);
  assert.equal(matchesUrlPattern("https://login.tailscale.com/a/", login), true);
  assert.equal(matchesUrlPattern("https://login.tailscale.com/b/abc", login), false);
  assert.equal(matchesUrlPattern("http://login.tailscale.com/a/abc", login), false);
  assert.equal(matchesUrlPattern("https://evil.example/login.tailscale.com/a/x", login), false);
  assert.equal(matchesUrlPattern("https://login.tailscale.com.evil.example/a/x", login), false);
  assert.equal(matchesUrlPattern("https://user:pass@login.tailscale.com/a/x", login), false);
  assert.equal(matchesUrlPattern("https://login.tailscale.com:8443/a/x", login), false);
  assert.equal(matchesUrlPattern("https://login.tailscale.com:443/a/x", login), true);
  const local = "http://127.0.0.1:*/*";
  assert.equal(matchesUrlPattern("http://127.0.0.1:43123/app?x=1", local), true);
  assert.equal(matchesUrlPattern("http://localhost:43123/", local), false);
  assert.equal(matchesUrlPattern("https://example.com/exact", "https://example.com/exact"), true);
  assert.equal(matchesUrlPattern("https://example.com/exact/more", "https://example.com/exact"), false);
  assert.equal(matchesUrlPattern("javascript:alert(1)", "https://example.com/*"), false);
  assert.equal(matchesUrlPattern("not a url", "https://example.com/*"), false);
  assert.equal(matchesUrlPattern("https://example.com/x", "https://*.example.com/*"), false);
});

function host(overrides = {}) {
  const calls = [], opened = [], copied = [], notes = [], saved = [];
  const value = createPluginViewHost({
    plugin: "tailscale", view: "tailscale",
    openUrls: ["https://login.tailscale.com/a/*"],
    call: async request => { calls.push(request); return request.method === "tools/list" ? { tools: [] } : { content: [{ type: "text", text: "ok" }] }; },
    confirm: async () => true,
    openUrl: async url => { opened.push(url); return true; },
    copy: async text => { copied.push(text); },
    notify: (message, tone) => notes.push([message, tone]),
    loadState: () => ({ query: "laptop" }),
    saveState: state => saved.push(state),
    theme: () => ({ scheme: "dark", tokens: { paper: "#111" } }),
    ...overrides,
  });
  return { ...value, calls, opened, copied, notes, saved };
}
const request = (id, method, params) => ({ jsonrpc: "2.0", id, method, ...(params === undefined ? {} : { params }) });

test("a page must initialize first and then gets its plugin, theme and saved state", async () => {
  const page = host();
  const early = await page.handle(request(1, "tools/list"));
  assert.equal(early.error.code, ERRORS.notAllowed);
  const wrong = await page.handle(request(2, "ui/initialize", { protocolVersion: 9 }));
  assert.equal(wrong.error.code, ERRORS.invalidParams);
  const ready = await page.handle(request(3, "ui/initialize", { protocolVersion: 1 }));
  assert.deepEqual(ready, { jsonrpc: "2.0", id: 3, result: {
    protocolVersion: 1, plugin: "tailscale", view: "tailscale", presentation: "page", theme: { scheme: "dark", tokens: { paper: "#111" } }, locale: "zh-CN", state: { query: "laptop" },
  } });
});

test("floating views expose presentation and clamp ui/resize dimensions", async () => {
  const sizes = [];
  const floating = host({ presentation: "floating", resize: next => sizes.push(next) });
  const ready = await floating.handle(request(1, "ui/initialize", { protocolVersion: 1 }));
  assert.equal(ready.result.presentation, "floating");
  assert.equal((await floating.handle(request(2, "ui/resize", {}))).error.code, ERRORS.invalidParams);
  assert.equal((await floating.handle(request(3, "ui/resize", { height: -10 }))).error.code, ERRORS.invalidParams);
  assert.deepEqual((await floating.handle(request(4, "ui/resize", { width: 100, height: 999 }))).result, { width: 220, height: 720 });
  assert.deepEqual((await floating.handle(request(5, "ui/resize", { height: 112.4 }))).result, { height: 112 });
  assert.deepEqual(sizes, [{ width: 220, height: 720 }, { height: 112 }]);
});

test("tool calls go to the app with the page's arguments; pages without a server get none", async () => {
  const page = host();
  await page.handle(request(1, "ui/initialize", { protocolVersion: 1 }));
  const result = await page.handle(request(2, "tools/call", { name: "status", arguments: { verbose: true } }));
  assert.deepEqual(result.result, { content: [{ type: "text", text: "ok" }] });
  assert.deepEqual(page.calls, [{ method: "tools/call", name: "status", arguments: { verbose: true } }]);
  const bad = await page.handle(request(3, "tools/call", { name: "status", arguments: [1] }));
  assert.equal(bad.error.code, ERRORS.invalidParams);
  const denied = host({ call: async () => { const error = new Error("用户拒绝了"); error.code = ERRORS.denied; throw error; } });
  await denied.handle(request(1, "ui/initialize", { protocolVersion: 1 }));
  assert.equal((await denied.handle(request(2, "tools/call", { name: "status" }))).error.code, ERRORS.denied);
  const bare = host({ hasServer: false });
  await bare.handle(request(1, "ui/initialize", { protocolVersion: 1 }));
  assert.equal((await bare.handle(request(2, "tools/list"))).error.code, ERRORS.unavailable);
});

test("only manifest urls open, and state, copy and notices are bounded", async () => {
  const page = host();
  await page.handle(request(1, "ui/initialize", { protocolVersion: 1 }));
  assert.deepEqual((await page.handle(request(2, "ui/openUrl", { url: "https://login.tailscale.com/a/x" }))).result, {});
  assert.equal((await page.handle(request(3, "ui/openUrl", { url: "https://example.com/" }))).error.code, ERRORS.notAllowed);
  assert.deepEqual(page.opened, ["https://login.tailscale.com/a/x"]);
  assert.equal((await page.handle(request(4, "ui/setState", { state: "x".repeat(70 << 10) }))).error.code, ERRORS.notAllowed);
  assert.deepEqual((await page.handle(request(5, "ui/setState", { state: { tab: 2 } }))).result, {});
  assert.deepEqual(page.saved, [{ tab: 2 }]);
  assert.equal((await page.handle(request(6, "ui/copy", { text: "x".repeat(65 << 10) }))).error.code, ERRORS.invalidParams);
  assert.deepEqual((await page.handle(request(7, "ui/confirm", { title: "断开？", message: "会关闭连接" }))).result, { confirmed: true });
  assert.equal((await page.handle(request(8, "ui/notify", { message: "好了", tone: "loud" }))).error.code, ERRORS.invalidParams);
});

test("malformed messages, notifications, unknown methods and floods are answered safely", async () => {
  const page = host();
  assert.equal(await page.handle("hello"), null);
  assert.equal(await page.handle({ jsonrpc: "2.0", method: "ui/initialize" }), null);
  assert.equal((await page.handle({ jsonrpc: "1.0", id: 1, method: "x" })).error.code, ERRORS.invalidRequest);
  await page.handle(request(1, "ui/initialize", { protocolVersion: 1 }));
  assert.equal((await page.handle(request(2, "ui/nothing"))).error.code, ERRORS.methodNotFound);
  assert.equal((await page.handle(request(3, "ui/copy", { text: "x".repeat((1 << 20) + 1) }))).error.code, ERRORS.notAllowed);
  const releases = [];
  const slow = host({ call: () => new Promise(resolve => { releases.push(resolve); }) });
  await slow.handle(request(1, "ui/initialize", { protocolVersion: 1 }));
  const pending = Array.from({ length: CONCURRENCY_LIMIT }, (_, i) => slow.handle(request(10 + i, "tools/list")));
  assert.equal((await slow.handle(request(99, "tools/list"))).error.code, ERRORS.notAllowed);
  for (const release of releases) release({ tools: [] });
  await Promise.all(pending);
  // Once they finish, the page can ask again.
  const again = slow.handle(request(100, "tools/list"));
  releases.at(-1)({ tools: [] });
  assert.deepEqual((await again).result, { tools: [] });
});
