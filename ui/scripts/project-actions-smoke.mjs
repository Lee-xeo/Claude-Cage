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
    const projectA = "/projects/alpha";
    const projectB = "/projects/beta";
    const fixture = await createWorkbench({
      url,
      clientContext: { mode: "local", connection_id: "local", client_platform: platform, server_platform: platform, connection: { state: "disconnected" } },
      cliRequest: route => route.fulfill({ json: {
        sessions: [{ id: "alpha-history", title: "已有会话", workspace: projectA }],
      } }),
    });
    const { browser, page, state, errors } = fixture;
    try {
      await page.addInitScript(value => Object.defineProperty(navigator, "platform", { value }), navigatorPlatform);
      Object.assign(state.status, { ready: true, vm_running: true, projects: [projectA, projectB], workspace: projectA });
      state.settings.workspace = projectA;
      await page.reload();
      const nextProject = page.getByRole("region", { name: "项目 beta" });
      const alpha = page.getByRole("region", { name: "项目 alpha" });
      const alphaDisclosure = alpha.getByRole("button", { name: "alpha", exact: true });
      await page.getByRole("button", { name: "已有会话", exact: true }).waitFor();
      await alphaDisclosure.click();
      await alpha.locator(".project-children").evaluate(el => Promise.all(el.getAnimations().map(animation => animation.finished)));
      const trigger = page.getByRole("button", { name: "在 alpha 新建终端" });
      const popup = page.getByRole("menu", { name: "新建终端" });
      const nextBounds = await nextProject.boundingBox();
      await trigger.click();
      const anchor = await trigger.boundingBox();
      const popupBounds = await popup.boundingBox();
      assert.ok(popupBounds.y >= anchor.y + anchor.height);
      assert.ok(popupBounds.y + popupBounds.height > nextBounds.y, "menu should overlap the next project");
      assert.equal(await popup.evaluate((el, y) => {
        const bounds = el.getBoundingClientRect();
        return el.contains(document.elementFromPoint(bounds.left + bounds.width / 2, Math.max(bounds.top, y) + 4));
      }, nextBounds.y), true, "menu should render in front of the project list");
      assert.deepEqual(await nextProject.boundingBox(), nextBounds, "menu moved the next project");
      assert.equal(await alphaDisclosure.getAttribute("aria-expanded"), "false", "menu expanded a collapsed project");

      await page.keyboard.press("ArrowDown");
      assert.equal(await page.locator(":focus").textContent(), "Claude 终端");
      await page.keyboard.press("Enter");
      const form = page.getByRole("group", { name: "新建 Claude Code 终端" });
      await form.waitFor();
      await form.getByRole("combobox", { name: "启动账号" }).press("Enter");
      await form.getByRole("listbox").waitFor();
      assert.deepEqual(await nextProject.boundingBox(), nextBounds, "account options moved the next project");
      await page.keyboard.press("Escape");
      await form.getByRole("listbox").waitFor({ state: "hidden" });
      await page.keyboard.press("Escape");
      await form.waitFor({ state: "hidden" });
      assert.equal(await trigger.evaluate(el => el === document.activeElement), true);

      const viewport = page.viewportSize();
      await page.setViewportSize({ ...viewport, height: Math.floor(anchor.y + anchor.height + 60) });
      await trigger.scrollIntoViewIfNeeded();
      await trigger.click();
      const above = await popup.boundingBox();
      assert.ok(above.y + above.height <= (await trigger.boundingBox()).y, "menu must open above an anchor near the bottom");
      await page.keyboard.press("Escape");
      await page.setViewportSize(viewport);
      await trigger.click();
      await page.getByRole("button", { name: "工作台", exact: true }).click();
      await popup.waitFor({ state: "hidden" });

      const header = page.locator("header.terminal-chrome");
      const checkHeader = async () => {
        assert.equal(await header.getByRole("button", { name: "打开文件", exact: true }).count(), 0);
        assert.equal(await header.getByRole("button", { name: "新建终端", exact: true }).count(), 0);
      };
      await checkHeader();
      await page.getByRole("button", { name: "打开 alpha 的编辑器" }).click();
      await page.getByRole("tab", { name: "文件 · alpha" }).waitFor();
      assert.equal(state.tabs.find(tab => tab.kind === "editor").workspace, projectA);
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
      assert.deepEqual(errors, []);
      console.log(`PASS ${platform}: 项目入口、标题栏和收起侧栏后的快捷键`);
    } finally {
      await browser.close();
    }
  }
} finally {
  await vite.close();
}
