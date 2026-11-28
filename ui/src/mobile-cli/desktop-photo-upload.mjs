import { desktopRequest } from "./transport.mjs";

const DEFAULT_CHUNK_SIZE = 44 * 1024;
const MAX_PNG_BYTES = 20 * 1024 * 1024;
const PASSTHROUGH_PNG_BYTES = 8 * 1024 * 1024;
const MAX_LONG_EDGE = 2048;

export function encodeBase64(bytes) {
  let binary = "";
  const step = 0x4000;
  for (let i = 0; i < bytes.length; i += step) {
    const slice = bytes.subarray(i, Math.min(i + step, bytes.length));
    binary += String.fromCharCode(...slice);
  }
  return btoa(binary);
}

function isPNGHeader(bytes) {
  return bytes.length >= 8 &&
    bytes[0] === 0x89 && bytes[1] === 0x50 && bytes[2] === 0x4e && bytes[3] === 0x47 &&
    bytes[4] === 0x0d && bytes[5] === 0x0a && bytes[6] === 0x1a && bytes[7] === 0x0a;
}

async function decodeImageSource(file) {
  if (typeof createImageBitmap === "function") {
    try {
      const bitmap = await createImageBitmap(file);
      return {
        source: bitmap,
        width: bitmap.width,
        height: bitmap.height,
        close: () => bitmap.close?.(),
      };
    } catch {
      // Fall back to HTMLImageElement when a WebView rejects createImageBitmap on HEIC/etc.
    }
  }
  const url = URL.createObjectURL(file);
  try {
    const img = new Image();
    img.src = url;
    await img.decode();
    return {
      source: img,
      width: img.naturalWidth || img.width,
      height: img.naturalHeight || img.height,
      close: () => {},
    };
  } finally {
    URL.revokeObjectURL(url);
  }
}

async function renderCanvasPNG(source, width, height) {
  const canvas = document.createElement("canvas");
  canvas.width = width;
  canvas.height = height;
  const ctx = canvas.getContext("2d");
  if (!ctx) throw new Error("当前浏览器无法转换图片格式。");
  ctx.drawImage(source, 0, 0, width, height);
  const blob = await new Promise((resolve, reject) => {
    canvas.toBlob(result => result ? resolve(result) : reject(new Error("图片转换失败。")), "image/png");
  });
  return new Uint8Array(await blob.arrayBuffer());
}

export async function preparePhotoPNG(file) {
  if (!file || typeof file.arrayBuffer !== "function") {
    throw new Error("无法读取选中的图片。");
  }
  const raw = new Uint8Array(await file.arrayBuffer());
  if (!raw.length) throw new Error("所选图片为空。");
  if (isPNGHeader(raw) && raw.length <= PASSTHROUGH_PNG_BYTES) {
    return raw;
  }
  const decoded = await decodeImageSource(file);
  try {
    if (!decoded.width || !decoded.height) throw new Error("图片尺寸无效。");
    let scale = Math.min(1, MAX_LONG_EDGE / Math.max(decoded.width, decoded.height));
    for (let attempt = 0; attempt < 4; attempt++) {
      const width = Math.max(1, Math.round(decoded.width * scale));
      const height = Math.max(1, Math.round(decoded.height * scale));
      const png = await renderCanvasPNG(decoded.source, width, height);
      if (png.length <= MAX_PNG_BYTES) return png;
      scale *= 0.72;
    }
    throw new Error("图片过大，超出 20 MB 上限。");
  } finally {
    decoded.close();
  }
}

export async function uploadDesktopPhoto({
  sessionId,
  bytes,
  request = desktopRequest,
  signal,
  onProgress,
}) {
  if (!(bytes instanceof Uint8Array) || bytes.length === 0) {
    throw new Error("图片数据无效。");
  }
  if (bytes.length > MAX_PNG_BYTES) {
    throw new Error("单张图片不能超过 20 MB。");
  }
  const endpoint = "/" + sessionId + "/upload";
  const started = await request(endpoint, {
    body: { op: "start", size: bytes.length },
    signal,
  });
  const uploadId = started?.upload_id;
  if (!uploadId) throw new Error("主机未确认图片上传。");
  const chunkSize = Number.isInteger(started.chunk_size) && started.chunk_size >= 4096 && started.chunk_size <= 48 * 1024
    ? started.chunk_size
    : DEFAULT_CHUNK_SIZE;
  let finished = false;
  try {
    for (let offset = 0; offset < bytes.length; offset += chunkSize) {
      if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
      const end = Math.min(offset + chunkSize, bytes.length);
      const reply = await request(endpoint, {
        body: {
          op: "chunk",
          upload_id: uploadId,
          offset,
          data: encodeBase64(bytes.subarray(offset, end)),
        },
        signal,
      });
      const received = Number.isFinite(reply?.received) ? reply.received : end;
      onProgress?.(Math.min(1, received / bytes.length));
    }
    if (signal?.aborted) throw new DOMException("Aborted", "AbortError");
    await request(endpoint, {
      body: { op: "finish", upload_id: uploadId },
      signal,
    });
    finished = true;
    onProgress?.(1);
  } catch (error) {
    if (!finished) {
      void request(endpoint, { body: { op: "cancel", upload_id: uploadId } }).catch(() => {});
    }
    throw error;
  }
}

export async function uploadDesktopPhotoBatch({
  sessionId,
  items,
  prepare = preparePhotoPNG,
  request = desktopRequest,
  signal,
  onUpdate,
  settleMs = 180,
}) {
  const total = items.length;
  let completed = 0;
  for (let index = 0; index < total; index++) {
    if (signal?.aborted) {
      return { completed, remaining: items.slice(index), aborted: true, error: "" };
    }
    const item = items[index];
    onUpdate?.({ active: true, total, current: index + 1, completed, percent: 0, error: "", remaining: items.slice(index) });
    try {
      const bytes = item.bytes ?? await prepare(item.file);
      item.bytes = bytes;
      await uploadDesktopPhoto({
        sessionId,
        bytes,
        request,
        signal,
        onProgress: ratio => {
          onUpdate?.({
            active: true,
            total,
            current: index + 1,
            completed,
            percent: Math.round(ratio * 100),
            error: "",
            remaining: items.slice(index),
          });
        },
      });
      completed++;
      if (index + 1 < total && settleMs > 0) {
        await new Promise(resolve => setTimeout(resolve, settleMs));
      }
    } catch (error) {
      if (signal?.aborted || error?.name === "AbortError") {
        return { completed, remaining: items.slice(index), aborted: true, error: "" };
      }
      const message = error?.status === 404
        ? "这台电脑尚不支持手机相册传图，请更新电脑端 Cage。"
        : (error?.message || "图片添加失败，请重试。");
      const remaining = items.slice(index);
      onUpdate?.({ active: false, total, current: index + 1, completed, percent: 0, error: message, remaining });
      return { completed, remaining, aborted: false, error: message };
    }
  }
  onUpdate?.({ active: false, total, current: total, completed, percent: 100, error: "", remaining: [] });
  return { completed, remaining: [], aborted: false, error: "" };
}
