import { readdirSync, readFileSync } from "node:fs";
import assert from "node:assert/strict";
import test from "node:test";
import { backendMessageKeys } from "./backend-message.mjs";

const read = path => readFileSync(new URL(path, import.meta.url), "utf8");

test("Cage's desktop language list and first-run translations match the README", () => {
  const readme = read("../../README.md");
  const documented = [...readme.matchAll(/\[([^\]]+)\]\(README(?:\.([\w-]+))?\.md\)/g)]
    .map(([, name, id]) => [id ?? "zh-CN", name]);
  const languages = JSON.parse(read("../../internal/uilocale/languages.json"));
  assert.deepEqual(languages.map(({ id, name }) => [id, name]), documented);
  const native = JSON.parse(read("../../internal/uilocale/native.json"));
  for (const { id } of languages.slice(1)) {
    const catalog = JSON.parse(read(`./locales/${id}.json`));
    for (const source of ["界面语言", "跟随系统", "注册 Cage 账户", "为 Cage 准备独立环境", "连接你的网络代理"]) {
      assert.ok(catalog[source] && catalog[source] !== source, `${id} is missing "${source}"`);
    }
    assert.ok(native[id]?.["正在启动 Cage 工作台…"], `${id} is missing the startup message`);
    for (const [source, value] of Object.entries(catalog)) {
      const placeholders = text => (text.match(/\{\d+\}/g) ?? []).sort();
      assert.deepEqual(placeholders(value), placeholders(source), `${id}: interpolation for "${source}"`);
      if (id !== "zh-TW") {
        assert.doesNotMatch(value, /\p{Script=Han}/u, `${id}: untranslated Han text in "${source}" => "${value}"`);
      }
    }
  }
});

