// Runs the shipped workbench and RFB client with isolated input status and a
// recording native bridge. Windows/WebView2 native acceptance remains separate.
import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";
import { startDisplay } from "./rfb-wire-fixture.mjs";

const display = await startDisplay(process.env.CAGE_UI_URL || "http://127.0.0.1:5198");
let available = true, activations = 0, finishHandoff;
const { browser, page, errors } = await createWorkbench({
  mockWebSockets: false,
  url: display.url,
  clientContext: { mode: "local", connection_id: "local", client_platform: "windows", server_platform: "linux", connection: { state: "disconnected" } },
  nativeRequest: request => {
    if (request.action === "remoteInput") {
      if (request.operation === "activate") activations++;
      if (request.operation === "deactivate" && request.returnFocus)
        return new Promise(resolve => { finishHandoff = resolve; });
    }
    return true;
  },
  async beforeNavigate(page, state) {
    state.status.ready = state.status.vm_running = true;
    state.status.keyboard = state.settings.keyboard = "native";
    state.active = "first";
    state.tabs = [{ id: "first", kind: "web", title: "First site", url: "https://example.test/first" }];
    await page.addInitScript(() => {
      localStorage.setItem("cage.audio", JSON.stringify({ muted: true, volume: 1 }));
      Object.defineProperty(navigator, "platform", { value: "Win32" });
      window.chrome ??= {};
      window.chrome.webview = {};
      window.cageNativeIMEAvailable = true;
      window.EventSource = class extends EventTarget {
        constructor() { super(); queueMicrotask(() => this.onopen?.()); }
        close() {}
      };
    });
    await page.route("**/api/input", route => route.fulfill({ json: available
      ? { available: true, rect: [16, 20, 1, 24], width: 640, height: 480 }
      : { available: false } }));
  },
});
page.setDefaultTimeout(10000);
const until = async predicate => {
  for (let attempt = 0; attempt < 200; attempt++) {
    if (predicate()) return;
    await new Promise(resolve => setTimeout(resolve, 20));
  }
  assert.fail("expected native input request did not arrive");
};
try {
  await page.getByRole("button", { name: "打开浏览器", exact: true }).click();
  const canvas = page.locator(".remote-canvas canvas");
  await canvas.waitFor();
  await page.evaluate(() => {
    window.handoffReplies = 0;
    const bridge = window.webkit.messageHandlers.cage;
    const post = bridge.postMessage;
    bridge.postMessage = async request => {
      const reply = await post(request);
      if (request.action === "remoteInput" && request.operation === "deactivate") window.handoffReplies++;
      return reply;
    };
  });
  for (const interruption of ["none", "shortcut", "native-command"]) {
    available = true;
    finishHandoff = undefined;
    const previousActivations = activations;
    await canvas.click({ position: { x: 50, y: 50 } });
    await until(() => activations > previousActivations);
    if (interruption === "shortcut") await page.keyboard.down("Control");
    available = false;
    await until(() => finishHandoff);
    // Simulate WebView2's automatic DOM focus when the native control hides.
    await page.getByRole("button", { name: "收起侧栏", exact: true }).evaluate(node => node.focus());
    if (interruption === "shortcut") {
      await page.keyboard.press("l");
      await page.getByRole("textbox", { name: "网页地址或搜索", exact: true }).evaluate(node => {
        if (document.activeElement !== node) throw new Error("Ctrl+L did not focus the address bar");
      });
    }
    if (interruption === "native-command")
      await page.evaluate(() => window.cageNativeAction("settings"));
    const previousReplies = await page.evaluate(() => window.handoffReplies);
    finishHandoff({ returnedFocus: true });
    await page.waitForFunction(previous => window.handoffReplies > previous, previousReplies);
    assert.equal(await page.evaluate(() => document.activeElement?.classList.contains("remote-text-input")), interruption === "none",
      `${interruption}: a late reply must preserve the user's focus choice`);
    if (interruption === "shortcut")
      assert.equal(await page.getByRole("textbox", { name: "网页地址或搜索", exact: true }).evaluate(node => document.activeElement === node), true);
    if (interruption === "none") {
      available = true;
      await until(() => activations > previousActivations + 1);
      await page.keyboard.press("x");
      await until(() => display.stats.keys.some(key => key.symbol === 120 && key.down));
      await page.getByRole("textbox", { name: "网页地址或搜索", exact: true }).focus();
    }
    if (interruption === "shortcut") await page.keyboard.up("Control");
  }
  assert.deepEqual(errors, []);
  console.log(JSON.stringify({ result: "PASS", checks: ["temporary blur restores remote focus", "held Ctrl then L cancels a late handoff", "native workbench command cancels a late handoff"] }));
} finally {
  await browser.close();
  await display.close();
}
