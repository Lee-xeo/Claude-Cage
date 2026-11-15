import assert from "node:assert/strict";
import { createPrivacyController } from "./browser-privacy-fixture.mjs";

const controller = await createPrivacyController();
const { api, cdp, origin, waitFor } = controller;
try {
  const ordinary = await api("/new", { url: origin + "/document" });
  const privateTab = await api("/new", { url: origin + "/private", incognito: true });
  for (const url of ["chrome://newtab/", "chrome://new-tab-page/", "chrome://new-tab-page-third-party/"]) {
    const { targetId } = await cdp(undefined, "Target.createTarget", { url });
    await waitFor(async () => (await api("/tabs")).tabs.find(tab => tab.id === targetId)?.url === origin + "/home",
      `native new tab ${url} must use the configured homepage`);
  }
  const { targetId: blank } = await cdp(undefined, "Target.createTarget", { url: "about:blank" });
  await waitFor(async () => (await api("/tabs")).tabs.find(tab => tab.id === blank), "blank popup registration");
  await api("/activate", { id: ordinary.id });
  let tabs = (await api("/tabs")).tabs;
  assert.equal(tabs.find(tab => tab.id === ordinary.id).url, origin + "/document");
  assert.equal(tabs.find(tab => tab.id === privateTab.id).url, origin + "/private");
  assert.equal(tabs.find(tab => tab.id === blank).url, "about:blank", "blank login/pop-up pages must remain available");
  await api("/close", { id: blank });
  await api("/homepage", { url: origin + "/changed-home" });
  const { targetId } = await cdp(undefined, "Target.createTarget", { url: "chrome://newtab/" });
  await waitFor(async () => (await api("/tabs")).tabs.find(tab => tab.id === targetId)?.url === origin + "/changed-home",
    "late native new tabs must use the latest homepage");
  await controller.restart();
  tabs = (await api("/tabs")).tabs;
  assert.ok(tabs.some(tab => tab.url === origin + "/document"), "restart must retain ordinary pages");
  assert.ok(!tabs.some(tab => /^chrome:\/\/new/.test(tab.url)), "restart must not leave a native new-tab page");
  console.log("PASS: native new-tab variants and late creation use the homepage; ordinary/private/blank pages and session restoration remain intact");
} finally { await controller.close(); }
