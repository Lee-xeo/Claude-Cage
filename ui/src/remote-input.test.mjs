import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import vm from "node:vm";
import { webcrypto } from "node:crypto";
const source = readFileSync(
  new URL("./remote-input.js", import.meta.url),
  "utf8",
)
  .replace(/^import[\s\S]*?;\n/gm, "")
  .replace("export function installRemoteInput", "function installRemoteInput");
function fixture(native = false, browserComposition = false) {
  const doc = new EventTarget(),
    win = new EventTarget(),
    canvas = {},
    local = {},
    keys = [],
    pastes = [],
    texts = [],
    compositions = [],
    writes = [],
    statuses = [];
  let reads = 0,
    now = 1000,
    active = true,
    mode = "mac",
    currentTabId = "tab-1";
  doc.activeElement = canvas;
  doc.body = { append() {} };
  const target = Object.assign(new EventTarget(), {
    value: "",
    style: {},
    classList: { add() {}, remove() {} },
    setAttribute() {},
    focus() {
      doc.activeElement = this;
    },
    remove() {},
  });
  doc.createElement = () => target;
  win.innerWidth = 1280;
  win.innerHeight = 800;
  const rfb = new EventTarget();
  rfb.cageSetKeyboardTarget = (element) => {
    rfb.target = element;
  };
  rfb.sendKey = (...args) => keys.push(args);
  rfb.focus = () => {
    doc.activeElement = rfb.target || canvas;
  };
  rfb.cagePasteText = (text) => {
    pastes.push(text);
    return Promise.resolve();
  };
  const context = {
    document: doc,
    window: win,
    setTimeout,
    clearTimeout,
    Promise,
    Date: { now: () => now },
    Map,
    Set,
    console,
    crypto: webcrypto,
    readClipboardContent: async () => {
      reads++;
      return { text: "中文🙂" };
    },
    transferFiles: (data) => Array.from(data?.files || []),
    hasFiles: (data) => Array.from(data?.types || []).includes("Files"),
    imagePayload: async () => ({ png: "fixture-image" }),
    writeClipboard: async (text) => writes.push(text),
    nativeAvailable: () => native,
    getKeycode: (e) => e.code,
    getKeysym: (e) =>
      ({ Shift: 0xffe1, Control: 0xffe3, Alt: 0xffe9, Meta: 0xffeb })[e.key] ??
      (e.key.length === 1 ? e.key.codePointAt(0) : null),
    Keysyms: {
      lookup: (codepoint) =>
        codepoint <= 255 ? codepoint : 0x01000000 | codepoint,
    },
  };
  const install = vm.runInNewContext(source + "\ninstallRemoteInput;", context);
  const container = Object.assign(new EventTarget(), {
      querySelector: () => null,
      contains: (element) => element === canvas || element === target,
      append() {},
      getBoundingClientRect: () => ({ left: 0, top: 0 }),
    });
  const controller = install(
    rfb,
    container,
    {
      mode: () => mode,
      onStatus: (t) => statuses.push(t),
      onFallback() {},
      isActive: () => active,
      insertText: async (text) => {
        texts.push(text);
      },
      insertComposition: browserComposition ? async (id, value) => {
        if (id !== currentTabId) throw new Error("Input target changed");
        compositions.push({ id, ...value });
      } : null,
      tabId: () => currentTabId,
      caretRect: browserComposition ? async () => ({
        available: true, context: "a".repeat(32), rect: [240, 120, 0, 24],
      }) : null,
      pasteContent: async (content) => {
        const value = await content;
        if (rfb.target) pastes.push(value.png || value.text);
      },
    },
  );
  const key = (type, key, code, extra = {}) => {
    const event = new Event(type, { cancelable: true });
    Object.assign(event, {
      key,
      code,
      metaKey: false,
      ctrlKey: false,
      shiftKey: false,
      altKey: false,
      ...extra,
    });
    doc.dispatchEvent(event);
    return event;
  };
  return {
    doc,
    win,
    rfb,
    keys,
    pastes,
    texts,
    compositions,
    writes,
    statuses,
    controller,
    key,
    pointer({ isTrusted = true, button = 0 } = {}) {
      const event = new Event("pointerdown");
      Object.defineProperty(event, "isTrusted", { value: isTrusted });
      event.button = button;
      event.clientX = event.clientY = 150;
      container.dispatchEvent(event);
    },
    advance: (ms) => { now += ms; },
    setActive: (value) => { active = value; },
    local,
    target,
    setMode: (value) => {
      mode = value;
    },
    setTabId: (value) => {
      currentTabId = value;
    },
    reads: () => reads,
  };
}
test("Mac find and redo preserve the Linux Ctrl/Shift chord", () => {
  const f = fixture();
  try {
    assert.equal(
      f.key("keydown", "f", "KeyF", { metaKey: true }).defaultPrevented,
      true,
    );
    assert.deepEqual(
      f.keys.map((k) => [k[0], k[2]]),
      [
        [0xffe3, true],
        [102, true],
        [102, false],
        [0xffe3, false],
      ],
    );
    f.keys.length = 0;
    f.key("keydown", "\0", "KeyZ", { metaKey: true, shiftKey: true });
    assert.deepEqual(
      f.keys.map((k) => [k[0], k[2]]),
      [
        [0xffe3, true],
        [0xffe1, true],
        [90, true],
        [90, false],
        [0xffe1, false],
        [0xffe3, false],
      ],
    );
  } finally {
    f.controller.dispose();
  }
});

