import { DesktopList } from "./account-desktops";
import { HostPluginsNav, HostPluginsSidebar, PluginPagesNav } from "./host-plugins";
import { shortcut } from "./platform.mjs";
import React, { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  CircleAlert,
  FileCode2,
  Folder,
  FolderOpen,
  Globe2,
  Monitor,
  MoreHorizontal,
  Network,
  PanelLeftClose,
  Pencil,
  PencilLine,
  Pin,
  PinOff,
  Plus,
  Power,
  RefreshCw,
  Settings,
  Server,
  SquareTerminal,
  Trash2,
  UsersRound,
} from "lucide-react";
import { EnvironmentShield } from "./environment-shield";
import logo from "./logo.png";
import claudeLogo from "./claude-logo.svg";
import { dropAfter, isClientProjectPath, moveItem, orderedProjects as sortProjects, projectName } from "./workspace-state.mjs";
import { WorkspaceTabs } from "./workspace-tabs";
import { projectSessionRows, sessionKey, unassignedTerminalTabs } from "./session-order.mjs";
import { AccountMenu } from "./account";
import { useSSHNavigation } from "./remote-hosts";
import { useTranslation } from "./interface-language.mjs";

const SESSION_PREVIEW_LIMIT = 5;

function ProjectActionMenu({
  menu,
  project,
  pinned,
  clientShare,
  ready,
  busy,
  onClose,
  onEditor,
  onPin,
  onRename,
  onEdit,
  onRemove,
}) {
  const tr = useTranslation();
  const node = useRef(null);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  const [position, setPosition] = useState(() => ({
    left: menu.point?.x ?? 8,
    top: menu.point?.y ?? 8,
    ready: false,
  }));

  useLayoutEffect(() => {
    const element = node.current;
    const anchor = menu.anchor;
    if (!element || !anchor?.isConnected) {
      closeRef.current();
      return;
    }
    const rect = element.getBoundingClientRect();
    const width = rect.width || 184;
    const height = rect.height || 210;
    const clamp = (value, min, max) => Math.max(min, Math.min(value, Math.max(min, max)));
    let left;
    let top;
    if (menu.point) {
      left = clamp(menu.point.x, 8, window.innerWidth - width - 8);
      top = clamp(menu.point.y, 8, window.innerHeight - height - 8);
    } else {
      const box = anchor.getBoundingClientRect();
      left = clamp(box.right - width, 8, window.innerWidth - width - 8);
      top = box.bottom + 4 + height <= window.innerHeight - 8
        ? box.bottom + 4
        : Math.max(8, box.top - height - 4);
    }
    setPosition({ left, top, ready: true });
    const dismiss = (restoreFocus = false) => {
      if (restoreFocus && anchor.isConnected) anchor.focus();
      closeRef.current();
    };
    const outside = (event) => {
      if (!element.contains(event.target) && !anchor.contains(event.target)) dismiss();
    };
    const keydown = (event) => {
      if (event.defaultPrevented) return;
      if (event.key === "Escape" || event.key === "Tab") {
        if (event.key === "Escape") event.preventDefault();
        event.stopPropagation();
        dismiss(event.key === "Escape");
      } else if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
        event.preventDefault();
        const items = [...element.querySelectorAll("button:not(:disabled)")];
        if (!items.length) return;
        const index = items.indexOf(document.activeElement);
        const next = event.key === "Home" ? 0
          : event.key === "End" ? items.length - 1
          : event.key === "ArrowUp" && index < 0 ? items.length - 1
          : (index + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
        items[next]?.focus();
      }
    };
    const closeCommand = () => dismiss(true);
    const onScroll = (event) => {
      if (!element.contains(event.target)) dismiss();
    };
    document.addEventListener("click", outside, true);
    document.addEventListener("keydown", keydown);
    document.addEventListener("scroll", onScroll, true);
    document.addEventListener("cage-close-modal", closeCommand);
    window.addEventListener("resize", closeCommand);
    return () => {
      document.removeEventListener("click", outside, true);
      document.removeEventListener("keydown", keydown);
      document.removeEventListener("scroll", onScroll, true);
      document.removeEventListener("cage-close-modal", closeCommand);
      window.removeEventListener("resize", closeCommand);
    };
  }, [menu]);

  // A hidden menu cannot take focus, so wait until the position is applied.
  useEffect(() => {
    if (position.ready) node.current?.querySelector("button:not(:disabled)")?.focus();
  }, [position.ready, menu]);

  const act = (handler) => {
    onClose();
    handler?.();
  };

  return createPortal(
    <div
      ref={node}
      id="project-action-menu"
      className="menu project-action-menu"
      role="menu"
      aria-label={tr("{0} 的操作", project)}
      style={{
        left: position.left,
        top: position.top,
        visibility: position.ready ? "visible" : "hidden",
      }}
    >
      <button
        type="button"
        role="menuitem"
        title={tr("在 code-server 中打开项目目录")}
        disabled={!ready || busy}
        onClick={() => act(onEditor)}
      >
        <FileCode2 size={15} aria-hidden="true" />
        {tr("打开文件")}
      </button>
      <button
        type="button"
        role="menuitem"
        disabled={busy || !onPin}
        onClick={() => act(onPin)}
      >
        {pinned ? <PinOff size={15} aria-hidden="true" /> : <Pin size={15} aria-hidden="true" />}
        {pinned ? tr("取消置顶") : tr("置顶")}
      </button>
      <button
        type="button"
        role="menuitem"
        disabled={busy || clientShare || !onRename}
        onClick={() => act(onRename)}
      >
        <Pencil size={15} aria-hidden="true" />
        {tr("重命名")}
      </button>
      <button
        type="button"
        role="menuitem"
        disabled={busy || !ready || clientShare || !onEdit}
        onClick={() => act(onEdit)}
      >
        <PencilLine size={15} aria-hidden="true" />
        {tr("编辑项目")}
      </button>
      <div className="menu-separator" role="separator" />
      <button
        type="button"
        role="menuitem"
        className="menu-item-danger"
        disabled={busy || clientShare || !onRemove}
        onClick={() => act(onRemove)}
      >
        <Trash2 size={15} aria-hidden="true" />
        {tr("移除项目")}
      </button>
    </div>,
    document.body,
  );
}

