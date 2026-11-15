import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { createWorkbench } from "./workbench-fixture.mjs";

for (const system of ["macos", "windows-11", "debian"]) {
  const calls = [];
  let moved = false;
  let failInventory = false;
  const fixture = await createWorkbench({ beforeNavigate: async (page, state) => {
    state.account.guest_system = state.status.guest_system = system;
    state.status.ready = state.status.vm_running = true;
    await page.route("**/api/environment/cache/**", async route => {
      const action = new URL(route.request().url()).pathname.split("/").at(-1);
      const input = route.request().postDataJSON();
      calls.push({ action, input });
      if (action === "inventory") {
        if (failInventory) return route.fulfill({ status: 409, contentType: "text/plain; charset=utf-8", body: "测试：请重新盘点，已有资料保留。" });
        return route.fulfill({ json: {
          version: 1, plan_id: "fixture-preview", scope: "fixture-scope", platform: system,
          items: [
            { id: "cli-default", title: "Claude Code · 默认账户缓存", location: "~/.claude/cache", bytes: 1024, files: 1, state: moved ? "absent" : "ready", reason: "可再生成缓存；保留恢复副本。" },
            { id: "browser-0", title: "浏览器 · Cache", location: "浏览器专用资料/Cache", bytes: 2048, files: 2, state: "blocked", reason: "相关客户端正在运行，缓存已保留。" },
          ],
          protected: [{ title: "账户、会话与自定义设置", present: 4, unknown: 0 }],
          batches: moved ? [{ id: "a".repeat(32), created_at: 1794600000, count: 1 }] : [],
          notice: "仅检查元数据，凭据与会话正文不读取。",
        } });
      }
      if (action === "clean") {
        assert.deepEqual(input, { plan_id: "fixture-preview", items: ["cli-default"] });
        moved = true;
        return route.fulfill({ json: { version: 1, status: "complete", outcomes: [{ id: "cli-default", state: "moved", reason: "已移入恢复批次。" }] } });
      }
      assert.equal(action, "restore");
      assert.equal(input.batch, "a".repeat(32));
      return route.fulfill({ json: { version: 1, status: "partial", outcomes: [{ id: "cli-default", state: "failed", reason: "原位置已生成新数据，恢复不会覆盖它。" }] } });
    });
  } });
  const { page, browser, errors } = fixture;
  try {
    await page.getByRole("button", { name: "设置", exact: true }).click();
    await page.getByRole("navigation", { name: "设置分组" }).getByRole("button", { name: "环境与恢复", exact: true }).click();
    const panel = page.getByRole("region", { name: "环境清理" });
    await panel.getByRole("button", { name: "只读盘点", exact: true }).waitFor();
    assert.equal(calls.length, 0, "Opening settings must not inspect or clean");
    await panel.getByRole("button", { name: "只读盘点", exact: true }).click();
    const cli = panel.getByRole("checkbox", { name: "Claude Code · 默认账户缓存", exact: true });
    await cli.waitFor();
    assert.equal(await cli.isChecked(), false);
    assert.equal(await panel.getByRole("checkbox", { name: "浏览器 · Cache", exact: true }).isDisabled(), true);
    await mkdir("output/cache-maintenance", { recursive: true });
    await panel.screenshot({ path: `output/cache-maintenance/${system}.png` });
    await cli.check();
    await panel.getByRole("button", { name: "清理所选缓存（1）", exact: true }).click();
    let dialog = page.getByRole("dialog", { name: "清理所选缓存？", exact: true });
    await dialog.waitFor();
    assert.match(await dialog.innerText(), /账户、登录、会话、项目和持久磁盘保留/);
    await dialog.getByRole("button", { name: "取消", exact: true }).click();
    assert.deepEqual(calls.map(call => call.action), ["inventory"]);
    await panel.getByRole("button", { name: "清理所选缓存（1）", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "清理所选缓存？", exact: true });
    await dialog.getByRole("button", { name: "清理缓存", exact: true }).click();
    await panel.getByRole("status").getByText("缓存清理完成，恢复副本已保留", { exact: true }).waitFor();
    await panel.getByRole("button", { name: "恢复此批缓存", exact: true }).click();
    dialog = page.getByRole("dialog", { name: "恢复这批缓存？", exact: true });
    await dialog.getByRole("button", { name: "恢复缓存", exact: true }).click();
    await panel.getByText("部分项目未完成，请查看原因", { exact: true }).waitFor();
    await panel.getByText(/原位置已生成新数据/).first().waitFor();
    failInventory = true;
    await panel.getByRole("button", { name: "重新盘点", exact: true }).click();
    await panel.getByRole("alert").filter({ hasText: "请重新盘点" }).waitFor();
    assert.equal(await panel.getByRole("checkbox").count(), 0, "Failed inventory must not retain stale selections");
    assert.deepEqual(calls.map(call => call.action), ["inventory", "clean", "inventory", "restore", "inventory", "inventory"]);
    assert.deepEqual(errors, []);
    console.log(JSON.stringify({ system, passed: true, actions: calls.map(call => call.action) }));
  } finally { await browser.close(); }
}
