// Real workbench UI with isolated desktop-instances fixture. No request reaches personal data.
import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

const base = process.env.CAGE_UI_URL || "http://127.0.0.1:5178";
const { browser, page, state, errors } = await createWorkbench({ url: base });

const accountA = "a".repeat(24);
const instanceA = "b".repeat(24);
const pendingB = "c".repeat(24);

const accounts = [
  { id: accountA, account_id: accountA, number: 1, label: "工作账号", running: false, dedicated: false },
];
let instances = [
  { id: "default", number: 0, label: "默认 Desktop", running: true, dedicated: false },
  { id: instanceA, account_id: accountA, number: 1, label: "工作账号", running: false, dedicated: false },
  { id: pendingB, number: 0, label: "待登录 Desktop", running: false, dedicated: false },
];
let defaultStopped = 0;
let failDelete = false;
const deletedIDs = [];

try {
  state.status.ready = state.status.vm_running = true;
  state.status.account_desktops_supported = true;
  state.status.desktop_catalog_revision = 1;

  await page.route("**/api/claude-desktop/stop", async (route) => {
    defaultStopped++;
    const def = instances.find((item) => item.id === "default");
    if (def) def.running = false;
    return route.fulfill({ json: { id: "job-default-stop", running: false } });
  });

  await page.route("**/api/desktop-instances**", async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace("/api", "");
    if (path === "/desktop-instances" && req.method() === "GET") {
      return route.fulfill({ json: { supported: true, accounts, instances, redirects: {} } });
    }
    const metaMatch = path.match(/^\/desktop-instances\/([^/]+)\/metadata$/);
    if (metaMatch && req.method() === "POST") {
      const id = metaMatch[1];
      const { label } = req.postDataJSON();
      const target = instances.find((item) => item.id === id);
      if (!target) return route.fulfill({ status: 404, contentType: "text/plain", body: "Not found" });
      target.label = label.trim();
      return route.fulfill({ json: target });
    }
    const actionMatch = path.match(/^\/desktop-instances\/([^/]+)\/(start|stop|vnc-credentials)$/);
    if (actionMatch) {
      const [, id, action] = actionMatch;
      const target = instances.find((item) => item.id === id);
      if (!target) return route.fulfill({ status: 404, contentType: "text/plain", body: "Not found" });
      if (action === "vnc-credentials") return route.fulfill({ json: { password: "fixture1" } });
      target.running = action === "start";
      return route.fulfill({ json: { id: "job-" + action, running: false } });
    }
    const deleteMatch = path.match(/^\/desktop-instances\/([^/]+)$/);
    if (deleteMatch && req.method() === "DELETE") {
      const id = deleteMatch[1];
      if (failDelete) {
        return route.fulfill({ status: 502, contentType: "text/plain", body: "无法清理客机内的 Desktop 实例数据，暂未删除记录，请重试" });
      }
      deletedIDs.push(id);
      instances = instances.filter((item) => item.id !== id);
      return route.fulfill({ json: { ok: true } });
    }
    return route.fallback();
  });

  await page.reload();
  await page.getByRole("button", { name: "Claude Desktop", exact: true }).click();

  // Default desktop supports rename and never shows delete.
  const defaultButton = page.getByRole("button", { name: "默认 Desktop", exact: true });
  await defaultButton.waitFor();
  await defaultButton.hover();
  const defaultActions = page.getByRole("group", { name: "默认 Desktop 的操作" });
  await defaultActions.getByRole("button", { name: "重命名", exact: true }).waitFor();
  assert.equal(await defaultActions.getByRole("button", { name: "删除", exact: true }).count(), 0);
  await defaultActions.getByRole("button", { name: "关闭", exact: true }).click();
  const stopDefaultDialog = page.getByRole("dialog", { name: "关闭 默认 Desktop？" });
  await stopDefaultDialog.getByRole("button", { name: "关闭应用", exact: true }).click();
  await stopDefaultDialog.waitFor({ state: "hidden" });
  assert.equal(defaultStopped, 1);
  await defaultButton.hover();
  assert.equal(await defaultActions.getByRole("button", { name: "关闭", exact: true }).count(), 0);
  await defaultActions.getByRole("button", { name: "重命名", exact: true }).click();
  const renameDialog = page.getByRole("dialog", { name: "重命名 Desktop" });
  await renameDialog.getByRole("textbox", { name: "Desktop 名称" }).fill("主力桌面");
  await renameDialog.getByRole("button", { name: "保存名称", exact: true }).click();
  await renameDialog.waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "主力桌面", exact: true }).waitFor();

  // Start the bound instance so its pane is open, then verify sidebar collapse/expand and rename.
  await page.getByRole("button", { name: "#01 工作账号 · 未启动", exact: true }).click();
  const pane = page.locator(`#desktop-pane-${instanceA}`);
  await pane.waitFor();
  await page.getByRole("button", { name: "收起侧栏", exact: true }).click();
  await page.getByRole("button", { name: "展开侧栏", exact: true }).click();
  const runningRowButton = page.getByRole("button", { name: "#01 工作账号 · 运行中", exact: true });
  await runningRowButton.focus();
  await page.keyboard.press("Shift+F10");
  const instanceActions = page.getByRole("group", { name: "#01 工作账号 的操作" });
  await instanceActions.getByRole("button", { name: "重命名", exact: true }).click();
  await renameDialog.getByRole("textbox", { name: "Desktop 名称" }).fill("研发专用");
  await renameDialog.getByRole("button", { name: "保存名称", exact: true }).click();
  await renameDialog.waitFor({ state: "hidden" });
  await page.getByRole("button", { name: "#01 研发专用 · 运行中", exact: true }).waitFor();
  assert.match(await pane.locator("header").innerText(), /研发专用/);

  // Delete flow: cancel keeps instance, error shows inside anchored dialog and keeps instance, retry deletes and closes pane.
  const renamedRowButton = page.getByRole("button", { name: "#01 研发专用 · 运行中", exact: true });
  await renamedRowButton.hover();
  const renamedActions = page.getByRole("group", { name: "#01 研发专用 的操作" });
  await renamedActions.getByRole("button", { name: "删除", exact: true }).click();
  const deleteDialog = page.getByRole("dialog", { name: "删除 #01 研发专用？" });
  await deleteDialog.waitFor();
  await deleteDialog.getByRole("button", { name: "取消", exact: true }).click();
  await deleteDialog.waitFor({ state: "hidden" });
  assert.equal(deletedIDs.length, 0);

  await renamedRowButton.hover();
  await renamedActions.getByRole("button", { name: "删除", exact: true }).click();
  failDelete = true;
  await deleteDialog.getByRole("button", { name: "删除实例", exact: true }).click();
  await deleteDialog.getByRole("alert").filter({ hasText: "无法清理客机内的 Desktop 实例数据" }).waitFor();
  assert.equal(deletedIDs.length, 0);

  failDelete = false;
  await deleteDialog.getByRole("button", { name: "删除实例", exact: true }).click();
  await deleteDialog.waitFor({ state: "hidden" });
  await pane.waitFor({ state: "hidden" });
  assert.deepEqual(deletedIDs, [instanceA]);
  assert.equal(await page.getByRole("button", { name: /研发专用/ }).count(), 0);

  // Pending desktop can also be deleted directly.
  const pendingButton = page.getByRole("button", { name: "待登录 Desktop · 未启动", exact: true });
  await pendingButton.hover();
  await page.getByRole("group", { name: "待登录 Desktop 的操作" }).getByRole("button", { name: "删除", exact: true }).click();
  const pendingDelete = page.getByRole("dialog", { name: "删除 待登录 Desktop？" });
  await pendingDelete.getByRole("button", { name: "删除实例", exact: true }).click();
  await pendingDelete.waitFor({ state: "hidden" });
  assert.deepEqual(deletedIDs, [instanceA, pendingB]);
  assert.equal(await page.getByRole("button", { name: /待登录 Desktop/ }).count(), 0);

  // Account remains available when creating a new Desktop.
  await page.getByRole("button", { name: "新建 Desktop", exact: true }).click();
  await page.getByRole("combobox", { name: "Desktop 所属账号" }).click();
  await page.getByRole("option", { name: "#01 工作账号" }).waitFor();

  assert.deepEqual(errors, []);
  console.log("PASS: desktop instance rename, Shift+F10 actions, anchored delete with cancel/retry, pane close, and account preservation");
} finally {
  await browser.close();
}
