import assert from "node:assert/strict";
import http from "node:http";
import { createRequire } from "node:module";
import { readFavicon } from "../../internal/cage/browser-favicon.mjs";
import { createWorkbench } from "./workbench-fixture.mjs";

const require = createRequire(import.meta.url);
const { chromium } = require(process.env.CAGE_PLAYWRIGHT_MODULE || "playwright");
const guest = await chromium.launch({ executablePath: process.env.CAGE_TEST_BROWSER_PATH });
const guestPage = await guest.newPage();
const png = Buffer.from((await guestPage.evaluate(() => {
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 64;
  const ctx = canvas.getContext("2d");
  ctx.fillStyle = "#da7756"; ctx.fillRect(0, 0, 64, 64);
  return canvas.toDataURL();
})).split(",")[1], "base64");
const svg = '<svg xmlns="http://www.w3.org/2000/svg" width="32" height="32"><circle cx="16" cy="16" r="15" fill="#277da8"/></svg>';
const site = http.createServer((req, res) => {
  if (req.url === "/icon.svg") { res.setHeader("Content-Type", "image/svg+xml"); res.end(svg); return; }
  if (["/icon.png", "/favicon.ico"].includes(req.url)) { res.setHeader("Content-Type", "image/png"); res.end(png); return; }
  if (req.url === "/missing") { res.writeHead(404); res.end(); return; }
  res.setHeader("Content-Type", "text/html");
  res.setHeader("Content-Security-Policy", `default-src 'self'; img-src 'self' http://localhost:${site.address().port}; connect-src 'self' http://localhost:${site.address().port}`);
  const href = req.url === "/svg" ? `http://localhost:${site.address().port}/icon.svg` : req.url === "/fallback" ? "/missing" : "/icon.png";
  res.end(`<link rel="icon" href="${href}"><title>Favicon fixture</title>`);
});
await new Promise(resolve => site.listen(0, "127.0.0.1", resolve));
const cdp = await guestPage.context().newCDPSession(guestPage);
const origin = `http://127.0.0.1:${site.address().port}`;
let ui;
try {
  await cdp.send("Page.enable");
  const read = async (path) => {
    await guestPage.goto(origin + path);
    return readFavicon((method, params) => cdp.send(method, params), "fixture-session");
  };
  const icon = await read("/png");
  assert.match(icon, /^data:image\/png;base64,/);
  const bytes = Buffer.from(icon.split(",")[1], "base64");
  assert.equal(bytes.readUInt32BE(16), 32);
  assert.equal(bytes.readUInt32BE(20), 32);
  const vector = await read("/svg");
  assert.equal(Buffer.from(vector.split(",")[1], "base64").toString(), svg, "cross-origin SVG must work without CORS or host requests");
  assert.match(await read("/fallback"), /^data:image\/png;base64,/, "missing declared icon falls back to /favicon.ico");

  ui = await createWorkbench();
  const { page, state, errors } = ui;
  state.status.ready = state.status.vm_running = true;
  state.tabs = [
    { id: "png", kind: "web", title: "Raster icon", url: origin, favicon: icon },
    { id: "svg", kind: "web", title: "Vector icon", url: origin + "/svg", favicon: vector },
    { id: "missing", kind: "web", title: "Missing icon", url: origin },
    { id: "broken", kind: "web", title: "Broken icon", url: origin, favicon: "data:image/png;base64,YnJva2Vu" },
    { id: "external", kind: "web", title: "External URL", url: origin, favicon: "https://example.invalid/icon.png" },
  ];
  state.active = "png";
  const externalRequests = [];
  page.on("request", request => { if (request.url().includes("example.invalid")) externalRequests.push(request.url()); });
  await page.reload();
  await page.getByRole("button", { name: "打开浏览器", exact: true }).click();
  const tab = title => page.getByRole("tab", { name: title, exact: true });
  for (const title of ["Raster icon", "Vector icon"])
    await tab(title).locator("img").evaluate(image => image.decode());
  for (const title of ["Missing icon", "Broken icon", "External URL"])
    await tab(title).locator("svg").waitFor();
  await page.getByRole("button", { name: "搜索标签页", exact: true }).click();
  const search = page.getByRole("dialog", { name: "搜索标签页" });
  await search.getByRole("button", { name: "Broken icon", exact: true }).locator("svg").waitFor();
  assert.equal(await search.locator("img.tab-favicon").count(), 2);
  await page.keyboard.press("Escape");
  state.tabs[0].favicon = vector;
  state.tabs[3].favicon = vector;
  await page.waitForFunction(value => document.querySelector('[data-tab-id="png"] img')?.src === value, vector);
  await tab("Broken icon").locator("img").evaluate(image => image.decode());
  assert.deepEqual(externalRequests, []);
  assert.deepEqual(errors, []);
  console.log("PASS: real Chromium PNG/SVG/favicon fallback, 32px raster normalization, tab/search rendering, broken-icon fallback and no host icon requests");
} finally {
  await ui?.browser.close();
  await guest.close();
  await new Promise(resolve => site.close(resolve));
}
