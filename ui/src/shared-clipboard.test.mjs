import test from "node:test";
import assert from "node:assert/strict";
import { clipboardReconnectDelay, copyGuestText, shareClipboard } from "./shared-clipboard.mjs";

const settle = () => new Promise(resolve => setImmediate(resolve));

test("web copy reads a fresh GUI revision only after its keys are acknowledged", async () => {
  const events = [];
  const value = await copyGuestText({
    request: async request => {
      events.push(request.phase);
      if (request.phase === "prepare") return { revision: 4, session: "gui-a" };
      assert.equal(request.revision, 4);
      return { revision: 5, session: "gui-a", text: "newly copied text" };
    },
    sendCopy: async () => { events.push("keys-ack"); },
    valid: () => true,
  });
  assert.deepEqual(events, ["prepare", "keys-ack", "read"]);
  assert.deepEqual(value, { text: "newly copied text" });
});

test("web copy rejects an unchanged clipboard and a switched GUI", async () => {
  for (const result of [{ revision: 4, session: "gui-a", text: "old" }, { revision: 5, session: "gui-b", text: "other session" }]) {
    await assert.rejects(copyGuestText({
      request: async request => request.phase === "prepare" ? { revision: 4, session: "gui-a" } : result,
      sendCopy: async () => {}, valid: () => true,
    }));
  }
});

test("web copy stops before key injection after its target changes", async () => {
  let sent = false;
  await assert.rejects(copyGuestText({
    request: async () => ({ revision: 4, session: "gui-a" }),
    sendCopy: async () => { sent = true; }, valid: () => false,
  }));
  assert.equal(sent, false);
});
function fixture({ available = true, deferFiles = false, initial = { text: "host initial" } } = {}) {
  const sent = [], writes = [], active = [], notices = [], released = [], reads = [];
  let revision = 1, content = initial, pauseRead, pauseWrite, nativeGeneration = 0, delayAcks = false;
  const pendingAcks = [];
  const socket = { readyState: 1, send: text => {
    const value = JSON.parse(text);
    sent.push(Object.fromEntries(Object.entries(value).filter(([key]) => key !== "serial")));
    if (delayAcks) pendingAcks.push(value.serial);
    else queueMicrotask(() => socket.onmessage({ data: JSON.stringify({ ack: value.serial }) }));
  }, close() { this.readyState = 3; this.onclose?.(); } };
  const controller = shareClipboard({
    connect: () => socket, interval: 60000, onStatus: text => notices.push(text), setActive: value => active.push(value),
    native: async (action, value) => {
      if (action === "clipboardRevision") return revision;
      if (action === "clipboardSyncRead") {
        const deferred = deferFiles && value?.defer_files === true && typeof content.files === "string";
        reads.push(value?.defer_files === true ? "deferred" : "read");
        const snapshot = deferred ? { revision, files_pending: true, count: 1 } : { ...content, revision };
        if (pauseRead) await pauseRead;
        return snapshot;
      }
      if (action === "clipboardSyncCancel") { nativeGeneration++; return; }
      if (action === "clipboardSyncRelease") { released.push(value.files); return; }
      if (action === "clipboardSyncWrite") {
        const generation = nativeGeneration;
        if (pauseWrite) await pauseWrite;
        if (generation !== nativeGeneration || value.revision !== revision) return { superseded: true, revision };
        content = { ...value }; delete content.revision;
        writes.push(content); return { revision: ++revision };
      }
    },
  });
  socket.onopen();
  socket.onmessage({ data: JSON.stringify({ hello: { version: 1, os: "windows", session: "fixture-session", formats: ["text", "png", "files"], available } }) });
  return { socket, controller, sent, writes, active, notices, released, reads, cancels: () => nativeGeneration,
    host: value => { content = value; revision++; },
    guest: value => socket.onmessage({ data: JSON.stringify(value) }),
    readDelay: value => { pauseRead = value; }, writeDelay: value => { pauseWrite = value; },
    delayAcks: () => { delayAcks = true; },
    acknowledge: () => {
      assert.ok(pendingAcks.length, "a guest acknowledgement is pending");
      socket.onmessage({ data: JSON.stringify({ ack: pendingAcks.shift() }) });
    },
  };
}

