import React, { useEffect, useRef, useState } from "react";
import { Archive, ArrowLeft, ArrowRight, ChevronRight, FolderOpen,
  Globe2, Cookie, Settings, Puzzle, ScanEye, Maximize2, MoreHorizontal, PanelLeftOpen, Plus, RefreshCw, X } from "lucide-react";
import { BrowserVaultBar } from "./password-vault";
import { WorkspaceTabs } from "./workspace-tabs";
import { BrowserFavorites } from "./browser-favorites";
import { BrowserDownloads } from "./browser-downloads";
import { BrowserCookies } from "./browser-cookies";
import { BrowserHistory } from "./browser-history";
import { BrowserAddressBar } from "./browser-address-bar";
import { tabName } from "./workspace-state.mjs";
import { shortcut } from "./platform.mjs";
import logo from "./logo.png";
import { useTranslation } from "./interface-language.mjs";

export const ExpandSidebar = ({ onClick }) => { const tr = useTranslation(); return  <button className="icon-button sidebar-expand"
  aria-label={tr("展开侧栏")} onClick={onClick}>
  <img className="sidebar-expand-logo" src={logo} alt="" />
  <PanelLeftOpen className="sidebar-expand-icon" size={18} />
</button>; };

export const ExpandPane = ({ onClick }) => { const tr = useTranslation(); return  <button className="icon-button expand-pane"
  aria-label={tr("退出分屏")} title={tr("在整个工作区显示此页")} onClick={onClick}><Maximize2 size={14} /></button>; };

export function TerminalHeader({ tab, contentWorkspace, project, metadata, terminalDetail, terminalDetailHint,
  onHome, busy, onClose, sidebarButton, exitButton, location, triggerRef, menu }) {
  const tr = useTranslation();
  // Matches closeTab: only a running conversation terminal is archived; an ended one is just closed.
  const archives = tab?.cli_session_id && !tab.closed;
  return <header className="chrome terminal-chrome">
    <div className="content-heading">
      {sidebarButton}
      <div className="content-breadcrumb">
        <button
          onClick={onHome}
          title={contentWorkspace}
          data-terminal-location={location}
          ref={triggerRef}
        >
          <FolderOpen size={15} />
          <span>{project}</span>
        </button>
        {tab && (
          <>
            <ChevronRight size={13} />
            <span className="current-title" title={tabName(tab, metadata, project, tr)}>
              {tabName(tab, metadata, project, tr)}
            </span>
            {terminalDetail && <span className="terminal-detail" title={terminalDetailHint}>{terminalDetail}</span>}
          </>
        )}
      </div>
      <div className="heading-actions">
        {tab && (
          <button
            className="icon-button"
            aria-label={archives ? tr("归档当前会话") : tr("关闭当前标签")}
            title={shortcut(archives ? "归档当前会话 ⌘W" : "关闭当前标签 ⌘W")}
            onClick={onClose}
            disabled={busy}
          >
            {archives ? <Archive size={16} /> : <X size={16} />}
          </button>
        )}
        {exitButton}
      </div>
    </div>
    {menu}
    {busy && <div className="progress-line" />}
  </header>;
}

// The selected title is primary; tab actions and the add button use the existing
// muted/hover tokens, 8px spacing and browser-tab typography in either layout.
export function BrowserTabRail({ tabs, metadata, active, ready, busy, splitDrag, sidebarButton,
  onSelect, onClose, onRename, onPin, onReorder, onNew }) {
  const tr = useTranslation();
  return <aside className="chrome browser-tab-rail" aria-label={tr("竖排标签栏")}>
    <div className="browser-tab-rail-heading">
      {sidebarButton}<strong>{tr("标签页")}</strong>
      <button className="icon-button browser-new-tab" aria-label={tr("新建网页")} title={shortcut("新建网页 ⌘T")}
        disabled={!ready || busy} onClick={onNew}><Plus size={17} /></button>
    </div>
    <WorkspaceTabs tabs={tabs} saved={metadata} active={active} label={tr("浏览器标签页")} vertical
      ready={ready} busy={busy} splitDrag={splitDrag} onSelect={onSelect} onClose={onClose}
      onRename={onRename} onPin={onPin} onReorder={onReorder} />
  </aside>;
}

