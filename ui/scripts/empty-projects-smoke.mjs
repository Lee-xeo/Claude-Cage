import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { createWorkbench } from "./workbench-fixture.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const vite = await createServer({ root, configFile: `${root}/vite.config.js`, server: { host: "127.0.0.1", port: 0 } });
await vite.listen();
const url = `http://127.0.0.1:${vite.httpServer.address().port}`;
try {
  for (const platform of ["darwin", "windows", "linux"]) {
    const { browser, page, state, errors } = await createWorkbench({ url,
      clientContext: { mode: "local", connection_id: "local", client_platform: platform, server_platform: platform, connection: { state: "disconnected" } },
      beforeNavigate: async (_page, state) => {
        Object.assign(state.status, { ready: true, vm_running: true, projects: [] });
        state.settings.workspace = "";
      },
    });
    try {
      assert.equal(await page.getByText("默认工作区", { exact: true }).count(), 0);
      await page.getByRole("button", { name: "打开项目", exact: true }).click();
      const dialog = page.getByRole("dialog", { name: "创建项目", exact: true });
      assert.equal(await dialog.getByRole("radio").count(), 0);
      await dialog.getByRole("button", { name: /选择文件夹/ }).click();
      await dialog.getByRole("button", { name: "创建项目", exact: true }).click();
      await dialog.waitFor({ state: "detached" });
      assert.deepEqual(state.status.projects, [state.chosenFolder]);
      await page.getByRole("region", { name: "项目 design-lab", exact: true }).waitFor();
      await page.reload();
      await page.getByRole("region", { name: "项目 design-lab", exact: true }).waitFor();
      assert.deepEqual(errors, []);
      console.log(`PASS ${platform} empty project list, folder chooser, explicit add, reload`);
    } finally { await browser.close(); }
  }
} finally { await vite.close(); }
