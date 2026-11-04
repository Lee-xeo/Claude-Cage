import test from "node:test";
import assert from "node:assert/strict";
import { readFavicon } from "./browser-favicon.mjs";

function fixture({ urls = ["https://example.com/icon.png"], type = "image/png", size = 4, failRead = false } = {}) {
  const closed = [], loads = [];
  const cdp = async (method, params, sid) => {
    assert.equal(sid, "tab-session");
    switch (method) {
      case "Page.getFrameTree": return { frameTree: { frame: { id: "frame", url: "https://example.com/" } } };
      case "Page.createIsolatedWorld": return { executionContextId: 42 };
      case "Runtime.evaluate":
        assert.equal(params.contextId, 42);
        return { result: { value: params.awaitPromise ? "data:image/png;base64,aWNvbg==" : urls } };
      case "Network.loadNetworkResource":
        loads.push(params.url);
        return { resource: { success: true, httpStatusCode: 200, stream: "icon-stream", headers: { "Content-Type": type } } };
      case "IO.read":
        if (failRead) throw new Error("disconnected");
        return { data: Buffer.alloc(size).toString("base64"), base64Encoded: true, eof: true };
      case "IO.close": closed.push(params.handle); return {};
      default: throw new Error(`unexpected ${method}`);
    }
  };
  return { cdp, closed, loads };
}

test("guest Chromium fetches and normalizes icons, closing the resource stream", async () => {
  const f = fixture();
  assert.match(await readFavicon(f.cdp, "tab-session"), /^data:image\/png;base64,/);
  assert.deepEqual(f.loads, ["https://example.com/icon.png"]);
  assert.deepEqual(f.closed, ["icon-stream"]);
});

test("disallowed URLs cannot read local files; small embedded icons need no network", async () => {
  const icon = "data:image/svg+xml;base64,PHN2Zy8+";
  const f = fixture({ urls: ["file:///etc/passwd", "javascript:alert(1)", icon] });
  assert.equal(await readFavicon(f.cdp, "tab-session"), icon);
  assert.deepEqual(f.loads, []);
});

for (const options of [{ type: "text/html" }, { size: 256 * 1024 + 1 }, { failRead: true }]) {
  test(`invalid, oversized or disconnected icon falls back and releases its stream: ${JSON.stringify(options)}`, async () => {
    const f = fixture(options);
    assert.equal(await readFavicon(f.cdp, "tab-session"), "");
    assert.deepEqual(f.closed, ["icon-stream"]);
  });
}
