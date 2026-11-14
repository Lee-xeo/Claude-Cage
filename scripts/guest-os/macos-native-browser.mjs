// The bound guest user's LaunchAgent owns the browser. SSH callers attach as clients.
import { installMacOSCLIBrowser } from "./macos-cli-browser.mjs";
import { spawn } from "node:child_process";
import { randomBytes } from "node:crypto";
import { mkdir, rm, writeFile } from "node:fs/promises";
import { homedir } from "node:os";
import path from "node:path";
import { browserCallback, browserSettingsInputs, nativeBrowserDispatch, windowRequest } from "./macos-browser-window.mjs";
import { createTailnetClient } from "./tailnet-client.mjs";
import { relayMacOSBrowser, serveMacOSBrowser } from "./macos-browser-service.mjs";
import { lstat, readFile } from "node:fs/promises";
import { checkedBrowserProfile } from "./browser-protocol.mjs";

const data = path.join(homedir(), "Library", "Application Support", "Cage");
const editors = new Map();

async function openEditor(id, restart, launch) {
  if (!/^[a-f0-9]{24}$/.test(id)) throw new Error("项目标识无效");
  const folder = path.join(homedir(), "Cage", "Projects", id);
  let editor = editors.get(id);
  if (restart && editor) {
    editors.delete(id);
    editor.child.kill();
    editor = null;
  }
  if (!editor) {
    const password = randomBytes(32).toString("hex");
    const directory = path.join(data, "EditorSessions");
    const config = path.join(directory, id + ".yaml");
    await mkdir(directory, { mode: 0o700, recursive: true });
    await writeFile(config, "bind-addr: 127.0.0.1:0\nauth: password\npassword: " + password + "\ncert: false\n", { mode: 0o600 });
    const child = spawn("/usr/local/libexec/cage-macos-terminal", ["--editor", folder], { stdio: "pipe" });
    child.stdin.on("error", () => {});
    editor = { child, password, config };
    editors.set(id, editor);
    child.once("exit", () => {
      if (editors.get(id) === editor) { editors.delete(id); void rm(config, { force: true }).catch(() => {}); }
    });
    editor.address = new Promise((resolve, reject) => {
      let recent = "";
      const failed = () => finish();
      const read = chunk => {
        recent = (recent + chunk).slice(-6000);
        const address = recent.match(/HTTP server listening on (http:\/\/127\.0\.0\.1:[1-9][0-9]*)/);
        if (address) finish(address[1]);
      };
      const timer = setTimeout(failed, 15000);
      function finish(address) {
        clearTimeout(timer);
        child.off("exit", failed); child.off("error", failed);
        for (const stream of [child.stdout, child.stderr]) { stream.off("data", read); stream.resume(); }
        if (address) resolve(address);
        else { child.kill(); reject(new Error("项目编辑器启动失败")); }
      }
      child.stdout.on("data", read); child.stderr.on("data", read);
      child.once("error", failed); child.once("exit", failed);
    });
  }
  const address = new URL(await editor.address);
  const login = await fetch(new URL("/login", address), {
    method: "POST", body: new URLSearchParams({ password: editor.password }),
    redirect: "manual", signal: AbortSignal.timeout(7000),
  });
  const cookies = login.headers.getSetCookie();
  if (![302, 303].includes(login.status) || !cookies.length) throw new Error("客机编辑器登录失败");
  // Cookies are scoped by hostname, independently of port. Deliver the editor's
  // own cookie only to this project's localhost alias, then open its server.
  const hostname = id + ".localhost";
  address.hostname = hostname;
  address.searchParams.set("folder", folder);
  const callback = await browserCallback(hostname, (_, response) => {
    response.writeHead(303, { "Set-Cookie": cookies, Location: address.href }).end();
  });
  try { await launch(callback.url); await callback.result; }
  finally { callback.close(); }
}

const marker = await lstat("/var/db/cage-guest-os");
if (process.platform !== "darwin" || process.getuid() < 500 || !marker.isFile() || marker.uid !== 0 || (marker.mode & 0o022) ||
    (await readFile("/var/db/cage-guest-os", "utf8")).trim() !== "macos") throw new Error("Cage browser requires its bound guest user");
const directory = path.join(homedir(), "Library", "Caches", "dev.cage.GuestBrowser");
if (process.argv.length > 3 || process.argv[2] && process.argv[2] !== "--service" && process.argv[2] !== "--client")
  throw new Error("Unknown browser service operation");
if (process.argv[2] !== "--service") {
  // Older hosts invoke this file with no arguments. They attach to the same
  // service and receive ownership capability metadata with the first reply.
  await relayMacOSBrowser(directory);
} else {
  const socket = path.join(homedir(), "Library", "Caches", "dev.cage.GuestClaudeWindow", "window.sock");
  const tailnet = createTailnetClient();
  void tailnet.restore().catch(() => {});
  const profile = async input => {
    const value = JSON.stringify(checkedBrowserProfile(input));
    await mkdir(data, { recursive: true, mode: 0o700 });
    const file = path.join(data, "guest-profile.json");
    try { await writeFile(file, value, { flag: "wx", mode: 0o600 }); }
    catch (error) { if (error.code !== "EEXIST" || JSON.stringify(checkedBrowserProfile(JSON.parse(await readFile(file, "utf8")))) !== value) throw error; }
  };
  const dispatch = nativeBrowserDispatch({ settingsInputs: info => browserSettingsInputs(info, path.join(data, "Browser")), window: (action, fields, signal) => windowRequest(socket, action, fields, signal), editor: openEditor, tailnet, profile });
  const close = async () => {
    for (const { child } of editors.values()) child.kill();
    tailnet.close();
  };
  const server = await serveMacOSBrowser({ directory, dispatch, close });
  let closeLogin;
  try { closeLogin = await installMacOSCLIBrowser(data, message => server.request(message)); }
  catch (error) { await server.close(); throw error; }
  const shutdown = async () => {
    try { await server.close(); }
    finally { await closeLogin(); }
  };
  process.once("SIGTERM", () => { void shutdown().catch(() => { process.exitCode = 1; }); });
  process.once("SIGINT", () => { void shutdown().catch(() => { process.exitCode = 1; }); });
}