export function BrowserHeader({ vault, onManageVault, tab, tabs, metadata, ready, busy, focused, compact, triggerRef, vertical,
  sidebarButton, exitButton, splitDrag, onSelect, onClose, onRename, onPin, onReorder, onNew,
  location, menu, back, forward, action, onNavigate, onOpen, onNewPrivate, onNewRegular, onCloseBrowser, onSettings, onReload, downloads, onStatus, onEditor, onRecover, recoveringEditor }) {
  const tr = useTranslation();
  const [toolsOpen, setToolsOpen] = useState(false);
  const [cookiesOpen, setCookiesOpen] = useState(false);
  const toolsTrigger = useRef(), toolsMenu = useRef();
  useEffect(() => { setToolsOpen(false); setCookiesOpen(false); }, [tab.id]);
  useEffect(() => {
    if (!toolsOpen) return;
    toolsMenu.current?.querySelector('[role="menuitem"]:not(:disabled)')?.focus();
    const dismiss = event => {
      if (!toolsMenu.current?.contains(event.target) && !toolsTrigger.current?.contains(event.target)) setToolsOpen(false);
    };
    document.addEventListener("pointerdown", dismiss);
    return () => document.removeEventListener("pointerdown", dismiss);
  }, [toolsOpen]);
  const menuKey = event => {
    if (event.key === "Tab") {
      setTimeout(() => {
        if (!toolsMenu.current?.contains(document.activeElement) && document.activeElement !== toolsTrigger.current) setToolsOpen(false);
      }, 0);
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault(); event.stopPropagation(); setToolsOpen(false); toolsTrigger.current?.focus();
      return;
    }
    if (event.target.getAttribute("role") !== "menuitem") return;
    const items = [...toolsMenu.current.querySelectorAll('[role="menuitem"]:not(:disabled)')];
    const index = items.indexOf(event.target);
    const next = event.key === "ArrowDown" ? (index + 1) % items.length :
      event.key === "ArrowUp" ? (index - 1 + items.length) % items.length :
      event.key === "Home" ? 0 : event.key === "End" ? items.length - 1 : -1;
    if (next >= 0) { event.preventDefault(); items[next].focus(); }
  };
  const closeButton = onCloseBrowser && <button className="icon-button browser-close" aria-label={tr("关闭浏览器")}
    title={tr("关闭浏览器并返回之前的页面")} onClick={onCloseBrowser}><X size={16} /></button>;
  return <header className={`chrome browser-chrome ${compact ? "split-chrome" : ""}`} data-browser-tab={tab.id}>
    {!vertical && <div className="browser-tab-bar">
      {sidebarButton}
      <WorkspaceTabs
        tabs={tabs}
        saved={metadata}
        active={tab.id}
        label={tr("浏览器标签页")}
        horizontal
        busy={busy}
        ready={ready}
        onSelect={onSelect}
        onClose={onClose}
        onRename={onRename}
        onPin={onPin}
        splitDrag={splitDrag}
        onReorder={onReorder}
      />
      <button
        className="icon-button browser-new-tab"
        ref={triggerRef}
        data-terminal-location={location}
        aria-label={tr("新建网页")}
        title={shortcut("新建网页 ⌘T")}
        disabled={!ready || busy}
        onClick={onNew}
      >
        <Plus size={17} />
      </button>
      {(exitButton || closeButton) && <div className="browser-window-actions">{exitButton}{closeButton}</div>}
    </div>}
    <div className="navigation-row">
      <div className="nav-actions">
        <button
          className="icon-button"
          aria-label={tr("后退")}
          disabled={!ready || !back}
          onClick={() =>
            action("/browser/back", { id: tab.id })
          }
        >
          <ArrowLeft size={17} />
        </button>
        <button
          className="icon-button"
          aria-label={tr("前进")}
          disabled={!ready || !forward}
          onClick={() =>
            action("/browser/forward", { id: tab.id })
          }
        >
          <ArrowRight size={17} />
        </button>
        <button
          className="icon-button"
          aria-label={tr("重新加载网页")}
          disabled={!ready}
          onClick={onReload || (() => action("/browser/reload", { id: tab.id }))}
        >
          <RefreshCw size={15} />
        </button>
      </div>
      <BrowserAddressBar tab={tab} tabs={tabs} ready={ready} onNavigate={onNavigate} onSelect={onSelect} />
      <BrowserFavorites current={tab} shortcuts={focused} ready={ready && !busy} onOpen={onOpen} />
      <BrowserDownloads downloads={downloads} ready={ready && !busy} onStatus={onStatus} />
      <button className="icon-button browser-more-trigger" aria-label={tr("浏览器菜单")} title={tr("浏览器菜单")}
        ref={toolsTrigger} aria-haspopup="menu"
        aria-expanded={toolsOpen} aria-controls={`browser-menu-${tab.id}`}
        onKeyDown={event => {
          if (event.key === "ArrowDown") {
            event.preventDefault(); setToolsOpen(true);
            if (toolsOpen) toolsMenu.current?.querySelector('[role="menuitem"]:not(:disabled)')?.focus();
          }
        }}
        onClick={() => { setCookiesOpen(false); setToolsOpen(!toolsOpen); }}><MoreHorizontal size={17} /></button>
      {vertical && exitButton}
      {vertical && closeButton}
    <div className="browser-more-menu" hidden={!toolsOpen} ref={toolsMenu}
      id={`browser-menu-${tab.id}`} role="menu" aria-label={tr("浏览器菜单")} onKeyDown={menuKey}
      onClick={event => {
        const item = event.target.closest('[role="menuitem"]');
        if (item && !item.closest(".vault-browser")) { setToolsOpen(false); toolsTrigger.current?.focus(); }
      }}>
      <div className="browser-more-actions">
        <button className="browser-menu-item" role="menuitem" disabled={!ready || busy} onClick={onNewPrivate}><ScanEye size={16} />{tr("新建无痕标签")}</button>
        {tab.incognito && <button className="browser-menu-item" role="menuitem" disabled={!ready || busy} onClick={onNewRegular}><Globe2 size={16} />{tr("新建普通标签")}</button>}
        {!tab.login && tab.kind !== "browser-settings" && <button className="browser-menu-item" role="menuitem" disabled={!ready || busy} onClick={() => setCookiesOpen(true)}><Cookie size={16} />{tr("管理 Cookie")}</button>}
        <button className="browser-menu-item" role="menuitem" disabled={!ready || busy} onClick={() => onSettings('extensions')}><Puzzle size={16} />{tr("扩展")}</button>
        <BrowserHistory menu shortcuts={focused} ready={ready && !busy} onOpen={onOpen} returnFocusRef={toolsTrigger} />
        <BrowserDownloads menu anchorRef={toolsTrigger} downloads={downloads} ready={ready && !busy} onStatus={onStatus} />
        {tab.kind === "editor" && <button className="browser-menu-item" role="menuitem" aria-label={tr("恢复编辑器")} title={tr("单独重启编辑器")}
          disabled={!ready || busy || recoveringEditor} onClick={onRecover}>
          <RefreshCw size={16} className={recoveringEditor ? "spin" : ""} /><span>{tr("恢复编辑器")}</span>
        </button>}
      </div>
      {vault && !tab.login && !tab.incognito && tab.kind === "web" && <BrowserVaultBar menu tab={tab} vault={vault} ready={ready} onManage={onManageVault} />}
      <div className="browser-settings-menu-row"><button className="browser-menu-item" role="menuitem" disabled={!ready || busy} onClick={() => onSettings('general')}><Settings size={16} />{tr("设置")}</button></div>
    </div>
    </div>
    {cookiesOpen && <BrowserCookies key={`${tab.id}:${tab.url}`} tab={tab} onClose={() => { setCookiesOpen(false); toolsTrigger.current?.focus(); }} />}
    {tab.incognito && <p className="browser-private-note">{tab.login ? tr("本次授权使用独立的无痕会话，授权结束后清理。") : tr("无痕会话 · 关闭全部无痕标签后清理网站数据；下载和主动保存的收藏会保留。")}</p>}
    {menu}
    {busy && <div className="progress-line" />}
  </header>;
}
