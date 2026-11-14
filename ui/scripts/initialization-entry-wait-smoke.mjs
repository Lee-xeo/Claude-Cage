// Real headless browser, isolated mock API: no guest, installation or Cage service.
// Start the UI on loopback, then set CAGE_UI_URL, CAGE_PLAYWRIGHT_MODULE and
// CAGE_TEST_BROWSER_PATH as needed. CAGE_INITIALIZATION_EVIDENCE selects output.
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { setTimeout as delay } from "node:timers/promises";
import { createWorkbench } from "./workbench-fixture.mjs";

const url = process.env.CAGE_UI_URL || "http://127.0.0.1:5198";
const origin = new URL(url).origin;
assert.ok(["127.0.0.1", "localhost", "[::1]"].includes(new URL(url).hostname), "use a loopback UI fixture only");
const evidenceRoot = process.env.CAGE_INITIALIZATION_EVIDENCE || fileURLToPath(new URL("../../_output/entry-fix/ui/", import.meta.url));
const evidence = path.join(evidenceRoot, `entry-wait-${new Date().toISOString().replace(/[:.]/g, "-")}`);
await fs.mkdir(evidence, { recursive: true });
const report = {
  evidenceType: "real-headless-browser-with-mocked-api",
  scope: "Windows initialization entry UI only; no real VM, backend lock, installation or installed-app acceptance",
  url,
  scenarios: [],
};

