import React, { useEffect, useRef } from "react";
import { PanelLeft } from "lucide-react";
import { api, writeClipboard } from "./api";
import { createPluginViewHost, ProtocolError, ERRORS } from "./plugin-view-host.mjs";
import "./plugin-view.css";

// Theme tokens a page may use to match the workbench, without the app's name.
const TOKENS = ["paper", "ink", "strong", "muted", "line", "chrome", "hover", "selected", "accent", "link", "focus", "input", "button", "button-ink", "success", "danger"];

function currentTheme() {
  const root = document.documentElement, style = getComputedStyle(root);
  const tokens = {};
  for (const name of TOKENS) {
    const value = style.getPropertyValue("--" + name).trim();
    if (value) tokens[name] = value;
  }
  return { scheme: root.dataset.theme === "dark" ? "dark" : "light", tokens };
}

// A page's saved state is a convenience for this viewer only, like a
// webview's state in VS Code: it may be missing, and the page must cope.
const stateKey = (plugin, view) => `plugin-view-state:${plugin}/${view}`;
function loadState(key) {
  try { const value = localStorage.getItem(key); return value === null ? null : JSON.parse(value); } catch { return null; }
}
function saveState(key, state) {
  try { localStorage.setItem(key, JSON.stringify(state)); } catch { /* Storage may be unavailable. */ }
}

// PluginView frames one plugin page in a sandbox and answers its requests.
// The frame has scripts but no same-origin access, so the page reaches the
// app only through the messages handled here.
export function PluginView({ plugin, view, onConfirm, onOpenUrl, onNotify }) {
  const frame = useRef(null);
  const actions = useRef({});
  actions.current = { onConfirm, onOpenUrl, onNotify };
  const openUrls = (view.open_urls || []).join("\n");
  useEffect(() => {
    if (!view.url) return undefined;
    const send = message => frame.current?.contentWindow?.postMessage(message, "*");
    const key = stateKey(plugin, view.id);
    const host = createPluginViewHost({
      plugin, view: view.id, openUrls: openUrls ? openUrls.split("\n") : [],
      call: async request => {
        try {
          return await api("/host-tools/call", { plugin, view: view.id, ...request });
        } catch (error) {
          throw new ProtocolError(error.code || ERRORS.failed, error.detail || error.message);
        }
      },
      confirm: ({ title, message, confirmLabel }) => actions.current.onConfirm({ title, body: message, confirm: confirmLabel || "确定" }),
      openUrl: url => actions.current.onOpenUrl(url),
      copy: text => writeClipboard(text),
      notify: message => actions.current.onNotify(message),
      loadState: () => loadState(key),
      saveState: state => saveState(key, state),
      theme: currentTheme,
    });
    const receive = event => {
      if (!frame.current || event.source !== frame.current.contentWindow) return;
      void host.handle(event.data).then(reply => { if (reply) send(reply); });
    };
    let theme = JSON.stringify(currentTheme());
    const observer = new MutationObserver(() => {
      const next = currentTheme(), text = JSON.stringify(next);
      if (text === theme) return;
      theme = text;
      send({ jsonrpc: "2.0", method: "ui/themeChanged", params: next });
    });
    observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme", "style"] });
    const visibility = () => send({ jsonrpc: "2.0", method: "ui/visibilityChanged", params: { visible: !document.hidden } });
    window.addEventListener("message", receive);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      window.removeEventListener("message", receive);
      document.removeEventListener("visibilitychange", visibility);
      observer.disconnect();
    };
  }, [plugin, view.id, view.url, openUrls]);
  if (!view.url) return (
    <div className="plugin-view-empty">
      <PanelLeft size={26} />
      <strong>这个页面暂时无法打开</strong>
      <p>插件可能已停用或无法运行。可以在插件页查看它的状态。</p>
    </div>
  );
  return <iframe ref={frame} key={view.url} className="plugin-view-frame" src={view.url} title={view.title || view.id}
    sandbox="allow-scripts" referrerPolicy="no-referrer" />;
}