function inputEvent(f, type, properties = {}) {
  const event = new Event(type, { cancelable: true });
  Object.assign(event, properties);
  f.target.dispatchEvent(event);
  return event;
}

test("host IME receives composition keys and sends only committed Unicode in order", () => {
  const f = fixture();
  try {
    f.rfb.focus();
    assert.equal(f.doc.activeElement, f.target);
    assert.equal(
      f.key("keydown", "Process", "KeyN", { keyCode: 229 }).defaultPrevented,
      false,
    );
    inputEvent(f, "compositionstart");
    f.target.value = "nihao";
    inputEvent(f, "input", {
      data: "nihao",
      isComposing: true,
      inputType: "insertCompositionText",
    });
    assert.equal(
      f.key("keydown", " ", "Space", { isComposing: true }).defaultPrevented,
      false,
    );
    assert.deepEqual(f.keys, []);
    inputEvent(f, "compositionend", { data: "你好🙂" });
    // Safari sends a final input after compositionend; Chromium sends it before.
    f.target.value = "你好🙂";
    inputEvent(f, "input", {
      data: "你好🙂",
      inputType: "insertFromComposition",
    });
    assert.deepEqual(f.keys, []);
    assert.equal(f.target.value, "");
    assert.deepEqual(f.texts, ["你好🙂"]);
    assert.equal(f.reads(), 0);
    inputEvent(f, "compositionstart");
    inputEvent(f, "compositionend", { data: "你好" });
    assert.equal(
      f.texts.length,
      2,
      "identical consecutive commits are not deduplicated",
    );
  } finally {
    f.controller.dispose();
  }
  assert.equal(f.rfb.target, null);
});

test("browser composition is forwarded as preedit, then committed separately", async () => {
  const f = fixture(false, true);
  await new Promise((resolve) => setTimeout(resolve, 5));
  try {
    f.rfb.focus();
    inputEvent(f, "compositionstart");
    f.target.value = "nihao";
    inputEvent(f, "compositionupdate", { data: "nihao" });
    inputEvent(f, "input", { data: "nihao", inputType: "insertCompositionText", isComposing: true });
    await Promise.resolve();
    inputEvent(f, "compositionend", { data: "你好" });
    await new Promise((resolve) => setImmediate(resolve));
    assert.deepEqual(f.texts, []);
    assert.equal(f.compositions[0].mode, "begin");
    assert.equal(new Set(f.compositions.map(value => value.session)).size, 1);
    assert.deepEqual(f.compositions.map(value => value.sequence), [0, 1, 2]);
    assert.deepEqual(JSON.parse(JSON.stringify(f.compositions.slice(1).map(({session, sequence, ...value}) => value))), [
      { id: "tab-1", mode: "update", text: "nihao", selectionStart: 5, selectionEnd: 5 },
      { id: "tab-1", mode: "commit", text: "你好", selectionStart: 0, selectionEnd: 0 },
    ]);
  } finally {
    f.controller.dispose();
  }
});

