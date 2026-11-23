import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Clock, Globe2, History, Search, Trash2, X } from "lucide-react";
import { api } from "./api";
import { InlineSelect } from "./inline-select";
import { cleanDisplayURL } from "./browser-omnibox.mjs";
import { isMac } from "./platform.mjs";
import { activeInterfaceLanguage, useTranslation } from "./interface-language.mjs";

function dayBucket(timestamp) {
  const date = new Date(timestamp || 0);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}

function formatGroupLabel(timestamp, now, locale, tr) {
  if (!timestamp) return tr("更早");
  const today = dayBucket(now);
  const yesterday = dayBucket(now - 24 * 60 * 60 * 1000);
  const bucket = dayBucket(timestamp);
  if (bucket === today) return tr("今天");
  if (bucket === yesterday) return tr("昨天");
  try {
    return new Intl.DateTimeFormat([locale], { month: "short", day: "numeric", weekday: "short" }).format(new Date(timestamp));
  } catch {
    return bucket;
  }
}

function formatVisitTime(timestamp, locale) {
  if (!timestamp) return "";
  try {
    return new Intl.DateTimeFormat([locale], { hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(timestamp));
  } catch {
    return "";
  }
}

export function BrowserHistory({ ready, onOpen, shortcuts = true, menu = false, returnFocusRef }) {
  const tr = useTranslation();
  const locale = activeInterfaceLanguage();
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState(null);
  const [query, setQuery] = useState("");
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [clearing, setClearing] = useState(false);
  const [range, setRange] = useState("hour");
  const searchRef = useRef();
  const triggerRef = useRef();
  const seqRef = useRef(0);
  const stateRef = useRef();

  const load = useCallback(async (text = "") => {
    const seq = ++seqRef.current;
    setLoading(true);
    setError("");
    try {
      const result = await api("/browser/history", { mode: "list", query: text, limit: 150 });
      if (seq !== seqRef.current) return;
      setItems(Array.isArray(result?.items) ? result.items : []);
    } catch (e) {
      if (seq !== seqRef.current) return;
      setError(e.message || tr("浏览历史暂时不可用，请重试"));
    } finally {
      if (seq === seqRef.current) setLoading(false);
    }
  }, [tr]);

  const show = useCallback(() => {
    if (!ready) return;
    setClearing(false);
    setOpen(true);
  }, [ready]);

  stateRef.current = { open, saving, show, shortcuts, ready };

  useEffect(() => {
    const onShow = () => stateRef.current.shortcuts && stateRef.current.ready && stateRef.current.show();
    const onClose = () => {
      if (!stateRef.current.saving) setOpen(false);
    };
    document.addEventListener("cage-show-history", onShow);
    document.addEventListener("cage-close-modal", onClose);
    return () => {
      document.removeEventListener("cage-show-history", onShow);
      document.removeEventListener("cage-close-modal", onClose);
    };
  }, []);

  useEffect(() => {
    if (!open) return;
    const timer = setTimeout(() => { void load(query); }, query ? 160 : 0);
    return () => clearTimeout(timer);
  }, [open, query, load]);

  useEffect(() => {
    if (!open) return;
    searchRef.current?.focus();
    const onKey = (event) => {
      if (event.key === "Escape" && !stateRef.current.saving) {
        event.preventDefault();
        setOpen(false);
      }
    };
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("keydown", onKey);
      (returnFocusRef?.current || triggerRef.current)?.focus();
    };
  }, [open, returnFocusRef]);

  const removeItem = async (url) => {
    if (saving) return;
    setSaving(true);
    setError("");
    try {
      await api("/browser/history", { mode: "delete", url });
      setItems(prev => (prev || []).filter(item => item.url !== url));
    } catch (e) {
      setError(e.message);
    } finally {
      setSaving(false);
    }
  };

  const clearHistory = async () => {
    if (saving) return;
    setSaving(true);
    setError("");
    try {
      await api("/browser/history", { mode: "clear", range });
      setClearing(false);
      await load(query);
    } catch (e) {
      setError(e.message);
    } finally {
      setSaving(false);
    }
  };

  const groups = useMemo(() => {
    const now = Date.now();
    const buckets = new Map();
    for (const item of items || []) {
      const key = dayBucket(item.lastVisitTime);
      if (!buckets.has(key)) {
        buckets.set(key, { key, label: formatGroupLabel(item.lastVisitTime, now, locale, tr), items: [] });
      }
      buckets.get(key).items.push(item);
    }
    return [...buckets.values()];
  }, [items, locale, tr]);

  const hint = isMac ? "⌘Y" : "Ctrl+H";

  return (
    <>
      <button
        ref={triggerRef}
        className={menu ? "browser-menu-item" : "icon-button"}
        role={menu ? "menuitem" : undefined}
        aria-label={tr("浏览历史")}
        title={`${tr("浏览历史")} ${hint}`}
        disabled={!ready}
        onClick={show}
      >
        <History size={16} />
        {menu && <span>{tr("浏览历史")}</span>}
        {menu && <span className="browser-menu-shortcut">{hint}</span>}
      </button>
      {open &&
        createPortal(
          <>
            <div
              className="favorites-dismiss"
              onClick={() => { if (!saving) setOpen(false); }}
            />
            <section
              className="favorites-panel history-panel"
              role="dialog"
              aria-label={tr("浏览历史")}
              data-browser-dialog
            >
              <header>
                <h2>{tr("浏览历史")}</h2>
                <button
                  className="icon-button"
                  aria-label={tr("清除历史记录")}
                  title={tr("清除历史记录")}
                  disabled={saving || !items?.length}
                  onClick={() => setClearing(value => !value)}
                >
                  <Trash2 size={16} />
                </button>
                <button
                  className="icon-button"
                  aria-label={tr("关闭浏览历史")}
                  onClick={() => setOpen(false)}
                >
                  <X size={17} />
                </button>
              </header>
              <label className="favorites-search">
                <Search size={15} />
                <input
                  ref={searchRef}
                  aria-label={tr("搜索浏览历史")}
                  placeholder={tr("搜索浏览历史")}
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                />
              </label>
              {clearing && (
                <div className="history-clear-bar">
                  <InlineSelect aria-label={tr("清除时间范围")} value={range} onValueChange={setRange}>
                    <option value="hour">{tr("最近 1 小时")}</option>
                    <option value="day">{tr("最近 24 小时")}</option>
                    <option value="week">{tr("最近 7 天")}</option>
                    <option value="all">{tr("全部历史")}</option>
                  </InlineSelect>
                  <button className="danger" disabled={saving} onClick={clearHistory}>
                    {tr("清除")}
                  </button>
                  <button disabled={saving} onClick={() => setClearing(false)}>
                    {tr("取消")}
                  </button>
                </div>
              )}
              {error && (
                <div className="favorites-error" role="alert">
                  <p>{error}</p>
                  <button onClick={() => load(query)}>{tr("重试")}</button>
                </div>
              )}
              <div className="favorites-list history-list">
                {loading && items === null && (
                  <div className="favorites-empty">
                    <Clock size={22} />
                    <p>{tr("正在读取浏览历史…")}</p>
                  </div>
                )}
                {!loading && !error && items && items.length === 0 && (
                  <div className="favorites-empty">
                    <History size={22} />
                    <p>{query.trim() ? tr("没有匹配的历史记录") : tr("暂无浏览历史")}</p>
                    <span>{tr("在普通标签页访问的网页会显示在这里。")}</span>
                  </div>
                )}
                {groups.map(group => (
                  <div className="history-group" key={group.key}>
                    <div className="history-group-heading">{group.label}</div>
                    {group.items.map(item => (
                      <div className="favorite-row history-row" key={`${item.id}-${item.url}`}>
                        <button
                          className="favorite-link"
                          disabled={!ready}
                          onClick={() => {
                            setOpen(false);
                            onOpen(item.url);
                          }}
                        >
                          <Globe2 size={15} />
                          <span>
                            <strong>{item.title || cleanDisplayURL(item.url)}</strong>
                            <small>{cleanDisplayURL(item.url)}</small>
                          </span>
                          <time className="history-time">{formatVisitTime(item.lastVisitTime, locale)}</time>
                        </button>
                        <button
                          className="icon-button history-delete"
                          aria-label={tr("删除此条历史")}
                          title={tr("删除此条历史")}
                          disabled={saving}
                          onClick={() => removeItem(item.url)}
                        >
                          <Trash2 size={14} />
                        </button>
                      </div>
                    ))}
                  </div>
                ))}
              </div>
              <footer>
                <span>{tr("无痕会话不记录历史")}</span>
                {items && <span>{items.length}</span>}
              </footer>
            </section>
          </>,
          document.body,
        )}
    </>
  );
}
