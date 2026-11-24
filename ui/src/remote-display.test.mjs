import assert from "node:assert/strict";
import test from "node:test";
import { setTimeout as pause } from "node:timers/promises";
import { displayGeometry, displaySizeSync, followDisplaySize, watchPixelRatio } from "./remote-display.mjs";

test("normal size preserves logical pixels; zoom changes workspace, large displays retain aspect ratio", () => {
  assert.deepEqual(displayGeometry({ width: 1000, height: 700 }, { desktop: true }), { width: 2000, height: 1400 });
  assert.deepEqual(displayGeometry({ width: 1000, height: 700 }, { desktop: true, zoom: 1.25 }), { width: 1600, height: 1120 });
  assert.deepEqual(displayGeometry({ width: 5000, height: 3000 }, { desktop: true }), { width: 8192, height: 4915 });
  assert.deepEqual(displayGeometry({ width: 286, height: 446 }), { width: 572, height: 892 });
  assert.deepEqual(displayGeometry({ width: 160, height: 240 }, { density: 1.5 }), { width: 240, height: 360 });
  assert.deepEqual(displayGeometry({ width: 1000, height: 700 }, { desktop: true, zoom: 1.25, density: 1.5 }), { width: 1200, height: 840 });
  assert.equal(displayGeometry({ width: 0, height: 700 }), null);
});

test("a guest that follows its viewer renders one pixel per screen pixel", () => {
  const size = { width: 1000, height: 700 };
  // The pixel count follows the screen; zoom only changes the density.
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 2 }), { width: 2000, height: 1400, scale: 2 });
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 1 }), { width: 1000, height: 700, scale: 1 });
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 1.5 }), { width: 1500, height: 1050, scale: 1.5 });
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 2, zoom: 1.25 }), { width: 2000, height: 1400, scale: 2.5 });
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 1, zoom: 0.75 }), { width: 1000, height: 700, scale: 0.75 });
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 2, zoom: 0.9 }), { width: 2000, height: 1400, scale: 1.796875 });
  // Reduced clarity keeps three quarters of the pixels, but never fewer than the 1x screen has.
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 2, density: 1.5 }), { width: 1500, height: 1050, scale: 1.5 });
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 1, density: 1.5 }), { width: 1000, height: 700, scale: 1 });
  // Denser screens are rendered as today, and odd ratios are bounded.
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 3 }), { width: 2000, height: 1400, scale: 2 });
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: 0.5 }), { width: 1000, height: 700, scale: 1 });
  // The browser keeps its minimum logical size, and a huge window its density.
  assert.deepEqual(displayGeometry({ width: 160, height: 240 }, { pixelRatio: 1.25 }), { width: 200, height: 300, scale: 1.25 });
  assert.deepEqual(displayGeometry({ width: 5000, height: 3000 }, { desktop: true, pixelRatio: 2 }), { width: 8192, height: 4915, scale: 1.640625 });
  // Without a ratio the guest keeps its fixed density and receives none.
  assert.deepEqual(displayGeometry(size, { desktop: true, pixelRatio: undefined }), { width: 2000, height: 1400 });
});

test("pixel ratio changes are reported until watching stops", () => {
  const queries = [];
  const view = {
    devicePixelRatio: 2,
    matchMedia(text) {
      const query = { text, listeners: new Set(), addEventListener(_, listener) { this.listeners.add(listener); }, removeEventListener(_, listener) { this.listeners.delete(listener); } };
      queries.push(query);
      return query;
    },
  };
  const move = ratio => { view.devicePixelRatio = ratio; const query = queries.at(-1); for (const listener of [...query.listeners]) { query.listeners.delete(listener); listener(); } };
  let changes = 0;
  const stop = watchPixelRatio(() => changes++, view);
  assert.equal(queries[0].text, "(resolution: 2dppx)");
  move(1);
  assert.equal(changes, 1);
  assert.equal(queries[1].text, "(resolution: 1dppx)");
  move(1.5);
  assert.equal(changes, 2);
  stop();
  assert.equal(queries[2].listeners.size, 0);
});

