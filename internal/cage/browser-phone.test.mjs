import assert from "node:assert/strict";
import { once } from "node:events";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import net from "node:net";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { randomUUID } from "node:crypto";
import { setTimeout as delay } from "node:timers/promises";
import test from "node:test";
import { PhoneBrowser } from "./browser-phone.mjs";
import { webAddress } from "./browser-phone-network.mjs";

test("phone navigation accepts public web URLs", () => {
  for (const url of ["file:///tmp/file", "chrome://settings", "http://2130706433", "http://10.0.0.1",
    "http://localhost./", "http://db.internal", "http://[::1]", "https://user:pass@example.com"]) {
    assert.throws(() => webAddress(url));
  }
  assert.equal(webAddress("https://example.org/path"), "https://example.org/path");
});

test("a failed Chromium launch reports an unavailable browser and releases the session", async t => {
  const directory = await mkdtemp(join(tmpdir(), "cage-phone-launch-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const phone = new PhoneBrowser({ binary: join(directory, "missing-browser"), directory });
  const id = randomUUID();
  await assert.rejects(phone.run("open", { id, profile: "a".repeat(64), width: 390, height: 640 }), { status: 503 });
  await assert.rejects(phone.run("frame", { id }), { status: 410 });
  await writeFile(join(directory, "b".repeat(64)), "not a profile directory");
  await assert.rejects(phone.run("open", { id, profile: "b".repeat(64), width: 390, height: 640 }), { status: 503 });
});

test("separate paired device profiles run independent concurrent phone browser sessions", async t => {
  const directory = await mkdtemp(join(tmpdir(), "cage-phone-multi-"));
  const launched = [];
  const profile1 = "1".repeat(64);
  const profile2 = "2".repeat(64);
  const phone = new PhoneBrowser({
    binary: "/fake/chromium",
    directory,
    pipeFactory: (_binary, args) => {
      const userDataArg = args.find(arg => arg.startsWith("--user-data-dir=")) || "";
      let currentURL = "about:blank";
      let closedResolve;
      const closed = new Promise(resolve => { closedResolve = resolve; });
      const pipe = {
        userDataArg,
        closed,
        async send(method, params = {}) {
          switch (method) {
            case "Target.getTargets":
              return { targetInfos: [{ targetId: "page-1", type: "page", url: "about:blank" }] };
            case "Target.attachToTarget":
              return { sessionId: "session-1" };
            case "Browser.getVersion":
              return { userAgent: "Mozilla/5.0 Chromium/136 Safari/537.36", product: "Chrome/136.0.0.0" };
            case "Page.navigate":
              currentURL = params.url;
              return {};
            case "Page.getFrameTree":
              return { frameTree: { frame: { id: "frame-1", loaderId: "loader-" + currentURL, url: currentURL } } };
            case "Page.getLayoutMetrics":
              return { cssVisualViewport: { scale: 1, offsetX: 0, offsetY: 0 } };
            case "Page.captureScreenshot":
              return { data: Buffer.from("jpeg:" + currentURL).toString("base64") };
            case "Target.getTargetInfo":
              return { targetInfo: { title: "Title " + currentURL } };
            case "Page.createIsolatedWorld":
              return { executionContextId: 1 };
            case "Runtime.evaluate":
              return { result: { value: { focus: "", password: false } } };
            default:
              return {};
          }
        },
        async close() {
          closedResolve();
        },
      };
      launched.push(pipe);
      return pipe;
    },
  });
  t.after(async () => {
    await phone.dispose();
    // Drain background history writes before removing the temporary profile directory.
    await Promise.all([
      phone.history({ profile: profile1, mode: "list" }).catch(() => {}),
      phone.history({ profile: profile2, mode: "list" }).catch(() => {}),
    ]);
    await rm(directory, { recursive: true, force: true });
  });

  const id1 = randomUUID();
  const id2 = randomUUID();

  assert.deepEqual(await phone.run("open", { id: id1, profile: profile1, url: "https://example.org/one", width: 390, height: 640 }), { id: id1 });
  assert.deepEqual(await phone.run("open", { id: id2, profile: profile2, url: "https://example.org/two", width: 820, height: 1180 }), { id: id2 });
  assert.equal(launched.length, 2);
  assert.notEqual(launched[0].userDataArg, launched[1].userDataArg);

  const [frame1, frame2] = await Promise.all([
    phone.run("frame", { id: id1 }),
    phone.run("frame", { id: id2 }),
  ]);
  assert.equal(frame1.url, "https://example.org/one");
  assert.equal(frame1.width, 390);
  assert.equal(frame2.url, "https://example.org/two");
  assert.equal(frame2.width, 820);

  // Closing device 1 must leave device 2's browser session active and unaffected.
  assert.deepEqual(await phone.run("close", { id: id1 }), { id: id1 });
  await assert.rejects(phone.run("frame", { id: id1 }), { status: 410 });
  const frame2After = await phone.run("frame", { id: id2 });
  assert.equal(frame2After.url, "https://example.org/two");
});

test("headed phone browser avoids HeadlessChrome, DevTools mobile emulation, and isolated-world spam", async t => {
  const directory = await mkdtemp(join(tmpdir(), "cage-phone-headed-"));
  const profile = "c".repeat(64);
  const calls = [];
  let launchedArgs = [];
  let launchedEnv;
  let displayClosed = false;
  let currentURL = "about:blank";
  let loaderSeq = 1;
  const phone = new PhoneBrowser({
    binary: "/fake/chromium",
    directory,
    displayFactory: async () => ({
      display: ":120",
      xauthority: "/tmp/fake-xauth",
      async close() { displayClosed = true; },
    }),
    pipeFactory: (_binary, args, env) => {
      launchedArgs = args;
      launchedEnv = env;
      let closedResolve;
      const closed = new Promise(resolve => { closedResolve = resolve; });
      return {
        closed,
        async send(method, params = {}) {
          calls.push({ method, params });
          switch (method) {
            case "Target.getTargets":
              return { targetInfos: [{ targetId: "page-1", type: "page", url: "about:blank" }] };
            case "Target.attachToTarget":
              return { sessionId: "session-1" };
            case "Page.navigate":
              currentURL = params.url;
              loaderSeq++;
              return {};
            case "Page.getFrameTree":
              return { frameTree: { frame: { id: "frame-1", loaderId: "loader-" + loaderSeq, url: currentURL } } };
            case "Page.getLayoutMetrics":
              return { cssVisualViewport: { scale: 1, offsetX: 0, offsetY: 0 } };
            case "Page.captureScreenshot":
              return { data: Buffer.from("jpeg:" + currentURL).toString("base64") };
            case "Target.getTargetInfo":
              return { targetInfo: { title: "Title " + currentURL } };
            case "Page.createIsolatedWorld":
              return { executionContextId: 100 + loaderSeq };
            case "Runtime.evaluate":
              return { result: { value: { focus: "", password: false } } };
            default:
              return {};
          }
        },
        async close() { closedResolve(); },
      };
    },
  });
  t.after(async () => {
    await phone.dispose();
    await phone.history({ profile, mode: "list" }).catch(() => {});
    await rm(directory, { recursive: true, force: true });
  });
  const id = randomUUID();
  await phone.run("open", { id, profile, url: "https://example.org/app", width: 390, height: 750 });
  assert.equal(launchedArgs.includes("--headless=new"), false);
  assert.equal(launchedEnv?.DISPLAY, ":120");
  assert.equal(launchedEnv?.XAUTHORITY, "/tmp/fake-xauth");
  assert.equal(calls.some(c => c.method === "Emulation.setUserAgentOverride"), false);
  assert.equal(calls.some(c => c.method === "Emulation.setTouchEmulationEnabled"), false);
  const metrics = calls.find(c => c.method === "Emulation.setDeviceMetricsOverride");
  assert.equal(metrics?.params.mobile, false);
  assert.equal("screenWidth" in metrics.params, false);

  const frame1 = await phone.run("frame", { id });
  await phone.run("frame", { id, after: frame1.frame_tag });
  await phone.run("frame", { id, after: frame1.frame_tag });
  const worldCalls = calls.filter(c => c.method === "Page.createIsolatedWorld");
  assert.equal(worldCalls.length, 1, "isolated world should be reused within the same document");
  assert.equal(worldCalls[0].params.worldName, "");

  await phone.run("action", { id, type: "tap", x: 100, y: 200, document: frame1.document });
  assert.equal(calls.some(c => c.method === "Input.dispatchTouchEvent"), false);
  assert.deepEqual(
    calls.filter(c => c.method === "Input.dispatchMouseEvent").map(c => c.params.type),
    ["mouseMoved", "mousePressed", "mouseReleased"],
  );

  await phone.run("close", { id });
  assert.equal(displayClosed, true);
});

test("headless fallback strips HeadlessChrome without forging Mobile Safari", async t => {
  const directory = await mkdtemp(join(tmpdir(), "cage-phone-fallback-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const calls = [];
  const phone = new PhoneBrowser({
    binary: "/fake/chromium",
    directory,
    pipeFactory: () => {
      let closedResolve;
      const closed = new Promise(resolve => { closedResolve = resolve; });
      return {
        closed,
        async send(method, params = {}) {
          calls.push({ method, params });
          switch (method) {
            case "Target.getTargets":
              return { targetInfos: [{ targetId: "page-1", type: "page", url: "about:blank" }] };
            case "Target.attachToTarget":
              return { sessionId: "session-1" };
            case "Browser.getVersion":
              return { userAgent: "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/136.0.0.0 Safari/537.36" };
            default:
              return {};
          }
        },
        async close() { closedResolve(); },
      };
    },
  });
  t.after(() => phone.dispose());
  const id = randomUUID();
  await phone.run("open", { id, profile: "d".repeat(64), width: 390, height: 750 });
  const uaCall = calls.find(c => c.method === "Emulation.setUserAgentOverride");
  assert.ok(uaCall);
  assert.equal(uaCall.params.userAgent.includes("HeadlessChrome"), false);
  assert.equal(uaCall.params.userAgent.includes("Mobile Safari"), false);
  assert.equal(uaCall.params.userAgent.includes("Chrome/136.0.0.0"), true);
  assert.equal(uaCall.params.userAgentMetadata.mobile, false);
});

// CAGE_TEST_CHROMIUM points at an installed, sandboxed Chromium. Only the
// fixture hostname is mapped to loopback; Chromium uses the production SOCKS
// listener, including its denial of requests for private literal addresses.
test("real phone Chromium routes, resizes, types, and keeps paired profiles separate",
  { skip: !process.env.CAGE_TEST_CHROMIUM }, async t => {
    const directory = await mkdtemp(join(tmpdir(), "cage-phone-browser-"));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const seen = [];
    let acceptLanguage;
    const site = http.createServer((req, res) => {
      seen.push(req.url);
      if (req.url === "/remember" || req.url === "/restored") acceptLanguage = req.headers["accept-language"];
      if (req.url === "/private") {
        res.writeHead(302, { Location: `http://127.0.0.1:${site.address().port}/forbidden` });
        return res.end();
      }
      if (req.url === "/private-ws") {
        res.setHeader("Content-Type", "text/html; charset=utf-8");
        return res.end(`<meta name="viewport" content="width=device-width,initial-scale=1"><script>
          const socket=new WebSocket('ws://127.0.0.1:${site.address().port}/forbidden-ws');
          document.title=JSON.stringify([innerWidth,innerHeight,'ws-attempted']);
        </script>`);
      }
      if (req.url === "/remember") res.setHeader("Set-Cookie", "state=known; Path=/; Max-Age=3600");
      res.setHeader("Content-Type", "text/html; charset=utf-8");
      res.end(`<meta name="viewport" content="width=device-width,initial-scale=1">
        <style>body{margin:0;height:320vh}input{display:block;width:200px;height:50px}</style>
        <input aria-label="draft"><input type=password aria-label="password">
        <script>
        const show = () => document.title = JSON.stringify([innerWidth, innerHeight,
          document.querySelector("input").value, document.cookie, scrollY, navigator.userAgent, navigator.language, Intl.DateTimeFormat().resolvedOptions().timeZone, devicePixelRatio,
          navigator.webdriver, Object.getOwnPropertyDescriptor(Navigator.prototype, 'webdriver').get.toString()]);
        oninput = onresize = onscroll = show; show();
        </script>`);
    });
    site.listen(0, "127.0.0.1");
    site.on("upgrade", (request, socket) => { seen.push(request.url); socket.destroy(); });
    await once(site, "listening");
    t.after(() => site.close());
    const browser = new PhoneBrowser({
      directory, binary: process.env.CAGE_TEST_CHROMIUM, language: "ja-JP",
      connect: async (name, port) => {
        if (name !== "phone.example" || port !== site.address().port) throw new Error("fixture denies destination");
        const socket = net.connect({ host: "127.0.0.1", port });
        await once(socket, "connect");
        return socket;
      },
    });
    t.after(() => browser.dispose());
    let id = randomUUID();
    const url = `http://phone.example:${site.address().port}`;
    const open = (profile, path = "/") => browser.run("open", { id, profile, url: url + path, width: 390, height: 750 });
    const action = (type, extra) => browser.run("action", { id, type, ...extra });
    async function read(check = () => true) {
      let latest;
      for (let i = 0; i < 25; i++) {
        try { latest = await browser.run("frame", { id }); }
        catch (error) { if (error.status !== 409) throw error; await delay(50); continue; }
        if (latest.title.startsWith("[") && check(JSON.parse(latest.title), latest)) return latest;
        await delay(50);
      }
      throw new Error("expected frame missing: " + latest?.title);
    }
    async function settled(frame) {
      // Chromium can paint the page after the DOM already has its new title.
      for (let i = 0; i < 20; i++) {
        const next = await browser.run("frame", { id, after: frame.frame_tag });
        if (next.unchanged) {
          assert.deepEqual(next, { id, frame_tag: frame.frame_tag, unchanged: true });
          return frame;
        }
        frame = next;
        await delay(50);
      }
      throw new Error("static page never produced an unchanged frame");
    }
    assert.deepEqual(await open("a".repeat(64), "/remember"), { id });
    let page = await read(([w, h, , cookie, , ua]) => w === 390 && h === 750 && cookie === "state=known" && typeof ua === "string");
    const parsed = JSON.parse(page.title);
    assert.equal(parsed[5].includes("HeadlessChrome"), false);
    assert.equal(parsed[5].includes("Mobile Safari"), false);
    assert.equal(parsed[6], "ja-JP");
    assert.equal(parsed[8], 2);
    assert.deepEqual(parsed.slice(9), [false, "function get webdriver() { [native code] }"]);
    assert.match(acceptLanguage, /^ja-JP/i);
    if (process.env.TZ) assert.equal(JSON.parse(page.title)[7], Intl.DateTimeFormat().resolvedOptions().timeZone);
    assert.ok(Buffer.from(page.image, "base64").subarray(0, 2).equals(Buffer.from([255, 216])));
    assert.match(page.frame_tag, /^[a-f0-9]{32}$/);
    page = await settled(page);
    const initialTag = page.frame_tag;
    page = await action("tap", { x: 60, y: 25, document: page.document, return_frame: true });
    assert.ok(page.focus && page.image);
    assert.notEqual(page.frame_tag, initialTag);
    const old = { document: page.document, focus: page.focus };
    assert.deepEqual(await action("text", { ...old, text: "你好 Cage" }), { id });
    page = await read(([, , value]) => value === "你好 Cage");
    const capture = browser.frame;
    browser.frame = async () => { throw new Error("frame changed after text input"); };
    try {
      assert.deepEqual(await action("text", { ...old, text: "!", return_frame: true }), { id });
    } finally { browser.frame = capture; }
    await read(([, , value]) => value === "你好 Cage!");
    await action("resize", { width: 390, height: 400 });
    page = await read(([, h]) => h === 400);
    assert.equal(page.focus, old.focus);
    assert.notEqual(page.document, old.document);
    await action("text", { focus: old.focus, document: page.document, text: "✓" });
    page = await read(([, , value]) => value === "你好 Cage!✓");
    await action("tap", { x: 60, y: 75, document: page.document });
    await read((_, frame) => frame.password && frame.focus !== old.focus);
    await assert.rejects(action("text", { document: page.document, focus: old.focus, text: "stale" }), /输入框已改变/);
    await action("scroll", { x: 100, y: 280, dx: 0, dy: 200, document: page.document });
    await read(([, , , , y]) => y > 0);
    await action("resize", { width: 750, height: 390 });
    await read(([w, h]) => w === 750 && h === 390);
    await assert.rejects(action("tap", { x: 30, y: 30, document: old.document }), /页面已切换/);
    await action("navigate", { url: url + "/next" });
    page = await read();
    await action("tap", { x: 60, y: 25, document: page.document });
    page = await read((_, frame) => Boolean(frame.focus));
    assert.notEqual(page.focus, old.focus);
    await assert.rejects(action("text", { document: page.document, focus: old.focus, text: "stale" }), /输入框已改变/);
    await action("resize", { width: 390, height: 750 });
    await action("navigate", { url: url + "/private" }).catch(() => {});
    await delay(250);
    assert.equal(seen.includes("/forbidden"), false);
    await action("navigate", { url: url + "/private-ws" });
    await read(([, , status]) => status === "ws-attempted");
    await delay(200);
    assert.equal(seen.includes("/forbidden-ws"), false, "a WebSocket bypassed the public-only proxy");
    await browser.run("close", { id });
    browser.language = "fr-FR";
    id = randomUUID(); await open("a".repeat(64), "/restored");
    page = await read(([, , , cookie]) => cookie === "state=known");
    assert.equal(JSON.parse(page.title)[6], "fr-FR");
    assert.match(acceptLanguage, /^fr-FR/i);
    await browser.run("close", { id });
    id = randomUUID(); await open("b".repeat(64));
    await read(([, , , cookie]) => cookie === "");
    // Verify secure-origin headers stay consistent with real desktop Chromium (no HeadlessChrome or forged mobile UA).
    const s = browser.session;
    const originalOnEvent = s.pipe.onEvent;
    const { product } = await s.pipe.send("Browser.getVersion");
    const major = product.split("/")[1].split(".")[0];
    s.pipe.onEvent = event => {
      originalOnEvent(event);
      if (event.method !== "Fetch.requestPaused") return;
      const { request, requestId } = event.params;
      const headers = Object.fromEntries(Object.entries(request.headers).map(([k, v]) => [k.toLowerCase(), v]));
      const coherent = headers["sec-ch-ua-mobile"] === "?0" &&
        !headers["user-agent"].includes("HeadlessChrome") &&
        !headers["user-agent"].includes(" Mobile ") &&
        headers["sec-ch-ua"]?.includes(`"Chromium";v="${major}"`);
      const html = `<meta name="viewport" content="width=device-width,initial-scale=1">
        <style>body{margin:0} form{width:100%}</style>
        <form><input></form><script>document.title=JSON.stringify([innerWidth, document.forms[0].offsetWidth,
          ${JSON.stringify(coherent)}, navigator.userAgentData.mobile, navigator.language]);</script>`;
      void s.send("Fetch.fulfillRequest", { requestId, responseCode: 200, responseHeaders: [{ name: "Content-Type", value: "text/html" }],
        body: Buffer.from(html).toString("base64") });
    };
    await s.send("Fetch.enable", { patterns: [{ urlPattern: "https://phone.example/*" }] });
    await action("navigate", { url: "https://phone.example/search" });
    page = await read(([w, form, coherent, mobile]) => w === 390 && form <= w && coherent === true && mobile === false);
    assert.equal(JSON.parse(page.title)[4], "fr-FR");
    page = await settled(page);
    // A change to the title alone must invalidate the conditional frame.
    await s.send("Runtime.evaluate", { expression: 'document.title = "new title"' });
    const renamed = await browser.run("frame", { id, after: page.frame_tag });
    assert.equal(renamed.title, "new title");
    assert.equal(renamed.image, page.image);
    assert.notEqual(renamed.frame_tag, page.frame_tag);
    await browser.run("close", { id });
  });
