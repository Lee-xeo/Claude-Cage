import assert from "node:assert/strict";
import { fileURLToPath } from "node:url";
import { createServer } from "vite";
import { createWorkbench } from "./workbench-fixture.mjs";

const root = fileURLToPath(new URL("..", import.meta.url));
const vite = await createServer({ root, configFile: `${root}/vite.config.js`, server: { host: "127.0.0.1", port: 0 } });
await vite.listen();
const url = `http://127.0.0.1:${vite.httpServer.address().port}`;
try {
  for (const [platform, navigatorPlatform, modifier] of [
    ["darwin", "MacIntel", "Meta"],
    ["windows", "Win32", "Control"],
    ["linux", "Linux x86_64", "Control"],
  ]) {
    const fixture = await createWorkbench({
      url,
      clientContext: { mode: "local", connection_id: "local", client_platform: platform, server_platform: platform, connection: { state: "disconnected" } },
    });
    const { browser, page, state, errors } = fixture;
    try {
      await page.addInitScript(value => Object.defineProperty(navigator, "platform", { value }), navigatorPlatform);
      const projectA = "/projects/alpha";
      const projectB = "/projects/beta";
      Object.assign(state.status, { ready: true, vm_running: true, projects: [projectA, projectB], workspace: projectA });
      state.settings.workspace = projectA;
      await page.reload();
      await page.getByRole("region", { name: "项目 beta" }).waitFor();
      const alphaRegion = page.getByRole("region", { name: "项目 alpha", exact: true });
      const betaRegion = page.getByRole("region", { name: "项目 beta", exact: true });
      // Project heading action bar keeps only New Terminal + More (...).
      assert.equal(await alphaRegion.locator(".project-heading > .icon-button").count(), 2);
      assert.equal(await alphaRegion.getByRole("button", { name: "打开 alpha 的编辑器" }).count(), 0);
      const header = page.locator("header.terminal-chrome");
      const checkHeader = async () => {
        assert.equal(await header.getByRole("button", { name: "打开文件", exact: true }).count(), 0);
        assert.equal(await header.getByRole("button", { name: "新建终端", exact: true }).count(), 0);
      };
      await checkHeader();
      // Open file/editor from ... menu.
      await alphaRegion.getByRole("button", { name: "alpha 的操作", exact: true }).click();
      const actionMenu = page.getByRole("menu", { name: "alpha 的操作", exact: true });
      await actionMenu.getByRole("menuitem", { name: "打开文件", exact: true }).click();
      await page.getByRole("tab", { name: "文件 · alpha" }).waitFor();
      assert.equal(state.tabs.find(tab => tab.kind === "editor").workspace, projectA);
      // Pin beta via right-click context menu on the project disclosure.
      await betaRegion.locator(".project-disclosure").click({ button: "right" });
      await page.getByRole("menu", { name: "beta 的操作", exact: true }).getByRole("menuitem", { name: "置顶", exact: true }).click();
      assert.deepEqual(
        await page.locator("[data-sidebar-project]").evaluateAll(nodes => nodes.map(node => node.dataset.sidebarProject)),
        [projectB, projectA],
      );
      await page.reload();
      await page.getByRole("region", { name: "项目 beta", exact: true }).waitFor();
      assert.deepEqual(
        await page.locator("[data-sidebar-project]").evaluateAll(nodes => nodes.map(node => node.dataset.sidebarProject)),
        [projectB, projectA],
      );
      assert.equal(await page.locator(`[data-sidebar-project="${projectB}"]`).getAttribute("data-project-pinned"), "true");
      // Quick rename beta from ... menu.
      await page.getByRole("button", { name: "beta 的操作", exact: true }).click();
      await page.getByRole("menuitem", { name: "重命名", exact: true }).click();
      const renameDialog = page.getByRole("dialog", { name: "重命名项目", exact: true });
      await renameDialog.getByLabel("项目名称", { exact: true }).fill("Beta 项目");
      await renameDialog.getByRole("button", { name: "保存名称", exact: true }).click();
      await page.getByRole("region", { name: "项目 Beta 项目", exact: true }).waitFor();
      // Direct edit from ... menu opens Edit Project without back-to-list button.
      await page.getByRole("button", { name: "Beta 项目 的操作", exact: true }).click();
      await page.getByRole("menuitem", { name: "编辑项目", exact: true }).click();
      const editDialog = page.getByRole("dialog", { name: "编辑项目", exact: true });
      assert.equal(await editDialog.getByRole("button", { name: "返回已有项目", exact: true }).count(), 0);
      await editDialog.getByLabel("项目名称", { exact: true }).fill("beta");
      await editDialog.getByRole("button", { name: "保存项目", exact: true }).click();
      await page.getByRole("region", { name: "项目 beta", exact: true }).waitFor();
      // Unpin beta restores original order.
      await page.getByRole("button", { name: "beta 的操作", exact: true }).click();
      await page.getByRole("menuitem", { name: "取消置顶", exact: true }).click();
      assert.deepEqual(
        await page.locator("[data-sidebar-project]").evaluateAll(nodes => nodes.map(node => node.dataset.sidebarProject)),
        [projectA, projectB],
      );
      // Direct remove from ... menu respects active editor guard, then succeeds when closed.
      await page.getByRole("button", { name: "alpha 的操作", exact: true }).click();
      await page.getByRole("menuitem", { name: "移除项目", exact: true }).click();
      let removeDialog = page.getByRole("dialog", { name: "移除项目", exact: true });
      await removeDialog.getByRole("button", { name: "确认移除项目", exact: true }).click();
      await removeDialog.getByRole("alert").waitFor();
      await removeDialog.getByRole("button", { name: "取消", exact: true }).click();
      await page.getByRole("button", { name: "打开浏览器", exact: true }).click();
      await page.getByRole("button", { name: "关闭 文件 · alpha", exact: true }).click();
      await page.getByRole("dialog").getByRole("button", { name: "关闭编辑器", exact: true }).click();
      await page.getByRole("button", { name: "alpha 的操作", exact: true }).click();
      await page.getByRole("menuitem", { name: "移除项目", exact: true }).click();
      removeDialog = page.getByRole("dialog", { name: "移除项目", exact: true });
      await removeDialog.getByRole("button", { name: "确认移除项目", exact: true }).click();
      await page.getByRole("region", { name: "项目 alpha", exact: true }).waitFor({ state: "detached" });
      await page.getByRole("button", { name: "在 beta 新建终端" }).click();
      await page.getByRole("menuitem", { name: "普通终端" }).click();
      await page.getByRole("tab", { name: "终端 1" }).waitFor();
      assert.equal(state.terminals[0].workspace, projectB);
      await checkHeader();
      await page.getByRole("button", { name: "收起侧栏" }).click();
      await page.keyboard.press(`${modifier}+Shift+Enter`);
      await page.getByRole("menuitem", { name: "普通终端" }).click();
      await page.waitForFunction(() => document.querySelector("header.terminal-chrome .current-title")?.textContent === "终端 2");
      assert.equal(state.terminals[1].workspace, projectB);
      await page.evaluate(() => window.cageNativeAction("newTerminal"));
      await page.getByRole("menuitem", { name: "Claude 终端" }).waitFor();
      await page.keyboard.press("Escape");
      assert.equal(await page.locator(":focus").getAttribute("title"), projectB);
      // Rename works even when the environment is stopped.
      Object.assign(state.status, { ready: false, vm_running: false });
      await page.reload();
      await page.getByRole("button", { name: "展开侧栏", exact: true }).click();
      await page.getByRole("button", { name: "beta 的操作", exact: true }).click();
      assert.equal(await page.getByRole("menuitem", { name: "打开文件", exact: true }).isDisabled(), true);
      assert.equal(await page.getByRole("menuitem", { name: "编辑项目", exact: true }).isDisabled(), true);
      assert.equal(await page.getByRole("menuitem", { name: "重命名", exact: true }).isDisabled(), false);
      assert.equal(await page.getByRole("menuitem", { name: "移除项目", exact: true }).isDisabled(), false);
      await page.getByRole("menuitem", { name: "重命名", exact: true }).click();
      const offlineRename = page.getByRole("dialog", { name: "重命名项目", exact: true });
      await offlineRename.getByLabel("项目名称", { exact: true }).fill("Beta 离线改名");
      await offlineRename.getByRole("button", { name: "保存名称", exact: true }).click();
      await page.getByRole("region", { name: "项目 Beta 离线改名", exact: true }).waitFor();
      assert.deepEqual(errors, []);
      console.log(`PASS ${platform}: 项目操作栏（新建终端+更多菜单）、置顶、重命名、编辑、移除、右键菜单和快捷键`);
    } finally {
      await browser.close();
    }
  }
} finally {
  await vite.close();
}
