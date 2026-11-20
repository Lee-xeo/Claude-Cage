import assert from "node:assert/strict";
import { spawn, execFileSync } from "node:child_process";
import { once } from "node:events";
import { existsSync } from "node:fs";
import { mkdtemp, mkdir, readdir, rm, writeFile } from "node:fs/promises";
import http from "node:http";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import test from "node:test";

const filename = fileURLToPath(import.meta.url);
const root = path.resolve(path.dirname(filename), "../..");
const token = "local-popup-regression-fixture"; // Public fixture value, not a user credential.
const delay = milliseconds => new Promise(resolve => setTimeout(resolve, milliseconds));

async function until(callback) {
  const deadline = Date.now() + 30000;
  let last;
  while (Date.now() < deadline) {
    try { const result = await callback(); if (result) return result; }
    catch (error) { last = error; }
    await delay(50);
  }
  throw last ?? new Error("Linux popup state did not settle");
}

async function inner() {
  // This process and the unchanged production agent share a new network
  // namespace: only synthetic loopback pages are reachable, never a user tab.
  const close = new Set();
  const fixture = http.createServer((request, response) => {
    const url = new URL(request.url, "http://localhost");
    if (url.pathname.startsWith("/signal/")) {
      response.setHeader("Content-Type", "application/json");
      response.end(JSON.stringify(close.has(url.pathname.slice(8))));
      return;
    }
    response.setHeader("Content-Type", "text/html");
    const id = url.pathname.split("/").at(-1);
    const script = url.pathname.startsWith("/opener/")
      ? `window.open('/popup/${id}', '', 'popup,width=500,height=400');`
      : url.pathname.startsWith("/popup/")
        ? `setInterval(async()=>{if(await(await fetch('/signal/${id}')).json())window.close()},50);`
        : "";
    response.end(`<!doctype html><title>${url.pathname}</title><h1>${url.pathname}</h1><script>${script}</script>`);
  });
  await new Promise(resolve => fixture.listen(0, "127.0.0.1", resolve));
  const origin = `http://127.0.0.1:${fixture.address().port}`;
  await writeFile("/etc/workbench/browser-control.json", JSON.stringify({ token }));
  await writeFile("/home/cage-browser/homepage.json", JSON.stringify({ url: origin + "/older" }));
  const agent = spawn(process.execPath, ["/project/internal/cage/browser-agent.mjs"], { stdio: ["ignore", "pipe", "pipe"] });
  let log = "";
  for (const stream of [agent.stdout, agent.stderr]) stream.on("data", chunk => { log = (log + chunk).slice(-12000); });
  const call = async (route, input, method = "POST") => {
    const response = await fetch("http://127.0.0.1:18500/" + route, {
      method, headers: { Authorization: "Bearer " + token, "Content-Type": "application/json" },
      ...(method === "GET" ? {} : { body: JSON.stringify(input ?? {}) }), signal: AbortSignal.timeout(5000),
    });
    const value = await response.json();
    if (!response.ok) throw new Error(value.error || "Controller request failed");
    return value;
  };
  const state = () => call("tabs", undefined, "GET");
  try {
    const initial = await until(state);
    const first = initial.tabs[0].id;
    const info = await call("info");
    console.log(`Linux actual browser-agent: ${info.product}; Node ${process.version}; ${os.release()}`);
    for (const mode of ["opener", "missing", "background"]) {
      const opener = await call("new", { url: origin + "/page/" + mode });
      const unrelated = await call("new", { url: origin + "/unrelated/" + mode });
      await call("activate", { id: opener.id });
      await call("navigate", { id: opener.id, url: origin + "/opener/" + mode });
      const popup = await until(async () => {
        const current = await state();
        return current.tabs.find(tab => tab.url === origin + "/popup/" + mode && current.active === tab.id);
      });
      if (mode === "missing") {
        await call("close", { id: opener.id });
        assert.equal((await state()).active, popup.id);
      }
      if (mode === "background") await call("activate", { id: unrelated.id });
      close.add(mode); // The real popup executes window.close(), not /close.
      const after = await until(async () => {
        const current = await state();
        return !current.tabs.some(tab => tab.id === popup.id) && current;
      });
      assert.equal(after.active, mode === "opener" ? opener.id : mode === "missing" ? first : unrelated.id);
      console.log(`PASS Linux ${mode}: real window.open/window.close, controller active target verified`);
    }
  } catch (error) {
    console.error(log);
    throw error;
  } finally {
    if (agent.exitCode === null && agent.signalCode === null) {
      const exited = once(agent, "exit");
      const timer = setTimeout(() => agent.kill("SIGKILL"), 5000);
      agent.kill("SIGTERM");
      try { await exited; } finally { clearTimeout(timer); }
    }
    fixture.closeAllConnections();
    await new Promise(resolve => fixture.close(resolve));
  }
}

