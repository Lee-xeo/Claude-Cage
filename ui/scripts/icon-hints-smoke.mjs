import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

const { browser, page, errors } = await createWorkbench({
  beforeNavigate: async (page, state) => {
    state.status.local_vm_storage = true;
    await page.route("**/api/host-tools**", route => route.fulfill({ json: {
      available: true, enabled: true, plugins: [], catalog: [], entries: [],
    } }));
  },
});
try {
  const button = page.getByRole("button", { name: "收起侧栏" });
  await button.hover();
  const hint = page.getByRole("tooltip");
  assert.equal(await hint.textContent(), "收起侧栏");
  assert.equal(await button.getAttribute("title"), null);
  await page.mouse.move(1200, 800);
  assert.equal(await button.getAttribute("title"), "收起侧栏");
  await button.focus();
  assert.equal(await hint.textContent(), "收起侧栏");
  await page.keyboard.press("Escape");
  assert.equal(await hint.isVisible(), false);

  // Clicking Back replaces the whole sidebar without a reliable pointerout/focusout.
  await page.getByRole("button", { name: "MCP 插件", exact: true }).click();
  const back = page.getByRole("button", { name: "退出 MCP 插件", exact: true });
  await back.hover();
  assert.equal(await hint.textContent(), "退出 MCP 插件（Esc）");
  await back.click();
  assert.equal(await back.count(), 0);
  assert.equal(await hint.isVisible(), false, "Back must dismiss its hint when the sidebar closes");

  await button.hover();
  await page.evaluate(() => window.dispatchEvent(new Event("blur")));
  assert.equal(await hint.isVisible(), false, "Window blur must dismiss the hint");
  await page.mouse.move(1200, 800);
  assert.equal(await hint.isVisible(), false, "Pointer exit must not revive dismissed focus state");
  await button.focus();
  assert.equal(await hint.isVisible(), true, "Keyboard focus must still reveal the hint after dismissal");
  await page.keyboard.press("Escape");

  // A native dialog is in the browser's top layer, so its hint must join it.
  await page.evaluate(() => {
    const dialog = document.createElement("dialog");
    dialog.innerHTML = '<button aria-label="关闭测试"><svg></svg></button>';
    document.body.append(dialog);
    dialog.showModal();
  });
  const dialogButton = page.getByRole("button", { name: "关闭测试" });
  await dialogButton.focus();
  assert.equal(await hint.textContent(), "关闭测试");
  assert.equal(await hint.evaluate(el => el.parentElement.tagName), "DIALOG");
  await dialogButton.evaluate(el => el.remove());
  assert.equal(await page.locator(".icon-hint").evaluate(el => el.hidden), true,
    "Removing a focused control without a blur event must dismiss its hint");
  await page.evaluate(() => document.querySelector("dialog").remove());

  // A long press reveals the label without activating the control on release.
  await button.evaluate(el => {
    window.__hintClicks = 0;
    el.addEventListener("click", () => { window.__hintClicks++; });
    el.dispatchEvent(new PointerEvent("pointerdown", { bubbles: true, pointerType: "touch" }));
  });
  await page.waitForTimeout(550);
  assert.equal(await hint.textContent(), "收起侧栏");
  await button.evaluate(el => {
    el.dispatchEvent(new PointerEvent("pointerup", { bubbles: true, pointerType: "touch" }));
    el.click();
  });
  assert.equal(await page.evaluate(() => window.__hintClicks), 0);
  assert.deepEqual(errors, []);
  console.log("Icon hints: sidebar exit, removed control, window blur, hover, focus, Escape, title restoration, dialog and touch passed");
} finally {
  await browser.close();
}

const mobile = await createWorkbench({ url: new URL("/mobile.html", process.env.CAGE_UI_URL || "http://127.0.0.1:5198").href, waitForReady: false, viewport: { width: 390, height: 844 } });
try {
  const back = mobile.page.getByRole("link", { name: "返回工作台" });
  await back.focus();
  assert.equal(await mobile.page.getByRole("tooltip").textContent(), "返回工作台");
  assert.deepEqual(mobile.errors, []);
  console.log("Mobile entry: icon hint passed");
} finally { await mobile.browser.close(); }
