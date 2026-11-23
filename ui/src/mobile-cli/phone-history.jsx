import React, { useEffect, useRef, useState } from "react";
import { ArrowLeft, Globe2, History, RefreshCw, Search, Trash2 } from "lucide-react";
import { InlineSelect } from "./inline-select";
import { browserRequest } from "./transport.mjs";
import { cleanDisplayURL } from "../browser-omnibox.mjs";

function formatTime(timestamp) {
  if (!timestamp) return "";
  try {
    return new Intl.DateTimeFormat(undefined, { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", hour12: false }).format(new Date(timestamp));
  } catch {
    return "";
  }
}

export function PhoneHistory({ navigate, onClose }) {
  const [items, setItems] = useState(null);
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [clearing, setClearing] = useState(false);
  const [range, setRange] = useState("hour");
  const back = useRef(null);
  const request = useRef(null);

  const sync = async (body, { refresh = false } = {}) => {
    if (request.current) {
      request.current.abort();
      request.current = null;
    }
    const controller = new AbortController();
    request.current = controller;
    setBusy(true);
    setError("");
    try {
      const payload = body || { mode: "list", query: query.trim(), limit: 120 };
      const next = await browserRequest("/history", { body: payload, signal: controller.signal });
      if (controller.signal.aborted) return;
      if (payload.mode === "list") {
        if (!Array.isArray(next?.items)) throw new Error("电脑返回的浏览历史格式无效，请更新电脑端 Cage。");
        setItems(next.items);
      } else {
        setClearing(false);
        if (refresh) {
          const reloaded = await browserRequest("/history", {
            body: { mode: "list", query: query.trim(), limit: 120 },
            signal: controller.signal,
          });
          if (!controller.signal.aborted && Array.isArray(reloaded?.items)) setItems(reloaded.items);
        }
      }
    } catch (e) {
      if (controller.signal.aborted) return;
      const unsupported = e.status === 404 || e.status === 405 || e instanceof SyntaxError;
      setError(unsupported ? "这台电脑尚不支持浏览历史，请更新电脑端 Cage。" : e.message);
    } finally {
      if (request.current === controller) {
        request.current = null;
        if (!controller.signal.aborted) setBusy(false);
      }
    }
  };

  useEffect(() => {
    back.current?.focus({ preventScroll: true });
    return () => { request.current?.abort(); request.current = null; };
  }, []);

  useEffect(() => {
    const timer = setTimeout(() => { void sync({ mode: "list", query: query.trim(), limit: 120 }); }, query ? 160 : 0);
    return () => clearTimeout(timer);
  }, [query]);

  const open = async (url) => {
    if (busy) return;
    setBusy(true);
    setError("");
    try {
      if (await navigate(url)) onClose();
      else setError("网页暂未打开，请查看远程连接。");
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (url) => {
    await sync({ mode: "delete", url }, { refresh: true });
  };

  const clear = async () => {
    await sync({ mode: "clear", range }, { refresh: true });
  };

  return (
    <section className="phone-favorites phone-history" aria-label="浏览历史">
      <div className="bookmark-heading">
        <button className="icon-button" type="button" ref={back} aria-label="返回网页" onClick={onClose}>
          <ArrowLeft size={18} />
        </button>
        <h2>浏览历史</h2>
        <button
          className="icon-button"
          type="button"
          disabled={busy || !items?.length}
          aria-label="清除浏览历史"
          onClick={() => setClearing(value => !value)}
        >
          <Trash2 size={17} />
        </button>
        <button
          className="icon-button"
          type="button"
          disabled={busy}
          aria-label="刷新浏览历史"
          onClick={() => sync({ mode: "list", query: query.trim(), limit: 120 })}
        >
          <RefreshCw size={18} />
        </button>
      </div>
      <label className="phone-history-search">
        <Search size={16} aria-hidden="true" />
        <input
          type="search"
          aria-label="搜索浏览历史"
          placeholder="搜索网址或标题"
          value={query}
          onChange={(event) => setQuery(event.target.value)}
        />
      </label>
      {clearing && (
        <div className="phone-history-clear">
          <InlineSelect
            label="清除范围"
            value={range}
            disabled={busy}
            onChange={setRange}
            options={[
              { value: "hour", label: "最近 1 小时" },
              { value: "day", label: "最近 24 小时" },
              { value: "week", label: "最近 7 天" },
              { value: "all", label: "全部历史" },
            ]}
          />
          <div className="bookmark-save-actions">
            <button className="action" type="button" disabled={busy} onClick={() => setClearing(false)}>取消</button>
            <button className="action danger" type="button" disabled={busy} onClick={clear}>清除</button>
          </div>
        </div>
      )}
      {error && <p className="notice" role="alert">{error}</p>}
      {items === null && !error && <p className="nav-note" role="status">正在读取浏览历史…</p>}
      {items && items.length === 0 && !error && (
        <p className="nav-note">{query.trim() ? "没有匹配的历史记录。" : "还没有浏览历史。"}</p>
      )}
      {items && items.length > 0 && (
        <ul className="phone-bookmarks">
          {items.map((item) => (
            <li key={`${item.id}-${item.url}`}>
              <button className="bookmark-open" type="button" disabled={busy} onClick={() => open(item.url)}>
                <Globe2 size={16} />
                <span>
                  <strong>{item.title || cleanDisplayURL(item.url)}</strong>
                  <small>{cleanDisplayURL(item.url)}{item.lastVisitTime ? ` · ${formatTime(item.lastVisitTime)}` : ""}</small>
                </span>
              </button>
              <button
                className="icon-button"
                type="button"
                disabled={busy}
                aria-label={"删除历史 " + (item.title || item.url)}
                onClick={() => remove(item.url)}
              >
                <Trash2 size={16} />
              </button>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
