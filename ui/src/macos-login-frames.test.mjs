import test from "node:test";
import assert from "node:assert/strict";
import { pollLoginFrames } from "./macos-login-frames.mjs";

function harness(inputPending = () => false) {
  let clock = 0, nextID = 0;
  const timers = new Map(), pending = [], delivered = [], errors = [];
  let captures = 0;
  const polling = pollLoginFrames({
    inputPending,
    capture() { captures++; return new Promise((resolve, reject) => pending.push({ resolve, reject })); },
    onFrame: frame => delivered.push(frame), onError: error => errors.push(error),
    now: () => clock,
    setTimer(fn, delay) { timers.set(++nextID, { fn, delay }); return nextID; },
    clearTimer(id) { timers.delete(id); },
  });
  return {
    polling, delivered, errors,
    get captures() { return captures; },
    get delays() { return [...timers.values()].map(t => t.delay); },
    async complete(ms, value = "frame") { clock += ms; pending.shift().resolve(value); await Promise.resolve(); },
    async fail(error) { pending.shift().reject(error); await Promise.resolve(); },
    advance(ms) { clock += ms; },
    tick() { const [id, timer] = timers.entries().next().value; timers.delete(id); timer.fn(); },
  };
}

test("capture time counts toward the frame interval, with a floor for slow captures", async () => {
  const h = harness();
  await h.complete(500);
  assert.deepEqual(h.delays, [50]);
  h.tick();
  await h.complete(800);
  assert.deepEqual(h.delays, [25]);
  h.polling.stop();
});

test("input refresh cancels the idle wait and repeated input keeps one pending capture", async () => {
  const h = harness();
  await h.complete(20);
  assert.deepEqual(h.delays, [530]);
  h.polling.refresh(); h.polling.refresh();
  assert.deepEqual(h.delays, [0]);
  h.tick();
  for (let i = 0; i < 100; i++) h.polling.refresh();
  assert.equal(h.captures, 2);
  assert.deepEqual(h.delays, []);
  await h.complete(500, "after input");
  assert.deepEqual(h.delays, [0]);
  h.tick();
  assert.equal(h.captures, 3);
  h.polling.stop();
  await h.complete(20);
  assert.deepEqual(h.delays, []);
  assert.deepEqual(h.delivered, ["frame", "after input"]);
});

test("closing or replacing a session discards late frames and late failures", async () => {
  for (const failure of [false, true]) {
    const h = harness();
    h.polling.stop(); h.polling.refresh();
    if (failure) await h.fail(new Error("old session"));
    else await h.complete(500);
    assert.deepEqual(h.delivered, []);
    assert.deepEqual(h.errors, []);
    assert.deepEqual(h.delays, []);
  }
});

test("a failed frame stops polling and reports the failure once", async () => {
  const h = harness(), error = new Error("disconnected");
  await h.fail(error);
  h.polling.refresh();
  assert.deepEqual(h.errors, [error]);
  assert.deepEqual(h.delays, []);
});

test("queued input takes priority over the next screenshot", async () => {
  let pending = true;
  const h = harness(() => pending);
  assert.equal(h.captures, 0);
  h.tick();
  assert.equal(h.captures, 0);
  pending = false;
  h.polling.refresh();
  assert.deepEqual(h.delays, [0]);
  h.tick();
  assert.equal(h.captures, 1);
  await h.complete(100);
  h.polling.stop();
});

test("input that stays queued delays a screenshot only for a bounded time", async () => {
  const h = harness(() => true);
  h.tick();
  h.advance(275); h.tick();
  assert.equal(h.captures, 0);
  assert.deepEqual(h.delays, [25]);
  h.advance(25); h.tick();
  assert.equal(h.captures, 1);
  await h.complete(100);
  // The next screenshot waits for input again, from the start.
  h.tick();
  assert.equal(h.captures, 1);
  h.advance(300); h.tick();
  assert.equal(h.captures, 2);
  h.polling.stop();
});
