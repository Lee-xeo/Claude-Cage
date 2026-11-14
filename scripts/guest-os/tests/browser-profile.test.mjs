import assert from "node:assert/strict";
import { test } from "node:test";
import { checkedBrowserProfile, browserProfileFlags } from "../browser-protocol.mjs";
import { nativeBrowserDispatch } from "../macos-browser-window.mjs";

const options = { browser_width: 1280, browser_height: 800, webgl: "disabled", canvas: "blocked" };
test("creation preferences accept only bounded typed fields", () => {
  assert.deepEqual(checkedBrowserProfile(options), options);
  for (const change of [{ browser_width: 0 }, { browser_height: "800" }, { webgl: "spoof" }, { canvas: "random" }, { args: ["--no-sandbox"] }, { proxy: "http://elsewhere" }])
    assert.throws(() => checkedBrowserProfile({ ...options, ...change }), { code: "invalid_configuration" });
  assert.deepEqual(browserProfileFlags(options), ["--window-size=1280,800", "--disable-3d-apis", "--disable-webgl", "--disable-reading-from-canvas"]);
  assert.deepEqual(browserProfileFlags({ ...options, webgl: "native", canvas: "native" }), ["--window-size=1280,800"]);
});
test("macOS creation settings are stored before opening a browser window", async () => {
  let saved;
  const dispatch = nativeBrowserDispatch({ profile: value => { saved = checkedBrowserProfile(value); }, window: () => { throw Error("window must stay closed"); } });
  assert.deepEqual(await dispatch({ method: "POST", path: "/profile", input: options }), { ok: true });
  assert.deepEqual(saved, options);
});