test("a screen change without a media event is found by comparing the ratio", () => {
  let tick, cleared = false;
  const view = {
    devicePixelRatio: 1,
    matchMedia: (text) => ({ text, addEventListener() {}, removeEventListener() {} }),
    setInterval: (fn) => { tick = fn; return 7; },
    clearInterval: (id) => { cleared = id === 7; },
  };
  let changes = 0;
  const stop = watchPixelRatio(() => changes++, view);
  tick();
  assert.equal(changes, 0);
  // Dragged from a 2K screen to a 4K one at 150%.
  view.devicePixelRatio = 1.5;
  tick();
  tick();
  assert.equal(changes, 1);
  stop();
  assert.equal(cleared, true);
});

test("a size with another density is a different request", async () => {
  const requests = [];
  const sync = displaySizeSync(async (size) => { requests.push(size.scale); }, { delay: 0 });
  try {
    sync.update({ width: 1000, height: 700, scale: 2 });
    await pause(10);
    sync.update({ width: 1000, height: 700, scale: 2 });
    sync.update({ width: 1000, height: 700, scale: 2.5 });
    await pause(10);
    assert.deepEqual(requests, [2, 2.5]);
  } finally { sync.dispose(); }
});

test("a slow guest applies the newest size last without concurrent requests", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const requests = [];
  let releaseFirstRequest;
  const firstRequest = new Promise((resolve) => { releaseFirstRequest = resolve; });
  const sync = displaySizeSync((size) => {
    requests.push(size.width);
    if (requests.length === 1) return firstRequest;
  }, { delay: 0 });
  try {
    sync.update({ width: 1000, height: 700 });
    t.mock.timers.tick(0);
    sync.update({ width: 1200, height: 700 });
    sync.update({ width: 1400, height: 700 });
    t.mock.timers.tick(0);
    assert.deepEqual(requests, [1000]);
    releaseFirstRequest();
    await firstRequest;
    t.mock.timers.tick(0);
    assert.deepEqual(requests, [1000, 1400]);
  } finally { sync.dispose(); }
});

test("transient failure recovers without another resize; disconnect cancels scheduled work", async () => {
  let requests = 0, recovered = 0;
  const sync = displaySizeSync(async () => {
    if (++requests === 1) throw new Error("guest busy");
  }, { delay: 0, onSuccess: () => recovered++ });
  sync.update({ width: 1000, height: 700 });
  await pause(600);
  assert.equal(requests, 2);
  assert.equal(recovered, 1);
  sync.update({ width: 1200, height: 700 });
  sync.dispose();
  await pause(20);
  assert.equal(requests, 2);
});

test("persistent failure has bounded retries and supports explicit retry", async () => {
  let requests = 0;
  const sync = displaySizeSync(async () => { requests++; throw new Error("guest unavailable"); }, { delay: 0 });
  try {
    sync.update({ width: 1000, height: 700 });
    await pause(3900);
    assert.equal(requests, 4);
    sync.retry();
    await pause(20);
    assert.equal(requests, 5);
  } finally { sync.dispose(); }
});

test("a continuous drag sends the latest size on each tick", async (t) => {
  t.mock.timers.enable({ apis: ["setTimeout"] });
  const requests = [];
  const sync = displaySizeSync(async size => { requests.push(size.width); }, { delay: 100 });
  try {
    for (let width = 1000; width < 1012; width++) {
      sync.update({ width, height: 700 });
      t.mock.timers.tick(25);
      await Promise.resolve();
    }
    assert.deepEqual(requests, [1003, 1007, 1011]);
  } finally { sync.dispose(); }
});

test("native desktop honors its actual DPI instead of silently doubling 100% and 125% guests", () => {
  for (const density of [1, 1.25, 1.5, 1.75, 2, 2.5, 3]) {
    assert.deepEqual(displayGeometry({ width: 1200, height: 800 }, { desktop: true, density }),
      { width: 1200 * density, height: 800 * density });
  }
});

test("automatic desktop fits the window at native readable scale and bounds large allocations", () => {
  assert.deepEqual(displayGeometry({ width: 1200, height: 800 }, { desktop: true, zoom: "auto", density: 1 }),
    { width: 1200, height: 800 });
  const large = displayGeometry({ width: 6000, height: 4000 }, { desktop: true, zoom: "auto", density: 3 });
  assert.ok(large.width * large.height <= 12_000_000);
  assert.ok(Math.abs(large.width / large.height - 1.5) < 0.002);
});