test("explicit sync rereads a superseded host snapshot and waits for the latest guest acknowledgement", async () => {
  const f = fixture();
  try {
    await settle();
    let release;
    f.host({ files: "slow host files" });
    f.readDelay(new Promise(resolve => { release = resolve; }));
    const background = f.controller.poll();
    await settle();
    f.host({ text: "latest host copy" });
    f.delayAcks();
    let finished = false;
    const sync = f.controller.sync().then(() => { finished = true; });
    release();
    await background; await settle();
    assert.deepEqual(f.sent.at(-1), { text: "latest host copy" });
    assert.deepEqual(f.released, ["slow host files"]);
    assert.equal(finished, false);
    f.acknowledge();
    await sync;
    assert.equal(finished, true);
  } finally { f.controller.dispose(); }
});

test("guest file references stay alive until the last queued consumer finishes", async () => {
  const f = fixture();
  try {
    await settle();
    let release;
    f.writeDelay(new Promise(resolve => { release = resolve; }));
    f.guest({ files: "shared-selection" });
    await settle();
    f.guest({ files: "shared-selection" });
    await settle();
    assert.deepEqual(f.released, []);
    release();
    await settle(); await settle();
    assert.deepEqual(f.released, ["shared-selection"]);
    assert.deepEqual(f.writes, [{ files: "shared-selection" }]);
  } finally { f.controller.dispose(); }
});

test("explicit sync follows a new host revision that arrives while its acknowledgement is pending", async () => {
  const f = fixture();
  try {
    await settle();
    f.delayAcks();
    f.host({ text: "first copy" });
    let finished = false;
    const sync = f.controller.sync().then(() => { finished = true; });
    await settle();
    f.host({ text: "second copy" });
    f.acknowledge();
    await settle();
    assert.deepEqual(f.sent.at(-1), { text: "second copy" });
    assert.equal(finished, false);
    f.acknowledge();
    await sync;
  } finally { f.controller.dispose(); }
});

for (const transition of ["lock", "switch", "disconnect"]) {
  test(`explicit sync fails when the guest ${transition} occurs before acknowledgement`, async () => {
    const f = fixture();
    try {
      await settle();
      f.delayAcks();
      f.host({ text: "unconfirmed host copy" });
      const rejected = assert.rejects(f.controller.sync());
      await settle();
      if (transition === "disconnect") f.socket.close();
      else f.guest({ hello: { version: 1, os: "windows", session: transition === "lock" ? "fixture-session" : "next-session",
        formats: ["text", "png", "files"], available: transition !== "lock" } });
      f.acknowledge();
      await rejected;
    } finally { f.controller.dispose(); }
  });
}

test("disconnect rejects explicit sync before a slow native read finishes", async () => {
  const f = fixture();
  let release;
  try {
    await settle();
    f.host({ files: "slow host files" });
    f.readDelay(new Promise(resolve => { release = resolve; }));
    const rejected = assert.rejects(f.controller.sync());
    await settle();
    f.socket.close();
    await Promise.race([rejected, new Promise((_, reject) => setTimeout(() => reject(new Error("sync waited for an invalidated native read")), 100))]);
  } finally { release?.(); f.controller.dispose(); }
});

test("explicit sync rejects an unavailable guest", async () => {
  const f = fixture({ available: false });
  try { await assert.rejects(f.controller.sync()); }
  finally { f.controller.dispose(); }
});

test("a guest write error rejects its pending sync", async () => {
  const f = fixture();
  try {
    await settle();
    f.delayAcks();
    f.host({ text: "unconfirmed copy" });
    const rejected = assert.rejects(f.controller.sync());
    await settle();
    f.guest({ error: "clipboard_write_failed" });
    await rejected;
    assert.equal(f.active.at(-1), false);
  } finally { f.controller.dispose(); }
});

