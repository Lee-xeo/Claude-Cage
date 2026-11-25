import React, { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ChevronDown, ChevronUp, GripHorizontal, PanelLeft, X } from "lucide-react";
import { api, writeClipboard } from "./api";
import { clientStorageKey } from "./client-context.mjs";
import { createPluginViewHost, ProtocolError, ERRORS } from "./plugin-view-host.mjs";
import {
  COLLAPSED_FLOATING_WIDTH,
  DEFAULT_FLOATING_HEIGHT,
  DEFAULT_FLOATING_WIDTH,
  FLOATING_HEADER_HEIGHT,
  clampFloatingPosition,
  clampFloatingSize,
  defaultFloatingPosition,
} from "./plugin-floating-state.mjs";
import "./plugin-view.css";
import { activeInterfaceLanguage, useTranslation } from "./interface-language.mjs";

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
const stateKey = (plugin, view) => clientStorageKey(`plugin-view-state:${plugin}/${view}`);
const floatingLayoutKey = key => clientStorageKey(`plugin-floating-layout:${key}`);

function loadState(key) {
  try { const value = localStorage.getItem(key); return value === null ? null : JSON.parse(value); } catch { return null; }
}
function saveState(key, state) {
  try { localStorage.setItem(key, JSON.stringify(state)); } catch { /* Storage may be unavailable. */ }
}

const currentViewport = () => ({ width: window.innerWidth, height: window.innerHeight });

