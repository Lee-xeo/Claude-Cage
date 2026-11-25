import { isWindows, isMac } from "./platform.mjs";
import React, { useEffect, useState } from "react";
import { FolderOpen, LoaderCircle, PencilLine, Plus, Trash2 } from "lucide-react";
import { api, native, nativeAvailable } from "./api";
import { SafetyMessage } from "./safety-message";
import { Modal } from "./views";
import { ProjectRecovery } from "./project-recovery";
import { projectGroups, projectName, rememberClientProjectName } from "./workspace-state.mjs";
import { getClientContext, isRemoteClient } from "./client-context.mjs";
import { useTranslation } from "./interface-language.mjs";

export function ProjectDialog({
  status,
  initialPath = "",
  initialMode = "browse",
  activate = true,
  onClose,
  onOpened,
  onRemoved,
  onSettings,
}) {
  const tr = useTranslation();
  const groups = projectGroups(status);
  const projects = groups.map(group => group.workspace);
  const initialGroup = groups.find(group => group.folders.includes(initialPath));
  const directEdit = initialMode === "edit" && Boolean(initialGroup);
  const directRemoveTarget = initialMode === "remove" ? (initialGroup?.workspace || initialPath) : "";
  const [adding, setAdding] = useState(directEdit || Boolean(initialPath && !initialGroup && !directRemoveTarget) || projects.length === 0);
  const [path, setPath] = useState(directEdit ? "" : (initialGroup?.workspace || initialPath || groups.find(group => group.folders.includes(status.workspace))?.workspace || projects[0] || ""));
  const [editing, setEditing] = useState(directEdit ? initialGroup.workspace : "");
  const [name, setName] = useState(directEdit ? initialGroup.name : "");
  const [folders, setFolders] = useState(directEdit ? initialGroup.folders : []);
  const [error, setError] = useState("");
  const [recovery, setRecovery] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [removeTarget, setRemoveTarget] = useState(directRemoveTarget);
  const [removed, setRemoved] = useState([]);
  const [directories, setDirectories] = useState(null);
  const [browsing, setBrowsing] = useState(false);
  const remote = isRemoteClient();
  const context = getClientContext();
  const canShare = remote && nativeAvailable() && context.connection?.manifest?.capabilities?.client_project_share === true;
  const [shares, setShares] = useState([]);
  const [writeAccess, setWriteAccess] = useState(false);
  const [sharing, setSharing] = useState(false);
  useEffect(() => {
    if (!canShare) return;
    let current = true;
    api("/client/project-shares").then(({ shares }) => {
      if (current) {
        (shares || []).forEach(rememberClientProjectName);
        setShares(shares || []);
      }
    }).catch((e) => { if (current) setError(e.message); });
    return () => { current = false; };
  }, [canShare, context.connection_id]);
  const share = async () => {
    setSubmitting(true); setError("");
    try {
      const item = await native("shareLocalProject", { mode: writeAccess ? "write" : "read" });
      if (item.canceled) return;
      rememberClientProjectName(item);
      setShares((old) => [...old.filter((s) => s.id !== item.id), item]);
      setPath(item.guest_path); setAdding(false); setSharing(false);
      const result = await api("/projects/open", { workspace: item.guest_path, add: false, activate });
      await onOpened(result.workspace);
    } catch (e) { setError(e.message); }
    finally { setSubmitting(false); }
  };
  const revoke = async (item) => {
    setSubmitting(true); setError("");
    try {
      await api("/client/project-shares/" + item.id, undefined, "DELETE");
      setShares((old) => old.filter((s) => s.id !== item.id));
      if (path === item.guest_path) setPath(status.workspace || "");
    } catch (e) { setError(e.message); }
    finally { setSubmitting(false); }
  };
  const projectList = [...new Set([...projects, ...shares.map((s) => s.guest_path)])].filter(project => !removed.includes(project));
  const displayName = value => projectName(value, groups);
  const draftFolders = [...new Set([...folders, ...(path.trim() ? [path.trim()] : [])])];
  const appendFolder = value => {
    const folder = value.trim();
    if (!folder) return;
    setFolders(previous => [...new Set([...previous, folder])]);
    setName(previous => previous || projectName(folder));
    setPath(""); setError("");
  };
  const edit = (targetPath = path) => {
    const targetGroup = groups.find(group => group.workspace === targetPath);
    if (!targetGroup) return;
    setEditing(targetGroup.workspace); setName(targetGroup.name);
    setFolders(targetGroup.folders); setPath(""); setAdding(true); setError("");
  };
  const selectedShare = shares.find((item) => item.guest_path === path);
  const busy = submitting || Boolean(status.job?.running);
  const remove = async () => {
    if (busy || !removeTarget) return;
    setSubmitting(true); setError("");
    try {
      const result = await api("/projects/remove", { workspace: removeTarget });
      const target = removeTarget;
      setRemoved(previous => [...previous, target]);
      setPath(result.workspace || "");
      if (!result.workspace) setAdding(true);
      setRemoveTarget("");
      await onRemoved?.(target, result.workspace);
      if (directRemoveTarget) onClose?.();
    } catch (e) { setError(e.message); }
    finally { setSubmitting(false); }
  };
  if (directRemoveTarget) {
    return (
      <Modal
        title={tr("移除项目")}
        onClose={busy ? undefined : onClose}
        footer={
          <>
            <span className="save-note">
              {busy ? submitting ? tr("正在移除项目…") : status.job?.message : tr("会话历史保留")}
            </span>
            <button disabled={busy} onClick={onClose}>
              {tr("取消")}
            </button>
            <button
              className="primary danger"
              type="button"
              disabled={busy || !removeTarget}
              onClick={remove}
            >
              {busy ? <LoaderCircle className="spin" size={15} /> : <Trash2 size={15} />}
              {tr("确认移除项目")}
            </button>
          </>
        }
      >
        <div className="modal-body project-form">
          <p>{tr("移除“")}{displayName(directRemoveTarget)}{tr("”将解除所有绑定文件夹的共享。原文件和会话历史保留，重新添加相同目录后可继续使用历史。")}</p>
          <p className="field-help">{tr("请先保存文件，并关闭此项目的终端、运行中会话和编辑器。移除当前项目后选中剩余项目；移除最后一个项目后，可重新添加文件夹。")}</p>
          {error && (
            <div className="error-message" role="alert">
              <SafetyMessage message={error} />
            </div>
          )}
        </div>
      </Modal>
    );
  }
  const browse = async (directory = path.trim()) => {
    setBrowsing(true); setError("");
    try { setDirectories(await api(`/projects/directories?${new URLSearchParams({ path: directory })}`)); }
    catch (e) { setError(e.message); }
    finally { setBrowsing(false); }
  };
  const choose = async () => {
    if (remote) { await browse(); return; }
    setBrowsing(true);
    try {
      const selected = await native("chooseFolder");
      if (selected) {
        appendFolder(selected);
      }
    } catch {
      setError("未能选择文件夹，请直接填写路径。");
    } finally {
      setBrowsing(false);
    }
  };
  const add = () => {
    setAdding(true);
    setEditing(""); setName(""); setFolders([]);
    setPath("");
    setError("");
    if (remote) void browse("");
    else if (nativeAvailable()) void choose();
  };
  const performOpen = async () => {
    let workspace = path.trim();
    if (adding) {
      const saved = await api("/projects/save", {
        workspace: editing, name: name.trim() || projectName(draftFolders[0]), folders: draftFolders,
      });
      workspace = saved.workspace;
      setEditing(saved.workspace);
    }
    const result = await api("/projects/open", { workspace, add: false, activate });
    await onOpened(result.workspace);
  };
  const open = async (event) => {
    event?.preventDefault();
    if (busy || sharing || removeTarget || !status.ready || !(adding ? draftFolders.length : path.trim())) return;
    setSubmitting(true);
    setError("");
    setRecovery("");
    try {
      await performOpen();
    } catch (e) {
      setError(e.message);
      setRecovery(e.recovery || "");
    } finally {
      setSubmitting(false);
    }
  };
  return (
    <Modal
      title={adding ? editing ? tr("编辑项目") : tr("创建项目") : activate ? tr("打开项目") : tr("选择工作项目")}
      onClose={busy ? undefined : onClose}
      footer={
        <>
          <span className="save-note">
            {busy
              ? submitting ? removeTarget ? tr("正在移除项目…") : tr("正在连接项目…") : status.job?.message
              : adding ? editing ? tr("会话历史保留") : tr("首个文件夹为默认目录") : tr("使用已授权的 Cage 项目")}
          </span>
          <button disabled={busy} onClick={onClose}>
            {tr("取消")}
          </button>
          <button
            className="primary"
            form="open-project"
            type="submit"
            disabled={!(adding ? draftFolders.length : path.trim()) || busy || sharing || Boolean(removeTarget) || !status.ready}
          >
            {busy ? (
              <LoaderCircle className="spin" size={15} />
            ) : (
              <FolderOpen size={15} />
            )}
            {adding ? editing ? tr("保存项目") : tr("创建项目") : activate ? tr("打开项目") : tr("使用此项目")}
          </button>
        </>
      }
    >
      <form
        id="open-project"
        className="modal-body project-form"
        onSubmit={open}
      >
        <p>{adding ? tr("为项目命名，并添加需要一起使用的文件夹。") : activate ? tr("选择要打开的项目，或添加新项目。") : tr("从 Cage 项目列表选择工作目录。新项目由 Cage 授权并共享。")}</p>
        {canShare && <>
          <button type="button" className="quiet-button project-picker-add" onClick={() => { setSharing(!sharing); setWriteAccess(false); }} disabled={busy} aria-expanded={sharing}>
            <FolderOpen size={16} />{tr("共享本机项目…")}
          </button>
          {sharing && <fieldset className="project-picker-list project-permissions" disabled={busy}>
            <legend>{tr("共享给")}{context.name || tr("当前云端 Cage")}</legend>
            <p className="field-help">{tr("云端按需读取项目。目录外访问和链接受限，断开连接后共享结束。")}</p>
            <label><input type="checkbox" checked={writeAccess} onChange={(e) => setWriteAccess(e.target.checked)} />{tr("允许云端修改和删除本机项目文件")}</label>
            <button type="button" onClick={share}>{tr("选择本机文件夹…")}</button>
          </fieldset>}
        </>}
        {!sharing && !adding && <>
          <fieldset className="project-picker-list" disabled={busy || Boolean(removeTarget)}>
            <legend>{tr("项目")}</legend>
            {projectList.map((project) => {
              const rowShare = shares.find((s) => s.guest_path === project);
              const rowGroup = groups.find(g => g.workspace === project);
              const label = displayName(project);
              return <div className={`project-picker-row ${path === project ? "selected" : ""}`} key={project}>
                <label className="project-picker-item">
                  <input type="radio" name="project" value={project} checked={path === project} onChange={() => { setPath(project); setError(""); }} />
                  <FolderOpen size={18} aria-hidden="true" />
                  <span>
                    <strong>{label}</strong>
                    <small>{rowShare?.path || rowGroup?.folders.join(" · ") || project}</small>
                    {status.project_errors?.some((item) => (rowGroup?.folders || [project]).includes(item.path)) && <small>{tr("需要重新连接，选择后重试")}</small>}
                  </span>
                </label>
                {!rowShare && !removeTarget && <div className="project-picker-actions">
                  {rowGroup && <button type="button" className="icon-button" aria-label={tr("编辑 {0}", label)} title={tr("编辑名称与文件夹…")} disabled={busy || !status.ready} onClick={() => edit(project)}><PencilLine size={16} aria-hidden="true" /></button>}
                  {activate && <button type="button" className="icon-button project-picker-remove" aria-label={tr("移除 {0}", label)} title={tr("从 Cage 移除此项目")} disabled={busy} onClick={() => { setPath(project); setRemoveTarget(project); setError(""); setRecovery(""); }}><Trash2 size={15} aria-hidden="true" /></button>}
                </div>}
              </div>;
            })}
          </fieldset>
          {selectedShare && <button type="button" disabled={submitting} onClick={() => revoke(selectedShare)}>{tr("停止共享本机项目（")}{selectedShare.readonly ? tr("只读") : tr("读写")}）</button>}
          {removeTarget && <section className="project-remove-confirm" aria-label={tr("确认移除项目")}>
            <p>{tr("移除“")}{displayName(removeTarget)}{tr("”将解除所有绑定文件夹的共享。原文件和会话历史保留，重新添加相同目录后可继续使用历史。")}</p>
            <p className="field-help">{tr("请先保存文件，并关闭此项目的终端、运行中会话和编辑器。移除当前项目后选中剩余项目；移除最后一个项目后，可重新添加文件夹。")}</p>
            <div className="project-remove-actions">
              <button type="button" disabled={busy} onClick={() => { setRemoveTarget(""); setError(""); }}>{tr("取消移除")}</button>
              <button type="button" className="primary danger" disabled={busy} onClick={remove}>{tr("确认移除项目")}</button>
            </div>
          </section>}
          <button type="button" className="quiet-button project-picker-add" onClick={add} disabled={busy || Boolean(removeTarget) || !status.ready}><Plus size={16} />{remote ? tr("添加云端项目…") : tr("添加本机项目…")}</button>
        </>}
        {!sharing && adding && <>
          {projects.length > 0 && !directEdit && <button type="button" className="quiet-button project-picker-add" disabled={busy} onClick={() => { setAdding(false); setEditing(""); setFolders([]); setPath(groups.find(group => group.folders.includes(status.workspace))?.workspace || projects[0]); setError(""); }}>{tr("返回已有项目")}</button>}
          <label htmlFor="project-name">{tr("项目名称")}</label>
          <input id="project-name" autoFocus value={name} maxLength={100} onChange={event => setName(event.target.value)} placeholder={tr("例如：课程系统")} disabled={busy} />
          <label>{tr("源文件夹")}</label>
          {folders.length > 0 && <ul className="project-folder-list" aria-label={tr("已绑定文件夹")}>
            {folders.map((folder, index) => <li key={folder}>
              <FolderOpen size={16} aria-hidden="true" /><span><strong>{projectName(folder)}</strong><small>{folder}</small></span>
              {index === 0 && <small className="project-folder-default">{tr("默认")}</small>}
              <button type="button" disabled={busy || folder === editing} aria-label={tr("移除文件夹 {0}", (projectName(folder)))} title={folder === editing ? tr("默认文件夹用于关联会话历史，需保留") : tr("解除文件夹绑定")} onClick={() => setFolders(previous => previous.filter(value => value !== folder))}>{tr("移除")}</button>
            </li>)}
          </ul>}
          <label htmlFor="project-path">{tr("添加文件夹")}</label>
          <div className="folder-input">
          <input
            id="project-path"
            value={path}
            onChange={(event) => {
              setPath(event.target.value);
              setError("");
            }}
            placeholder={remote ? "/home/cage/Projects/my-project" : isWindows ? tr("C:/Users/你的名字/Projects/my-project") : isMac ? tr("/Users/你的名字/Projects/my-project") : tr("/home/你的名字/Projects/my-project")}
            disabled={busy}
            spellCheck="false"
            autoComplete="off"
          />
          {path.trim() && <button type="button" disabled={busy} onClick={() => appendFolder(path)}>{tr("添加")}</button>}
          {(remote || nativeAvailable()) && (
            <button type="button" onClick={choose} disabled={busy || browsing}>
              {browsing ? tr("正在读取…") : remote ? tr("浏览云端文件夹…") : tr("选择文件夹…")}
            </button>
          )}
          </div>
          {remote && directories && <section className="remote-directory-picker" aria-label={tr("云端文件夹")}>
            <div className="remote-directory-heading"><strong>{directories.path}</strong><button type="button" disabled={browsing} onClick={() => setDirectories(null)}>{tr("收起")}</button></div>
            <div className="remote-directory-list">
              {directories.parent && <button type="button" disabled={browsing} onClick={() => browse(directories.parent)}>{tr("返回上级文件夹")}</button>}
              {directories.directories?.map((directory) => <button type="button" key={directory.path} disabled={browsing} onClick={() => browse(directory.path)}><FolderOpen size={16} />{directory.name}</button>)}
              {directories.directories?.length === 0 && <p className="field-help">{tr("这个目录下没有可浏览的文件夹。")}</p>}
            </div>
            <button type="button" disabled={browsing || !directories.selectable} onClick={() => { appendFolder(directories.path); setDirectories(null); }}>{tr("选择此云端文件夹")}</button>
          </section>}
        </>}
        {adding && <p className="field-help">{tr("Claude 从默认文件夹启动，并可读写其余绑定文件夹。修改绑定后，新建或重新打开会话生效。")}{editing && tr("默认文件夹用于关联会话历史，需保留。解绑前请关闭项目的终端、运行中会话和编辑器。")}</p>}
        <p className="field-help project-picker-note">
          <span>{remote ? tr("云端项目保存在服务器上。") : tr("绑定文件夹中的改动会直接写入本机。")}</span>
          <span>{tr("所有项目共用 Cage 环境，已有终端和网页会保持运行。")}</span>
        </p>
        {!status.ready && <p className="field-help">{tr("环境就绪后可添加或打开项目。已有项目仍可从列表移除。")}</p>}
        {error && (
          <div className="error-message" role="alert">
            <SafetyMessage message={error} />
            {recovery && <ProjectRecovery recovery={recovery} disabled={busy} onRetry={() => void open()}
              onSettings={onSettings} />}
          </div>
        )}
      </form>
    </Modal>
  );
}