test("a guest without an unlocked GUI cannot activate clipboard sharing", async () => {
  const f = fixture({ available: false });
  try {
    await settle(); await f.controller.poll();
    assert.deepEqual(f.sent, []);
    assert.equal(f.active.at(-1), false);
    f.guest({ hello: { version: 1, os: "windows", session: "unlocked-session", formats: ["text", "png", "files"], available: true } });
    await settle();
    assert.deepEqual(f.sent, [{ text: "host initial" }]);
    assert.equal(f.active.at(-1), true);
  } finally { f.controller.dispose(); }
});

test("locking a guest invalidates an in-flight host clipboard snapshot", async () => {
  const f = fixture();
  try {
    await settle();
    let release;
    f.host({ text: "copy made before guest lock" });
    f.readDelay(new Promise(resolve => { release = resolve; }));
    const pending = f.controller.poll(); await settle();
    f.guest({ hello: { version: 1, os: "windows", session: "locked", formats: ["text", "png", "files"], available: false } });
    release(); await pending; await settle();
    assert.equal(f.sent.some(value => value.text === "copy made before guest lock"), false);
    assert.equal(f.active.at(-1), false);
  } finally { f.controller.dispose(); }
});

test("a restarted GUI session seeds the current host selection again", async () => {
  const f=fixture();
  try {
    await settle();
    f.guest({hello:{version:1,os:"windows",session:"restarted-gui",formats:["text","png","files"],available:true}});
    await settle();
    assert.deepEqual(f.sent,[{text:"host initial"},{text:"host initial"}]);
    assert.equal(f.active.at(-1),true);
  }finally{f.controller.dispose();}
});

for (const transition of ["lock", "switch", "disconnect"]) {
  test(`a pending native file write cannot update the host after guest ${transition}`, async () => {
    const f = fixture();
    try {
      await settle();
      let release;
      f.writeDelay(new Promise(resolve => { release = resolve; }));
      f.guest({ files: "old-session-selection" });
      await settle();
      if (transition === "disconnect") f.socket.close();
      else f.guest({ hello: { version: 1, os: "windows", session: transition === "lock" ? "fixture-session" : "next-session",
        formats: ["text", "png", "files"], available: transition !== "lock" } });
      release();
      await settle();
      assert.deepEqual(f.writes, []);
      assert.deepEqual(f.notices, []);
    } finally { f.controller.dispose(); }
  });
}

test("text, images and file selections synchronize both ways and do not echo", async () => {
  const f = fixture();
  try {
    await settle();
    assert.deepEqual(f.sent, [{ text: "host initial" }]);
    for (const content of [{ text: "中文🙂\nsecond line" }, { png: "fixture-png" }, { files: "fixture-file-selection" }]) {
      f.guest(content); await settle();
      assert.deepEqual(f.writes.at(-1), content);
      const sent = f.sent.length;
      await f.controller.poll(); assert.equal(f.sent.length, sent, "a native write is not sent back");
      const writes = f.writes.length;
      f.guest(content); await settle(); assert.equal(f.writes.length, writes, "a duplicate guest event is ignored");
      f.host({ ...content, ...(content.text ? { text: "new host text" } : content.png ? { png: "new host PNG" } : { files: "new host files" }) });
      await f.controller.poll();
      assert.equal(f.sent.length, sent + 1);
    }
  } finally { f.controller.dispose(); }
});

test("a local copy made before a delayed guest write wins", async () => {
  const f = fixture();
  try {
    await settle();
    let release;
    f.writeDelay(new Promise(resolve => { release = resolve; }));
    f.guest({ text: "old guest copy" }); await settle();
    f.host({ text: "new local copy" }); release(); await settle(); await settle();
    assert.deepEqual(f.writes, []);
    assert.deepEqual(f.sent.at(-1), { text: "new local copy" });
  } finally { f.controller.dispose(); }
});

