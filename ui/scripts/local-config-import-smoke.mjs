import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

for (const platform of ["darwin", "windows", "linux"]) {
  let imports = 0, applies = 0, scans = 0, stale = true;
  let proxy = { active: false, selected: "", route: [], sources: [], port: 17896, running: false, core_available: true };
  const f = await createWorkbench({
    viewport: { width: platform === "linux" ? 540 : 1280, height: 850 },
    clientContext: { mode: "local", connection_id: "local", client_platform: platform, server_platform: platform },
    initialization: { required: true, tracked: true, started: false },
    beforeNavigate: async (page, state) => {
      await page.route("**/api/environment/guest-systems", route => route.fulfill({ json: { fresh: true, available: false } }));
      await page.route("**/api/environment/storage", route => route.fulfill({ json: { directory: "/preview/state", has_environment: false } }));
      await page.route("**/api/environment/guest-profile", route => route.fulfill({ json: { editable: false } }));
      await page.route("**/api/proxy", route => route.fulfill({ json: proxy }));
      await page.route("**/api/proxy/local-configs", route => {
        scans++;
        return route.fulfill({ json: { configs: platform === "linux" && scans === 1 ? [] : [{ id: `config-${scans}`, client: "Clash Verge Rev", name: "当前生效配置", current: true, nodes: 1, skipped: 0, refreshable: false }] } });
      });
      await page.route("**/api/proxy/import-local", async route => {
        imports++;
        assert.deepEqual(Object.keys(route.request().postDataJSON()), ["id"]);
        const job = { id: `import-${imports}`, kind: "proxy", running: true };
        state.status.job = job;
        setTimeout(() => {
          state.status.job = { ...job, running: false, ...(stale ? { error: "这份配置已经更新，请重新检测后再导入。" } : {}) };
          if (!stale) proxy = { ...proxy, sources: [{ id: "local-source", name: "Clash Verge Rev", nodes: [{ id: "independent", name: "独立出口", type: "ss" }], skipped: [] }] };
        }, 120);
        return route.fulfill({ json: job });
      });
      await page.route("**/api/proxy/route", route => {
        applies++;
        assert.deepEqual(route.request().postDataJSON().node_ids, ["independent"]);
        const job = { id: "apply", kind: "proxy", running: false };
        state.status.job = job; proxy = { ...proxy, active: true, selected: "independent", route: ["independent"], running: true };
        return route.fulfill({ json: job });
      });
    },
  });
  try {
    const { page } = f;
    await page.getByRole("button", { name: "下一步：配置代理", exact: true }).click();
    const local = page.getByRole("region", { name: "导入本机代理配置", exact: true });
    if (platform === "linux") {
      await local.getByText(/还没有找到可导入的配置/).waitFor();
      assert.ok(await page.getByRole("button", { name: "添加来源", exact: true }).isVisible());
      await local.getByRole("button", { name: "重新检测", exact: true }).click();
    }
    const button = local.getByRole("button", { name: "导入此配置", exact: true });
    await button.waitFor();
    await button.evaluate(button => { button.click(); button.click(); });
    await page.getByRole("alert").filter({ hasText: "这份配置已经更新" }).waitFor();
    assert.equal(imports, 1, "double click must submit only one job");
    assert.equal(proxy.sources.length, 0);
    await local.getByRole("button", { name: "重新检测", exact: true }).click();
    stale = false;
    await button.click();
    await local.getByRole("button", { name: "已导入", exact: true }).waitFor();
    assert.equal(imports, 2);
    assert.equal(applies, 0, "import must leave exit selection explicit");
    assert.ok(await page.getByRole("button", { name: "开始安装", exact: true }).isDisabled());
    await page.getByRole("button", { name: "选择节点 独立出口", exact: true }).click();
    await page.getByRole("button", { name: "应用代理", exact: true }).click();
    await page.getByRole("button", { name: "开始安装", exact: true }).waitFor({ state: "visible" });
    await page.waitForFunction(() => [...document.querySelectorAll("button")].some(button => button.textContent === "开始安装" && !button.disabled));
    assert.equal(applies, 1);
    assert.equal(await page.evaluate(() => document.documentElement.scrollWidth > innerWidth), false);
    if (process.env.CAGE_LOCAL_CONFIG_EVIDENCE) await page.screenshot({ path: `${process.env.CAGE_LOCAL_CONFIG_EVIDENCE}/${platform}.png`, fullPage: true });
    assert.deepEqual(f.errors, []);
    console.log(`PASS ${platform}: discovery, stale import recovery, duplicate-click protection, independent node import and explicit application`);
  } finally { await f.browser.close(); }
}
