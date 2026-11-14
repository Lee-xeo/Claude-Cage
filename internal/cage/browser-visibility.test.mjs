import assert from "node:assert/strict";
import { test } from "node:test";
import { BrowserVisibility, rememberOpener, targetAfterClose } from "./browser-visibility.mjs";

test("closing a popup returns to its opener rather than the oldest tab", () => {
  const unrelated = { targetId: "unrelated" }, opener = { targetId: "login" };
  const targets = new Map([[unrelated.targetId, unrelated], [opener.targetId, opener]]);
  assert.equal(targetAfterClose(targets, { targetId: "popup", openerId: "login" }), "login");
});

test("navigation and target refresh preserve the popup return target", () => {
  const popup = { targetId: "popup", openerId: "login", url: "about:blank" };
  const navigated = rememberOpener({ targetId: "popup", url: "https://example.org/" }, popup);
  const refreshed = rememberOpener({ targetId: "popup", url: "https://example.org/" }, navigated);
  assert.equal(refreshed.openerId, "login");
  assert.equal(refreshed.url, "https://example.org/");
});

test("new opener metadata takes precedence", () => {
  const next = { targetId: "popup", openerId: "new" };
  assert.equal(rememberOpener(next, { targetId: "popup", openerId: "old" }), next);
});

test("an opener is never copied from another target or browser context", () => {
  const previous = { targetId: "popup", browserContextId: "one", openerId: "login" };
  for (const next of [{ targetId: "other", browserContextId: "one" },
                      { targetId: "popup", browserContextId: "two" }]) {
    assert.equal(rememberOpener(next, previous).openerId, undefined);
  }
});

test("closed openers retain the existing first-live-tab fallback", () => {
  const targets = new Map([["unrelated", { targetId: "unrelated" }]]);
  assert.equal(targetAfterClose(targets, { targetId: "popup", openerId: "closed" }), "unrelated");
});

test("ordinary and final tab closures preserve their existing fallback", () => {
  assert.equal(targetAfterClose(new Map([["one", { targetId: "one" }]]), { targetId: "closed" }), "one");
  assert.equal(targetAfterClose(new Map(), { targetId: "closed" }), "");
});

test("a return target in another context is not treated as an opener", () => {
  const targets = new Map([["one", { targetId: "one", browserContextId: "private" }],
                           ["login", { targetId: "login" }]]);
  assert.equal(targetAfterClose(targets, { targetId: "popup", openerId: "login", browserContextId: "private" }), "one");
});

function fixture() {
  const windows = new Map([[1, { windowState: "fullscreen", left: 0, top: 0, width: 1200, height: 800 }]]);
  const targets = new Map([["one", 1], ["two", 1]]);
  const calls = [];
  let legacyViewer = false;
  const cdp = async (method, input) => {
    calls.push({ method, ...input });
    if (method === "Browser.getWindowForTarget") {
      const windowId = targets.get(input.targetId);
      return { windowId, bounds: { ...windows.get(windowId) } };
    }
    if (method === "Browser.getWindowBounds") return { bounds: { ...windows.get(input.windowId) } };
    const current = windows.get(input.windowId);
    assert.ok(current);
    if (input.bounds.windowState === "minimized") assert.equal(current.windowState, "normal");
    Object.assign(current, input.bounds);
  };
  const visibility = new BrowserVisibility(cdp, () => [...targets.keys()], async () => !legacyViewer);
  return { visibility, windows, targets, calls, setLegacyViewer: value => { legacyViewer = value; } };
}

test("browser visibility restores fullscreen and processes shared windows once", async () => {
  const f = fixture();
  await f.visibility.set(false);
  assert.equal(f.windows.get(1).windowState, "minimized");
  assert.equal(f.calls.filter(call => call.method === "Browser.setWindowBounds").length, 2);
  await f.visibility.set(false);
  assert.equal(f.calls.filter(call => call.method === "Browser.setWindowBounds").length, 2);
  await f.visibility.set(true);
  assert.equal(f.windows.get(1).windowState, "fullscreen");
  assert.equal(f.visibility.saved.size, 0);
});

test("browser visibility preserves geometry, closed windows and legacy viewers", async () => {
  const f = fixture();
  const bounds = { windowState: "normal", left: 40, top: 80, width: 900, height: 600 };
  f.windows.set(2, { ...bounds }); f.targets.set("popup", 2);
  f.setLegacyViewer(true);
  await f.visibility.set(false);
  assert.equal(f.calls.filter(call => call.method === "Browser.setWindowBounds").length, 0);
  f.setLegacyViewer(false);
  await f.visibility.set(false);
  f.targets.delete("one"); f.targets.delete("two"); f.windows.delete(1);
  await f.visibility.set(true);
  assert.deepEqual(f.windows.get(2), bounds);
  assert.equal(f.visibility.saved.size, 0);
});

test("a newer browser viewer restores a hide that is already running", async () => {
  let release, entered;
  const blocked = new Promise(resolve => { entered = resolve; });
  const ready = new Promise(resolve => { release = resolve; });
  let state = "fullscreen";
  const visibility = new BrowserVisibility(async (method, input) => {
    if (method === "Target.activateTarget") return {};
    if (method === "Browser.getWindowForTarget") return { windowId: 1, bounds: { windowState: state } };
    if (method === "Browser.getWindowBounds") return { bounds: { windowState: state } };
    if (input.bounds.windowState === "minimized") { entered(); await ready; }
    state = input.bounds.windowState;
  }, () => ["one"]);
  const hidden = visibility.set(false);
  await blocked;
  const activated = visibility.activate("one");
  const shown = visibility.set(true);
  release();
  await Promise.all([hidden, activated, shown]);
  assert.equal(state, "fullscreen");
  assert.equal(visibility.saved.size, 0);
});
