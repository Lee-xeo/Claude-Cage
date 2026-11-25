import test from "node:test";
import assert from "node:assert/strict";

globalThis.location = { pathname: "/" };
globalThis.window = { devicePixelRatio: 2 };
globalThis.ResizeObserver = class {
  observe() {}
  disconnect() {}
};
globalThis.Image = class {
  set src(value) { this._src = value; }
  async decode() {}
};

const {
  DESKTOP_SCROLL_STEP,
  connectDisplay,
  desktopDragDelta,
  desktopReleaseDelta,
} = await import("./mobile-cli/browser-session.mjs");

test("desktopDragDelta locks vertical thumb drift and quantizes to scroll steps (#822)", () => {
  const gesture = { lastX: 0, lastY: 0, scrolled: false, axis: "" };
  assert.equal(desktopDragDelta(gesture, 8, 25, 1), null);
  assert.equal(gesture.axis, "y");
  assert.deepEqual(desktopDragDelta(gesture, 14, 65, 1), { dx: 0, dy: DESKTOP_SCROLL_STEP });
  assert.equal(gesture.scrolled, true);
  assert.deepEqual(desktopDragDelta(gesture, 18, 175, 1), { dx: 0, dy: DESKTOP_SCROLL_STEP * 2 });
  // Remaining 25px meets half-step threshold and flushes cleanly on release.
  assert.deepEqual(desktopReleaseDelta(gesture, 18, 175, 1), { dx: 0, dy: 25 });
});

test("desktopReleaseDelta flushes short flicks while ignoring sub-half-step tail (#822)", () => {
  const shortFlick = { lastX: 0, lastY: 0, scrolled: false, axis: "" };
  assert.equal(desktopDragDelta(shortFlick, 4, 22, 1), null);
  assert.deepEqual(desktopReleaseDelta(shortFlick, 4, 22, 1), { dx: 0, dy: 22 });

  const withSmallTail = { lastX: 0, lastY: 0, scrolled: false, axis: "" };
  assert.deepEqual(desktopDragDelta(withSmallTail, 3, 110, 1), { dx: 0, dy: 100 });
  assert.equal(desktopReleaseDelta(withSmallTail, 3, 110, 1), null);
});

test("desktopDragDelta supports horizontal and diagonal drags (#822)", () => {
  const horizontal = { lastX: 0, lastY: 0, scrolled: false, axis: "" };
  assert.deepEqual(desktopDragDelta(horizontal, 120, 12, 1), { dx: 100, dy: 0 });
  assert.equal(horizontal.axis, "x");

  const diagonal = { lastX: 0, lastY: 0, scrolled: false, axis: "" };
  assert.deepEqual(desktopDragDelta(diagonal, 100, 100, 1), { dx: 100, dy: 100 });
  assert.equal(diagonal.axis, "xy");
});

test("connectDisplay coalesces consecutive desktop scroll actions and uses action_frame without ?after (#822)", async () => {
  const originalFetch = globalThis.fetch;
  const requests = [];
  let releaseFirstScroll;
  const firstScrollEntered = new Promise(resolve => { releaseFirstScroll = { enter: resolve }; });
  const firstScrollHold = new Promise(resolve => { releaseFirstScroll.go = resolve; });
  let scrollCalls = 0;

  const baseFrame = {
    id: "desktop-1",
    document: "doc-a",
    width: 390,
    height: 640,
    image: "ZGVza3RvcA==",
    focus: "",
    window: "desktop",
    shared: true,
    action_frame: true,
  };

  globalThis.fetch = async (url, init = {}) => {
    const body = init.body ? JSON.parse(init.body) : null;
    requests.push({ url, body });
    if (url.endsWith("/open")) {
      return new Response(JSON.stringify({ id: "desktop-1" }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    if (url.includes("/frame")) {
      assert.equal(url.includes("?after="), false, "desktop frame reads must never send ?after=");
      return new Response(JSON.stringify(baseFrame), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    if (url.endsWith("/action")) {
      if (body.type === "scroll") {
        scrollCalls++;
        if (scrollCalls === 1) {
          releaseFirstScroll.enter();
          await firstScrollHold;
        }
      }
      return new Response(JSON.stringify({ ...baseFrame, image: "c2Nyb2xsZWQ=" }), {
        status: 200,
        headers: { "Content-Type": "application/json" },
      });
    }
    if (url.endsWith("/close")) {
      return new Response(JSON.stringify({ id: "desktop-1" }), { status: 200, headers: { "Content-Type": "application/json" } });
    }
    throw new Error("unexpected URL " + url);
  };

  let latestState = {};
  let readyResolve;
  const frameReady = new Promise(resolve => { readyResolve = resolve; });
  const stage = { clientWidth: 390, clientHeight: 640 };
  const session = connectDisplay(stage, next => {
    latestState = next;
    if (next.frame && readyResolve) {
      readyResolve();
      readyResolve = null;
    }
  }, "desktop-1", true, "desktop");

  try {
    await frameReady;
    const expected = latestState.frame;
    // Start first drag step and wait for it to reach the server.
    session.scroll(expected, { x: 180, y: 320, dx: 0, dy: 50 });
    await firstScrollEntered;
    // Further drag deltas while the first scroll is in-flight must coalesce, not drop.
    session.scroll(expected, { x: 180, y: 320, dx: 0, dy: 50 });
    session.scroll(expected, { x: 180, y: 320, dx: 0, dy: 100 });
    releaseFirstScroll.go();

    for (let i = 0; i < 40 && scrollCalls < 2; i++) {
      await new Promise(resolve => setTimeout(resolve, 15));
    }
    const scrollBodies = requests.filter(r => r.body?.type === "scroll").map(r => r.body);
    assert.equal(scrollBodies.length, 2);
    assert.equal(scrollBodies[0].dy, 50);
    assert.equal(scrollBodies[0].return_frame, true);
    assert.equal(scrollBodies[1].dy, 150);
    assert.equal(scrollBodies[1].return_frame, true);
  } finally {
    session.close();
    globalThis.fetch = originalFetch;
  }
});
