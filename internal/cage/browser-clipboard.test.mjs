import test from "node:test";
import assert from "node:assert/strict";
import { BrowserClipboard } from "./browser-clipboard.mjs";

const settle = () => new Promise(resolve => setImmediate(resolve));
async function fixture(options = {}) {
  const calls = [], copies = [], worlds = { next: 3 };
  const observer = new BrowserClipboard(async (method, params, session) => {
    calls.push({ method, params, session });
    if (method === "Page.getFrameTree") return { frameTree: { frame: { id: "frame" } } };
    if (method === "Page.createIsolatedWorld") return { executionContextId: worlds.next };
    return { result: { value: true } };
  }, value => copies.push(value), { source: async () => "source", ...options });
  await observer.attach("tab-session", "tab");
  const state = observer.sessions.get("tab-session");
  // A committed document in a frame, whose isolated world gets the given id.
  const context = async (id, frame = "frame") => {
    worlds.next = id;
    observer.event({ sessionId: "tab-session", method: "Page.frameNavigated", params: { frame: { id: frame } } });
    await settle();
  };
  await observer.setContext("tab", "context-a");
  const gesture = (token = state.token, executionContextId = 3) => observer.event({ sessionId: "tab-session", method: "Runtime.bindingCalled",
    params: { name: state.binding, executionContextId, payload: JSON.stringify({ token, context: "context-a", epoch: 0 }) } });
  let serial = 0;
  const copied = (value = { text: "exact🙂" }, token = observer.clipboardSource.token, sessionId = "source") =>
    { const sequence = ++serial;
      for (const payload of [{ token, serial: sequence, begin: true }, { token, serial: sequence, ...value }])
        observer.event({ sessionId, method: "Runtime.bindingCalled", params: { name: observer.clipboardSource.name, payload: JSON.stringify(payload) } });
    };
  return { observer, state, calls, copies, context, gesture, copied };
}

test("clipboard input code and binding are restricted to an isolated world", async () => {
  const { state, calls } = await fixture();
  const injection = calls.find(call => call.method === "Page.addScriptToEvaluateOnNewDocument");
  assert.equal(injection.params.worldName, state.world);
  const bound = calls.find(call => call.method === "Runtime.addBinding" && call.session === "tab-session").params;
  assert.deepEqual(bound, { name: state.binding, executionContextId: 3 });
  assert.ok(!calls.some(call => call.method === "Runtime.enable" && call.session === "tab-session"));
  assert.ok(!injection.params.source.includes("defineProperty"));
  assert.ok(!calls.some(call => call.method === "Browser.grantPermissions"));
});

test("copies require both a private source and trusted input from a live isolated context", async () => {
  const f = await fixture();
  f.copied();
  await settle();
  assert.deepEqual(f.copies, []);
  f.gesture("wrong");
  f.gesture(f.state.token, 4);
  f.copied();
  await settle();
  assert.deepEqual(f.copies, []);
  f.gesture();
  f.copied({}, "wrong");
  f.copied({ text: "forged" }, f.observer.clipboardSource.token, "tab-session");
  f.copied({ text: "x".repeat(65537) });
  f.copied();
  await settle();
  assert.deepEqual(f.copies, [{ id: "tab", context: "context-a", text: "exact🙂" }]);
  await f.context(5);
  f.copied();
  await settle();
  assert.equal(f.copies.length, 1);
  f.gesture(f.state.token, 5);
  f.copied();
  await settle();
  assert.equal(f.copies.length, 2);
});

test("navigation or tab closure during source validation cancels the result", async () => {
  const f = await fixture();
  let release;
  f.observer.cdp = () => new Promise(resolve => { release = resolve; });
  f.gesture();
  f.copied();
  f.observer.detachTab("tab");
  release({ result: { value: true } });
  await settle();
  assert.deepEqual(f.copies, []);
  assert.equal(f.observer.copyContexts.size, 0);
  assert.equal(f.observer.sessions.size, 0);
});

test("native format changes require an enabled receiver", async () => {
  for (const includeChanges of [false, true]) {
    const f = await fixture({ includeChanges });
    f.gesture();
    f.copied({ changed: true });
    await settle();
    assert.deepEqual(f.copies, includeChanges ? [{ id: "tab", context: "context-a", changed: true }] : []);
  }
});

test("new frames inherit the host context and setup failures permit retry", async () => {
  const f = await fixture();
  const before = f.calls.length;
  await f.context(9, "child-frame");
  const later = f.calls.slice(before);
  assert.ok(later.some(call => call.method === "Runtime.addBinding" && call.params.executionContextId === 9));
  assert.ok(later.some(call => call.method === "Runtime.evaluate" && call.params.contextId === 9 && call.params.expression.includes('"context-a"')));
  const cdp = f.observer.cdp;
  f.observer.cdp = async () => { throw new Error("CDP unavailable"); };
  await assert.rejects(f.observer.attach("child", "tab"), /CDP unavailable/);
  assert.equal(f.observer.sessions.has("child"), false);
  f.observer.cdp = cdp;
  await f.observer.attach("child", "tab");
  f.observer.sessions.get("child").contexts.set(10, "child-frame");
  await f.observer.setContext("tab", "context-b");
  assert.ok(f.calls.some(call => call.session === "child" && call.params.contextId === 10 && call.params.expression.includes('"context-b"')));
});

test("a document replaced during installation never becomes a live context", async () => {
  const f = await fixture();
  const cdp = f.observer.cdp;
  let release;
  f.observer.cdp = (method, params, session) => method === "Page.createIsolatedWorld" && !release
    ? new Promise(resolve => { release = () => resolve({ executionContextId: 7 }); }) : cdp(method, params, session);
  f.observer.event({ sessionId: "tab-session", method: "Page.frameNavigated", params: { frame: { id: "frame" } } });
  await settle();
  await f.context(8);
  release();
  await settle();
  assert.deepEqual([...f.state.contexts.keys()], [8]);
});

test("unavailable native notifications leave ordinary input usable", async () => {
  const observer = new BrowserClipboard(async () => ({}), () => {});
  await observer.attach("session", "tab");
  await observer.setContext("tab", "context");
  assert.equal(observer.available, false);
  assert.equal(observer.copyContexts.get("tab"), "context");
});
