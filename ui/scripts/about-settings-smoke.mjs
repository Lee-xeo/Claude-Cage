import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createWorkbench } from "./workbench-fixture.mjs";

const output = new URL("../../output/about-settings/", import.meta.url);
await fs.mkdir(output, { recursive: true });
for (const platform of ["darwin", "windows", "linux"]) {
  const build = { version: "0.1.7", commit: "a".repeat(40), modified: true };
  const remoteContext = { mode: "remote", connection_id: "remote-test", instance_id: "test-instance", client_platform: platform, server_platform: "linux", connection: { state: "connected" } };
  const nativeCalls = [];
  let checks = 0, pending, finish;
  let result = {
    build, source: { status: "available", commit: "b".repeat(40), aheadBy: 5 },
    release: { status: "available", version: "0.2.0-rc.1", prerelease: true }, checkedAt: new Date().toISOString(),
  };
  const fixture = await createWorkbench({
    clientContext: remoteContext,
    clientRequest: async (route, path) => {
      if (path === "/client/context") return route.fulfill({ json: remoteContext });
      if (path !== "/client/about") return route.fulfill({ json: {} });
      if (route.request().method() === "GET") return route.fulfill({ json: build });
      checks++;
      if (pending) await pending;
      return route.fulfill({ json: result });
    },
    nativeRequest: async body => { nativeCalls.push(body); return body.action === "appReference" ? true : null; },
  });
  const { page, browser, errors } = fixture;
  try {
    await page.getByRole("button", { name: "设置", exact: true }).click();
    await page.getByRole("navigation", { name: "设置分组" }).getByRole("button", { name: "关于", exact: true }).click();
    const about = page.getByRole("region", { name: "关于 Cage", exact: true });
    await about.getByText("0.1.7", { exact: true }).waitFor();
    await about.getByText("aaaaaaaaaaaa", { exact: true }).waitFor();
    await about.getByText(/优先通过主机网络与系统代理/).waitFor();
    assert.equal(checks, 0, "opening About must not trigger an online check");
    assert.ok(await about.getByText(/包含未提交的修改/).isVisible());
    assert.equal(await page.getByRole("button", { name: "保存设置", exact: true }).count(), 0);
    pending = new Promise(resolve => { finish = resolve; });
    await about.getByRole("button", { name: "检测更新", exact: true }).click();
    await about.getByRole("status").filter({ hasText: "正在核对" }).waitFor();
    assert.ok(await about.getByRole("button", { name: "正在检查…", exact: true }).isDisabled());
    finish(); pending = null;
    await about.getByText("主分支新增 5 个提交", { exact: true }).waitFor();
    await about.getByText("0.2.0-rc.1 · 预发行版", { exact: true }).waitFor();
    assert.equal(checks, 1);
    await about.getByRole("link", { name: "查看源码提交" }).click();
    await about.getByRole("link", { name: "查看发行说明与下载" }).click();
    assert.deepEqual(nativeCalls.filter(call => call.action === "appReference").map(call => call.kind), ["source", "releases"]);
    await page.screenshot({ path: fileURLToPath(new URL(`${platform}-available.png`, output)), fullPage: true });

    result = { ...result, source: { status: "failed", error: "源码检查连接中断" }, release: { status: "current", version: "0.1.7" } };
    await about.getByRole("button", { name: "检测更新", exact: true }).click();
    await about.getByText("源码检查连接中断", { exact: true }).waitFor();
    await about.getByText("已是最新发行版（含预发行）", { exact: true }).waitFor();
    assert.equal(await about.getByText("主分支新增 5 个提交", { exact: true }).count(), 0, "old results must clear on retry");

    result = { ...result, build: { version: "0.1.7", modified: false }, source: { status: "unknown", commit: "b".repeat(40) } };
    await about.getByRole("button", { name: "检测更新", exact: true }).click();
    await about.getByText("此构建缺少提交信息，无法核对是否最新", { exact: true }).waitFor();
    await about.getByText("未记录", { exact: true }).waitFor();

    await page.clock.install();
    result = { ...result, source: { status: "current", commit: "a".repeat(40) }, release: { status: "failed", error: "更新源请求受限，请稍后重试", retryAt: new Date(Date.now()+120000).toISOString() } };
    await about.getByRole("button", { name: "检测更新", exact: true }).click();
    await about.getByText("已与主分支同步", { exact: true }).waitFor();
    await about.getByRole("button", { name: "等待重试", exact: true }).waitFor();
    assert.ok(await about.getByRole("button", { name: "等待重试", exact: true }).isDisabled());
    await page.clock.fastForward(121000);
    await about.getByRole("button", { name: "检测更新", exact: true }).waitFor();
    assert.equal(checks, 4, "cooldown must not schedule automatic checks");
    result = { ...result, source: { status: "diverged", commit: "b".repeat(40) }, release: { status: "ahead", version: "0.1.6" } };
    await about.getByRole("button", { name: "检测更新", exact: true }).click();
    await about.getByText("当前构建与主分支存在分叉，请核对差异", { exact: true }).waitFor();
    await about.getByText("当前版本高于最新发行版", { exact: true }).waitFor();
    assert.equal(await page.evaluate(() => document.querySelector(".settings-content").scrollWidth > document.querySelector(".settings-content").clientWidth), false);
    assert.deepEqual(errors, []);
    console.log(`PASS: ${platform} About version, manual source/release checks, prerelease, native links, partial failure, unknown commit, cooldown, retry`);
  } finally { await browser.close(); }
}