test("a guest copy invalidates a host snapshot that was already being read", async () => {
  const f = fixture();
  try {
    await settle();
    f.host({ files: "slow host upload" });
    let release;
    f.readDelay(new Promise(resolve => { release = resolve; }));
    const poll = f.controller.poll(); await settle();
    f.guest({ text: "new guest copy" }); await settle(); release(); await poll; await settle();
    assert.equal(f.sent.some(value => value.files === "slow host upload"), false);
  } finally { f.controller.dispose(); }
});

test("disposed connections cannot send snapshots or receive delayed writes", async () => {
  const f = fixture(); await settle();
  f.controller.dispose();
  f.guest({ text: "late guest update" }); await f.controller.poll(); await settle();
  assert.deepEqual(f.writes, []);
  assert.deepEqual(f.active.at(-1), false);
});

test("a failing guest clipboard reconnects with backoff that resets after a successful sync", async t => {
  const delays = [], pending = [];
  let fail = true, revision = 1;
  const hello = { hello: { version: 1, os: "darwin", session: "gui-a", available: true, formats: ["text", "png", "files"] } };
  const connect = () => {
    const socket = { readyState: 1, send: text => {
      const value = JSON.parse(text);
      queueMicrotask(() => socket.onmessage({ data: JSON.stringify({ ack: value.serial }) }));
    }, close() { this.readyState = 3; this.onclose?.(); } };
    queueMicrotask(() => { socket.onopen?.(); socket.onmessage({ data: JSON.stringify(hello) }); });
    return socket;
  };
  const controller = shareClipboard({
    connect, interval: 60000, onStatus: () => {}, setActive: () => {},
    schedule: (callback, delay) => { delays.push(delay); pending.push(callback); return 0; },
    native: async action => {
      if (action === "clipboardRevision") return revision;
      if (action === "clipboardSyncRead") {
        if (fail) throw new Error("guest clipboard unavailable");
        return { text: "host", revision };
      }
    },
  });
  t.after(() => controller.dispose());
  for (let attempt = 0; attempt < 6; attempt++) {
    await settle(); await settle();
    pending.shift()?.();
  }
  assert.deepEqual(delays, [1500, 3000, 6000, 12000, 24000, 30000]);
  fail = false;
  await settle(); await settle();
  assert.equal(pending.length, 0);
  fail = true;
  revision = 2;
  controller.poll().catch(() => {});
  await settle(); await settle();
  assert.equal(delays.at(-1), 1500);
  assert.equal(clipboardReconnectDelay(0), 1500);
  assert.equal(clipboardReconnectDelay(100), 30000);
});

test("hidden window pauses host clipboard polling but keeps guest push and syncs immediately when shown", async t => {
  let revision = 1, content = { text: "initial" }, revisionReads = 0;
  const sent = [], writes = [];
  const socket = {
    readyState: 1,
    send: text => {
      const value = JSON.parse(text);
      sent.push(Object.fromEntries(Object.entries(value).filter(([k]) => k !== "serial")));
      queueMicrotask(() => socket.onmessage({ data: JSON.stringify({ ack: value.serial }) }));
    },
    close() { this.readyState = 3; this.onclose?.(); },
  };
  const controller = shareClipboard({
    connect: () => socket,
    interval: 20,
    isVisible: () => false,
    onStatus: () => {},
    setActive: () => {},
    native: async (action, value) => {
      if (action === "clipboardRevision") { revisionReads++; return revision; }
      if (action === "clipboardSyncRead") return { ...content, revision };
      if (action === "clipboardSyncWrite") {
        content = { ...value }; delete content.revision;
        writes.push(content);
        return { revision: ++revision };
      }
    },
  });
  t.after(() => controller.dispose());
  socket.onopen();
  socket.onmessage({ data: JSON.stringify({ hello: { version: 1, os: "linux", session: "s1", formats: ["text", "png", "files"], available: true } }) });
  await settle(); await settle();
  const afterHelloReads = revisionReads;
  await new Promise(resolve => setTimeout(resolve, 80));
  assert.equal(revisionReads, afterHelloReads, "polling must stay paused while window is hidden");
  // Guest -> host push still works while hidden.
  socket.onmessage({ data: JSON.stringify({ text: "from guest while hidden" }) });
  await settle(); await settle();
  assert.deepEqual(writes, [{ text: "from guest while hidden" }]);
  // Host copy while hidden syncs immediately when window becomes visible.
  content = { text: "host copied while hidden" };
  revision++;
  controller.setVisible(true);
  await settle(); await settle();
  assert.deepEqual(sent.at(-1), { text: "host copied while hidden" });
  // Hiding again stops polling again.
  controller.setVisible(false);
  const afterHideReads = revisionReads;
  await new Promise(resolve => setTimeout(resolve, 80));
  assert.equal(revisionReads, afterHideReads, "polling must stop again after hiding");
});

