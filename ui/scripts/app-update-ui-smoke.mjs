import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { createWorkbench } from "./workbench-fixture.mjs";

const output = process.env.CAGE_UPDATE_QA_OUTPUT || "/tmp/cage-update-diagnosis/ui";
await fs.mkdir(output, { recursive: true });
const calls = [];
const references = [];
let update = { phase: "idle", currentVersion: "0.1.3-alpha.1" };
const fixture = await createWorkbench({ nativeRequest: async body => {
  if (body.action === "appReference") { references.push(body.kind); return true; }
  if (body.action !== "appUpdate") return body.action === "workbenchReady" ? true : null;
  calls.push(body.operation);
  if (body.operation === "check") update = { ...update, phase: "current", message: "已是最新版本" };
  if (body.operation === "install") update = { ...update, phase: "downloading", receivedBytes: 50, totalBytes: 100 };
  if (body.operation === "complete") update = { ...update, phase: "installing" };
  return update;
} });
const { page, browser, errors } = fixture;
const publish = async value => {
  update = value;
  await page.evaluate(detail => window.dispatchEvent(new CustomEvent("cage-app-update", { detail })), value);
};
const count = operation => calls.filter(value => value === operation).length;
try {
  await page.clock.install();
  calls.length = 0;
  update = { phase: "idle", currentVersion: "0.1.3-alpha.1" };
  await page.reload();
  await page.getByRole("heading", { name: "先启动 Cage 环境" }).waitFor();
  await page.exposeFunction("updateCheckCount", () => count("check"));
  await page.waitForFunction(async () => await window.updateCheckCount() === 1);
  await page.waitForFunction(() => !document.querySelector(".app-update-button"));
  assert.deepEqual(calls, ["status", "check"], "startup must check without a user click");
  await page.clock.fastForward(6 * 60 * 60 * 1000 + 60000);
  await page.waitForFunction(async () => await window.updateCheckCount() === 2);
  await page.waitForFunction(() => !document.querySelector(".app-update-button"));
  assert.equal(count("check"), 2, "periodic checks run while the window is visible");
  await publish({ phase: "failed", currentVersion: update.currentVersion, message: "所选代理未运行" });
  await page.locator(".app-update-button").waitFor({ state: "hidden" });
  assert.equal(await page.locator(".app-update-button").count(), 0, "background check failure must not advertise an update");
  await page.clock.fastForward(5 * 60 * 1000 + 60000);
  await page.waitForFunction(async () => await window.updateCheckCount() === 3);
  assert.equal(count("check"), 3, "a failed background check retries after the proxy can recover");
  await publish({ phase: "available", currentVersion: "0.1.3-alpha.1", latestVersion: "0.1.3-alpha.2" });
  const button = page.getByRole("button", { name: "下载更新", exact: true });
  await button.waitFor();
  assert.match(await button.getAttribute("title"), /0.1.3-alpha.2.*先下载并验证.*保存工作.*手动停止环境/);
  await page.screenshot({ path: `${output}/available.png` });
  await button.click();
  await page.getByRole("button", { name: "正在下载更新 50%", exact: true }).waitFor();
  assert.equal(count("install"), 1);
  assert.equal(await page.getByRole("progressbar", { name: "更新下载进度" }).getAttribute("aria-valuenow"), "50");
  assert.equal(await page.locator(".app-update-percent").innerText(), "50%");
  assert.equal(await page.getByRole("dialog", { name: "Cage 更新" }).count(), 0);
  assert.ok(await page.getByRole("button", { name: "正在下载更新 50%", exact: true }).isDisabled());
  await page.screenshot({ path: `${output}/downloading.png` });
  await publish({ ...update, receivedBytes: 110 });
  await page.getByRole("button", { name: "正在下载更新 100%", exact: true }).waitFor();
  await publish({ ...update, totalBytes: 0 });
  assert.equal(await page.getByRole("progressbar").getAttribute("aria-valuenow"), null, "unknown totals must stay indeterminate");
  await publish({ ...update, phase: "verifying" });
  await page.getByRole("button", { name: "正在验证更新", exact: true }).waitFor();
  assert.equal(fixture.state.requests.filter(request => request.path === "/stop").length, 0, "downloading and verification must leave the environment running");
  await publish({ ...update, phase: "ready" });
  const prompt = page.getByRole("dialog", { name: "停止环境并安装更新？" });
  await prompt.waitFor();
  await prompt.screenshot({ path: `${output}/stop-before-install.png`, animations: "disabled" });
  assert.equal(count("complete"), 0, "download completion must wait for the user");
  await prompt.getByRole("button", { name: "取消", exact: true }).click();
  await prompt.waitFor({ state: "hidden" });
  assert.equal(count("complete"), 0, "cancel must preserve the prepared update without installing");
  const resume = page.getByRole("button", { name: "安装已下载的更新", exact: true });
  await resume.click();
  await prompt.waitFor();
  assert.equal(count("install"), 1, "resuming a verified update must not download again");
  let stopping = true;
  let stopFailure = "停止失败，请重试";
  let stopID = 0;
  let refuseStop = true;
  await page.route("**/api/stop", route => {
    if (refuseStop) return route.fulfill({ status: 409, contentType: "text/plain; charset=utf-8", body: "当前任务尚未结束，请稍后停止" });
    stopID++;
    return route.fulfill({ json: { id: `update-stop-${stopID}` } });
  });
  await page.route("**/api/status", route => route.fulfill({ json: {
    ...fixture.state.status, vm_running: stopping,
    job: { id: `update-stop-${stopID}`, kind: "stop", running: stopping, error: stopFailure },
  } }));
  await prompt.getByRole("button", { name: "保存并停止环境后安装" }).click();
  await prompt.getByRole("alert").filter({ hasText: "当前任务尚未结束" }).waitFor();
  assert.equal(count("complete"), 0, "a rejected stop request must remain retryable without installing");
  refuseStop = false;
  await prompt.getByRole("button", { name: "重试停止并安装" }).click();
  await prompt.getByRole("status").waitFor();
  assert.equal(count("complete"), 0, "installation must wait for shutdown confirmation");
  stopping = false;
  await page.clock.fastForward(1000);
  await prompt.getByRole("alert").filter({ hasText: "停止失败" }).waitFor();
  assert.equal(count("complete"), 0, "a failed stop must prevent installation");
  stopFailure = "";
  stopping = true;
  await prompt.getByRole("button", { name: "重试停止并安装" }).click();
  await prompt.getByRole("status").waitFor();
  await prompt.getByRole("button", { name: "稍后安装" }).click();
  stopping = false;
  await page.clock.fastForward(1000);
  assert.equal(count("complete"), 0, "cancelling during shutdown prevents subsequent installation");
  await resume.click();
  await prompt.getByRole("button", { name: "保存并停止环境后安装" }).click();
  await page.getByRole("button", { name: "正在安装更新，完成后自动重启", exact: true }).waitFor();
  await prompt.waitFor({ state: "hidden" });
  assert.equal(count("complete"), 1, "install only after this window's successful stop");
  await publish({ ...update, phase: "failed", message: "下载中断，请检查代理后重试", retryable: true });
  await page.getByRole("alert").filter({ hasText: "下载中断" }).waitFor();
  const retry = page.getByRole("button", { name: "重试更新", exact: true });
  await retry.waitFor();
  await retry.click();
  await page.getByRole("button", { name: "正在下载更新 50%", exact: true }).waitFor();
  assert.equal(count("install"), 2);
  await page.getByRole("button", { name: "收起侧栏", exact: true }).click();
  assert.equal(await page.locator(".app-update-button").count(), 1, "collapsed sidebar keeps one progress indicator");
  await publish({ phase: "current", currentVersion: "0.1.3-alpha.2" });
  await page.locator(".app-update-button").waitFor({ state: "hidden" });
  assert.equal(await page.locator(".app-update-button").count(), 0, "a current version leaves no update affordance");
  await publish({ phase: "manual", currentVersion: "0.1.3-alpha.2" });
  await page.locator(".app-update-button").waitFor({ state: "hidden" });
  assert.equal(await page.locator(".app-update-button").count(), 0, "system package updates must not offer an automatic installation");
  await publish({ phase: "manual", currentVersion: "0.1.3-alpha.2", latestVersion: "0.1.3-alpha.3", message: "Linux 版本使用系统安装包更新。" });
  const view = page.getByRole("button", { name: "查看新版本", exact: true });
  await view.waitFor();
  assert.match(await view.getAttribute("title"), /0.1.3-alpha.3.*系统安装包.*发布页面/);
  const installs = count("install");
  await view.click();
  for (let attempt = 0; attempt < 50 && !references.length; attempt++) await new Promise(resolve => setTimeout(resolve, 100));
  assert.deepEqual(references, ["releases"], "a system package update opens the release page");
  assert.equal(count("install"), installs, "a system package update must not start an automatic installation");
  assert.deepEqual(errors, []);
  const result = { checks: ["automatic startup and periodic checks", "failed background check retry", "latest version hidden", "download before confirmation", "ring and percentage", "indeterminate progress", "manual stop card, cancellation and resume without download", "stop failure blocks install; retry waits for shutdown", "visible failure and retry", "collapsed sidebar", "Linux manual update remains separate", "Linux newer package opens the release page"], calls };
  await fs.writeFile(`${output}/checks.json`, JSON.stringify(result, null, 2));
  console.log(JSON.stringify(result));
} finally { await browser.close(); }

