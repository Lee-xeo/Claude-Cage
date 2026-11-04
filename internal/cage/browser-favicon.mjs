const sourceLimit = 256 * 1024;
const iconLimit = 8192;

// Fetch through Chromium, so icons use the guest's network policy and cache.
export async function readFavicon(cdp, sessionId) {
  const call = (method, params) => cdp(method, params, sessionId);
  const { frameTree } = await call("Page.getFrameTree", {});
  const frameId = frameTree.frame.id;
  if (!/^https?:/.test(frameTree.frame.url)) return "";
  const { executionContextId } = await call("Page.createIsolatedWorld", { frameId, worldName: "cage-favicon" });
  const { result } = await call("Runtime.evaluate", {
    contextId: executionContextId,
    returnByValue: true,
    expression: `Array.from(new Set([
      ...Array.from(document.querySelectorAll('link[rel~="icon"]')).reverse().map(link => link.href),
      new URL('/favicon.ico', location.href).href,
    ])).filter(url => url.length <= 8192).slice(0, 8)`,
  });
  for (const url of result.value || []) {
    let stream;
    try {
      if (/^data:image\/(png|svg\+xml);base64,[a-zA-Z0-9+/=]+$/.test(url) && url.length <= iconLimit) return url;
      if (!/^https?:/.test(url)) continue;
      const { resource } = await call("Network.loadNetworkResource", {
        frameId, url, options: { disableCache: false, includeCredentials: true },
      });
      stream = resource.stream;
      if (!resource.success || !stream || resource.httpStatusCode >= 400) continue;
      const type = String(Object.entries(resource.headers || {}).find(([key]) => key.toLowerCase() === "content-type")?.[1] || "").split(";")[0].trim().toLowerCase();
      if (!/^image\/(png|x-icon|vnd\.microsoft\.icon|jpeg|gif|webp|svg\+xml)$/.test(type)) continue;
      const chunks = [];
      let size = 0;
      for (;;) {
        const chunk = await call("IO.read", { handle: stream, size: Math.min(65536, sourceLimit + 1 - size) });
        const bytes = Buffer.from(chunk.data, chunk.base64Encoded ? "base64" : "utf8");
        size += bytes.length;
        if (size > sourceLimit) throw new Error("Icon exceeds size limit");
        chunks.push(bytes);
        if (chunk.eof) break;
      }
      const data = Buffer.concat(chunks).toString("base64");
      if (type === "image/svg+xml") {
        const icon = `data:${type};base64,${data}`;
        if (icon.length <= iconLimit) return icon;
        continue;
      }
      const { result: image } = await call("Runtime.evaluate", {
        contextId: executionContextId, awaitPromise: true, returnByValue: true,
        expression: `(async () => {
          const bytes = Uint8Array.from(atob(${JSON.stringify(data)}), value => value.charCodeAt(0));
          const image = await createImageBitmap(new Blob([bytes], {type: ${JSON.stringify(type)}}), {resizeWidth: 32, resizeHeight: 32});
          try {
            const canvas = document.createElement('canvas');
            canvas.width = canvas.height = 32;
            canvas.getContext('2d').drawImage(image, 0, 0);
            return canvas.toDataURL('image/png');
          } finally { image.close(); }
        })()`,
      });
      if (typeof image.value === "string" && image.value.startsWith("data:image/png;base64,") && image.value.length <= iconLimit)
        return image.value;
    } catch {
      // A broken or blocked icon must not prevent the tab from opening.
    } finally {
      if (stream) await call("IO.close", { handle: stream }).catch(() => {});
    }
  }
  return "";
}
