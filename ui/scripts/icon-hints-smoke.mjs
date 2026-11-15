import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

const { browser, page, errors } = await createWorkbench();
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
  console.log("Icon hints: hover, focus, Escape, title restoration, dialog and touch passed");
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
