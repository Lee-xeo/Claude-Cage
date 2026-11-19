import { DesktopList } from "./account-desktops";
import { HostPluginsNav, HostPluginsSidebar, PluginPagesNav } from "./host-plugins";
import { shortcut } from "./platform.mjs";
import React, { useEffect, useRef, useState } from "react";
import {
  CircleAlert,
  ExternalLink,
  Folder,
  FolderOpen,
  Globe2,
  Home,
  Monitor,
  Network,
  PanelLeftClose,
  Plus,
  RefreshCw,
  Settings,
  Server,
  SquareTerminal,
  UsersRound,
} from "lucide-react";
import { EnvironmentShield } from "./environment-shield";
import logo from "./logo.png";
import claudeLogo from "./claude-logo.svg";
import { dropAfter, moveItem, projectName } from "./workspace-state.mjs";
import { WorkspaceTabs } from "./workspace-tabs";
import { projectSessionRows, sessionKey } from "./session-order.mjs";
import { AccountMenu } from "./account";
import { useSSHNavigation } from "./remote-hosts";

const SESSION_PREVIEW_LIMIT = 5;

export function WorkspaceSidebar({
  workspace,
  projects,
  projectGroups = [],
  projectOrder = [],
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
  accountDesktops,
  desktopOpen,
  onEditor,
  onSettings,
  onAccountConfirm,
  onAccountStatus,
  onSafety,
  onCollapse,
  updateAction,
}) {
  const ssh = useSSHNavigation();
  const [collapsed, setCollapsed] = useState({});
  const [allSessionsShown, setAllSessionsShown] = useState({});
  const sessionBaseline = useRef(new Map());
  const scroll = useRef(null);
  const projectPointer = useRef(null);
  const suppressProjectClick = useRef(false);
  const [projectDrag, setProjectDrag] = useState(null);
  const projectRanks = new Map(projectOrder.map((path, index) => [path, index]));
  const projectRank = path => projectRanks.get(path) ?? projectRanks.size;
  const orderedProjects = [...projects].sort((a, b) => projectRank(a) - projectRank(b));
  const moveProject = (from, to, after) => {
    const order = moveItem(orderedProjects, from, to, after);
    if (order !== orderedProjects) onProjectReorder(order);
  };
  const projectDrop = (event, from) => {
    const section = document.elementFromPoint(event.clientX, event.clientY)?.closest("[data-sidebar-project]");
    const to = section?.dataset.sidebarProject;
    if (!to || to === from || !scroll.current?.contains(section)) return null;
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
  const mainSidebar = (
    <aside className="workspace-sidebar" aria-label="项目与终端" inert={plugins?.open}>
      <div className="sidebar-top">
        <span className="sidebar-brand">
          <img className="brand-logo" src={logo} alt="" />
          Cage
        </span>
        <button
          className="icon-button"
          onClick={onCollapse}
          aria-label="收起侧栏"
          title="收起侧栏"
        >
          <PanelLeftClose size={17} />
        </button>
      </div>
      <nav className="sidebar-navigation" aria-label="主导航">
        <button
          className={`sidebar-link ${home ? "selected" : ""}`}
          onClick={onHome}
        >
          <Home size={17} />
          工作台
        </button>
        <div className={`sidebar-browser-row ${!desktopOpen && !home && tabs.some((tab) => tab.id === active && tab.kind !== "terminal") ? "selected" : ""}`}>
          <button
            className="sidebar-link"
            onClick={onBrowser}
            disabled={!ready || busy}
            aria-label="打开浏览器"
            title="打开浏览器"
          >
            <Globe2 size={17} />
            浏览器
          </button>
          <button
            className="icon-button sidebar-claude-shortcut"
            onClick={onClaudeWeb}
            disabled={!ready || busy}
            aria-label="claude.ai"
            title="打开 claude.ai"
          >
            <img src={claudeLogo} width="18" height="18" alt="" />
          </button>
        </div>
        <button className={`sidebar-link ${tailscaleOpen ? "selected" : ""}`} aria-current={tailscaleOpen ? "page" : undefined} onClick={onTailscale} aria-label="打开 Tailscale"><Network size={17} />Tailscale</button>
        {plugins?.pages?.length > 0 && <PluginPagesNav pages={plugins.pages} selected={plugins.pageOpen} onOpen={plugins.onView} />}
        {plugins && <HostPluginsNav open={plugins.open} onOpen={plugins.onOpen} />}
        {ssh && <button className="sidebar-link ssh-navigation" onClick={ssh.open} aria-label="SSH 主机"><Server size={17} />SSH</button>}
        {accountDesktops ? <DesktopList model={accountDesktops} disabled={!running || busy} selected={desktopOpen} onDefault={onClaudeDesktop}/> : <button className={`sidebar-link ${desktopOpen?"selected":""}`} onClick={onClaudeDesktop} disabled={!running||busy}><Monitor size={17}/>Claude Desktop</button>}
        <button className={`sidebar-link ${claudeAccountsOpen ? "selected" : ""}`} onClick={onClaudeAccounts}
          aria-current={claudeAccountsOpen ? "page" : undefined}>
          <UsersRound size={17} />Claude 账号管理
        </button>
      </nav>
      <div className="sidebar-scroll" ref={scroll}>
        <div className="sidebar-section-heading">
          <span>项目</span>
          <span className="sidebar-section-actions">
            {onProjectRefresh && (
              <button
                className="icon-button"
                onClick={onProjectRefresh}
                aria-label="刷新项目文件"
                title="刷新项目文件：让环境立即看到在本机上所做的改动"
                disabled={busy || !running || projectRefreshing}
              >
                <RefreshCw size={14} className={projectRefreshing ? "spin" : undefined} />
              </button>
            )}
            <button
              className="icon-button"
              onClick={() => onOpenProject()}
              aria-label="添加项目"
              title="添加项目"
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
                <strong>会话列表暂未更新</strong>
              </div>
              <p>{sessionError}</p>
            </div>
            <button
              onClick={onSessionRefresh}
              disabled={sessionRefreshing || busy || !running}
              aria-busy={sessionRefreshing || undefined}
            >
              <RefreshCw size={14} aria-hidden="true" />
              {sessionRefreshing ? "正在读取…" : "重新读取会话"}
            </button>
          </div>
        )}
        {orderedProjects.map((path, index) => {
          const group = projectGroups.find(group => group.workspace === path);
          const folders = group?.folders || [path];
          const project = projectName(path, projectGroups);
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
              aria-label={`项目 ${project}`}
              data-sidebar-project={path}
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
                    if (!event.altKey || !["ArrowUp", "ArrowDown"].includes(event.key)) return;
                    event.preventDefault();
                    const after = event.key === "ArrowDown";
                    const target = orderedProjects[index + (after ? 1 : -1)];
                    if (target) {
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
                </button>
                <button
                  className="icon-button"
                  aria-label={`打开 ${project} 的编辑器`}
                  title="在 code-server 中打开项目目录"
                  disabled={!ready || busy}
                  onClick={() => onEditor(path)}
                >
                  <ExternalLink size={15} />
                </button>
                <button
                  className="icon-button"
                  aria-label={`在 ${project} 新建终端`}
                  title="新建终端"
                  aria-haspopup="menu"
                  data-terminal-location={path}
                  disabled={!running || busy}
                  onClick={(event) => {
                    setCollapsed((previous) => ({
                      ...previous,
                      [path]: false,
                    }));
                    onNewTerminal(path, event.currentTarget);
                  }}
                >
                  <SquareTerminal size={15} />
                </button>
              </div>
              {terminalMenuAt(path)}
              {expanded && folders.length > 1 && <div className="sidebar-project-folders" aria-label={`${project} 的文件夹`}>
                {folders.map((folder, index) => <button key={folder} title={folder} disabled={!ready || busy} onClick={() => onEditor(folder)} aria-label={`打开文件夹 ${projectName(folder)} 的编辑器`}><Folder size={13} /><span>{projectName(folder)}</span>{index === 0 && <small>默认</small>}</button>)}
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
                      active={home || desktopOpen ? "" : active}
                      label={`${project} 的终端`}
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
                      {showAllSessions ? "收起显示" : "展开显示"}
                    </button>
                  )}
                  {!rows.length && !running && (
                    <p className="sidebar-empty">启动后可新建终端</p>
                  )}
                </div>
              </div>
            </section>
          );
        })}
        {!projects.length && (
          <p className="sidebar-empty">打开本地文件夹开始工作</p>
        )}
      </div>
      <div className="sidebar-bottom">
        <button className="sidebar-status" onClick={onSafety}>
          <i className={`status-dot ${network.mode}`} />
          <span>{healthy ? "环境运行正常" : runtimeLabel}</span>
          <EnvironmentShield size={17} healthy={healthy} />
        </button>
        <div className="sidebar-footer-actions">
          <AccountMenu onSettings={onSettings} onConfirm={onAccountConfirm} onStatus={onAccountStatus} busy={busy} />
          <button className="icon-button" aria-label="设置" title={shortcut("设置 ⌘,")} onClick={() => onSettings("keyboard")}><Settings size={17} /></button>
          {updateAction}
        </div>
      </div>
    </aside>
  );
  return (
    <div className="workspace-sidebar-stack">
      {mainSidebar}
      {plugins && <HostPluginsSidebar visible={plugins.open} model={plugins.model} page={plugins.page} onPage={plugins.onPage}
        onBack={plugins.onBack} onConfirm={plugins.onConfirm} />}
    </div>
  );
}