test("queued composition stays bound to its original browser tab", async () => {
  const f = fixture(false, true);
  await new Promise((resolve) => setTimeout(resolve, 5));
  f.rfb.focus();
  inputEvent(f, "compositionstart");
  f.target.value = "nihao";
  inputEvent(f, "input", { data: "nihao", inputType: "insertCompositionText", isComposing: true });
  f.setTabId("tab-2");
  inputEvent(f, "compositionend", { data: "你好" });
  await new Promise((resolve) => setImmediate(resolve));
  assert.deepEqual(f.compositions.map(value => value.mode), ["begin", "update"]);
  assert.ok(f.statuses.includes("Input target changed"));
  f.controller.dispose();
});

test("cancelled and disconnected compositions do not insert text", () => {
  const f = fixture();
  f.rfb.focus();
  inputEvent(f, "compositionstart");
  inputEvent(f, "compositionend", { data: "" });
  assert.deepEqual(f.keys, []);
  inputEvent(f, "compositionstart");
  f.controller.dispose();
  inputEvent(f, "compositionend", { data: "迟到的文本" });
  assert.deepEqual(f.keys, []);
  assert.deepEqual(f.texts, []);
});

test("text input without a composition supports emoji and clears the host buffer", () => {
  const f = fixture();
  try {
    f.rfb.focus();
    f.target.value = "🙂";
    inputEvent(f, "input", { data: "🙂", inputType: "insertText" });
    assert.deepEqual(f.keys, []);
    assert.deepEqual(f.texts, ["🙂"]);
    assert.equal(f.target.value, "");
  } finally {
    f.controller.dispose();
  }
});

