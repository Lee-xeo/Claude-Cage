import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { createWorkbench } from "./workbench-fixture.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const vite = await createServer({ root, configFile: `${root}/vite.config.js`, server: { host: "127.0.0.1", port: 0 } });
await vite.listen();
const url = `http://127.0.0.1:${vite.httpServer.address().port}`;
try {
  for (const [host, accountSystem, statusSystem, label] of [
    ["windows", "macos", undefined, "macOS"],
    ["darwin", "windows-11", "windows-11", "Windows"],
    ["darwin", undefined, undefined, "Linux"],
  ]) {
    const { browser, page, state, errors } = await createWorkbench({
      url,
      clientContext: { mode: "local", connection_id: "local", client_platform: host, server_platform: host },
    });
    try {
      state.account.guest_system = accountSystem;
      state.status.guest_system = statusSystem;
      await page.reload();
      const sidebar = page.getByRole("complementary", { name: "项目与终端" });
      const icon = sidebar.getByRole("img", { name: `客户机系统：${label}` });
      await icon.waitFor();
      assert.equal(await icon.getAttribute("title"), `客户机系统：${label}`);
      const iconBox = await icon.boundingBox();
      const brandBox = await sidebar.locator(".sidebar-brand").boundingBox();
      const collapseBox = await sidebar.getByRole("button", { name: "收起侧栏" }).boundingBox();
      assert.ok(iconBox.x > brandBox.x && iconBox.x + iconBox.width < collapseBox.x);
      if (process.env.CAGE_TEST_CAPTURE) await page.screenshot({ path: `${process.env.CAGE_TEST_CAPTURE}-${label}.png` });
      await page.reload();
      await icon.waitFor();
      assert.deepEqual(errors, []);
      console.log(`PASS 客机 ${label}，客户端 ${host}`);
    } finally { await browser.close(); }
  }
} finally { await vite.close(); }
