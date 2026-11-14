import assert from "node:assert/strict";
import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { createWorkbench } from "./workbench-fixture.mjs";

const output = new URL("../../output/about-settings/", import.meta.url);
await fs.mkdir(output, { recursive: true });
for (const platform of ["darwin", "windows", "linux"]) {
  const build = { version: "0.1.7", commit: "a".repeat(40), modified: true };
  const remoteContext = { mode: "remote", connection_id: "remote-test", instance_id: "test-instance", client_platform: platform, server_platform: "linux", connection: { state: "connected" } };
  const nativeCalls = [];
  let checks = 0, pending, finish;
  let result = {
    build, source: { status: "available", commit: "b".repeat(40), aheadBy: 5 },
    release: { status: "available", version: "0.2.0-rc.1", prerelease: true }, checkedAt: new Date().toISOString(),
  };
  const fixture = await createWorkbench({
    clientContext: remoteContext,
    clientRequest: async (route, path) => {
      if (path === "/client/context") return route.fulfill({ json: remoteContext });
      if (path !== "/client/about") return route.fulfill({ json: {} });
      if (route.request().method() === "GET") return route.fulfill({ json: build });
      checks++;
      if (pending) await pending;
      return route.fulfill({ json: result });
    },
    nativeRequest: async body => { nativeCalls.push(body); return body.action === "appReference" ? true : null; },
  });
  const { page, browser, errors } = fixture;
  try {
    await page.getByRole("button", { name: "设置", exact: true }).click();
    await page.getByRole("navigation", { name: "设置分组" }).getByRole("button", { name: "关于", exact: true }).click();
    const about = page.getByRole("region", { name: "关于 Cage", exact: true });
    await about.getByText("0.1.7", { exact: true }).waitFor();
    await about.getByText("aaaaaaaaaaaa", { exact: true }).waitFor();
    await about.getByText(/优先通过主机网络与系统代理/).waitFor();