// PluginView frames one plugin page in a sandbox and answers its requests.
// The frame has scripts but no same-origin access, so the page reaches the
// app only through the messages handled here.
export function PluginView({ plugin, view, visible = true, presentation, onResize, onConfirm, onOpenUrl, onNotify }) {
  const tr = useTranslation();
  const frame = useRef(null);
  const actions = useRef({});
  const mode = presentation || (view.presentation === "floating" ? "floating" : "page");
  actions.current = { onConfirm, onOpenUrl, onNotify, onResize, visible };
  const openUrls = (view.open_urls || []).join("\n");
  useEffect(() => {
    if (!view.url) return undefined;
    const send = message => frame.current?.contentWindow?.postMessage(message, "*");
    const key = stateKey(plugin, view.id);
    const host = createPluginViewHost({
      plugin, view: view.id, presentation: mode, openUrls: openUrls ? openUrls.split("\n") : [],
      locale: activeInterfaceLanguage(),
      call: async request => {
        try {
          return await api("/host-tools/call", { plugin, view: view.id, ...request });
        } catch (error) {
          throw new ProtocolError(error.code || ERRORS.failed, error.detail || error.message);
        }
      },
      confirm: ({ title, message, confirmLabel, danger }) =>
        actions.current.onConfirm({ title, body: message, confirm: confirmLabel || "确定", danger: danger === true }),
      openUrl: url => actions.current.onOpenUrl(url),
      copy: text => writeClipboard(text),
      notify: (message, tone) => actions.current.onNotify(message, tone),
      loadState: () => loadState(key),
      saveState: state => saveState(key, state),
      resize: next => actions.current.onResize?.(next),
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
    const visibility = () => send({
      jsonrpc: "2.0",
      method: "ui/visibilityChanged",
      params: { visible: !document.hidden && actions.current.visible !== false },
    });
    window.addEventListener("message", receive);
    document.addEventListener("visibilitychange", visibility);
    return () => {
      window.removeEventListener("message", receive);
      document.removeEventListener("visibilitychange", visibility);
      observer.disconnect();
    };
  }, [plugin, view.id, view.url, openUrls, mode]);
  useEffect(() => {
    frame.current?.contentWindow?.postMessage(
      { jsonrpc: "2.0", method: "ui/visibilityChanged", params: { visible: !document.hidden && visible } },
      "*",
    );
  }, [visible]);
  if (!view.url) return (
    <div className="plugin-view-empty">
      <PanelLeft size={26} />
      <strong>{tr("这个页面暂时无法打开")}</strong>
      <p>{tr("插件可能已停用或无法运行。可以在插件页查看它的状态。")}</p>
    </div>
  );
  return <iframe ref={frame} key={view.url}
    className={`plugin-view-frame${mode === "floating" ? " plugin-view-frame-floating" : ""}`}
    src={view.url} title={view.title || view.id}
    sandbox="allow-scripts" referrerPolicy="no-referrer" />;
}

function FloatingIcon({ src }) {
  const [failed, setFailed] = useState(false);
  if (!src || failed) return <PanelLeft size={14} aria-hidden="true" className="plugin-floating-icon-fallback" />;
  return <img className="plugin-floating-icon" src={src} alt="" draggable="false" onError={() => setFailed(true)} />;
}

// FloatingPluginView hosts a plugin view in a draggable, translucent frosted-glass
// overlay so it can float over terminals, browsers and desktops without replacing them.
export function FloatingPluginView({ page, stackIndex = 0, zIndex = 18, onFocus, onClose, onConfirm, onOpenUrl, onNotify }) {
  const tr = useTranslation();
  const storageKey = floatingLayoutKey(page.key);
  const initialSize = clampFloatingSize(
    { width: page.view.width || DEFAULT_FLOATING_WIDTH, height: page.view.height || DEFAULT_FLOATING_HEIGHT },
    currentViewport(),
  );
  const [size, setSize] = useState(initialSize);
  const [collapsed, setCollapsed] = useState(() => Boolean(loadState(storageKey)?.collapsed));
  const [position, setPosition] = useState(() => {
    const saved = loadState(storageKey);
    const box = {
      width: saved?.collapsed ? COLLAPSED_FLOATING_WIDTH : initialSize.width,
      height: saved?.collapsed ? FLOATING_HEADER_HEIGHT : initialSize.height + FLOATING_HEADER_HEIGHT,
    };
    const viewport = currentViewport();
    if (saved && Number.isFinite(saved.x) && Number.isFinite(saved.y)) {
      return clampFloatingPosition(saved, box, viewport);
    }
    return defaultFloatingPosition(box, viewport, stackIndex);
  });
  const [dragging, setDragging] = useState(false);
  const dragRef = useRef(null);
  const posRef = useRef(position);
  posRef.current = position;

  const shownWidth = collapsed ? COLLAPSED_FLOATING_WIDTH : size.width;
  const shownHeight = collapsed ? FLOATING_HEADER_HEIGHT : size.height + FLOATING_HEADER_HEIGHT;

  const persistLayout = useCallback((nextPos, nextCollapsed) => {
    saveState(storageKey, { x: nextPos.x, y: nextPos.y, collapsed: nextCollapsed });
  }, [storageKey]);

  const handleResize = useCallback((requested) => {
    setSize(prev => clampFloatingSize(
      { width: requested.width ?? prev.width, height: requested.height ?? prev.height },
      currentViewport(),
    ));
  }, []);

  useLayoutEffect(() => {
    const clampToViewport = () => {
      const viewport = currentViewport();
      setSize(prev => {
        const clamped = clampFloatingSize(prev, viewport);
        return clamped.width === prev.width && clamped.height === prev.height ? prev : clamped;
      });
      setPosition(prev => {
        const clamped = clampFloatingPosition(prev, { width: shownWidth, height: shownHeight }, viewport);
        return clamped.x === prev.x && clamped.y === prev.y ? prev : clamped;
      });
    };
    clampToViewport();
    window.addEventListener("resize", clampToViewport);
    return () => window.removeEventListener("resize", clampToViewport);
  }, [shownWidth, shownHeight]);

  const startDrag = event => {
    if (event.button !== 0 || !event.isPrimary) return;
    if (event.target.closest("button")) return;
    onFocus?.();
    event.currentTarget.setPointerCapture?.(event.pointerId);
    dragRef.current = {
      pointerId: event.pointerId,
      startX: event.clientX,
      startY: event.clientY,
      originX: posRef.current.x,
      originY: posRef.current.y,
    };
    setDragging(true);
  };

  const moveDrag = event => {
    const drag = dragRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    const next = clampFloatingPosition(
      { x: drag.originX + (event.clientX - drag.startX), y: drag.originY + (event.clientY - drag.startY) },
      { width: shownWidth, height: shownHeight },
      currentViewport(),
    );
    setPosition(next);
  };

  const finishDrag = event => {
    const drag = dragRef.current;
    if (!drag || drag.pointerId !== event.pointerId) return;
    dragRef.current = null;
    if (event.currentTarget.hasPointerCapture?.(event.pointerId)) {
      event.currentTarget.releasePointerCapture(event.pointerId);
    }
    setDragging(false);
    persistLayout(posRef.current, collapsed);
  };

  const resetPosition = event => {
    if (event.target.closest("button")) return;
    const next = defaultFloatingPosition({ width: shownWidth, height: shownHeight }, currentViewport(), stackIndex);
    setPosition(next);
    persistLayout(next, collapsed);
  };

  const toggleCollapsed = () => {
    const nextCollapsed = !collapsed;
    setCollapsed(nextCollapsed);
    const nextBox = {
      width: nextCollapsed ? COLLAPSED_FLOATING_WIDTH : size.width,
      height: nextCollapsed ? FLOATING_HEADER_HEIGHT : size.height + FLOATING_HEADER_HEIGHT,
    };
    const nextPos = clampFloatingPosition(posRef.current, nextBox, currentViewport());
    setPosition(nextPos);
    persistLayout(nextPos, nextCollapsed);
  };

  const onHeaderKeyDown = event => {
    if (event.target !== event.currentTarget) return;
    const step = event.shiftKey ? 48 : 16;
    let dx = 0, dy = 0;
    if (event.key === "ArrowLeft") dx = -step;
    else if (event.key === "ArrowRight") dx = step;
    else if (event.key === "ArrowUp") dy = -step;
    else if (event.key === "ArrowDown") dy = step;
    else return;
    event.preventDefault();
    const next = clampFloatingPosition(
      { x: posRef.current.x + dx, y: posRef.current.y + dy },
      { width: shownWidth, height: shownHeight },
      currentViewport(),
    );
    setPosition(next);
    persistLayout(next, collapsed);
  };

  return (
    <aside
      className={`plugin-floating-window${collapsed ? " collapsed" : ""}`}
      data-dragging={dragging || undefined}
      style={{
        width: `${shownWidth}px`,
        transform: `translate3d(${position.x}px, ${position.y}px, 0)`,
        zIndex,
      }}
      aria-label={page.title}
      onPointerDown={() => onFocus?.()}
    >
      <header
        className="plugin-floating-head"
        tabIndex={0}
        title={tr("拖动以移动位置，双击复位")}
        onPointerDown={startDrag}
        onPointerMove={moveDrag}
        onPointerUp={finishDrag}
        onPointerCancel={finishDrag}
        onDoubleClick={resetPosition}
        onKeyDown={onHeaderKeyDown}
      >
        <FloatingIcon key={page.icon} src={page.icon} />
        <span className="plugin-floating-title">{page.title}</span>
        <GripHorizontal size={13} className="plugin-floating-grip" aria-hidden="true" />
        <div className="plugin-floating-actions">
          <button
            type="button"
            className="plugin-floating-btn"
            aria-label={collapsed ? tr("展开浮窗") : tr("收起浮窗")}
            title={collapsed ? tr("展开浮窗") : tr("收起浮窗")}
            onClick={toggleCollapsed}
          >
            {collapsed ? <ChevronDown size={14} /> : <ChevronUp size={14} />}
          </button>
          <button
            type="button"
            className="plugin-floating-btn"
            aria-label={tr("关闭")}
            title={tr("关闭")}
            onClick={onClose}
          >
            <X size={14} />
          </button>
        </div>
      </header>
      <div
        className="plugin-floating-body"
        style={{ height: collapsed ? "0px" : `${size.height}px` }}
        hidden={collapsed}
      >
        <PluginView
          plugin={page.plugin}
          view={page.view}
          presentation="floating"
          visible={!collapsed}
          onResize={handleResize}
          onConfirm={onConfirm}
          onOpenUrl={onOpenUrl}
          onNotify={onNotify}
        />
      </div>
    </aside>
  );
}