const recovered = await createWorkbench({ nativeRequest: async body => {
  if (body.action !== "appUpdate") return null;
  return body.operation === "status"
    ? { phase: "failed", message: "新版本工作台未能就绪，已恢复原应用", retryable: true }
    : { phase: "current", currentVersion: "0.1.3-alpha.1" };
} });
try {
  await recovered.page.getByRole("alert").filter({ hasText: "已恢复原应用" }).waitFor();
  assert.equal(await recovered.page.locator(".app-update-button").count(), 0, "rollback failure stays visible without advertising an unavailable update");
  assert.deepEqual(recovered.errors, []);
  console.log("PASS: persisted update failure remains visible while the startup check completes");
} finally { await recovered.browser.close(); }

// The desktop update must never stop the selected cloud environment.
const remoteCalls = [];
const remote = await createWorkbench({
  clientContext: { mode: "remote", connection_id: "cloud-test", host_id: "cloud-host", connection: { state: "connected" } },
  nativeRequest: async body => {
    if (body.action !== "appUpdate") return null;
    remoteCalls.push(body.operation);
    return { phase: "ready", currentVersion: "0.1.0", latestVersion: "0.2.0" };
  },
});
try {
  const dialog = remote.page.getByRole("dialog", { name: "停止环境并安装更新？" });
  await dialog.getByRole("button", { name: "保存并停止环境后安装" }).click();
  await dialog.getByRole("alert").filter({ hasText: "先切回本机工作台" }).waitFor();
  assert.equal(remote.state.requests.filter(request => request.path === "/stop").length, 0);
  assert.ok(!remoteCalls.includes("complete"), "a cloud selection cannot authorize stopping or installing the local app");
  assert.deepEqual(remote.errors, []);
  console.log("PASS: application update never stops the selected cloud environment");
} finally { await remote.browser.close(); }
