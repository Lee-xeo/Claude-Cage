import { randomBytes } from "node:crypto";

// This runs only in a CDP isolated world. Website globals and clipboard APIs
// stay unchanged; this observer records trusted input, never clipboard data.
function observeInput(binding, token, key) {
  if (globalThis[key]) return;
  let context = "", epoch = 0;
  const current = () => document.hasFocus() && document.visibilityState === "visible";
  globalThis[key] = {
    set(value) { if (!value) epoch++; context = value; },
    valid(value) { return value === epoch && current() && navigator.userActivation.isActive; },
  };
  const input = event => {
    if (event.isTrusted && context && current()) globalThis[binding](JSON.stringify({ token, context, epoch }));
  };
  for (const name of ["pointerdown", "click", "keydown", "copy", "cut"]) document.addEventListener(name, input, true);
  const cancel = () => { epoch++; };
  window.addEventListener("blur", event => { if (event.target === window) cancel(); }, true);
  window.addEventListener("pagehide", cancel, true);
  document.addEventListener("visibilitychange", cancel, true);
}

// The controller owns this private page and its clipboard-read permission.
// Websites never receive that permission or this binding. Only successful
// native changes trigger reads, including repeated copies of identical text.
function observeClipboard(binding, token, includeChanges) {
  if (!navigator.clipboard || !("onclipboardchange" in navigator.clipboard)) return false;
  const emit = globalThis[binding];
  let version = 0;
  navigator.clipboard.addEventListener("clipboardchange", async event => {
    if (!event.isTrusted) return;
    const serial = ++version;
    emit(JSON.stringify({ token, serial, begin: true }));
    try {
      let text;
      if (!event.types.includes("image/png")) text = await navigator.clipboard.readText();
      if (serial !== version) return;
      if (typeof text === "string" && text.length <= 65536) emit(JSON.stringify({ token, serial, text }));
      else if (includeChanges) emit(JSON.stringify({ token, serial, changed: true }));
    } catch {}
  });
  return true;
}

export class BrowserClipboard {
  constructor(cdp, onCopy, { includeChanges = false, source } = {}) {
    Object.assign(this, { cdp, onCopy, includeChanges, source });
    this.sessions = new Map();
    this.copyContexts = new Map();
  }

  async attach(sessionId, tabId) {
    if (this.sessions.has(sessionId)) return this.sessions.get(sessionId).ready;
    const state = { tabId, contexts: new Map(), documents: new Map(), installing: new Set(), world: randomBytes(16).toString("hex"),
      key: randomBytes(16).toString("hex"), binding: randomBytes(16).toString("hex"), token: randomBytes(32).toString("hex") };
    this.sessions.set(sessionId, state);
    state.ready = this.#install(sessionId, state);
    return state.ready;
  }

  async #install(sessionId, state) {
    try {
      // Website sessions never enable the Runtime domain: an enabled domain
      // changes how the page's own console calls behave. Each document's
      // isolated world is reached through the Page domain instead.
      await this.cdp("Page.enable", {}, sessionId);
      const source = `(${observeInput})(${JSON.stringify(state.binding)},${JSON.stringify(state.token)},${JSON.stringify(state.key)})`;
      state.source = source;
      await this.cdp("Page.addScriptToEvaluateOnNewDocument", { source, worldName: state.world }, sessionId);
      const { frameTree } = await this.cdp("Page.getFrameTree", {}, sessionId);
      const frames = node => [node.frame.id, ...(node.childFrames || []).flatMap(frames)];
      const skipped = [];
      for (const frameId of frameTree ? frames(frameTree) : []) {
        try { await this.#world(sessionId, state, frameId); }
        catch { skipped.push({ frameId, reason: "Document changed during input observer installation" }); }
      }
      await this.cdp("Target.setAutoAttach", { autoAttach: true, waitForDebuggerOnStart: false, flatten: true,
        filter: [{ type: "iframe" }, { exclude: true }] }, sessionId);
      if (this.copyContexts.has(state.tabId)) await this.#applyContext(sessionId, state, this.copyContexts.get(state.tabId));
      return { skipped };
    } catch (error) {
      this.sessions.delete(sessionId);
      throw error;
    }
  }