export function WorkspaceSidebar({
  workspace,
  projects,
  projectGroups = [],
  projectOrder = [],
  pinnedProjects = [],
  tabs,
  sessions = [],
  sessionError,
  sessionRefreshing,
  onSessionRefresh,
  onProjectRefresh,
  projectRefreshing,
  onSessionOpen,
  openingSession,
  sessionOpenError,
  onSessionAccount,
  onSessionArchive,
  onSessionPin,
  onSessionRename,
  saved,
  active,
  home,
  busy,
  ready,
  running,
  runtimeLabel,
  network,
  onHome,
  onOpenProject,
  onProjectSelect,
  onProjectReorder,
  onProjectPin,
  onProjectRename,
  onProjectEdit,
  onProjectRemove,
  onSelect,
  onClose,
  onRename,
  onPin,
  onSessionReorder,
  splitDrag,
  onNewTerminal,
  terminalMenuAt,
  onBrowser,
  onTailscale,
  tailscaleOpen,
  plugins,
  onClaudeAccounts,
  claudeAccountsOpen,
  onClaudeWeb,
  onClaudeDesktop,
  defaultDesktopRunning,
  onDefaultDesktopStop,
  accountDesktops,
  onDesktopRename,
  desktopOpen,
  onEditor,
  onSettings,
  onAccountConfirm,
  onAccountStatus,
  onSafety,
  onCollapse,
  updateAction,
}) {
  const tr = useTranslation();
  const ssh = useSSHNavigation();
  const [collapsed, setCollapsed] = useState({});
  const [allSessionsShown, setAllSessionsShown] = useState({});
  const [projectMenu, setProjectMenu] = useState(null);
  const sessionBaseline = useRef(new Map());
  const scroll = useRef(null);
  const projectPointer = useRef(null);
  const suppressProjectClick = useRef(false);
  const [projectDrag, setProjectDrag] = useState(null);
  const pinnedSet = new Set(pinnedProjects);
  const orderedProjects = sortProjects(projects, projectOrder, pinnedProjects);
  const moveProject = (from, to, after) => {
    if (pinnedSet.has(from) !== pinnedSet.has(to)) return;
    const order = moveItem(orderedProjects, from, to, after);
    if (order !== orderedProjects) onProjectReorder(order);
  };
  const projectDrop = (event, from) => {
    const section = document.elementFromPoint(event.clientX, event.clientY)?.closest("[data-sidebar-project]");
    const to = section?.dataset.sidebarProject;
    if (!to || to === from || pinnedSet.has(from) !== pinnedSet.has(to) || !scroll.current?.contains(section)) return null;
    return { to, after: dropAfter(section.querySelector(".project-heading"), event.clientY) };
  };
  const endProjectDrag = (event, commit = false) => {
    const current = projectPointer.current;
    if (!current || current.pointerId !== event.pointerId) return;
    projectPointer.current = null;
    if (!current.moved) return;
    suppressProjectClick.current = true;
    setProjectDrag(null);
    const drop = commit && projectDrop(event, current.path);
    if (drop) moveProject(current.path, drop.to, drop.after);
  };
  const openProjectMenu = (path, anchor, point = null) => {
    setProjectMenu((current) => !point && current?.path === path ? null : { path, anchor, point });
  };
  useEffect(() => {
    if (projectMenu && !projects.includes(projectMenu.path)) setProjectMenu(null);
  }, [projectMenu, projects]);
  useEffect(() => {
    if (!projectDrag) return;
    const cancel = event => {
      if (event.key !== "Escape") return;
      event.preventDefault();
      event.stopImmediatePropagation();
      endProjectDrag(projectPointer.current);
    };
    document.addEventListener("keydown", cancel, true);
    return () => document.removeEventListener("keydown", cancel, true);
  }, [Boolean(projectDrag)]);
  const activeFolder = tabs.find((tab) => tab.id === active)?.workspace;
  const activeWorkspace = projectGroups.find(group => group.folders.includes(activeFolder))?.workspace || activeFolder;
  useEffect(() => {
    if (activeWorkspace)
      setCollapsed((previous) => ({ ...previous, [activeWorkspace]: false }));
  }, [active, activeWorkspace]);
  if (ssh?.expanded) return null;
  const healthy = ready && network.mode === "open";
  // Terminals whose project was removed still count toward the host limit and must stay closable.
  const otherTerminals = projectSessionRows(unassignedTerminalTabs(tabs, projects, projectGroups), [], saved, undefined, sessionBaseline.current);
  const mainSidebar = (
    <aside className="workspace-sidebar" aria-label={tr("项目与终端")} inert={plugins?.open}>
      <div className="sidebar-top">
        <button
          type="button"
          className={`sidebar-brand ${home ? "selected" : ""}`}
          onClick={onHome}
          aria-label={tr("工作台")}
          aria-current={home ? "page" : undefined}
          title={tr("工作台")}
        >
          <img className="brand-logo" src={logo} alt="" />
          <span>Cage</span>
        </button>
        <button
          className="icon-button"
          onClick={onCollapse}
          aria-label={tr("收起侧栏")}
          title={tr("收起侧栏")}
        >
          <PanelLeftClose size={17} />
        </button>
      </div>
      <nav className="sidebar-navigation" aria-label={tr("主导航")}>
        {ssh && <button className="sidebar-link ssh-navigation" onClick={ssh.open} aria-label={tr("SSH 主机")}><Server size={17} />SSH</button>}
        <div className={`sidebar-browser-row ${!desktopOpen && !home && tabs.some((tab) => tab.id === active && tab.kind !== "terminal") ? "selected" : ""}`}>
          <button
            className="sidebar-link"
            onClick={onBrowser}
            disabled={!ready || busy}
            aria-label={tr("打开浏览器")}
            title={tr("打开浏览器")}
          >
            <Globe2 size={17} />
            {tr("浏览器")}
          </button>
          <button
            className="icon-button sidebar-claude-shortcut"
            onClick={onClaudeWeb}
            disabled={!ready || busy}
            aria-label="claude.ai"
            title={tr("打开 claude.ai")}
          >
            <img src={claudeLogo} width="18" height="18" alt="" />
          </button>
        </div>
        {accountDesktops ? (
          <DesktopList
            model={accountDesktops}
            disabled={!running || busy}
            selected={desktopOpen}
            defaultRunning={defaultDesktopRunning}
            onDefault={onClaudeDesktop}
            onDefaultStop={onDefaultDesktopStop}
            onRename={onDesktopRename}
          />
        ) : (
          <div className={`account-desktop-row ${desktopOpen ? "active" : ""}`}>
            <button
              className="sidebar-link"
              onClick={onClaudeDesktop}
              disabled={!running || busy}
            >
              <Monitor size={17} />
              Claude Desktop
              {defaultDesktopRunning ? <span className="account-desktop-status" aria-hidden="true">●</span> : null}
            </button>
            {defaultDesktopRunning && onDefaultDesktopStop && (
              <div className="terminal-row-actions" role="group" aria-label={tr("{0} 的操作", "Claude Desktop")}>
                <button
                  type="button"
                  className="icon-button"
                  aria-label={tr("关闭应用")}
                  title={tr("关闭应用")}
                  disabled={!running || busy}
                  onClick={(event) => onDefaultDesktopStop(null, event.currentTarget)}
                >
                  <Power size={14} />
                </button>
              </div>
            )}
          </div>
        )}
        <button className={`sidebar-link ${claudeAccountsOpen ? "selected" : ""}`} onClick={onClaudeAccounts}
          aria-current={claudeAccountsOpen ? "page" : undefined}>
          <UsersRound size={17} />{tr("Claude 账号管理")}
        </button>
        {(plugins || onTailscale) && (
          <>
            <div className="sidebar-nav-divider" aria-hidden="true" />
            {plugins && <HostPluginsNav open={plugins.open} onOpen={plugins.onOpen} />}
            {onTailscale && <button className={`sidebar-link ${tailscaleOpen ? "selected" : ""}`} aria-current={tailscaleOpen ? "page" : undefined} onClick={onTailscale} aria-label={tr("打开 Tailscale")}><Network size={17} />Tailscale</button>}
            {plugins?.pages?.length > 0 && <PluginPagesNav pages={plugins.pages} selected={plugins.pageOpen} floatingOpen={plugins.floatingOpen} onOpen={plugins.onView} />}
          </>
        )}
      </nav>
      <div className="sidebar-scroll" ref={scroll}>
        <div className="sidebar-section-heading">
          <span>{tr("项目")}</span>
          <span className="sidebar-section-actions">
            {onProjectRefresh && (
              <button
                className="icon-button"
                onClick={onProjectRefresh}
                aria-label={tr("刷新项目文件")}
                title={tr("刷新项目文件：让环境立即看到在本机上所做的改动")}
                disabled={busy || !running || projectRefreshing}
              >
                <RefreshCw size={14} className={projectRefreshing ? "spin" : undefined} />
              </button>
            )}
            <button
              className="icon-button"
              onClick={() => onOpenProject()}
              aria-label={tr("添加项目")}
              title={tr("添加项目")}
              disabled={busy}
            >
              <Plus size={15} />
            </button>
          </span>
        </div>
        {sessionError && (
          <div className="sidebar-session-error">
            <div role="alert">
              <div className="sidebar-session-error-heading">
                <CircleAlert size={16} aria-hidden="true" />
                <strong>{tr("会话列表暂未更新")}</strong>
              </div>
              <p>{sessionError}</p>
            </div>
            <button
              onClick={onSessionRefresh}
              disabled={sessionRefreshing || busy || !running}
              aria-busy={sessionRefreshing || undefined}
            >
              <RefreshCw size={14} aria-hidden="true" />
              {sessionRefreshing ? tr("正在读取…") : tr("重新读取会话")}
            </button>
          </div>
        )}
        {orderedProjects.map((path, index) => {
          const group = projectGroups.find(group => group.workspace === path);
          const folders = group?.folders || [path];
          const project = projectName(path, projectGroups);
          const pinned = pinnedSet.has(path);
          const rows = projectSessionRows(tabs.filter(tab => tab.kind === "terminal" && folders.includes(tab.workspace)),
            sessions.filter(session => folders.includes(session.workspace)), saved, saved.sessionOrder?.[path], sessionBaseline.current);
          const rowMetadata = { ...saved };
          for (const row of rows) rowMetadata[row.id] = { ...saved[row.id], pinned: row.sessionPinned };
          const expanded = !collapsed[path];
          const showAllSessions = Boolean(allSessionsShown[path]);
          const visibleRows = showAllSessions ? rows : rows.slice(0, SESSION_PREVIEW_LIMIT);
          return (
            <section
              className="sidebar-project"
              key={path}
              aria-label={tr("项目 {0}", (project))}
              data-sidebar-project={path}
              data-project-pinned={pinned || undefined}
              data-drop-position={projectDrag?.drop?.to === path ? projectDrag.drop.after ? "after" : "before" : undefined}
              data-project-dragging={projectDrag?.from === path || undefined}
            >
              <div
                className={`project-heading ${folders.includes(workspace) ? "current-project" : ""}`}
              >
                <button
                  className="project-disclosure"
                  title={folders.join("\n")}
                  aria-expanded={expanded}
                  aria-keyshortcuts={projects.length > 1 ? "Alt+ArrowUp Alt+ArrowDown" : undefined}
                  onContextMenu={event => {
                    if (busy) return;
                    event.preventDefault();
                    const more = event.currentTarget.parentElement?.querySelector(".project-more-button") || event.currentTarget;
                    openProjectMenu(path, more, { x: event.clientX, y: event.clientY });
                  }}
                  onPointerDown={event => {
                    if (event.button !== 0 || !event.isPrimary) return;
                    suppressProjectClick.current = false;
                    if (projects.length < 2) return;
                    projectPointer.current = { path, pointerId: event.pointerId, x: event.clientX, y: event.clientY };
                    event.currentTarget.setPointerCapture(event.pointerId);
                  }}
                  onPointerMove={event => {
                    const current = projectPointer.current;
                    if (!current || current.pointerId !== event.pointerId) return;
                    if (!event.buttons) return endProjectDrag(event);
                    if (!current.moved && Math.hypot(event.clientX - current.x, event.clientY - current.y) < 5) return;
                    current.moved = true;
                    setProjectDrag({ from: path, drop: projectDrop(event, path) });
                  }}
                  onPointerUp={event => endProjectDrag(event, true)}
                  onPointerCancel={endProjectDrag}
                  onLostPointerCapture={endProjectDrag}
                  onKeyDown={event => {
                    if ((event.key === "F10" && event.shiftKey) || event.key === "ContextMenu") {
                      if (busy) return;
                      event.preventDefault();
                      const more = event.currentTarget.parentElement?.querySelector(".project-more-button") || event.currentTarget;
                      openProjectMenu(path, more);
                      return;
                    }
                    if (!event.altKey || !["ArrowUp", "ArrowDown"].includes(event.key)) return;
                    event.preventDefault();
                    const after = event.key === "ArrowDown";
                    const target = orderedProjects[index + (after ? 1 : -1)];
                    if (target && pinnedSet.has(target) === pinned) {
                      moveProject(path, target, after);
                      const button = event.currentTarget;
                      requestAnimationFrame(() => button.focus());
                    }
                  }}
                  onClick={event => {
                    if (event.detail && suppressProjectClick.current) {
                      suppressProjectClick.current = false;
                      return;
                    }
                    setCollapsed((previous) => ({
                      ...previous,
                      [path]: expanded,
                    }));
                    setAllSessionsShown((previous) => ({ ...previous, [path]: false }));
                    onProjectSelect(path);
                  }}
                >
                  {expanded ? <FolderOpen size={16} /> : <Folder size={16} />}
                  <span>{project}</span>
                  {pinned && <Pin size={11} className="project-pin" aria-label={tr("已置顶")} />}
                </button>
                <button
                  className="icon-button"
                  aria-label={tr("在 {0} 新建终端", (project))}
                  title={tr("新建终端")}
                  aria-haspopup="menu"
                  data-terminal-location={path}
                  disabled={!running || busy}
                  onClick={(event) => {
                    setProjectMenu(null);
                    setCollapsed((previous) => ({
                      ...previous,
                      [path]: false,
                    }));
                    onNewTerminal(path, event.currentTarget);
                  }}
                >
                  <SquareTerminal size={15} />
                </button>
                <button
                  type="button"
                  className="icon-button project-more-button"
                  aria-label={tr("{0} 的操作", (project))}
                  title={tr("{0} 的操作", (project))}
                  aria-haspopup="menu"
                  aria-expanded={projectMenu?.path === path}
                  aria-controls={projectMenu?.path === path ? "project-action-menu" : undefined}
                  disabled={busy}
                  onClick={(event) => openProjectMenu(path, event.currentTarget)}
                >
                  <MoreHorizontal size={15} />
                </button>
              </div>
              {terminalMenuAt(path)}
              {expanded && folders.length > 1 && <div className="sidebar-project-folders" aria-label={tr("{0} 的文件夹", (project))}>
                {folders.map((folder, index) => <button key={folder} title={folder} disabled={!ready || busy} onClick={() => onEditor(folder)} aria-label={tr("打开文件夹 {0} 的编辑器", (projectName(folder)))}><Folder size={13} /><span>{projectName(folder)}</span>{index === 0 && <small>{tr("默认")}</small>}</button>)}
              </div>}
              <div
                className={`project-children ${expanded ? "expanded" : ""}`}
                inert={!expanded}
              >
                <div>
                  {!!rows.length && (
                    <WorkspaceTabs
                      tabs={visibleRows}
                      saved={rowMetadata}
                      openingSession={openingSession}
                      sessionOpenError={sessionOpenError}
                      onSessionAccount={onSessionAccount}
                      active={home || desktopOpen ? "" : active}
                      label={tr("{0} 的终端", (project))}
                      busy={busy}
                      ready={ready}
                      onSelect={(id, ...args) => {
                        const row = rows.find(row => row.id === id);
                        if (row?.historySession) onSessionOpen(row.historySession);
                        else onSelect(id, ...args);
                      }}
                      onClose={(row, anchor) => row.historySession ? onSessionArchive(row.historySession, anchor) : onClose(row, anchor)}
                      onRename={row => row.historySession ? onSessionRename(row.historySession) : onRename(row)}
                      onPin={id => {
                        const row = rows.find(row => row.id === id);
                        if (row?.historySession) onSessionPin(row.historySession);
                        else onPin(id);
                      }}
                      onSidebarReorder={(from, to, after) => {
                        const keys = rows.map(sessionKey);
                        const keyFor = id => {
                          const row = rows.find(row => row.id === id);
                          return row && sessionKey(row);
                        };
                        const order = moveItem(keys, keyFor(from), keyFor(to), after);
                        if (order !== keys) onSessionReorder(path, order);
                      }}
                      splitDrag={splitDrag}
                    />
                  )}
                  {rows.length > SESSION_PREVIEW_LIMIT && (
                    <button
                      className="project-session-toggle"
                      aria-expanded={showAllSessions}
                      onClick={() => setAllSessionsShown((previous) => ({
                        ...previous,
                        [path]: !showAllSessions,
                      }))}
                    >
                      {showAllSessions ? tr("收起显示") : tr("展开显示")}
                    </button>
                  )}
                  {!rows.length && !running && (
                    <p className="sidebar-empty">{tr("启动后可新建终端")}</p>
                  )}
                </div>
              </div>
            </section>
          );
        })}
        {!!otherTerminals.length && (
          <section className="sidebar-project sidebar-other-terminals" aria-label={tr("其他终端")}>
            <div className="sidebar-section-heading"><span>{tr("其他终端")}</span></div>
            <WorkspaceTabs
              tabs={otherTerminals}
              saved={saved}
              active={home || desktopOpen ? "" : active}
              label={tr("其他终端")}
              busy={busy}
              ready={ready}
              onSelect={onSelect}
              onClose={onClose}
              onRename={onRename}
              onPin={onPin}
              splitDrag={splitDrag}
            />
          </section>
        )}
        {!projects.length && (
          <p className="sidebar-empty">{tr("打开本地文件夹开始工作")}</p>
        )}
      </div>
      <div className="sidebar-bottom">
        <button className="sidebar-status" onClick={onSafety}>
          <i className={`status-dot ${network.mode}`} />
          <span>{healthy ? tr("环境运行正常") : runtimeLabel}</span>
          <EnvironmentShield size={17} healthy={healthy} />
        </button>
        <div className="sidebar-footer-actions">
          <AccountMenu onSettings={onSettings} onConfirm={onAccountConfirm} onStatus={onAccountStatus} busy={busy} />
          <button className="icon-button" aria-label={tr("设置")} title={`${tr("设置")} ${shortcut("⌘,")}`} onClick={() => onSettings("keyboard")}><Settings size={17} /></button>
          {updateAction}
        </div>
      </div>
    </aside>
  );
  const activeProjectMenu = projectMenu && projects.includes(projectMenu.path) ? (
    <ProjectActionMenu
      menu={projectMenu}
      project={projectName(projectMenu.path, projectGroups)}
      pinned={pinnedSet.has(projectMenu.path)}
      clientShare={isClientProjectPath(projectMenu.path)}
      ready={ready}
      busy={busy}
      onClose={() => setProjectMenu(null)}
      onEditor={() => onEditor(projectMenu.path)}
      onPin={onProjectPin ? () => onProjectPin(projectMenu.path) : undefined}
      onRename={onProjectRename ? () => onProjectRename(projectMenu.path) : undefined}
      onEdit={onProjectEdit ? () => onProjectEdit(projectMenu.path) : undefined}
      onRemove={onProjectRemove ? () => onProjectRemove(projectMenu.path) : undefined}
    />
  ) : null;
  return (
    <div className="workspace-sidebar-stack">
      {mainSidebar}
      {activeProjectMenu}
      {plugins && <HostPluginsSidebar visible={plugins.open} model={plugins.model} page={plugins.page} onPage={plugins.onPage}
        onBack={plugins.onBack} onConfirm={plugins.onConfirm} />}
    </div>
  );
}