async function runScenario(name, firstWaitMs, rejectFirst = false) {
  const result = { name, firstWaitMs, rejectFirst, requests: [], samples: [], screenshots: [] };
  report.scenarios.push(result);
  const blockedRequests = [];
  let fixture;
  let completeCount = 0;
  let pending;
  const timeoutMessage = "等待后台检查结束超时，请稍后重试进入工作台。";
  const screenshot = async (label) => {
    const filename = path.join(evidence, `${name}-${label}.png`);
    await fixture.page.screenshot({ path: filename, fullPage: true });
    result.screenshots.push(filename);
  };
  try {
    fixture = await createWorkbench({
      url,
      initialization: { required: true, tracked: true, started: true },
      clientContext: { mode: "local", connection_id: "local", client_platform: "windows", server_platform: "windows" },
      beforeNavigate: async (page, state) => {
        state.account.guest_system = "windows-11";
        Object.assign(state.status, {
          guest_system: "windows-11", ready: true, vm_running: true,
          safety: { gate: { mode: "open", until: new Date(Date.now() + 3600000).toISOString() } },
        });
        await page.addInitScript(() => localStorage.setItem("cage.audio", JSON.stringify({ muted: true })));
        // Assets may reach only the loopback UI. All /api calls fall through to
        // the existing fixture routes, never to Vite's backend proxy.
        await page.route("**/*", route => {
          const requestURL = new URL(route.request().url());
          if (requestURL.origin !== origin) {
            blockedRequests.push(requestURL.href);
            return route.abort();
          }
          if (requestURL.pathname.startsWith("/api/")) return route.fallback();
          return route.continue();
        });
        await page.route("**/api/environment/initialization", async route => {
          if (route.request().method() === "GET") return route.fulfill({ json: state.initialization });
          assert.deepEqual(route.request().postDataJSON(), { action: "complete" });
          const attempt = { number: ++completeCount, startedAt: Date.now(), responseStatus: null };
          result.requests.push(attempt);
          await new Promise(resolve => { pending = { attempt, release: resolve }; });
          attempt.waitedMs = Date.now() - attempt.startedAt;
          if (rejectFirst && attempt.number === 1) {
            attempt.responseStatus = 409;
            return route.fulfill({ status: 409, contentType: "text/plain; charset=utf-8", body: timeoutMessage });
          }
          attempt.responseStatus = 200;
          state.initialization.required = false;
          return route.fulfill({ json: state.initialization });
        });
      },
    });
    const { page } = fixture;
    result.browserVersion = fixture.browser.version();
    page.setDefaultTimeout(5000);
    const entryButton = () => page.getByRole("button", { name: "进入工作台", exact: true });
    const waitingButton = () => page.getByRole("button", { name: "正在进入…", exact: true });
    const workbench = () => page.getByRole("complementary", { name: "项目与终端" });
    await entryButton().waitFor();
    await page.getByRole("heading", { name: "Cage 已准备就绪", exact: true }).waitFor();
    await screenshot("ready");

    async function attemptEntry(waitMs, attemptNumber) {
      // Same-tick click burst exercises the synchronous duplicate-submit guard,
      // before a React render can make the native button disabled.
      await entryButton().evaluate(button => { button.click(); button.click(); button.click(); });
      await waitingButton().waitFor();
      const deadline = Date.now() + 5000;
      while ((!pending || pending.attempt.number !== attemptNumber) && Date.now() < deadline) await delay(10);
      assert.equal(pending?.attempt.number, attemptNumber, "entry must submit exactly the expected request");
      const activeRequest = pending;
      await waitingButton().evaluate(button => { button.click(); button.click(); });
      const box = await waitingButton().boundingBox();
      assert.ok(box, "waiting button remains present");
      await page.mouse.click(box.x + box.width / 2, box.y + box.height / 2, { clickCount: 2 });
      await screenshot(`waiting-${attemptNumber}`);
      do {
        const sample = await page.evaluate(() => {
          const button = [...document.querySelectorAll("button")].find(item => item.textContent === "正在进入…");
          return {
            buttonPresent: Boolean(button),
            disabled: button?.disabled,
            ariaBusy: button?.getAttribute("aria-busy"),
            spinner: Boolean(button?.querySelector("svg.spin")),
            readyTitle: document.querySelector("h1")?.textContent,
            readyPanel: Boolean(document.querySelector(".initialization-ready")),
            waitingHeading: document.querySelector(".initialization-ready h2")?.textContent,
            waitingStatus: document.querySelector(".initialization-ready [role=status]")?.textContent,
            stopInstallation: [...document.querySelectorAll("button")].some(item => item.textContent === "停止安装"),
            installationOperation: Boolean(document.querySelector(".initialization-operation")),
            previousDisabled: [...document.querySelectorAll("button")].find(item => item.textContent === "上一步")?.disabled,
            workbenchPresent: Boolean(document.querySelector('[aria-label="项目与终端"]')),
          };
        });
        const elapsedMs = Date.now() - activeRequest.attempt.startedAt;
        result.samples.push({ attemptNumber, elapsedMs, ...sample });
        assert.deepEqual(sample, {
          buttonPresent: true, disabled: true, ariaBusy: "true", spinner: true,
          readyTitle: "Cage 已准备就绪", readyPanel: true,
          waitingHeading: "正在进入工作台",
          waitingStatus: "正在等待后台检查结束并确认环境状态，请稍候…",
          stopInstallation: false, installationOperation: false,
          previousDisabled: true, workbenchPresent: false,
        }, `entry waiting UI at ${elapsedMs}ms`);
        assert.equal(completeCount, attemptNumber, "double clicks must not submit another complete request");
        assert.equal(fixture.state.initialization.required, true, "initialization must stay required while complete is pending");
        if (elapsedMs >= waitMs) break;
        await delay(Math.min(200, waitMs - elapsedMs));
      } while (true);
      await screenshot(`waiting-${attemptNumber}-held`);
      activeRequest.release();
      pending = null;
    }

    await attemptEntry(firstWaitMs, 1);
    if (rejectFirst) {
      await page.getByRole("alert").filter({ hasText: timeoutMessage }).waitFor();
      await entryButton().waitFor();
      assert.equal(await entryButton().isDisabled(), false, "timeout must allow explicit retry");
      assert.equal(await entryButton().getAttribute("aria-busy"), "false");
      assert.equal(await entryButton().locator("svg.spin").count(), 0);
      assert.equal(await workbench().count(), 0, "409 must not enter the workbench");
      assert.equal(completeCount, 1, "timeout must not retry automatically");
      assert.equal(fixture.state.initialization.required, true);
      await screenshot("timeout-retryable");
      await attemptEntry(1000, 2);
    }
    await workbench().waitFor();
    assert.equal(completeCount, rejectFirst ? 2 : 1);
    assert.equal(await page.locator(".initialization").count(), 0);
    assert.ok(result.requests[0].waitedMs >= firstWaitMs);
    assert.equal(fixture.state.initialization.required, false);
    assert.deepEqual(fixture.errors, []);
    assert.deepEqual(blockedRequests, [], "no external asset or API requests are allowed");
    const unsafeCalls = fixture.state.requests.filter(request => ["/start", "/stop", "/safety/check"].includes(request.path));
    assert.deepEqual(unsafeCalls, [], "entry must not install, stop or restart checks");
    await screenshot("workbench");
    result.passed = true;
    console.log(`PASS ${name}: ${result.requests.map(request => `${request.waitedMs}ms HTTP ${request.responseStatus}`).join(", ")}; ${result.samples.length} waiting-state samples; one complete per explicit attempt`);
  } catch (error) {
    result.passed = false;
    result.error = error.stack || String(error);
    if (fixture) await screenshot("failure").catch(() => {});
    throw error;
  } finally {
    pending?.release();
    if (fixture) await fixture.browser.close();
    result.blockedRequests = blockedRequests;
    await fs.writeFile(path.join(evidence, "report.json"), JSON.stringify(report, null, 2) + "\n");
  }
}

await runScenario("delayed-success-6s", 6000);
await runScenario("delayed-success-10s", 10000);
await runScenario("timeout-409-retry", 6000, true);
console.log(`Evidence: ${evidence}`);
