// The workbench side of the protocol a plugin page speaks over postMessage:
// JSON-RPC 2.0, as MCP Apps does. The page runs in a sandboxed frame and can
// only reach its own plugin's tools and the few workbench actions below.

export const PROTOCOL_VERSION = 1;
export const MESSAGE_LIMIT = 1 << 20;
export const STATE_LIMIT = 64 << 10;
export const COPY_LIMIT = 64 << 10;
export const CONCURRENCY_LIMIT = 8;
export const FLOATING_BOUNDS = { minWidth: 220, maxWidth: 720, minHeight: 80, maxHeight: 720 };

export const ERRORS = {
  parse: -32700,
  invalidRequest: -32600,
  methodNotFound: -32601,
  invalidParams: -32602,
  denied: -32001,
  notAllowed: -32002,
  unavailable: -32003,
  timeout: -32004,
  failed: -32005,
};

export class ProtocolError extends Error {
  constructor(code, message, data) {
    super(message);
    this.code = code;
    this.data = data;
  }
}

// matchesUrlPattern reports whether url is one the manifest's openUrls allow.
// A pattern names its scheme and host outright; the port may be "*", and the
// path may end in "*" to match everything under it.
export function matchesUrlPattern(raw, pattern) {
  let url;
  try { url = new URL(raw); } catch { return false; }
  if (url.username || url.password) return false;
  const match = /^(https?):\/\/([a-z0-9.-]+)(?::(\*|\d{1,5}))?(\/[^\s#]*)$/i.exec(pattern);
  if (!match) return false;
  const [, scheme, host, port, path] = match;
  if (url.protocol !== scheme.toLowerCase() + ":" || url.hostname !== host.toLowerCase()) return false;
  const standard = url.protocol === "https:" ? "443" : "80";
  if (port !== "*" && (url.port || standard) !== (port || standard)) return false;
  const target = url.pathname + url.search;
  return path.endsWith("*") ? target.startsWith(path.slice(0, -1)) : target === path;
}

const object = value => value !== null && typeof value === "object" && !Array.isArray(value);
const text = (value, limit = 2000) => typeof value === "string" && value.length > 0 && value.length <= limit;

// createPluginViewHost answers one page's requests. The functions it is given
// do the work: call reaches the plugin's tools through the app, and the rest
// use the workbench's own dialogs, browser, clipboard and storage.
export function createPluginViewHost({
  plugin, view, openUrls = [], hasServer = true, presentation = "page",
  call, confirm, openUrl, copy, notify, loadState, saveState, resize, theme, locale = "zh-CN",
}) {
  let initialized = false, running = 0;
  const methods = {
    "ui/initialize": params => {
      if (params?.protocolVersion !== PROTOCOL_VERSION) throw new ProtocolError(ERRORS.invalidParams, "unsupported protocolVersion");
      initialized = true;
      return { protocolVersion: PROTOCOL_VERSION, plugin, view, presentation, theme: theme(), locale, state: loadState() ?? null };
    },
    "tools/list": async () => {
      if (!hasServer) throw new ProtocolError(ERRORS.unavailable, "this page has no server");
      return call({ method: "tools/list" });
    },
    "tools/call": async params => {
      if (!hasServer) throw new ProtocolError(ERRORS.unavailable, "this page has no server");
      if (!text(params?.name, 200) || params.arguments !== undefined && !object(params.arguments))
        throw new ProtocolError(ERRORS.invalidParams, "name and an arguments object are expected");
      return call({ method: "tools/call", name: params.name, arguments: params.arguments || {} });
    },
    "ui/openUrl": async params => {
      if (!text(params?.url, 4096)) throw new ProtocolError(ERRORS.invalidParams, "url is expected");
      if (!openUrls.some(pattern => matchesUrlPattern(params.url, pattern)))
        throw new ProtocolError(ERRORS.notAllowed, "url is not in the manifest's openUrls");
      if (!await openUrl(params.url)) throw new ProtocolError(ERRORS.failed, "the page could not be opened");
      return {};
    },
    "ui/confirm": async params => {
      if (!text(params?.title, 200) || !text(params?.message) || params.confirmLabel !== undefined && !text(params.confirmLabel, 40))
        throw new ProtocolError(ERRORS.invalidParams, "title and message are expected");
      return { confirmed: Boolean(await confirm({ title: params.title, message: params.message, confirmLabel: params.confirmLabel, danger: Boolean(params.danger) })) };
    },
    "ui/copy": async params => {
      if (typeof params?.text !== "string" || params.text.length > COPY_LIMIT) throw new ProtocolError(ERRORS.invalidParams, "text up to 64 KiB is expected");
      await copy(params.text);
      return {};
    },
    "ui/notify": params => {
      if (!text(params?.message, 500) || params.tone !== undefined && !["info", "error"].includes(params.tone))
        throw new ProtocolError(ERRORS.invalidParams, "message is expected");
      notify(params.message, params.tone || "info");
      return {};
    },
    "ui/setState": params => {
      if (!object(params) || !("state" in params)) throw new ProtocolError(ERRORS.invalidParams, "state is expected");
      const serialized = JSON.stringify(params.state);
      if (serialized === undefined || serialized.length > STATE_LIMIT) throw new ProtocolError(ERRORS.notAllowed, "state is over 64 KiB");
      saveState(params.state);
      return {};
    },
    "ui/resize": params => {
      if (!object(params)) throw new ProtocolError(ERRORS.invalidParams, "width or height is expected");
      const hasW = params.width !== undefined, hasH = params.height !== undefined;
      if (!hasW && !hasH) throw new ProtocolError(ERRORS.invalidParams, "width or height is expected");
      const validDim = value => typeof value === "number" && Number.isFinite(value) && value > 0;
      if (hasW && !validDim(params.width) || hasH && !validDim(params.height))
        throw new ProtocolError(ERRORS.invalidParams, "width and height must be positive numbers");
      const next = {};
      if (hasW) next.width = Math.round(Math.min(FLOATING_BOUNDS.maxWidth, Math.max(FLOATING_BOUNDS.minWidth, params.width)));
      if (hasH) next.height = Math.round(Math.min(FLOATING_BOUNDS.maxHeight, Math.max(FLOATING_BOUNDS.minHeight, params.height)));
      if (presentation === "floating" && typeof resize === "function") resize(next);
      return next;
    },
  };

  // handle returns the reply to send, or null for a message that needs none.
  async function handle(message) {
    let size;
    try { size = JSON.stringify(message)?.length ?? 0; } catch { size = Infinity; }
    const id = object(message) && (typeof message.id === "number" || typeof message.id === "string") ? message.id : null;
    const reply = (result, error) => error
      ? { jsonrpc: "2.0", id, error: { code: error.code, message: error.message, ...(error.data ? { data: error.data } : {}) } }
      : { jsonrpc: "2.0", id, result };
    if (!object(message) || message.jsonrpc !== "2.0" || typeof message.method !== "string")
      return id === null ? null : reply(null, new ProtocolError(ERRORS.invalidRequest, "a JSON-RPC 2.0 request is expected"));
    // Notifications from the page carry nothing the workbench acts on yet.
    if (id === null) return null;
    if (size > MESSAGE_LIMIT) return reply(null, new ProtocolError(ERRORS.notAllowed, "message is over 1 MiB"));
    const method = methods[message.method];
    if (!method) return reply(null, new ProtocolError(ERRORS.methodNotFound, `no method ${message.method}`));
    if (!initialized && message.method !== "ui/initialize") return reply(null, new ProtocolError(ERRORS.notAllowed, "call ui/initialize first"));
    if (running >= CONCURRENCY_LIMIT) return reply(null, new ProtocolError(ERRORS.notAllowed, "too many requests at once"));
    running++;
    try {
      return reply(await method(message.params));
    } catch (error) {
      return reply(null, error instanceof ProtocolError ? error : new ProtocolError(error?.code || ERRORS.failed, error?.message || "request failed"));
    } finally {
      running--;
    }
  }
  return { handle };
}
