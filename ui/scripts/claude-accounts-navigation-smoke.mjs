import assert from "node:assert/strict";
import { mkdir } from "node:fs/promises";
import { createWorkbench } from "./workbench-fixture.mjs";

const capture = process.env.CAGE_TEST_CAPTURE;
if (capture) await mkdir(capture, { recursive: true });
for (const [platform, system] of [["darwin", "macos"], ["win32", "windows-11"], ["linux", "debian-13"]]) {
  const { page, state, browser, errors } = await createWorkbench({
    clientContext: { mode: "local", connection_id: "local", client_platform: platform,
      server_platform: platform, connection: { state: "disconnected" } },
    beforeNavigate: async (page, state) => {
      await page.addInitScript(value => Object.defineProperty(navigator, "platform", { get: () => value }),
        platform === "darwin" ? "MacIntel" : platform === "win32" ? "Win32" : "Linux x86_64");
      state.account.guest_system = state.status.guest_system = system;
      state.status.ready = state.status.vm_running = true;
      state.tabs = [{ id: "existing-web", kind: "web", title: "原有网页", url: "https://example.test/" }];
      state.active = "existing-web";
      state.terminals = [{ id: "existing-terminal", kind: "shell", title: "原有终端", workspace: state.settings.workspace }];
      const desktop = { installed: true, running: true, native_user_session: true, version: "4.0", screen: false, input: false };
      await page.route("**/api/claude-desktop", route => route.fulfill({ json: desktop }));
      await page.route("**/api/macos/claude/window", route => route.fulfill({ json: desktop }));
      await page.route("**/api/claude-accounts*", route => route.fulfill({ json: {
        accounts: [{ id: "navigation-account", email: "test@example.invalid", label: "测试账号", enabled: true, status: "ready" }],
        detection_status: "ready", scheduling: { mode: "balanced", switch_policy: "ask" },
      } }));
    },
  });
  try {
    const nav = page.getByRole("navigation", { name: "主导航" });
    const entry = nav.getByRole("button", { name: "Claude 账号管理", exact: true });
    const accountPage = page.getByRole("main", { name: "Claude 账号管理", exact: true });
    const sidebar = page.getByRole("complementary", { name: "项目与终端" });
    const openAccounts = async () => {
      await entry.click();
      await accountPage.waitFor();
      await accountPage.getByRole("article", { name: "测试账号", exact: true }).waitFor();
      assert.equal(await entry.getAttribute("aria-current"), "page");
      assert.ok(await sidebar.isVisible());
      assert.equal(await page.getByRole("dialog").count(), 0);
      assert.equal(await page.locator(".workbench-shell").getAttribute("inert"), null);
      const bounds = await accountPage.boundingBox(), side = await sidebar.boundingBox();
      assert.ok(bounds.x >= side.x + side.width - 1, "account page must stay beside the sidebar");
      assert.ok(await page.locator(".workspace").isHidden());
    };
    await nav.getByRole("button", { name: "打开浏览器", exact: true }).click();
    await page.locator(".address-bar input").first().waitFor();
    await openAccounts();
    await nav.getByRole("button", { name: "工作台", exact: true }).click();
    await accountPage.waitFor({ state: "hidden" });
    assert.equal(await entry.getAttribute("aria-current"), null);
    const terminal = page.getByRole("tab", { name: "原有终端", exact: true });
    await terminal.click();
    await page.locator(".terminal-lane:not([hidden]) .terminal-view").waitFor();
    const connections = state.terminalConnections;
    await openAccounts();
    await terminal.click();
    await accountPage.waitFor({ state: "hidden" });
    assert.equal(state.terminalConnections, connections, "page switching must preserve the terminal connection");
    await openAccounts();
    await nav.getByRole("button", { name: "打开浏览器", exact: true }).click();
    await page.locator(".address-bar input").first().waitFor();
    assert.equal(state.requests.filter(request => request.path === "/browser/new").length, 0);
    await openAccounts();
    await nav.getByRole("button", { name: "Claude Desktop", exact: true }).click();
    await page.locator(".claude-desktop-toolbar").waitFor();
    await openAccounts();
    await page.getByRole("button", { name: "设置", exact: true }).click();
    await page.getByRole("navigation", { name: "设置分组" }).waitFor();
    await page.locator("#settings-back").click();
    await accountPage.waitFor();
    await page.getByRole("button", { name: "收起侧栏", exact: true }).click();
    await accountPage.getByRole("button", { name: "展开侧栏", exact: true }).click();
    await sidebar.waitFor();
    if (capture) await page.screenshot({ path: `${capture}/${platform}.png` });
    await page.keyboard.press(platform === "darwin" ? "Meta+w" : "Control+w");
    await accountPage.waitFor({ state: "hidden" });
    assert.equal(state.requests.filter(request => /\/close$/.test(request.path)).length, 0,
      "closing the account page must preserve the previous terminal and browser");
    assert.deepEqual(errors, []);
    console.log(`${platform}: main-area account page, visible sidebar, active navigation, retained browser/terminal/Desktop, settings return and collapse/expand passed`);
  } finally { await browser.close(); }
}
