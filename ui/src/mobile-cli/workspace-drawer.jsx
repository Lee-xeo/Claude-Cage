import { mobileText } from "./language.mjs";
import React, { useEffect, useRef } from "react";
import { Folder, FolderOpen, Globe2, LoaderCircle, LogOut, Monitor, Plus, Settings, Sparkles } from "lucide-react";
import { InlineSelect } from "./inline-select";
import { SessionRow, sortSessions } from "./session-row";

export const workspaceTools = [
  {id:"browser", name:mobileText("浏览器"), icon:Globe2},
  {id:"claude", name:"Claude.ai", icon:Sparkles},
  {id:"desktop", name:"Claude Desktop", icon:Monitor},
];

export function CodeNavigation({catalog, project, sessions, current, creating, onProject, onSession, onNew, onAction, formatTime}) {
  if (!catalog) return <p className="nav-note" role="status">{mobileText("正在读取项目…")}</p>;
  if (!project) return <p className="nav-note">{mobileText("请先在电脑 Cage 打开项目。")}</p>;
  return <section className="code-navigation" aria-label={mobileText("Claude Code 项目和会话")}>
    <div className="project-field">
      <InlineSelect label={mobileText("选择项目")} icon={<FolderOpen size={16} />} value={project.path}
        disabled={creating} onChange={onProject} options={catalog.projects.map(p => ({
          value:p.path, label:p.name, optionLabel:p.name + " · " + p.path,
        }))} />
    </div>
    <div className="row-heading"><h2>{mobileText("会话")}</h2><button type="button" className="nav-new" onClick={onNew} disabled={creating || !catalog.ready}>
      {creating ? <LoaderCircle size={15} className="spin" /> : <Plus size={15} />}{mobileText("新建会话")}</button></div>
    {sessions.length ? <ul className="session-list">{sessions.map(s => <SessionRow key={s.id} session={s} current={current === s.id}
      disabled={creating} onSelect={() => onSession(s.id)} onAction={onAction} detail={formatTime(s.updated_at)}/>)}</ul>
      : <p className="nav-note">{catalog.ready ? mobileText("这个项目还没有会话。") : mobileText("请先在电脑上启动 Cage。")}</p>}
  </section>;
}

export function ProjectTree({catalog, projectPath, sessionID, expanded, toggle, creating, onNew, onSession, onAction, formatTime}) {
  const projects = catalog?.projects || [];
  return <section className="project-tree" aria-label={mobileText("Claude Code 项目和会话")}>
    <h2>Claude Code</h2>
    {catalog?.shared_sessions === false && <p className="nav-note">{mobileText("当前启动配置使用独立移动会话。共享电脑终端需要使用 claude 启动命令。")}</p>}
    {!catalog ? <p className="nav-note" role="status">{mobileText("正在读取项目…")}</p> :
      !projects.length ? <p className="nav-note">{mobileText("请在电脑 Cage 打开项目。")}</p> : projects.map((project, index) => {
        const path = project.path;
        const open = expanded.get(path) ?? (path === projectPath || index === 0);
        const name = projects.some(other => other !== project && other.name === project.name) ? path : project.name;
        const sessions = catalog.sessions.filter(s => (project.folders || [path]).includes(s.workspace))
          .sort(sortSessions);
        return <div className="tree-project" key={path}>
          <div className="tree-row">
            <button type="button" className="tree-folder" title={path} aria-label={mobileText("项目 {0}", name)}
              aria-expanded={open} onClick={() => toggle(path, !open)}>
              {open ? <FolderOpen size={17} /> : <Folder size={17} />}
              <span>{name}</span>
            </button>
            <button type="button" className="icon-button tree-new" aria-label={mobileText("在 {0} 新建会话", name)}
              title={mobileText("在 {0} 新建会话", name)} disabled={creating || !catalog.ready} onClick={() => onNew(project)}>
              <Plus size={16} />
            </button>
          </div>
          {open && <ul className="tree-sessions" aria-label={mobileText("{0}的会话", name)}>
            {sessions.length ? sessions.map(s => <SessionRow key={s.id} session={s} current={s.id === sessionID} disabled={creating} detail={s.paused && formatTime(s.updated_at)}
              onSelect={() => onSession(s.workspace, s.id)} onAction={onAction}/>) : <li className="tree-empty">{mobileText("暂无会话")}</li>}
          </ul>}
        </div>;
      })}
  </section>;
}

function WorkspaceSidebarBody({active, name, onTool, onExit, onSettings, nativePhone, children}) {
  return <>
    <header className="drawer-heading"><Monitor size={18} /><h2>{name || mobileText("远程工作台")}</h2></header>
    <div className="drawer-content">
      <nav className="workspace-tools" aria-label={mobileText("远程工具")}>{workspaceTools.map(({id, name:toolName, icon:Icon}) =>
        <button key={id} type="button" className="tool-link" aria-current={id === active ? "page" : undefined} onClick={() => onTool(id)}>
          <Icon size={19} /><span>{toolName}</span>
        </button>)}</nav>
      {children}
    </div>
    <footer className="drawer-footer"><button className="drawer-exit" type="button" onClick={onSettings}><Settings size={18}/>{mobileText("设置")}</button>
      <a className="drawer-exit" href={nativePhone ? "cage://home" : "/"}
      onClick={event => {if (onExit()) event.preventDefault();}}><LogOut size={18}/>{mobileText("退出")}</a></footer>
  </>;
}

export function WorkspaceSidebar(props) {
  return <aside className="workspace-sidebar" aria-label={mobileText("工作区侧栏")}>
    <WorkspaceSidebarBody {...props} />
  </aside>;
}

export function WorkspaceDrawer({open, onClose, ...bodyProps}) {
  const panel = useRef(null);
  useEffect(() => {
    const dialog = panel.current;
    let frame;
    let timer;
    if (open) {
      if (!dialog.open) dialog.showModal();
      frame = requestAnimationFrame(() => { frame = requestAnimationFrame(() => { dialog.dataset.visible = "true"; }); });
    } else if (dialog.open) {
      delete dialog.dataset.visible;
      if (matchMedia("(prefers-reduced-motion: reduce)").matches) dialog.close();
      else timer = setTimeout(() => dialog.close(), 240);
    }
    return () => { cancelAnimationFrame(frame); clearTimeout(timer); };
  }, [open]);
  return <dialog className="workspace-drawer" ref={panel} aria-label={mobileText("工作区侧栏")}
    onCancel={event => {event.preventDefault(); onClose();}}
    onClick={event => {
      if (event.target !== panel.current) return;
      const rect = panel.current.getBoundingClientRect();
      if (event.clientX < rect.left || event.clientX >= rect.right || event.clientY < rect.top || event.clientY >= rect.bottom) onClose();
    }}>
    <WorkspaceSidebarBody {...bodyProps} />
  </dialog>;
}