test("host files wait for an explicit paste instead of being read by background polling or a new GUI session", async () => {
  const f = fixture({ deferFiles: true, initial: { files: "host selection" } });
  try {
    await settle();
    assert.deepEqual(f.sent, [{ text: "" }]);
    assert.equal(f.active.at(-1), true);
    await f.controller.poll(); await f.controller.poll();
    assert.deepEqual(f.reads, ["deferred"]);
    f.guest({ hello: { version: 1, os: "windows", session: "restarted-gui", formats: ["text", "png", "files"], available: true } });
    await settle();
    assert.deepEqual(f.reads, ["deferred", "deferred"]);
    assert.equal(f.sent.some(value => value.files), false);
    await f.controller.sync();
    assert.deepEqual(f.sent.filter(value => value.files), [{ files: "host selection" }]);
    await f.controller.sync(); await f.controller.poll();
    assert.deepEqual(f.reads, ["deferred", "deferred", "read"]);
  } finally { f.controller.dispose(); }
});

test("a replaced host clipboard drops a file selection that was never pasted", async () => {
  const f = fixture({ deferFiles: true });
  try {
    await settle();
    f.host({ files: "unpasted files" }); await f.controller.poll();
    assert.deepEqual(f.sent.at(-1), { text: "" });
    f.host({ text: "newer host text" }); await f.controller.poll();
    assert.deepEqual(f.sent.at(-1), { text: "newer host text" });
    await f.controller.sync();
    assert.equal(f.sent.some(value => value.files), false);
    assert.equal(f.reads.includes("read"), false);
  } finally { f.controller.dispose(); }
});

test("a guest copy reaches the host while host files wait for a paste", async () => {
  const f = fixture({ deferFiles: true });
  try {
    await settle();
    f.host({ files: "unpasted files" }); await f.controller.poll();
    f.guest({ text: "guest copy" }); await settle(); await settle();
    assert.deepEqual(f.writes, [{ text: "guest copy" }]);
    await f.controller.sync();
    assert.equal(f.sent.some(value => value.files), false);
  } finally { f.controller.dispose(); }
});

test("disconnecting during an explicit file paste cancels the native transfer", async () => {
  const f = fixture({ deferFiles: true });
  let release;
  try {
    await settle();
    f.host({ files: "large host files" }); await f.controller.poll();
    f.readDelay(new Promise(resolve => { release = resolve; }));
    const rejected = assert.rejects(f.controller.sync());
    await settle();
    const cancels = f.cancels();
    f.socket.close();
    await rejected;
    assert.equal(f.cancels(), cancels + 1);
    assert.equal(f.sent.some(value => value.files), false);
  } finally { release?.(); f.controller.dispose(); }
});

test("external clipboard revision bumps with identical content or native unchanged marker do not re-send", async () => {
  const f = fixture({ initial: { png: "same-image" } });
  try {
    await settle();
    assert.deepEqual(f.sent, [{ png: "same-image" }]);
    // Simulate an external remote-desktop tool rewriting the same image (bumping OS revision).
    f.host({ png: "same-image" });
    await f.controller.poll();
    assert.equal(f.sent.length, 1, "identical image content must not be echoed on revision bump");
    // Simulate native unchanged signal (pixel-identical image re-encoded by another tool).
    f.host({ unchanged: true });
    await f.controller.poll();
    assert.equal(f.sent.length, 1, "native unchanged marker must not send or fail");
  } finally { f.controller.dispose(); }
});
