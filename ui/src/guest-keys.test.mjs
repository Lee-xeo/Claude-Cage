import assert from "node:assert/strict";
import test from "node:test";
import { guestKeys } from "./guest-keys.mjs";

const SHIFT = [0xffe1, "ShiftLeft"], CONTROL = [0xffe3, "ControlLeft"];

// A clock the test advances by hand.
function setup() {
  const sent = [], pending = [];
  let now = 0, next = 1;
  const timers = {
    setTimeout(run, delay) { pending.push({ id: next, at: now + delay, run }); return next++; },
    clearTimeout(id) { const index = pending.findIndex(entry => entry.id === id); if (index >= 0) pending.splice(index, 1); },
  };
  const advance = (ms) => {
    const end = now + ms;
    for (;;) {
      pending.sort((a, b) => a.at - b.at);
      if (!pending.length || pending[0].at > end) break;
      const entry = pending.shift();
      now = entry.at;
      entry.run();
    }
    now = end;
  };
  const keys = guestKeys((keysym, code, down) => sent.push([keysym, code, down]), timers);
  return { keys, sent, advance, waiting: () => pending.length };
}

test("a held key repeats like a guest keyboard, and the host's own repeats are not added", () => {
  const { keys, sent, advance } = setup();
  keys.sendKey(0x61, "KeyA", true);
  keys.sendKey(0x61, "KeyA", true);
  keys.sendKey(0x61, "KeyA", true);
  assert.deepEqual(sent, [[0x61, "KeyA", true]]);
  advance(659);
  assert.equal(sent.length, 1);
  advance(1);
  assert.equal(sent.length, 2);
  advance(840);
  // 660 ms, then every 40 ms: the same 22 presses the guest display produced in 1.5 s.
  assert.equal(sent.length, 23);
  assert.ok(sent.every(([keysym, code, down]) => keysym === 0x61 && code === "KeyA" && down));
  keys.sendKey(0x61, "KeyA", false);
  advance(5000);
  assert.deepEqual(sent.at(-1), [0x61, "KeyA", false]);
  assert.equal(sent.length, 24);
});

test("only the key pressed last repeats, and modifiers never do", () => {
  const { keys, sent, advance } = setup();
  keys.sendKey(...SHIFT, true);
  advance(3000);
  assert.equal(sent.length, 1);
  keys.sendKey(0x4a, "KeyJ", true);
  advance(300);
  keys.sendKey(0x4b, "KeyK", true);
  advance(700);
  const repeated = sent.slice(3).map(([keysym]) => keysym);
  assert.ok(repeated.length > 0 && repeated.every(keysym => keysym === 0x4b));
  // Releasing the earlier key leaves the later one repeating.
  keys.sendKey(0x4a, "KeyJ", false);
  const before = sent.length;
  advance(200);
  assert.equal(sent.length, before + 5);
});

test("a press and release sent together does not start a repeat", () => {
  const { keys, sent, advance, waiting } = setup();
  keys.sendKey(0xff0d, "Enter");
  advance(5000);
  assert.deepEqual(sent, [[0xff0d, "Enter", true], [0xff0d, "Enter", false]]);
  assert.equal(waiting(), 0);
});

test("a shortcut takes its symbol from the held Shift, not from the host's spelling", () => {
  const { keys, sent } = setup();
  // macOS reports Command+Shift+Z with a lowercase letter.
  keys.sendKey(...CONTROL, true);
  keys.sendKey(...SHIFT, true);
  keys.sendKey(0x7a, "KeyZ", true);
  keys.sendKey(...SHIFT, false);
  keys.sendKey(0x7a, "KeyZ", false);
  assert.deepEqual(sent.slice(2), [[0x5a, "KeyZ", true], [0xffe1, "ShiftLeft", false], [0x5a, "KeyZ", false]]);
  // Caps Lock on the host turns Control+C into an uppercase letter.
  sent.length = 0;
  keys.sendKey(0x43, "KeyC");
  assert.deepEqual(sent, [[0x63, "KeyC", true], [0x63, "KeyC", false]]);
  // A symbol key follows the guest layout only when it is that key.
  sent.length = 0;
  keys.sendKey(...SHIFT, true);
  keys.sendKey(0x5b, "BracketLeft");
  keys.sendKey(0x2f, "Digit7");
  assert.deepEqual(sent.slice(1), [[0x7b, "BracketLeft", true], [0x7b, "BracketLeft", false], [0x2f, "Digit7", true], [0x2f, "Digit7", false]]);
});

test("typing is sent as the host spelled it", () => {
  const { keys, sent } = setup();
  keys.sendKey(...SHIFT, true);
  keys.sendKey(0x61, "KeyA");
  keys.sendKey(...SHIFT, false);
  keys.sendKey(0x41, "KeyA");
  keys.sendKey(0x3f, null);
  keys.sendKey(0x1004e2d, null);
  assert.deepEqual(sent.filter(([keysym]) => keysym < 0xff00 || keysym > 0xffff).map(([keysym]) => keysym),
    [0x61, 0x61, 0x41, 0x41, 0x3f, 0x3f, 0x1004e2d, 0x1004e2d]);
});

test("stopping ends a repeat and forgets held keys", () => {
  const { keys, sent, advance, waiting } = setup();
  keys.sendKey(0xff08, "Backspace", true);
  advance(1000);
  keys.stop();
  const count = sent.length;
  advance(5000);
  assert.equal(sent.length, count);
  assert.equal(waiting(), 0);
  // A release that arrives afterwards still reaches the guest.
  keys.sendKey(0xff08, "Backspace", false);
  assert.deepEqual(sent.at(-1), [0xff08, "Backspace", false]);
  keys.sendKey(0xff08, "Backspace", true);
  assert.deepEqual(sent.at(-1), [0xff08, "Backspace", true]);
});