if (process.argv.includes("--linux-inner")) {
  await inner();
} else {
  const enabled = process.platform === "linux" && process.env.CAGE_BROWSER_TEST_CHROMIUM;
  test("Linux production controller handles real popup closure and both fallbacks", { skip: !enabled, timeout: 120000 }, async t => {
    assert.ok(process.env.DISPLAY, "Run this headed test under xvfb-run");
    execFileSync("bwrap", ["--version"]);
    const scratch = await mkdtemp(path.join(os.tmpdir(), "cage-popup-"));
    let child;
    t.after(async () => {
      if (child && child.exitCode === null && child.signalCode === null) {
        const exited = once(child, "exit");
        child.kill("SIGKILL");
        await exited;
      }
      await rm(scratch, { recursive: true, force: true });
    });
    for (const name of ["home", "config"]) await mkdir(path.join(scratch, name));
    // The controller always starts /usr/bin/chromium and loads its extension
    // from /usr/local/lib/cage-split, as in the Debian guest image. Replace
    // /usr/bin with links to the host binaries so only chromium is swapped.
    const browser = path.resolve(process.env.CAGE_BROWSER_TEST_CHROMIUM);
    assert.notEqual(browser, "/usr/bin/chromium", "Point CAGE_BROWSER_TEST_CHROMIUM at the browser binary itself, such as /usr/lib/chromium/chromium");
    const wrapper = path.join(scratch, "chromium");
    await writeFile(wrapper, `#!/bin/sh\nexec ${JSON.stringify(browser)} --disable-popup-blocking "$@"\n`, { mode: 0o700 });
    const hostBinaries = (await readdir("/usr/bin")).filter(name => name !== "chromium")
      .flatMap(name => ["--symlink", "/run/host-bin/" + name, "/usr/bin/" + name]);
    const browserMount = browser.startsWith("/usr/") ? [] : ["--ro-bind", path.dirname(browser), path.dirname(browser)];
    const args = ["--die-with-parent", "--unshare-user", "--unshare-pid", "--unshare-net",
      "--ro-bind", "/usr", "/usr", "--ro-bind", "/lib", "/lib",
      ...(existsSync("/lib64") ? ["--ro-bind", "/lib64", "/lib64"] : []),
      "--symlink", "usr/bin", "/bin", "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
      "--dir", "/run", "--ro-bind", "/usr/bin", "/run/host-bin", "--tmpfs", "/usr/bin", ...hostBinaries,
      "--ro-bind", wrapper, "/usr/bin/chromium", ...browserMount,
      "--dir", "/etc", "--ro-bind", "/etc/fonts", "/etc/fonts", "--ro-bind", "/etc/passwd", "/etc/passwd",
      "--ro-bind", "/etc/group", "/etc/group", "--bind", path.join(scratch, "config"), "/etc/workbench",
      "--bind", path.join(scratch, "home"), "/home/cage-browser", "--ro-bind", root, "/project",
      "--ro-bind", process.execPath, "/runtime/node",
      "--tmpfs", "/usr/local/lib", "--dir", "/usr/local/lib/cage-split",
      "--ro-bind", path.join(root, "internal/cage/split-extension"), "/usr/local/lib/cage-split",
      "--ro-bind", "/tmp/.X11-unix", "/tmp/.X11-unix",
      "--setenv", "HOME", "/home/cage-browser", "--setenv", "TMPDIR", "/tmp",
      "--setenv", "PATH", "/usr/bin:/bin", "--setenv", "DISPLAY", process.env.DISPLAY];
    if (process.env.XAUTHORITY) args.push("--ro-bind", process.env.XAUTHORITY, "/tmp/popup-Xauthority", "--setenv", "XAUTHORITY", "/tmp/popup-Xauthority");
    args.push("--", "/runtime/node", "/project/internal/cage/browser-popup.integration.mjs", "--linux-inner");
    child = spawn("bwrap", args, { stdio: ["ignore", "pipe", "pipe"] });
    let output = "";
    for (const stream of [child.stdout, child.stderr]) stream.on("data", chunk => { output += chunk; });
    const [code, signal] = await once(child, "exit");
    console.log(output);
    assert.equal(signal, null);
    assert.equal(code, 0);
  });
}
