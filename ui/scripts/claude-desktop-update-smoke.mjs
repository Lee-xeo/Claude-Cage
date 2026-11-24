import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

const { browser, page, state, errors } = await createWorkbench();
const desktop = { installed: true, running: true, version: "4.4212.0", failed: false };
const actions = [];
let failUpdate = true;
await page.route("**/api/claude-desktop**", async route => {
  const path = new URL(route.request().url()).pathname;
  if (path.endsWith("/vnc-credentials")) return route.fulfill({ json: { password: "fixture-only" } });
  if (route.request().method() === "GET") return route.fulfill({ json: desktop });
  if (path.endsWith("/resize")) return route.fulfill({ json: { ok: true } });
  const action = path.split("/").at(-1);
  actions.push(action);
  const job = { id: `desktop-${actions.length}`, kind: `claude-desktop-${action}`, running: false, message: "已完成" };
  if (action === "update") {
    desktop.running = false;
    if (failUpdate) job.error = "测试：下载失败，登录资料和已安装版本保留";
    else desktop.version = "4.4213.0";
  }
  if (action === "start") desktop.running = true;
  state.status.job = job;
  return route.fulfill({ json: job });
});
try {
  state.status.vm_running = state.status.ready = true;
  state.status.guest_system = "debian-13";
  await page.route("**/api/environment/versions", route => route.fulfill({ json: {
    vm_running: true, controller: { version: "0.2.0" }, components: { status: "current", update_required: false },
    tools: { claude: { current: "1.0.0", required: "1.0.0" } }, tools_update_allowed: true,
  } }));
  await page.reload();
  await page.getByRole("button", { name: /^设置/ }).click();
  await page.getByRole("button", { name: "环境与恢复", exact: true }).click();
  const versions = page.getByRole("region", { name: "环境版本" });
  await versions.getByText("v4.4212.0", { exact: true }).waitFor();
  await versions.getByRole("button", { name: "更新 Claude Desktop", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "取消", exact: true }).click();
  assert.deepEqual(actions, [], "cancelling must preserve the running application");
  assert.equal(desktop.running, true);
  await versions.getByRole("button", { name: "更新 Claude Desktop", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "更新应用", exact: true }).click();
  await versions.getByRole("alert").filter({ hasText: "下载失败" }).waitFor();
  assert.deepEqual(actions, ["update"], "failed update must not start a partial installation");
  assert.equal(desktop.version, "4.4212.0");
  failUpdate = false;
  await versions.getByRole("button", { name: "更新 Claude Desktop", exact: true }).click();
  await versions.getByText("v4.4213.0", { exact: true }).waitFor();
  assert.deepEqual(actions, ["update", "update", "start"]);
  assert.equal(desktop.installed, true);
  assert.equal(desktop.running, true);
  assert.deepEqual(errors, []);
  console.log("PASS: settings version entry, Linux update confirmation/cancel, failure preservation, retry/reopen");
} finally {
  await browser.close();
}
