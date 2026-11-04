import { shortcut } from "./platform.mjs";
import React, { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  FileCode2,
  ChevronDown,
  Globe2,
  MoreHorizontal,
  Pin,
  TerminalSquare,
  X,
} from "lucide-react";
import { tabName } from "./workspace-state.mjs";

export function TabIcon({ kind, favicon, size = 16 }) {
  const [failed, setFailed] = useState("");
  if (kind === "terminal") return <TerminalSquare size={size} />;
  if (kind === "editor") return <FileCode2 size={size} />;
  if (typeof favicon === "string" && favicon.length <= 8192 && /^data:image\/(png|svg\+xml);base64,/.test(favicon) && failed !== favicon)
    return <img className="tab-favicon" src={favicon} width={size} height={size} alt="" draggable={false} onError={() => setFailed(favicon)} />;
  return <Globe2 size={size} />;
}

// Active content carries the strongest emphasis; titles and actions use the
// existing paper/selected/muted tokens, 14px type and 8px spacing in both layouts.
export function WorkspaceTabs({
  tabs,
  saved,
  active,
  label,
  horizontal = false,
  busy,
  ready,
  onSelect,
  onClose,
  onRename,
  onPin,
  onReorder,
}) {
  const [menu, setMenu] = useState(null);
  const [dragged, setDragged] = useState(null);
  const [drag, setDrag] = useState(null);
  const [allOpen, setAllOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [width, setWidth] = useState(0);
  const pointer = useRef(null);
  const suppressClick = useRef(false);
  const trigger = useRef();
  const list = useRef();
  useEffect(() => {
    if (!horizontal || !list.current) return;
    const observer = new ResizeObserver(([entry]) =>
      setWidth(entry.contentRect.width),
    );
    observer.observe(list.current);
    return () => observer.disconnect();
  }, [horizontal]);
  const gap = tabs.length > 20 ? 1 : 4;
  const available = Math.max(0, width - gap * Math.max(0, tabs.length - 1));
  const regularWidth = Math.min(220, available / Math.max(1, tabs.length));
  const activeWidth =
    tabs.length > 1 && tabs.some((tab) => tab.id === active)
      ? Math.min(220, Math.max(regularWidth, Math.min(160, available * 0.4)))
      : regularWidth;
  const otherWidth = Math.max(
    0,
    (available - activeWidth) / Math.max(1, tabs.length - 1),
  );
  const tabWidth = (id) =>
    id === active ? activeWidth : Math.min(220, otherWidth);
  const matchingTabs = tabs.filter((tab) =>
    `${tabName(tab, saved)} ${tab.url || ""}`
      .toLowerCase()
      .includes(query.toLowerCase()),
  );
  const finishDrag = (commit) => {
    const current = pointer.current;
    pointer.current = null;
    setDrag(null);
    if (current?.moved) {
      suppressClick.current = true;
      if (commit) onReorder(current.id, null, current.order);
    }
  };
  useEffect(() => {
    if (!drag) return;
    const cancel = (event) => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopImmediatePropagation();
      finishDrag(false);
    };
    document.addEventListener("keydown", cancel, true);
    return () => document.removeEventListener("keydown", cancel, true);
  }, [Boolean(drag)]);
  const startDrag = (event, tab) => {
    if (
      !horizontal ||
      busy ||
      !ready ||
      event.button !== 0 ||
      event.target.closest(".tab-close")
    )
      return;
    suppressClick.current = false;
    const items = [...list.current.children].map((node) => ({
      id: node.dataset.tabId,
      left: node.offsetLeft,
      width: node.getBoundingClientRect().width,
    }));
    const group = items.filter(
      (item) =>
        Boolean(saved[item.id]?.pinned) === Boolean(saved[tab.id]?.pinned),
    );
    pointer.current = {
      id: tab.id,
      startX: event.clientX,
      items,
      group,
      order: items.map((item) => item.id),
      moved: false,
    };
  };
  const moveDrag = (event) => {
    const current = pointer.current;
    if (!current) return;
    if (event.buttons === 0) {
      finishDrag(false);
      return;
    }
    const item = current.items.find((item) => item.id === current.id);
    let dx = event.clientX - current.startX;
    if (!current.moved && Math.abs(dx) < 5) return;
    if (!current.moved) event.currentTarget.setPointerCapture(event.pointerId);
    current.moved = true;
    dx = Math.max(
      current.group[0].left - item.left,
      Math.min(
        dx,
        current.group.at(-1).left +
          current.group.at(-1).width -
          item.left -
          item.width,
      ),
    );
    const center = item.left + item.width / 2 + dx;
    const rest = current.group.filter((other) => other.id !== item.id);
    const target = rest.filter(
      (other) => center > other.left + other.width / 2,
    ).length;
    const groupOrder = rest.map((other) => other.id);
    groupOrder.splice(target, 0, item.id);
    let at = 0;
    current.order = current.items.map((other) =>
      current.group.includes(other) ? groupOrder[at++] : other.id,
    );
    let left = current.items[0].left;
    const offsets = {};
    for (const id of current.order) {
      const original = current.items.find((other) => other.id === id);
      offsets[id] = id === item.id ? dx : left - original.left;
      left += original.width + gap;
    }
    setDrag({
      id: item.id,
      offsets,
      widths: Object.fromEntries(
        current.items.map((item) => [item.id, item.width]),
      ),
    });
  };
  const showMenu = (tab, event) => {
    event.preventDefault();
    trigger.current = event.currentTarget;
    const rect = event.currentTarget.getBoundingClientRect();
    setMenu({
      tab,
      left: Math.max(8, Math.min(rect.left, window.innerWidth - 224)),
      top: Math.max(8, Math.min(rect.bottom + 4, window.innerHeight - 152)),
    });
  };
  useEffect(() => {
    if (horizontal) return;
    list.current
      ?.querySelector('[aria-selected="true"]')
      ?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [active, horizontal]);
  useEffect(() => {
    if (!menu && !allOpen) return;
    const dismiss = (event) => {
      if (event.key === "Escape") {
        event.stopPropagation();
        setMenu(null);
        setAllOpen(false);
        trigger.current?.focus();
      }
    };
    document.addEventListener("keydown", dismiss);
    return () => document.removeEventListener("keydown", dismiss);
  }, [menu, allOpen]);
  return (
    <>
      <div
        ref={list}
        role="tablist"
        aria-label={label}
        aria-orientation={horizontal ? "horizontal" : "vertical"}
        className={
          horizontal
            ? `browser-tabs ${drag ? "is-dragging" : ""}`
            : "sidebar-tabs"
        }
        style={
          horizontal
            ? {
                gap,
                width: tabs.length * 220 + gap * Math.max(0, tabs.length - 1),
                flex: "0 1 auto",
              }
            : undefined
        }
      >
        {tabs.map((tab, index) => {
          const title = tabName(tab, saved);
          return (
            <div
              key={tab.id}
              data-tab-id={tab.id}
              className={`${horizontal ? "browser-tab" : "sidebar-tab"} ${tab.id === active ? "active" : ""} ${tab.closed ? "ended" : ""} ${drag?.id === tab.id ? "dragging" : ""} ${horizontal && tabWidth(tab.id) < 96 ? "compact" : ""} ${horizontal && tabWidth(tab.id) < 40 ? "tiny" : ""} ${horizontal && tabWidth(tab.id) < 24 ? "sliver" : ""}`}
              style={
                horizontal
                  ? {
                      width: drag?.widths[tab.id] ?? tabWidth(tab.id),
                      transform: drag
                        ? `translateX(${drag.offsets[tab.id] || 0}px)`
                        : undefined,
                    }
                  : undefined
              }
              onPointerDown={(event) => startDrag(event, tab)}
              onPointerMove={moveDrag}
              onPointerUp={() => finishDrag(true)}
              onPointerCancel={() => finishDrag(false)}
              onLostPointerCapture={() => {
                if (pointer.current) finishDrag(false);
              }}
              onClickCapture={(event) => {
                if (suppressClick.current) {
                  event.preventDefault();
                  event.stopPropagation();
                  suppressClick.current = false;
                }
              }}
              draggable={!horizontal && !busy}
              onDragStart={(event) => {
                setDragged(tab.id);
                event.dataTransfer.setData("text/plain", tab.id);
                event.dataTransfer.effectAllowed = "move";
              }}
              onDragOver={(event) => {
                if (dragged) event.preventDefault();
              }}
              onDrop={(event) => {
                event.preventDefault();
                if (dragged) onReorder(dragged, tab.id);
                setDragged(null);
              }}
              onDragEnd={() => setDragged(null)}
            >
              <button
                role="tab"
                aria-label={title}
                aria-selected={tab.id === active}
                tabIndex={
                  tab.id === active ||
                  (!tabs.some((t) => t.id === active) && index === 0)
                    ? 0
                    : -1
                }
                disabled={!ready && tab.kind !== "terminal"}
                title={tab.url ? `${title}\n${tab.url}` : title}
                onClick={() => onSelect(tab.id)}
                onContextMenu={(event) => showMenu(tab, event)}
                onKeyDown={(event) => {
                  if (event.key === "F10" && event.shiftKey) {
                    showMenu(tab, event);
                    return;
                  }
                  const prev = horizontal ? "ArrowLeft" : "ArrowUp";
                  const next = horizontal ? "ArrowRight" : "ArrowDown";
                  if (![prev, next, "Home", "End"].includes(event.key)) return;
                  event.preventDefault();
                  const at =
                    event.key === "Home"
                      ? 0
                      : event.key === "End"
                        ? tabs.length - 1
                        : (index +
                            (event.key === prev ? -1 : 1) +
                            tabs.length) %
                          tabs.length;
                  list.current.querySelectorAll('[role="tab"]')[at]?.focus();
                  onSelect(tabs[at].id);
                }}
              >
                <TabIcon kind={tab.kind} favicon={tab.favicon} size={15} />
                <span>{title}</span>
                {saved[tab.id]?.pinned && <Pin size={11} className="tab-pin" />}
              </button>
              {horizontal ? (
                <button
                  className="tab-close icon-button"
                  aria-label={`关闭 ${title}`}
                  disabled={busy}
                  onClick={() => onClose(tab)}
                >
                  <X size={14} />
                </button>
              ) : (
                <button
                  className="tab-more icon-button"
                  aria-label={`管理 ${title}`}
                  onClick={(event) => showMenu(tab, event)}
                  disabled={busy}
                >
                  <MoreHorizontal size={15} />
                </button>
              )}
            </div>
          );
        })}
      </div>
      {horizontal && (
        <div className="browser-all-tabs">
          <button
            className="icon-button"
            aria-label="搜索标签页"
            title="搜索标签页"
            aria-expanded={allOpen}
            onClick={(event) => {
              trigger.current = event.currentTarget;
              setAllOpen(!allOpen);
              setQuery("");
            }}
          >
            <ChevronDown size={16} />
          </button>
          {allOpen && (
            <>
              <div className="menu-dismiss" onClick={() => setAllOpen(false)} />
              <div
                className="menu all-tabs-menu"
                role="dialog"
                aria-label="搜索标签页"
              >
                <input
                  autoFocus
                  aria-label="搜索已打开的标签页"
                  placeholder="搜索标签页"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
                <div className="all-tabs-results">
                  {matchingTabs.length === 0 && (
                    <p className="favorites-empty">没有匹配的标签页</p>
                  )}
                  {matchingTabs.map((tab) => (
                    <button
                      key={tab.id}
                      title={tab.url}
                      onClick={() => {
                        setAllOpen(false);
                        onSelect(tab.id);
                      }}
                    >
                      <TabIcon kind={tab.kind} favicon={tab.favicon} />
                      <span>{tabName(tab, saved)}</span>
                      {tab.id === active && (
                        <span className="current-tab-marker">当前</span>
                      )}
                    </button>
                  ))}
                </div>
              </div>
            </>
          )}
        </div>
      )}
      {menu &&
        createPortal(
          <>
            <div className="menu-dismiss" onClick={() => setMenu(null)} />
            <div
              className="menu workspace-tab-menu"
              style={{ top: menu.top, left: menu.left }}
              role="menu"
              aria-label="标签操作"
            >
              <button
                role="menuitem"
                onClick={() => {
                  setMenu(null);
                  onRename(menu.tab);
                }}
              >
                重命名标签
              </button>
              <button
                role="menuitem"
                onClick={() => {
                  setMenu(null);
                  onPin(menu.tab.id);
                }}
              >
                <Pin size={14} />
                {saved[menu.tab.id]?.pinned ? "取消置顶" : "置顶标签"}
              </button>
              <button
                role="menuitem"
                onClick={() => {
                  setMenu(null);
                  onClose(menu.tab);
                }}
              >
                <X size={15} />
                关闭标签<kbd>{shortcut("⌘W")}</kbd>
              </button>
            </div>
          </>,
          document.body,
        )}
    </>
  );
}