test("fullwidth punctuation without composition uses text, while host punctuation keeps its default action", () => {
  const f = fixture();
  try {
    f.rfb.focus();
    for (const character of ["？", "？", "，", "。", "！", "🙂"]) {
      assert.equal(f.key("keydown", character, "Slash", { shiftKey: true }).defaultPrevented, true);
      f.key("keyup", character, "Slash");
    }
    assert.deepEqual(f.texts, ["？", "？", "，", "。", "！", "🙂"]);
    assert.deepEqual(f.keys, []);
    assert.equal(f.key("keydown", "?", "Slash", { shiftKey: true }).defaultPrevented, false);
    f.target.value = "？";
    inputEvent(f, "input", { data: "？", inputType: "insertText" });
    assert.equal(f.texts.at(-1), "？");
    assert.equal(f.key("keydown", "?", "Slash", { ctrlKey: true }).defaultPrevented, false);
  } finally { f.controller.dispose(); }
});
test("local fields and native keyboard mode do not acquire mapped shortcuts", () => {
  const f = fixture();
  try {
    f.doc.activeElement = f.local;
    assert.equal(
      f.key("keydown", "f", "KeyF", { metaKey: true }).defaultPrevented,
      false,
    );
    assert.equal(f.keys.length, 0);
    f.rfb.focus();
    f.setMode("native");
    f.key("keydown", "f", "KeyF", { metaKey: true });
    assert.equal(f.keys.length, 0);
  } finally {
    f.controller.dispose();
  }
});
test("background clipboard notifications never write to the host", async () => {
  const f = fixture();
  try {
    const clipboard = new Event("clipboard");
    clipboard.detail = { text: "guest text" };
    f.rfb.dispatchEvent(clipboard);
    await Promise.resolve();
    assert.equal(f.reads(), 0);
    assert.deepEqual(f.writes, []);
    f.key("keydown", "c", "KeyC", { metaKey: true });
    f.rfb.dispatchEvent(clipboard);
    await Promise.resolve();
    assert.deepEqual(f.writes, ["guest text"]);
  } finally {
    f.controller.dispose();
  }
});
test("a real remote click exports one text update without sending a copy chord", async () => {
  const f = fixture(true);
  const clipboard = new Event("clipboard");
  clipboard.detail = { text: "网页按钮复制的命令🙂" };
  try {
    f.pointer();
    f.rfb.dispatchEvent(clipboard);
    f.rfb.dispatchEvent(clipboard);
    await Promise.resolve();
    assert.deepEqual(f.writes, [clipboard.detail.text]);
    assert.deepEqual(f.keys, []);
    assert.equal(f.reads(), 0);
    f.pointer();
    f.rfb.dispatchEvent(clipboard);
    await Promise.resolve();
    assert.equal(f.writes.length, 2, "the same command can be copied again");
  } finally {
    f.controller.dispose();
  }
});
test("synthetic, expired, inactive and blurred clicks cannot export clipboard updates", async () => {
  for (const kind of ["synthetic", "right-button", "expired", "inactive-click", "inactive-update", "blur", "local-field", "disposed"]) {
    const f = fixture();
    try {
      if (kind === "inactive-click") f.setActive(false);
      f.pointer({ isTrusted: kind !== "synthetic", button: kind === "right-button" ? 2 : 0 });
      if (kind === "expired") f.advance(1600);
      if (kind === "inactive-click") f.setActive(true);
      if (kind === "inactive-update") f.setActive(false);
      if (kind === "blur") f.win.dispatchEvent(new Event("blur"));
      if (kind === "local-field") inputEvent(f, "blur");
      if (kind === "disposed") f.controller.dispose();
      const clipboard = new Event("clipboard");
      clipboard.detail = { text: "background text" };
      f.rfb.dispatchEvent(clipboard);
      await Promise.resolve();
      assert.deepEqual(f.writes, [], kind);
    } finally {
      f.controller.dispose();
    }
  }
});
test("native menu paste accepts every rapid gesture, without debouncing", async () => {
  const f = fixture(true);
  try {
    for (let i = 0; i < 10; i++)
      f.key("keydown", "v", "KeyV", { metaKey: true });
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(f.reads(), 10);
    assert.equal(f.pastes.length, 10);
    assert.ok(f.pastes.every((t) => t === "中文🙂"));
  } finally {
    f.controller.dispose();
  }
});
test("mapped modifier is released even after focus is lost", () => {
  const f = fixture();
  try {
    f.key("keydown", "Shift", "ShiftLeft", { metaKey: true, shiftKey: true });
    f.doc.activeElement = f.local;
    f.win.dispatchEvent(new Event("blur"));
    assert.deepEqual(f.keys, [
      [0xffe1, "ShiftLeft", true],
      [0xffe1, "ShiftLeft", false],
    ]);
  } finally {
    f.controller.dispose();
  }
});

test("a clipboard read finishing after disconnect is not pasted into a stale connection", async () => {
  const f = fixture(true);
  f.controller.readAndPaste();
  f.controller.dispose();
  await new Promise((resolve) => setImmediate(resolve));
  assert.equal(f.pastes.length, 0);
});

test("malformed native Command letters retain clipboard and redo semantics", async () => {
  const f = fixture(true);
  try {
    f.key("keydown", "\u0016", "KeyV", { metaKey: true });
    await new Promise((resolve) => setImmediate(resolve));
    assert.equal(f.reads(), 1);
    assert.equal(f.pastes.length, 1);
    assert.equal(f.keys.length, 0);
    for (const letter of ["\ufffd", "\uf700", "Ω"]) {
      f.keys.length = 0;
      f.key("keydown", letter, "KeyZ", { metaKey: true, shiftKey: true });
      assert.deepEqual(
        f.keys.map((k) => [k[0], k[2]]),
        [
          [0xffe3, true],
          [0xffe1, true],
          [90, true],
          [90, false],
          [0xffe1, false],
          [0xffe3, false],
        ],
      );
    }
  } finally {
    f.controller.dispose();
  }
});
