import assert from "node:assert/strict";
import test from "node:test";
import {
  DEFAULT_SIDEBAR_WIDTH,
  MAX_SIDEBAR_WIDTH,
  MIN_SIDEBAR_WIDTH,
  clampSidebarWidth,
  maxSidebarWidth,
  nextSidebarWidth,
} from "./sidebar-width.mjs";

test("clampSidebarWidth enforces min, max, viewport cap and invalid fallbacks", () => {
  assert.equal(clampSidebarWidth(undefined, 1440), DEFAULT_SIDEBAR_WIDTH);
  assert.equal(clampSidebarWidth("bad", 1440), DEFAULT_SIDEBAR_WIDTH);
  assert.equal(clampSidebarWidth(100, 1440), MIN_SIDEBAR_WIDTH);
  assert.equal(clampSidebarWidth(900, 1440), MAX_SIDEBAR_WIDTH);
  assert.equal(clampSidebarWidth(340.4, 1440), 340);
  assert.equal(maxSidebarWidth(800), 384);
  assert.equal(clampSidebarWidth(500, 800), 384);
  assert.equal(maxSidebarWidth(360), MIN_SIDEBAR_WIDTH);
});

test("nextSidebarWidth respects LTR and RTL drag directions", () => {
  assert.equal(nextSidebarWidth(272, 48, false, 1440), 320);
  assert.equal(nextSidebarWidth(272, -48, false, 1440), 224);
  assert.equal(nextSidebarWidth(272, -48, true, 1440), 320);
  assert.equal(nextSidebarWidth(272, 48, true, 1440), 224);
});
