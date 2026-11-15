// Replay IME direct commits against the workbench and inspect what the shell receives.
import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

const inputs = [];
const { browser, page, errors } = await createWorkbench({
  mockWebSockets: false,
  async beforeNavigate(page) {
    await page.routeWebSocket("**/api/**", socket => {
      if (!socket.url().includes("/terminals/")) return socket.close();
      socket.onMessage(message => {
        const { type, data } = JSON.parse(message);
        if (type === "input") {
          inputs.push(data);
          if (data === "\r") socket.send(Buffer.from("INPUT_DONE"));
        }
      });
      socket.send(Buffer.from("$ "));
    });
  },
});
try {
  await page.getByRole("button", { name: "启动 Cage 环境", exact: true }).click();
  await page.getByRole("button", { name: "新建终端", exact: true }).first().click();
  await page.getByRole("menuitem", { name: "普通终端", exact: true }).click();
  await page.getByText("$", { exact: true }).first().waitFor();
  const textarea = page.locator(".xterm-helper-textarea");
  await textarea.focus();
  await textarea.evaluate(async el => {
    const key = (type, key, keyCode) => el.dispatchEvent(new KeyboardEvent(type, {
      key, keyCode, shiftKey: true, bubbles: true, cancelable: true, composed: true,
    }));
    const commit = data => {
      const init = { inputType: "insertText", data, bubbles: true, composed: true };
      el.dispatchEvent(new InputEvent("beforeinput", init));
      el.value += data;
      el.dispatchEvent(new InputEvent("input", init));
    };
    for (const [order, mark] of [
      ["input-first", "（"],
      ["keydown-first", "）"],
      ["modifier-after-keydown", "？"],
    ]) {
      if (order === "modifier-after-keydown") key("keydown", "Process", 229);
      key("keydown", "Shift", 16);
      if (order === "keydown-first") key("keydown", "Process", 229);
      commit(mark);
      if (order === "input-first") key("keydown", "Process", 229);
      // xterm's keyCode 229 handler reads the textarea on its next timer.
      await new Promise(resolve => setTimeout(resolve, 0));
      key("keyup", "Process", 229);
      key("keyup", "Shift", 16);
    }
  });
  await page.keyboard.press("a");
  await page.keyboard.press("Enter");
  await page.locator(".xterm-rows").getByText(/INPUT_DONE/).waitFor();
  assert.deepEqual(inputs, ["（", "）", "？", "a", "\r"]);
  assert.deepEqual(errors, []);
  console.log("IME direct commits and ordinary input reached the shell once per key.");
} finally { await browser.close(); }