test("all 14 locale catalogs have identical key sets and cover every translated UI and backend text", async () => {
  const { tailnetText } = await import("./tailscale-text.mjs");
  const { errorText } = await import("./error-message.mjs");
  const sources = new Set([...Object.values(tailnetText), ...Object.values(errorText), ...backendMessageKeys]);
  const files = readdirSync(new URL(".", import.meta.url), { recursive: true })
    .filter(name => /\.(jsx|js|mjs)$/.test(name) && !/\.test\.mjs$/.test(name));
  for (const name of files) {
    // Only double-quoted literals are catalog keys; template literals are not.
    for (const [, literal] of read(`./${name}`).matchAll(/\b(?:tr|translate)\(("(?:[^"\\\n]|\\.)*")/g)) {
      sources.add(JSON.parse(literal));
    }
  }
  assert.ok(sources.size > 1000, `found only ${sources.size} translated texts`);
  const languages = JSON.parse(read("../../internal/uilocale/languages.json")).slice(1);
  const referenceKeys = new Set(Object.keys(JSON.parse(read(`./locales/${languages[0].id}.json`))));
  const missing = [];
  const parityMismatch = [];
  for (const { id } of languages) {
    const catalog = JSON.parse(read(`./locales/${id}.json`));
    for (const source of sources) {
      if (typeof catalog[source] !== "string" || !catalog[source].trim()) missing.push(`${id}: ${source}`);
    }
    for (const key of referenceKeys) {
      if (!(key in catalog)) parityMismatch.push(`${id} missing key from ${languages[0].id}: ${key}`);
    }
    for (const key of Object.keys(catalog)) {
      if (!referenceKeys.has(key)) parityMismatch.push(`${id} has extra key not in ${languages[0].id}: ${key}`);
    }
  }
  assert.deepEqual(missing, []);
  assert.deepEqual(parityMismatch, []);
});

test("all 14 non-zh-CN languages localize Tailscale, proxy intervals, API failures and generated tab names", async () => {
  const { tailnetText } = await import("./tailscale-text.mjs");
  const { formatMessage } = await import("./message-catalog.mjs");
  const { httpErrorMessage, connectionErrorMessage, nativeFailureMessage } = await import("./error-message.mjs");
  const { tabName } = await import("./workspace-state.mjs");
  const languages = JSON.parse(read("../../internal/uilocale/languages.json")).slice(1);
  const intervals = ["仅手动更新", "每小时", "每 6 小时", "每天", "每周"];

  for (const { id } of languages) {
    const catalog = JSON.parse(read(`./locales/${id}.json`));
    const localize = (source, ...values) => formatMessage(Object.hasOwn(catalog, source) ? catalog[source] : source, ...values);

    for (const source of Object.values(tailnetText)) {
      const value = catalog[source] ?? "";
      assert.ok(value.trim(), `${id}: missing Tailscale message: ${source}`);
      if (id !== "zh-TW") assert.doesNotMatch(value, /\p{Script=Han}/u, `${id}: Han leak in Tailscale message: ${source}`);
    }
    for (const label of intervals) {
      const value = localize(label);
      assert.ok(value.trim(), `${id}: missing interval label: ${label}`);
      if (id !== "zh-TW") assert.doesNotMatch(value, /\p{Script=Han}/u, `${id}: Han leak in interval "${label}" => "${value}"`);
    }

    const httpMsg = httpErrorMessage("/environment/device-reset", 409, "", "", "GET", localize);
    const connMsg = connectionErrorMessage("/client/notifications", "GET", localize);
    const nativeMsg = nativeFailureMessage("notificationStatus", "timeout", localize);
    const desktopOpMsg = httpErrorMessage("/desktop-instances", 500, "", "", "POST", localize);
    const tabLabel = tabName({ incognito: true, kind: "editor" }, {}, "demo", localize);
    const customTabLabel = tabName({ id: "custom", incognito: true }, { custom: { title: "我的项目" } }, undefined, localize);

    assert.ok(httpMsg.includes("409"), `${id}: missing HTTP status in "${httpMsg}"`);
    assert.ok(customTabLabel.includes("我的项目"), `${id}: user tab title should be preserved`);
    if (id !== "zh-TW") {
      for (const msg of [httpMsg, connMsg, nativeMsg, desktopOpMsg, tabLabel]) {
        assert.doesNotMatch(msg, /\p{Script=Han}/u, `${id}: Han leak in "${msg}"`);
      }
    }

    if (id === "ru") {
      assert.equal(localize("每天"), "Каждый день");
      assert.equal(localize(tailnetText.copyDeviceAddress, "laptop"), "Скопировать адрес устройства laptop");
      assert.equal(localize(tailnetText.onlineCount, 2), "В сети: 2");
      assert.match(httpMsg, /Чтение состояния переноса.*HTTP 409/u);
      assert.match(connMsg, /Чтение настроек уведомлений.*Соединение с Cage прервалось/u);
      assert.match(nativeMsg, /Проверка системных уведомлений/u);
      assert.equal(tabLabel, "Инкогнито · Файл · demo");
      assert.equal(customTabLabel, "Инкогнито · 我的项目");
    }
  }
});

test("desktop UI components do not hardcode zh-CN Intl formatters or raw proxy refresh interval labels", () => {
  const exitPolicy = read("./exit-policy.jsx");
  assert.match(exitPolicy, /new Intl\.DisplayNames\(\s*\[locale\]/);
  assert.doesNotMatch(exitPolicy, /const names = new Intl\.DisplayNames\(\s*\["zh-CN"\]/);
  assert.doesNotMatch(exitPolicy, /toLocaleTimeString\(\s*"zh-CN"\s*\)/);
  assert.doesNotMatch(read("./cli-sessions.jsx"), /new Intl\.DateTimeFormat\(\s*"zh-CN"/);
  assert.doesNotMatch(read("./safety.jsx"), /toLocaleString\(\s*"zh-CN"\s*\)/);
  assert.match(read("./proxy-settings.jsx"), /<option value=\{v\} key=\{v\}>\s*\{tr\(label\)\}\s*<\/option>/);
});
