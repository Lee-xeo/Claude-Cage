import test from "node:test";
import assert from "node:assert/strict";
import {
  clampFloatingSize,
  clampFloatingPosition,
  defaultFloatingPosition,
  FLOATING_MARGIN,
  FLOATING_HEADER_HEIGHT,
} from "./plugin-floating-state.mjs";

test("clampFloatingSize respects manifest bounds and small viewports", () => {
  assert.deepEqual(clampFloatingSize({ width: 312, height: 274 }, { width: 1440, height: 900 }), { width: 312, height: 274 });
  assert.deepEqual(clampFloatingSize({ width: 100, height: 999 }, { width: 1440, height: 900 }), { width: 220, height: 720 });
  assert.deepEqual(clampFloatingSize({ width: 500, height: 500 }, { width: 360, height: 300 }), {
    width: 360 - FLOATING_MARGIN * 2,
    height: 300 - FLOATING_HEADER_HEIGHT - FLOATING_MARGIN * 2,
  });
});

test("clampFloatingPosition keeps floating views inside the visible viewport", () => {
  const box = { width: 312, height: 310 };
  const viewport = { width: 1200, height: 800 };
  assert.deepEqual(clampFloatingPosition({ x: -100, y: -50 }, box, viewport), { x: FLOATING_MARGIN, y: FLOATING_MARGIN });
  assert.deepEqual(clampFloatingPosition({ x: 2000, y: 2000 }, box, viewport), {
    x: 1200 - 312 - FLOATING_MARGIN,
    y: 800 - 310 - FLOATING_MARGIN,
  });
  assert.deepEqual(clampFloatingPosition({ x: 400.4, y: 120.6 }, box, viewport), { x: 400, y: 121 });
});

test("defaultFloatingPosition anchors top-right and cascades stacked overlays", () => {
  const box = { width: 312, height: 310 };
  const viewport = { width: 1200, height: 800 };
  const first = defaultFloatingPosition(box, viewport, 0);
  const second = defaultFloatingPosition(box, viewport, 1);
  assert.deepEqual(first, { x: 1200 - 312 - 20, y: 20 });
  assert.deepEqual(second, { x: first.x - 20, y: first.y + 28 });
});
