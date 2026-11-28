import test from "node:test";
import assert from "node:assert/strict";

globalThis.location = { pathname: "/" };

const {
  uploadDesktopPhoto,
  uploadDesktopPhotoBatch,
} = await import("./mobile-cli/desktop-photo-upload.mjs");

test("uploadDesktopPhoto chunks payload, reports progress, and finishes cleanly (#1007)", async () => {
  const bytes = new Uint8Array(10000);
  for (let i = 0; i < bytes.length; i++) bytes[i] = i & 0xff;
  const calls = [];
  const progress = [];

  const request = async (path, { body }) => {
    calls.push({ path, op: body.op, offset: body.offset, size: body.size });
    if (body.op === "start") return { upload_id: "up-1", chunk_size: 4096 };
    if (body.op === "chunk") return { upload_id: "up-1", received: body.offset + Buffer.from(body.data, "base64").length };
    if (body.op === "finish") return { ok: true };
    throw new Error("unexpected op " + body.op);
  };

  await uploadDesktopPhoto({
    sessionId: "sess-1",
    bytes,
    request,
    onProgress: p => progress.push(Math.round(p * 100)),
  });

  assert.deepEqual(calls.map(c => c.op), ["start", "chunk", "chunk", "chunk", "finish"]);
  assert.deepEqual(calls.filter(c => c.op === "chunk").map(c => c.offset), [0, 4096, 8192]);
  assert.equal(progress.at(-1), 100);
});

test("uploadDesktopPhoto cancels server staging when aborted mid-upload (#1007)", async () => {
  const bytes = new Uint8Array(12000);
  const controller = new AbortController();
  const ops = [];

  const request = async (_path, { body }) => {
    ops.push(body.op);
    if (body.op === "start") return { upload_id: "up-2", chunk_size: 4096 };
    if (body.op === "chunk") {
      controller.abort();
      return { upload_id: "up-2", received: body.offset + 4096 };
    }
    if (body.op === "cancel") return { ok: true };
    throw new Error("unexpected op " + body.op);
  };

  await assert.rejects(
    uploadDesktopPhoto({ sessionId: "sess-1", bytes, request, signal: controller.signal }),
    { name: "AbortError" },
  );
  assert.deepEqual(ops, ["start", "chunk", "cancel"]);
});

test("uploadDesktopPhotoBatch preserves failed tail for retry (#1007)", async () => {
  let failOnce = true;
  const completedIDs = [];
  const request = async (_path, { body }) => {
    if (body.op === "start") return { upload_id: "up-" + body.size, chunk_size: 4096 };
    if (body.op === "chunk") {
      if (body.upload_id === "up-200" && failOnce) {
        failOnce = false;
        throw new Error("临时断网");
      }
      return { upload_id: body.upload_id, received: body.offset + Buffer.from(body.data, "base64").length };
    }
    if (body.op === "finish") {
      completedIDs.push(body.upload_id);
      return { ok: true };
    }
    if (body.op === "cancel") return { ok: true };
    throw new Error("unexpected op");
  };

  const items = [
    { name: "a.png", bytes: new Uint8Array(100) },
    { name: "b.png", bytes: new Uint8Array(200) },
    { name: "c.png", bytes: new Uint8Array(300) },
  ];
  const first = await uploadDesktopPhotoBatch({ sessionId: "sess-1", items, request, settleMs: 0 });
  assert.equal(first.completed, 1);
  assert.equal(first.remaining.length, 2);
  assert.equal(first.error, "临时断网");

  // Retry only remaining items.
  const retry = await uploadDesktopPhotoBatch({ sessionId: "sess-1", items: first.remaining, request, settleMs: 0 });
  assert.equal(retry.completed, 2);
  assert.equal(retry.remaining.length, 0);
  assert.equal(retry.error, "");
  assert.deepEqual(completedIDs, ["up-100", "up-200", "up-300"]);
});
