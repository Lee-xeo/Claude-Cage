import assert from "node:assert/strict";
import { test } from "node:test";
import vm from "node:vm";
import { auditBrowser } from "./browser-audit.mjs";

test("local browser audit uses a syntactically valid async probe in the regular profile", async () => {
  let closed = false;
  const cdp = async (method, input) => {
    if (method === "Target.createTarget") return { targetId: "probe" };
    if (method === "Target.attachToTarget") return { sessionId: "isolated" };
    if (method === "Runtime.evaluate") {
      new vm.Script(input.expression);
      assert.equal(input.awaitPromise, true);
      assert(input.expression.includes("collectDeviceObservation"));
      return { result: { value: { timezone: "UTC", language: "en-US", platform: "Linux x86_64", device: { webgl: { state: "unavailable" } } } } };
    }
    if (method === "Target.closeTarget") { closed = true; return {}; }
    throw Error(`unexpected probe operation: ${method}`);
  };
  const result = await auditBrowser(cdp, new Set(), false, "UTC");
  assert.equal(result.device.webgl.state, "unavailable");
  assert.equal(closed, true);
});
