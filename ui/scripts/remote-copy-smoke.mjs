// Real pointer events and RFB clipboard packets; host clipboard writes are recorded.
import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";
import { startDisplay } from "./rfb-wire-fixture.mjs";

const display = await startDisplay(process.env.CAGE_UI_URL || "http://127.0.0.1:5198");
const writes = [];
const { browser, page, state, errors } = await createWorkbench({
  mockWebSockets: false,
  url: display.url,
  nativeRequest: (request) => {
    if (request.action === "clipboardWrite") writes.push(request.text);
    return true;
  },
});
const command = "sudo apt update && sudo apt upgrade";
const canvas = page.locator(".remote-canvas canvas");
async function received(text) {
  display.clipboard(text);
  // The existing transfer panel exposes the cached remote text after receipt.
  await page.getByRole("button", { name: "文本传递", exact: true }).click();
  const field = page.getByRole("dialog").locator("textarea");
  await field.waitFor();
  assert.equal(await field.inputValue(), text);
  await page.getByRole("dialog").getByRole("button", { name: "关闭", exact: true }).click();
}
try {
  state.status.vm_running = state.status.ready = true;
  await page.reload();
  await page.getByRole("button", { name: "打开浏览器", exact: true }).click();
  await canvas.waitFor();
  await page.waitForFunction(() => document.querySelector(".remote-text-input"));
  await received("background clipboard");
  assert.deepEqual(writes, []);

  await canvas.click({ position: { x: 150, y: 150 } });
  display.clipboard(command);
  await page.getByText("已复制到本机", { exact: true }).waitFor();
  assert.deepEqual(writes, [command]);

  await received("unsolicited clipboard");
  assert.deepEqual(writes, [command], "one click must only export one update");

  await canvas.click({ position: { x: 150, y: 150 } });
  await page.getByRole("textbox", { name: "网页地址或搜索" }).focus();
  await received("local field clipboard");
  assert.deepEqual(writes, [command], "local fields must cancel click export");

  await canvas.click({ position: { x: 150, y: 150 } });
  await page.keyboard.press("Meta+c");
  await page.getByText("正在复制所选文本…", { exact: true }).waitFor();
  display.clipboard("keyboard selection");
  await page.getByText("已复制到本机", { exact: true }).waitFor();
  assert.deepEqual(writes, [command, "keyboard selection"]);
  assert.deepEqual(errors, []);
  console.log("PASS: real pointer copy, one-shot RFB clipboard export, background/local-field isolation and Command+C");
} finally {
  await browser.close();
  await display.close();
}
