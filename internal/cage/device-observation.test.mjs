import assert from "node:assert/strict";
import { test } from "node:test";
import vm from "node:vm";
import { collectDeviceObservation } from "./device-observation.mjs";

test("local observation degrades explicitly when graphics and device labels are unavailable", async () => {
  const context = vm.createContext({ navigator: { userAgent: "test-browser", platform: "test-platform" }, screen: { width: 1920, height: 1080, colorDepth: 24 }, devicePixelRatio: 1, innerWidth: 1280, innerHeight: 720,
    document: { createElement: () => ({ getContext: () => null }) }, setTimeout });
  const result = await vm.runInContext(`(${collectDeviceObservation.toString()})()`, context);
  assert.equal(result.webgl.state, "unavailable");
  assert.equal(result.canvas.state, "unavailable");
  assert.equal(result.audio.state, "unavailable");
  assert.equal(result.user_agent, "test-browser");
  assert(!JSON.stringify(result).includes("deviceId"));
});

test("Canvas privacy failures are reported separately from successful rendering", async () => {
  const context = vm.createContext({ navigator: { userAgent: "test-browser", platform: "test-platform" }, screen: { width: 800, height: 600, colorDepth: 24 }, devicePixelRatio: 1, innerWidth: 800, innerHeight: 600,
    document: { createElement: () => ({ getContext: kind => kind === "2d" ? { fillRect() {}, getImageData() { throw Error("readback blocked"); } } : null }) }, setTimeout });
  const result = await vm.runInContext(`(${collectDeviceObservation.toString()})()`, context);
  assert.equal(result.canvas.state, "blocked");
  assert.equal(result.webgl.state, "unavailable");
});
