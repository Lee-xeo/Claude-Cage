import assert from "node:assert/strict";
import { createWorkbench } from "./workbench-fixture.mjs";

let failing = true, proxyRequests = 0;
const f = await createWorkbench({
  viewport: { width: 1544, height: 700 },
  initialization: { required: true, tracked: true, started: false },
  beforeNavigate: async (page, state) => {
    Object.assign(state.status, { ready: false, vm_running: false, job: {} });
    await page.route("**/api/environment/guest-systems", route => route.fulfill({ json: { fresh: true, available: false } }));
    await page.route("**/api/environment/storage", route => route.fulfill({ json: { directory: "/preview/walkthrough/state", has_environment: false } }));
    await page.route("**/api/environment/guest-profile", route => route.fulfill({ json: { editable: false } }));
    await page.route("**/api/proxy", async route => {
      proxyRequests++;
      // A status response arrives first, while the proxy endpoint remains slow
      // and unavailable. This reproduces the live walkthrough's HTTP 400.
      await new Promise(resolve => setTimeout(resolve, 180));
      return failing
        ? route.fulfill({ status: 400, body: "测试：代理设置暂不可读" })
        : route.fulfill({ json: { active: false, selected: "", sources: [], core_available: true } });
    });
  },
});
try {
  const { page } = f;
  const error = page.getByText(/读取代理设置失败/);
  await error.waitFor();
  await page.locator(".initialization-content").evaluate(async element => {
    await Promise.all(element.getAnimations({ subtree: true }).map(animation => animation.finished));
  });
  await page.locator(".initialization").evaluate(element => {
    element.scrollTop = element.scrollHeight;
    const notice = element.querySelector(".initialization-notice");
    window.pollingObservations = { removed: 0, heights: [element.scrollHeight], scroll: [element.scrollTop] };
    window.pollingObserver = new MutationObserver(() => {
      const result = window.pollingObservations;
      if (!notice.isConnected) result.removed++;
      result.heights.push(element.scrollHeight);
      result.scroll.push(element.scrollTop);
    });
    window.pollingObserver.observe(element, { childList: true, subtree: true });
  });
  const initialRequests = proxyRequests;
  while (proxyRequests < initialRequests + 3) await page.waitForResponse("**/api/proxy");
  await error.waitFor();
  const measured = await page.evaluate(() => {
    window.pollingObserver.disconnect();
    return window.pollingObservations;
  });
  console.log(JSON.stringify({ proxyRequests, ...measured }));
  assert.equal(measured.removed, 0, "an unchanged failure disappeared during a partial successful poll");
  assert.ok(Math.max(...measured.heights) - Math.min(...measured.heights) < 2, "polling changed the page height");
  assert.ok(Math.max(...measured.scroll) - Math.min(...measured.scroll) < 2, "polling moved the bottom scroll position");
  failing = false;
  await page.waitForResponse(response => response.url().endsWith("/api/proxy") && response.status() === 200);
  await error.waitFor({ state: "hidden" });
  assert.deepEqual(f.errors, []);
  console.log("PASS initialization polling: persistent failure keeps notice, layout and bottom position; complete success clears it");
} finally { await f.browser.close(); }