  // Binds the current document of one frame. The binding exists only in that
  // document's isolated world, and a newer document supersedes this attempt.
  async #world(sessionId, state, frameId) {
    const document = state.documents.get(frameId) || 0;
    const current = () => this.sessions.get(sessionId) === state && (state.documents.get(frameId) || 0) === document;
    const { executionContextId } = await this.cdp("Page.createIsolatedWorld", { frameId, worldName: state.world }, sessionId);
    if (!current()) return;
    await this.cdp("Runtime.addBinding", { name: state.binding, executionContextId }, sessionId);
    if (!current()) return;
    state.contexts.set(executionContextId, frameId);
    await this.cdp("Runtime.evaluate", { expression: state.source, contextId: executionContextId }, sessionId);
    if (current() && this.copyContexts.has(state.tabId))
      await this.#applyContext(sessionId, state, this.copyContexts.get(state.tabId), [executionContextId]);
  }

  async #source() {
    if (!this.sourceReady) {
      this.sourceReady = (async () => {
        if (!this.source) throw new Error("Clipboard change source unavailable");
        const session = await this.source();
        const name = randomBytes(16).toString("hex"), token = randomBytes(32).toString("hex");
        await this.cdp("Runtime.enable", {}, session);
        await this.cdp("Runtime.addBinding", { name }, session);
        await this.cdp("Emulation.setFocusEmulationEnabled", { enabled: true }, session);
        const installed = await this.cdp("Runtime.evaluate", {
          expression: `(${observeClipboard})(${JSON.stringify(name)},${JSON.stringify(token)},${this.includeChanges})`, returnByValue: true,
        }, session);
        if (installed.result?.value !== true) throw new Error("Native clipboard change notifications unavailable");
        this.clipboardSource = { session, name, token };
      })().catch(error => { this.sourceReady = undefined; throw error; });
    }
    return this.sourceReady;
  }

  async setContext(id, context) {
    if (typeof context !== "string" || context.length > 64) throw new Error("Invalid clipboard context");
    this.copyContexts.set(id, context);
    if (context && this.available !== false) {
      try { await this.#source(); this.available = true; }
      catch { this.available = false; }
    }
    const applied = new Set();
    for (;;) {
      const entries = [...this.sessions].filter(([, state]) => state.tabId === id && !applied.has(state));
      if (!entries.length) return;
      await Promise.all(entries.map(async ([session, state]) => {
        await state.ready;
        await Promise.all(state.installing);
        if (this.sessions.get(session) === state) await this.#applyContext(session, state, context);
        applied.add(state);
      }));
    }
  }

  async #applyContext(session, state, context, ids = [...state.contexts.keys()]) {
    await Promise.all(ids.map(contextId => this.cdp("Runtime.evaluate", {
      expression: `globalThis[${JSON.stringify(state.key)}]?.set(${JSON.stringify(context)})`, contextId,
    }, session).catch(() => {})));
  }

  async #copied(value, input) {
    if (!input || !this.sessions.get(input.session)?.contexts.has(input.contextId)) return;
    try {
      const valid = await this.cdp("Runtime.evaluate", {
        expression: `globalThis[${JSON.stringify(input.key)}]?.valid(${input.epoch}) === true`,
        contextId: input.contextId, returnByValue: true,
      }, input.session);
      if (valid.result?.value !== true || this.input !== input || !this.sessions.get(input.session)?.contexts.has(input.contextId)) return;
      this.onCopy({ id: input.tabId, context: input.context,
        ...(typeof value.text === "string" ? { text: value.text } : { changed: true }) });
    } catch {}
  }

  event(event) {
    const p = event.params || {};
    if (event.method === "Runtime.bindingCalled" && this.clipboardSource?.session === event.sessionId && p.name === this.clipboardSource.name) {
      if (typeof p.payload !== "string" || p.payload.length > 6 * 65536 + 200) return;
      try {
        const value = JSON.parse(p.payload);
        if (value?.token !== this.clipboardSource.token || !Number.isSafeInteger(value.serial) || value.serial < 1) return;
        if (value.begin === true) {
          this.receipt = { serial: value.serial, input: this.input };
          return;
        }
        if (value.serial !== this.receipt?.serial || !(typeof value.text === "string" && value.text.length <= 65536 ||
            this.includeChanges && value.changed === true && value.text === undefined)) return;
        const receipt = this.receipt;
        this.receipt = undefined;
        void this.#copied(value, receipt.input);
      } catch {}
      return;
    }
    const state = this.sessions.get(event.sessionId);
    if (!state) return;
    if (event.method === "Target.attachedToTarget" && p.targetInfo?.type === "iframe") {
      void this.attach(p.sessionId, state.tabId).catch(() => {});
    } else if (event.method === "Target.detachedFromTarget") this.detachSession(p.sessionId);
    else if (event.method === "Page.frameDetached" || event.method === "Page.frameNavigated") {
      const frame = p.frameId || p.frame?.id;
      state.documents.set(frame, (state.documents.get(frame) || 0) + 1);
      for (const [id, value] of state.contexts) if (value === frame) state.contexts.delete(id);
      if (event.method === "Page.frameDetached") return;
      // The document-start script has already run in the new document; give
      // it the binding and the host context, including frames created after
      // the host copy handshake.
      const pending = Promise.resolve().then(async () => {
        await state.ready;
        await this.#world(event.sessionId, state, frame);
      }).catch(() => {}).finally(() => state.installing.delete(pending));
      state.installing.add(pending);
    } else if (event.method === "Runtime.bindingCalled" && p.name === state.binding && state.contexts.has(p.executionContextId)) {
      if (typeof p.payload !== "string" || p.payload.length > 1024) return;
      try {
        const value = JSON.parse(p.payload);
        if (value?.token !== state.token || !value.context || typeof value.context !== "string" || value.context.length > 64 ||
            !Number.isSafeInteger(value.epoch) || value.epoch < 0) return;
        this.input = { key: state.key, tabId: state.tabId, session: event.sessionId, contextId: p.executionContextId,
          context: value.context, epoch: value.epoch };
      } catch {}
    }
  }

  detachSession(id) {
    this.sessions.delete(id);
    if (this.input?.session === id) this.input = undefined;
  }

  detachTab(id) {
    this.copyContexts.delete(id);
    for (const [session, state] of this.sessions) if (state.tabId === id) this.detachSession(session);
  }
}
