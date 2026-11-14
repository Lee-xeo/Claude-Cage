import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

for (const platform of ["darwin", "windows", "linux"]) {
  for (const install of [false, true]) {
    let gitRequests = 0;
    const f = await createWorkbench({
      initialization: { required: install, tracked: install },
      waitForReady: !install,
      clientContext: { mode: "local", connection_id: "local", client_platform: platform, server_platform: platform },
      beforeNavigate: async page => {
        if (install) await page.route("**/api/environment/storage", route => route.fulfill({ json: { directory: "/preview/cage-state", has_environment: false } }));
        await page.route("**/api/host/git", route => { gitRequests++; return route.fulfill({ status: 503, body: "Git is unavailable" }); });
      },
    });
    try {
      const { page, state, errors } = f;
      if (install) {
        await page.getByRole("button", { name: "下一步：配置代理" }).click();
        await page.getByRole("button", { name: "开始安装", exact: true }).click();
      } else {
        await page.getByRole("button", { name: "启动 Cage 环境", exact: true }).click();
      }
      await page.getByRole(install ? "button" : "heading", { name: install ? "进入工作台" : "Cage 环境已就绪", exact: true }).waitFor();
      assert.equal(state.requests.filter(request => request.path === "/start").length, 1);
      assert.equal(state.requests.some(request => request.path === "/host/git"), false);
      assert.equal(await page.getByText(/启用.*Git.*保护|保护本机 Git/).count(), 0);
      await page.reload();
      assert.equal(state.requests.some(request => request.path === "/host/git"), false);
      assert.equal(gitRequests, 0);
      assert.deepEqual(errors, []);
      console.log(`PASS: ${platform} ${install ? "installation" : "startup"} without host Git checks or writes`);
    } finally { await f.browser.close(); }
  }
}
