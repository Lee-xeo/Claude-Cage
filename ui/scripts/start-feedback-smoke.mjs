import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

const { browser, page, state, errors } = await createWorkbench();
let releaseStart;
const startGate = new Promise(resolve => { releaseStart = resolve; });
let starts = 0;
const loading = page.getByRole("button", { name: "正在处理…", exact: true });
const assertLoading = async () => {
  await loading.waitFor({ timeout: 500 });
  assert.equal(await loading.isDisabled(), true);
  assert.equal(await page.getByRole("dialog").count(), 0);
  await page.getByRole("heading", { name: "正在准备 Cage 环境", exact: true }).waitFor({ timeout: 500 });
};
try {
  await page.route("**/api/start", async route => {
    starts++;
    await startGate;
    await route.fulfill({ json: { id: "delayed-start" } });
  });
  const startRequested = page.waitForRequest("**/api/start");
  await page.getByRole("button", { name: "启动 Cage 环境", exact: true }).click();
  await startRequested;
  await assertLoading();
  const startResponse = page.waitForResponse("**/api/start");
  releaseStart();
  await startResponse;
  // An older idle status must not re-enable the button after acceptance.
  await page.waitForResponse("**/api/status");
  await assertLoading();
  assert.equal(starts, 1);
  state.status.job = { id: "delayed-start", kind: "start", running: true, message: "正在准备测试环境…" };
  await page.locator(".welcome-subtitle").getByText("正在准备测试环境…", { exact: true }).waitFor();
  await assertLoading();
  state.status.job = { ...state.status.job, running: false, error: "测试启动失败" };
  await page.getByRole("button", { name: "启动 Cage 环境", exact: true }).waitFor();

  await page.route("**/api/start", route => route.fulfill({ status: 503, contentType: "text/plain; charset=utf-8", body: "测试请求失败" }));
  await page.getByRole("button", { name: "启动 Cage 环境", exact: true }).click();
  await page.getByText("测试请求失败", { exact: true }).waitFor();
  assert.equal(await page.getByRole("button", { name: "启动 Cage 环境", exact: true }).isEnabled(), true);

  assert.equal(state.requests.some(request => request.path === "/host/git"), false);
  assert.deepEqual(errors, []);
  console.log("PASS: immediate feedback; direct start without Git checks; continuous loading across delayed requests and stale polling; job handoff; failure allows retry.");
} finally {
  releaseStart();
  await browser.close();
}
