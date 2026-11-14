// Production UI/controller with a newly created temporary profile. No VM starts,
// no native trust is enrolled, and no personal configuration is opened.
import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { createRequire } from "node:module";

const cage = process.argv[2];
assert.ok(path.isAbsolute(cage || ""), "Supply an absolute Cage helper path");
const run = promisify(execFile);
const root = await fs.mkdtemp(path.join(os.tmpdir(), "account-recovery-live-"));
const profile = path.join(root, "profile");
await fs.mkdir(profile, { mode: 0o700 });
// This scenario exercises account settings without provisioning a VM. Seed only
// the completed-onboarding marker; account, vault and recovery use real APIs.
await fs.writeFile(path.join(profile, "initialization.json"), JSON.stringify({ version: 1, completed: true }), { mode: 0o600 });
const env = { ...process.env, CLAUDE_CAGE_CONFIG: path.join(profile, "config.json") };
const require = createRequire(import.meta.url);
const engines = require(process.env.CAGE_PLAYWRIGHT_MODULE || "playwright");
const engine = engines[process.env.CAGE_TEST_BROWSER || "chromium"];
const browser = await engine.launch({ headless: true });
let context, page, service;
const errors = [];
const output = process.env.CAGE_RECOVERY_OUTPUT;
if (output) await fs.mkdir(output, { recursive: true });
const command = async (...args) => JSON.parse((await run(cage, args, { env, timeout: 60000 })).stdout);
const restart = async () => {
  await context?.close();
  await command("prepare-update", "--json");
  service = spawn(cage, ["serve"], { env, stdio: "ignore" });
  let entry;
  for (let attempt = 0; attempt < 100; attempt++) {
    try { entry = await command("server-entry", "--json"); break; }
    catch { if (service.exitCode !== null) throw new Error("Isolated controller exited before readiness"); }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
  assert.ok(entry, "Isolated controller did not become ready");
  context = await browser.newContext({ acceptDownloads: true });
  page = await context.newPage();
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto(`${entry.origin}/#${entry.ticket}`);
};
const menu = () => page.getByRole("button", { name: /^账户菜单，/ });
const call = (endpoint, body) => page.evaluate(async ({ endpoint, body }) => {
  const response = await fetch("/api" + endpoint, { method: body ? "POST" : "GET", headers: body ? { "Content-Type": "application/json" } : {}, body: body ? JSON.stringify(body) : undefined });
  return { status: response.status, value: await response.json() };
}, { endpoint, body });
const saveKey = async (reset = false) => {
  const region = page.getByRole("region", { name: "保存恢复密钥", exact: true });
  await region.waitFor();
  if (output) await page.screenshot({ path: path.join(output, reset ? "reset-key.png" : "save-key.png"), mask: [page.locator("input,textarea")] });
  const code = await region.getByLabel("恢复密钥", { exact: true }).inputValue();
  assert.match(code, /^(?:[A-F0-9]{8}-){7}[A-F0-9]{8}$/);
  const proceed = region.getByRole("button", { name: reset ? "返回登录" : "继续使用 Cage", exact: true });
  assert.equal(await proceed.isDisabled(), true);
  // Exercise the UI/native contract separately from the browser download.
  // The real native implementations are built/checked by platform checks.
  await page.evaluate(() => {
    window.recoverySaveMode = "cancel";
    window.cageNativeBridge = { async postMessage(request) {
      const key = document.querySelector('textarea[aria-label="恢复密钥"]').value;
      if (request.action === "saveRecoveryKey") {
        if (request.key !== key) throw new Error("保存内容不完整");
        if (window.recoverySaveMode === "fail") throw new Error("模拟保存位置不可写");
        return window.recoverySaveMode === "save";
      }
      if (request.action === "clipboardWrite") {
        if (request.text !== key) throw new Error("复制内容不完整");
        return true;
      }
      return false;
    } };
  });
  const saveNative = region.getByRole("button", { name: "下载密钥文件", exact: true });
  await saveNative.click();
  assert.equal(await region.getByText("恢复密钥文件已保存，请妥善保管。", { exact: true }).count(), 0);
  await page.evaluate(() => { window.recoverySaveMode = "fail"; });
  await saveNative.click();
  await region.getByRole("status").filter({ hasText: "未能保存文件" }).waitFor();
  await page.evaluate(() => { window.recoverySaveMode = "save"; });
  await saveNative.click();
  await region.getByRole("status").filter({ hasText: "恢复密钥文件已保存" }).waitFor();
  await region.getByRole("button", { name: "复制恢复密钥", exact: true }).click();
  await region.getByRole("status").filter({ hasText: "恢复密钥已复制" }).waitFor();
  await page.evaluate(() => { delete window.cageNativeBridge; delete window.recoverySaveMode; });
  const downloadEvent = page.waitForEvent("download");
  await region.getByRole("button", { name: "下载密钥文件", exact: true }).click();
  const download = await downloadEvent;
  const file = path.join(root, "recovery-download.txt");
  await download.saveAs(file);
  assert.ok((await fs.readFile(file, "utf8")).includes(code), "download omitted the full secret");
  await fs.rm(file);
  // The screen remains available through polling, then supports keyboard confirmation.
  await region.getByRole("checkbox", { name: "我已安全保存完整恢复密钥" }).focus();
  await page.keyboard.press("Space");
  await proceed.click();
  assert.equal(await region.count(), 0);
  return code;
};
const unlock = async (password) => {
  await page.getByLabel("账户密码", { exact: true }).fill(password);
  await page.getByRole("button", { name: "解锁 Cage", exact: true }).click();
  await menu().waitFor();
};
const lock = async () => {
  await menu().click();
  await page.getByRole("menuitem", { name: /锁定 Cage/ }).click();
  await page.getByRole("region", { name: "解锁 Cage", exact: true }).waitFor();
};
try {
  await restart();
  await page.getByLabel("用户名", { exact: true }).fill("recovery_live_qa");
  await page.getByLabel("设置密码", { exact: true }).fill("original-test-password");
  await page.getByLabel("确认密码", { exact: true }).fill("original-test-password");
  await page.getByRole("button", { name: "创建本地账户", exact: true }).click();
  const registrationKey = await saveKey();
  await menu().waitFor();
  assert.equal((await call("/account")).value.recovery_ready, true);
  assert.equal((await call("/account")).value.recovery_key, undefined);
  assert.equal((await call("/vault", { action: "save", entry: { origin: "https://example.com", username: "recovery-live", password: "existing-login-secret" } })).status, 200);
  const vaultPath = path.join(profile, "password-vault.json");
  const vaultBefore = await fs.readFile(vaultPath);
  const sentinel = path.join(profile, "personal-disk.fixture");
  await fs.writeFile(sentinel, "original personal data and login state");

  // Reproduce an existing account from before recovery support, keeping its
  // password wrapping, vault and personal data unchanged.
  await context.close(); context = null;
  await command("prepare-update", "--json");
  const accountPath = path.join(profile, "account.json");
  const legacy = JSON.parse(await fs.readFile(accountPath, "utf8"));
  delete legacy.recovery;
  await fs.writeFile(accountPath, JSON.stringify(legacy), { mode: 0o600 });
  await restart();
  await page.getByRole("button", { name: "忘记密码？", exact: true }).click();
  await page.getByText(/此账户尚未设置恢复密钥。请使用原密码/).waitFor();
  assert.equal(await page.getByRole("button", { name: "重置密码", exact: true }).count(), 0);
  await page.getByRole("button", { name: "返回密码登录", exact: true }).click();
  await unlock("original-test-password");
  await menu().click();
  await page.getByRole("menuitem", { name: "设置", exact: true }).click();
  const section = page.getByRole("region", { name: "账户恢复", exact: true });
  await section.getByLabel("生成密钥前验证账户密码", { exact: true }).fill("original-test-password");
  await section.getByRole("button", { name: "生成恢复密钥", exact: true }).click();
  const enrolledKey = await saveKey();
  assert.notEqual(enrolledKey, registrationKey);
  await menu().waitFor();
  await page.reload();
  await menu().waitFor();
  await lock();
  await page.getByRole("button", { name: "忘记密码？", exact: true }).click();
  const recovery = page.getByRole("region", { name: "恢复账户", exact: true });
  await recovery.getByLabel("已保存的恢复密钥", { exact: true }).fill(enrolledKey.toLowerCase());
  await recovery.getByLabel("新密码", { exact: true }).fill("recovered-test-password");
  await recovery.getByLabel("确认新密码", { exact: true }).fill("mismatched-test-password");
  await recovery.getByRole("button", { name: "重置密码", exact: true }).click();
  await recovery.getByRole("alert").filter({ hasText: "两次密码不一致" }).waitFor();
  await recovery.getByLabel("确认新密码", { exact: true }).fill("recovered-test-password");
  await recovery.getByRole("button", { name: "重置密码", exact: true }).click();
  const replacementKey = await saveKey(true);
  assert.notEqual(replacementKey, enrolledKey);
  assert.equal((await call("/account")).value.unlocked, false);
  await unlock("recovered-test-password");
  const vault = (await call("/vault")).value;
  const revealed = await call("/vault", { action: "reveal", id: vault.entries[0].id, account_password: "recovered-test-password" });
  assert.equal(revealed.value.password, "existing-login-secret");
  assert.deepEqual(await fs.readFile(vaultPath), vaultBefore);
  assert.equal(await fs.readFile(sentinel, "utf8"), "original personal data and login state");
  const disk = await fs.readFile(accountPath, "utf8");
  for (const code of [registrationKey, enrolledKey, replacementKey]) assert.ok(!disk.includes(code));
  const storage = await page.evaluate(() => JSON.stringify([Object.entries(localStorage), Object.entries(sessionStorage)]));
  for (const code of [registrationKey, enrolledKey, replacementKey]) assert.ok(!storage.includes(code));
  await restart();
  await unlock("recovered-test-password");
  assert.equal((await call("/account")).value.recovery_ready, true);
  await lock();
  await page.setViewportSize({ width: 390, height: 700 });
  await page.getByRole("button", { name: "忘记密码？", exact: true }).click();
  await page.getByLabel("已保存的恢复密钥", { exact: true }).fill(replacementKey);
  await page.getByLabel("新密码", { exact: true }).fill("final-test-password");
  await page.getByLabel("确认新密码", { exact: true }).fill("final-test-password");
  await page.getByRole("button", { name: "重置密码", exact: true }).click();
  assert.equal(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth), true);
  await saveKey(true);
  await unlock("final-test-password");
  assert.deepEqual(errors, []);
  console.log("PASS: production UI/controller registration, key download/confirmation, native-save contract (cancel/failure/success) and copy, legacy enrollment, locked recovery, replacement key reuse after restart, confirmation errors, narrow layout, unchanged vault and personal data");
} catch (error) {
  if (output && page && !page.isClosed()) {
    await page.screenshot({ path: path.join(output, "failure.png"), mask: [page.locator("input,textarea")] }).catch(() => {});
    console.error((await page.locator("body").innerText()).replace(/(?:[A-Fa-f0-9]{8}[-\s]){7}[A-Fa-f0-9]{8}/g, "[recovery secret]"));
  }
  throw error;
} finally {
  await browser.close();
  try { await command("prepare-update", "--json"); }
  finally { service?.kill(); await fs.rm(root, { recursive: true, force: true }); }
}
