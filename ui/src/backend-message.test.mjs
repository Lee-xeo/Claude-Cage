import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import test from "node:test";
import { backendMessageKeys, localizeBackendMessage } from "./backend-message.mjs";
import { formatMessage } from "./message-catalog.mjs";
import { httpErrorMessage, nativeFailureMessage } from "./error-message.mjs";

const read = (path) => readFileSync(new URL(path, import.meta.url), "utf8");
const languages = JSON.parse(read("../../internal/uilocale/languages.json")).slice(1);

test("localizeBackendMessage preserves original Chinese in default locale", () => {
  const errLocalNode = "本机代理端口不能用作节点。请导入原客户端的具体节点，在 Cage 中按中转到最终出口的顺序重新配置并应用";
  assert.equal(localizeBackendMessage(errLocalNode, formatMessage), errLocalNode);
  const routeErr = `节点 "local-1080" 需要迁移：${errLocalNode}`;
  assert.equal(localizeBackendMessage(routeErr, formatMessage), routeErr);
});

test("localizeBackendMessage translates ErrLocalProxyNode and dynamic backend errors across all 14 languages", () => {
  const errLocalNode = "本机代理端口不能用作节点。请导入原客户端的具体节点，在 Cage 中按中转到最终出口的顺序重新配置并应用";
  const samples = [
    errLocalNode,
    `节点 "local-socks" 需要迁移：${errLocalNode}`,
    `没有可导入的节点，已跳过 2 个；第 1 个节点 "socks1"：${errLocalNode}；第 2 个节点 "bad"：节点协议暂不支持`,
    `没有可导入的节点，已跳过 1 个；第 1 个节点 "hk"：Clash Verge Ninja 专用的 ninja 协议，Cage 随附的标准 Mihomo 核心无法解析，请向服务商获取同一出口的标准 Mihomo 节点后重新导入`,
    `没有可导入的节点，已跳过 1 个；第 1 个节点 "neo"：Clash Neo 专用的 nexo 协议，Cage 随附的标准 Mihomo 核心无法解析，请向服务商获取同一出口的标准 Mihomo 节点后重新导入`,
    `订阅更新移除或跳过了当前出口或中转节点 "HK-01"：节点未包含在更新中；原配置已保留，请先应用其他节点再更新`,
    "订阅服务器返回 HTTP 403",
    "测速失败：代理核心返回 HTTP 504",
    "测速失败：节点未载入核心；请先应用节点以载入当前列表",
    "节点字段 server 存在重复写法，请仅保留一种",
    "Hysteria2 字段 up 与 up_mbps 冲突",
    "订阅的第 2 个节点 DNS 端口无效",
    "有 3 份节点列表未能从本机缓存读取，这些节点不会导入。可先在原软件中更新配置，再重新检测。",
    "以下节点未导入：WireGuard 暂不支持（2 个）；协议 ssr 暂不支持（1 个）",
    "包含暂不支持的连接选项：proxycommand",
    "另一个操作仍在进行，请稍候：当前正在应用代理节点。请等待完成，或先停止当前操作。",
    "已取消：已被启动环境替换",
    "应用代理节点失败",
    "恢复联网失败：等待响应超时。请重新检测；仍失败时导出诊断包。",
    "恢复代理连接失败：程序提前结束（退出码 1），未返回可识别的原因。连接恢复后会自动重试，也可手动重新检测。",
  ];

  for (const { id } of languages) {
    const catalog = JSON.parse(read(`./locales/${id}.json`));
    const localize = (source, ...values) => formatMessage(Object.hasOwn(catalog, source) ? catalog[source] : source, ...values);

    for (const key of backendMessageKeys) {
      assert.ok(typeof catalog[key] === "string" && catalog[key].trim(), `${id} missing backend key: ${key}`);
    }
    for (const sample of samples) {
      const translated = localizeBackendMessage(sample, localize);
      assert.notEqual(translated, sample, `${id} failed to translate: ${sample}`);
      if (id !== "zh-TW") {
        assert.doesNotMatch(translated, /\p{Script=Han}/u, `${id} leaked Han characters in: ${translated}`);
      }
    }

    // Verify HTTP text/plain and native error bridges also translate backend messages.
    const httpTranslated = httpErrorMessage("/proxy/import", 400, errLocalNode, "text/plain; charset=utf-8", "POST", localize);
    assert.notEqual(httpTranslated, errLocalNode, `${id} httpErrorMessage did not translate ErrLocalProxyNode`);
    if (id !== "zh-TW") {
      assert.doesNotMatch(httpTranslated, /\p{Script=Han}/u, `${id} httpErrorMessage leaked Han: ${httpTranslated}`);
    }
    const nativeTranslated = nativeFailureMessage("uploadHostFiles", "每个文件不能超过 2 GB", localize);
    assert.notEqual(nativeTranslated, "每个文件不能超过 2 GB", `${id} nativeFailureMessage did not translate`);
    if (id !== "zh-TW") {
      assert.doesNotMatch(nativeTranslated, /\p{Script=Han}/u, `${id} nativeFailureMessage leaked Han: ${nativeTranslated}`);
    }
  }
});

test("Russian ErrLocalProxyNode and daily refresh interval match issue #749 expectations", () => {
  const catalog = JSON.parse(read("./locales/ru.json"));
  const localize = (source, ...values) => formatMessage(Object.hasOwn(catalog, source) ? catalog[source] : source, ...values);

  assert.equal(localize("每天"), "Каждый день");
  const errLocalNode = "本机代理端口不能用作节点。请导入原客户端的具体节点，在 Cage 中按中转到最终出口的顺序重新配置并应用";
  const ruErr = localizeBackendMessage(errLocalNode, localize);
  assert.match(ruErr, /\p{Script=Cyrillic}/u);
  assert.doesNotMatch(ruErr, /\p{Script=Han}/u);
});