function displayFixture({ ratio = 2, framebuffer = { width: 0, height: 0 } } = {}) {
  const listeners = new Set(), observers = [];
  let now = 10_000, interval = null;
  const view = {
    devicePixelRatio: ratio,
    matchMedia: (text) => ({ text, addEventListener() {}, removeEventListener() {} }),
    setInterval: () => (interval = 3),
    clearInterval: (id) => { if (id === interval) interval = null; },
    performance: { now: () => now },
    ResizeObserver: class { constructor(callback) { this.callback = callback; this.connected = false; observers.push(this); } observe() { this.connected = true; } disconnect() { this.connected = false; } },
  };
  const container = {
    getBoundingClientRect: () => ({ width: 1000, height: 700 }),
    addEventListener: (type, listener) => listeners.add(listener),
    removeEventListener: (type, listener) => listeners.delete(listener),
  };
  const client = { cageFramebufferSize: framebuffer, scaleViewport: false };
  return {
    view, container, client, observers, listeners,
    click: () => { for (const listener of listeners) listener(); },
    advance: (ms) => { now += ms; },
    watching: () => interval !== null,
  };
}

test("an instance that follows its viewer is sized in screen pixels at their density", async () => {
  const fixture = displayFixture({ ratio: 2 });
  const requests = [];
  const sizing = followDisplaySize({
    ...fixture, desktop: true, density: 2, follow: true,
    resize: async (size) => { requests.push(size); return { ok: true, scale: size.scale }; },
  });
  try {
    await pause(200);
    assert.deepEqual(requests, [{ width: 2000, height: 1400, scale: 2 }]);
    // A 4K screen at 150%.
    fixture.view.devicePixelRatio = 1.5;
    fixture.observers[0].callback();
    await pause(200);
    assert.deepEqual(requests.at(-1), { width: 1500, height: 1050, scale: 1.5 });
  } finally { sizing.dispose(); }
  assert.equal(fixture.observers[0].connected, false);
  assert.equal(fixture.listeners.size, 0);
  assert.equal(fixture.watching(), false);
});

test("an instance that keeps its density is sized for that density", async () => {
  // A guest that answered without the requested density stops being asked for one.
  let fixture = displayFixture({ ratio: 2 });
  const requests = [];
  let sizing = followDisplaySize({
    ...fixture, desktop: true, density: 2, follow: true,
    resize: async (size) => { requests.push(size); return { ok: true }; },
  });
  try {
    await pause(400);
    assert.deepEqual(requests, [{ width: 2000, height: 1400, scale: 2 }, { width: 2000, height: 1400 }]);
  } finally { sizing.dispose(); }
  // A session started by an earlier version keeps 96 DPI: CSS pixels, as before.
  fixture = displayFixture({ ratio: 2 });
  requests.length = 0;
  sizing = followDisplaySize({
    ...fixture, desktop: true, density: 1, follow: false,
    resize: async (size) => { requests.push(size); return { ok: true }; },
  });
  try {
    await pause(200);
    assert.deepEqual(requests, [{ width: 1000, height: 700 }]);
  } finally { sizing.dispose(); }
});

test("a guest that kept another size is asked again, at most every three seconds", async () => {
  const fixture = displayFixture({ ratio: 2, framebuffer: { width: 2560, height: 1600 } });
  let requests = 0;
  const sizing = followDisplaySize({
    ...fixture, desktop: true, density: 2, follow: true, verifyDelay: 20,
    resize: async (size) => { requests++; return { scale: size.scale }; },
  });
  try {
    await pause(250);
    // Once after the change.
    assert.equal(requests, 2);
    fixture.click();
    await pause(50);
    assert.equal(requests, 2);
    fixture.advance(3000);
    fixture.click();
    await pause(50);
    assert.equal(requests, 3);
    // A matching framebuffer is left alone.
    fixture.client.cageFramebufferSize = { width: 2000, height: 1400 };
    fixture.advance(3000);
    fixture.click();
    await pause(50);
    assert.equal(requests, 3);
  } finally { sizing.dispose(); }
});
