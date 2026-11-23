import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { ArrowUpRight, Globe2, History, ScanEye, Search, Star, AppWindow } from "lucide-react";
import { api } from "./api";
import {
  buildAddressSuggestions,
  completeDotCom,
  findInlineCompletion,
} from "./browser-omnibox.mjs";
import { useTranslation } from "./interface-language.mjs";

export function BrowserAddressBar({ tab, tabs, ready, onNavigate, onSelect }) {
  const tr = useTranslation();
  const url = tab.url === "about:blank" ? "" : tab.url || "";
  const [address, setAddress] = useState(url);
  const [typed, setTyped] = useState("");
  const [focused, setFocused] = useState(false);
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const [favorites, setFavorites] = useState([]);
  const [history, setHistory] = useState([]);
  const [composing, setComposing] = useState(false);
  const inputRef = useRef(null);
  const wrapRef = useRef(null);
  const pendingSelection = useRef(null);
  const historySeq = useRef(0);
  const lastFavoritesFetch = useRef(0);

  useEffect(() => {
    setAddress(url);
    setTyped("");
    setOpen(false);
    setActiveIndex(-1);
  }, [tab.id, url]);

  const loadFavorites = useCallback(async () => {
    const now = Date.now();
    if (now - lastFavoritesFetch.current < 4000) return;
    lastFavoritesFetch.current = now;
    try {
      const data = await api("/favorites");
      if (Array.isArray(data?.items)) setFavorites(data.items);
    } catch {
      /* Ignore offline/locked favorites during omnibox typing. */
    }
  }, []);

  const loadHistory = useCallback(async (queryText) => {
    if (!ready) return;
    const seq = ++historySeq.current;
    try {
      const data = await api("/browser/history", {
        mode: "list",
        query: queryText.trim().slice(0, 200),
        limit: queryText.trim() ? 30 : 60,
      });
      if (seq === historySeq.current && Array.isArray(data?.items)) {
        setHistory(data.items);
      }
    } catch {
      /* Older guests without history endpoint degrade gracefully. */
    }
  }, [ready]);

  useEffect(() => {
    if (!focused || !ready) return;
    const timer = setTimeout(() => { void loadHistory(typed); }, typed ? 110 : 0);
    return () => clearTimeout(timer);
  }, [focused, ready, typed, loadHistory]);

  const suggestions = useMemo(() => {
    if (!open || !typed.trim()) return [];
    return buildAddressSuggestions({
      query: typed,
      tabs,
      favorites,
      history,
      activeTabId: tab.id,
      allowSearch: true,
      limit: 8,
    });
  }, [open, typed, tabs, favorites, history, tab.id]);

  useEffect(() => {
    if (!pendingSelection.current || !inputRef.current) return;
    const { start, end } = pendingSelection.current;
    pendingSelection.current = null;
    try {
      inputRef.current.setSelectionRange(start, end);
    } catch {
      /* Ignore selection errors on unfocused inputs. */
    }
  }, [address]);

  useEffect(() => {
    if (activeIndex >= suggestions.length) setActiveIndex(-1);
  }, [activeIndex, suggestions.length]);

  useEffect(() => {
    if (!open) return;
    const dismiss = (event) => {
      if (!wrapRef.current?.contains(event.target)) {
        setOpen(false);
        setActiveIndex(-1);
      }
    };
    document.addEventListener("pointerdown", dismiss);
    return () => document.removeEventListener("pointerdown", dismiss);
  }, [open]);

  const commitItem = (item) => {
    setOpen(false);
    setActiveIndex(-1);
    setTyped("");
    if (item.kind === "tab" && onSelect) {
      onSelect(item.tabId);
      inputRef.current?.blur();
      return;
    }
    setAddress(item.url);
    onNavigate(item.url);
    inputRef.current?.blur();
  };

  const handleChange = (event) => {
    const next = event.target.value;
    const inputType = event.nativeEvent?.inputType || "";
    const isDelete = inputType.startsWith("delete");
    const caretAtEnd = event.target.selectionStart === next.length;
    setTyped(next);
    setActiveIndex(-1);
    setOpen(Boolean(next.trim()));

    if (!composing && !isDelete && caretAtEnd && next.trim()) {
      const nextSuggestions = buildAddressSuggestions({
        query: next,
        tabs,
        favorites,
        history,
        activeTabId: tab.id,
        allowSearch: false,
        limit: 6,
      });
      const completion = findInlineCompletion(next, nextSuggestions);
      if (completion && completion.text.length > next.length) {
        pendingSelection.current = { start: next.length, end: completion.text.length };
        setAddress(completion.text);
        return;
      }
    }
    setAddress(next);
  };

  const handleKeyDown = (event) => {
    if (composing || event.nativeEvent?.isComposing || event.keyCode === 229) return;
    if (event.key === "Enter" && event.ctrlKey && !event.metaKey && !event.altKey) {
      const completed = completeDotCom(typed || address);
      if (completed) {
        event.preventDefault();
        setOpen(false);
        setActiveIndex(-1);
        setAddress(completed);
        onNavigate(completed);
        inputRef.current?.blur();
        return;
      }
    }
    if (event.key === "ArrowDown" && suggestions.length) {
      event.preventDefault();
      const nextIndex = (activeIndex + 1) % suggestions.length;
      setActiveIndex(nextIndex);
      const item = suggestions[nextIndex];
      if (item) setAddress(item.kind === "search" ? item.title : item.url);
      return;
    }
    if (event.key === "ArrowUp" && suggestions.length) {
      event.preventDefault();
      const nextIndex = activeIndex <= 0 ? suggestions.length - 1 : activeIndex - 1;
      setActiveIndex(nextIndex);
      const item = suggestions[nextIndex];
      if (item) setAddress(item.kind === "search" ? item.title : item.url);
      return;
    }
    if ((event.key === "Tab" || event.key === "ArrowRight") && inputRef.current) {
      const { selectionStart, selectionEnd, value } = inputRef.current;
      if (selectionStart != null && selectionEnd != null && selectionEnd > selectionStart && selectionEnd === value.length) {
        if (event.key === "Tab") event.preventDefault();
        inputRef.current.setSelectionRange(value.length, value.length);
        setTyped(value);
        return;
      }
    }
    if (event.key === "Escape") {
      if (open || address !== url) {
        event.preventDefault();
        event.stopPropagation();
        setOpen(false);
        setActiveIndex(-1);
        setTyped("");
        setAddress(url);
      } else {
        inputRef.current?.blur();
      }
    }
  };

  const handleSubmit = (event) => {
    event.preventDefault();
    if (composing) return;
    if (open && activeIndex >= 0 && suggestions[activeIndex]) {
      commitItem(suggestions[activeIndex]);
      return;
    }
    setOpen(false);
    setActiveIndex(-1);
    setTyped("");
    onNavigate(address);
    inputRef.current?.blur();
  };

  const kindIcon = (kind) => {
    if (kind === "favorite") return <Star size={14} />;
    if (kind === "history") return <History size={14} />;
    if (kind === "tab") return <AppWindow size={14} />;
    if (kind === "search") return <Search size={14} />;
    return <Globe2 size={14} />;
  };

  const kindBadge = (kind) => {
    if (kind === "tab") return tr("切换标签");
    if (kind === "favorite") return tr("收藏");
    if (kind === "history") return tr("历史");
    if (kind === "search") return tr("搜索");
    return "";
  };

  const listId = `address-suggestions-${tab.id}`;

  return (
    <div className="address-bar-wrap" ref={wrapRef}>
      <form className="address-bar" onSubmit={handleSubmit}>
        {tab.incognito ? (
          <span
            className="browser-private-badge"
            title={tab.login ? tr("授权结束后清理本次登录会话") : tr("关闭全部无痕标签后清理此会话")}
          >
            <ScanEye size={13} />
            {tr("无痕")}
          </span>
        ) : (
          <Globe2 size={13} />
        )}
        <input
          ref={inputRef}
          aria-autocomplete="both"
          aria-expanded={open && suggestions.length > 0}
          aria-controls={open && suggestions.length > 0 ? listId : undefined}
          aria-activedescendant={open && activeIndex >= 0 && suggestions[activeIndex] ? `${listId}-${activeIndex}` : undefined}
          aria-label={tr("网页地址或搜索")}
          value={address}
          onChange={handleChange}
          onKeyDown={handleKeyDown}
          onCompositionStart={() => setComposing(true)}
          onCompositionEnd={(event) => {
            setComposing(false);
            setTyped(event.currentTarget.value);
          }}
          onFocus={(event) => {
            setFocused(true);
            event.target.select();
            void loadFavorites();
          }}
          onBlur={() => {
            setFocused(false);
            setOpen(false);
            setActiveIndex(-1);
          }}
          placeholder={
            ready
              ? tr("输入网址或搜索，在 Cage 内打开")
              : tr("启动环境后浏览网页")
          }
          disabled={!ready}
          autoComplete="off"
          spellCheck="false"
        />
        <button
          className="address-go"
          type="submit"
          aria-label={tr("打开地址")}
          disabled={!ready}
        >
          <ArrowUpRight size={15} />
        </button>
      </form>
      {open && suggestions.length > 0 && (
        <ul
          className="address-suggestions"
          id={listId}
          role="listbox"
          aria-label={tr("地址栏建议")}
        >
          {suggestions.map((item, index) => {
            const badge = kindBadge(item.kind);
            const selected = index === activeIndex;
            return (
              <li
                key={item.id}
                id={`${listId}-${index}`}
                role="option"
                aria-selected={selected}
                className={`address-suggestion ${selected ? "active" : ""}`}
                onMouseEnter={() => setActiveIndex(index)}
                onMouseDown={(event) => {
                  event.preventDefault();
                  commitItem(item);
                }}
              >
                <span className="address-suggestion-icon">{kindIcon(item.kind)}</span>
                <span className="address-suggestion-body">
                  <strong>{item.kind === "search" ? tr("搜索 “{0}”", item.title) : item.title}</strong>
                  {item.displayURL && <small>{item.displayURL}</small>}
                </span>
                {badge && <span className="address-suggestion-badge">{badge}</span>}
              </li>
            );
          })}
        </ul>
      )}
    </div>
  );
}
