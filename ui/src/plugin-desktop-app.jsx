import React, { useEffect, useRef, useState } from "react";
import { Download, Monitor, Power, RefreshCw, Trash2 } from "lucide-react";
import { api, nativeAvailable } from "./api";
import { DesktopPane } from "./account-desktops";
import { BrowserUploads } from "./browser-uploads";
import { ProjectDialog } from "./project-dialog";
import { SafetyMessage } from "./safety-message";
import { useWindowVisibility } from "./window-visibility";
import { useTranslation } from "./interface-language.mjs";
import "./claude-desktop.css";

export const desktopAppPath = (plugin, app) =>
  `/desktop-apps/${encodeURIComponent(plugin)}/${encodeURIComponent(app)}`;

// PluginDesktopApp is a vendor's application that a plugin runs in the
// environment: installed from the vendor's source at its latest release,
// updated from here, and shown in a display of its own.
export function PluginDesktopApp({ plugin, app, status, uploads = [], visible = true, keyboard, systemClipboard = true,
  onStatus, onFallback, onConfirm, onDetails, onProjectOpened }) {
  const tr = useTranslation();
  const path = desktopAppPath(plugin, app.id);
  const windowVisible = useWindowVisibility();
  const [state, setState] = useState(null);
  const [error, setError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [jobID, setJobID] = useState("");
  const [closedChooser, setClosedChooser] = useState("");
  const opened = useRef(false);
  const busy = submitting || Boolean(status?.job?.running) || Boolean(jobID);
  const ownJob = status?.job?.kind?.startsWith("desktop-app-");
  const refresh = async (valid = () => true) => {
    try {
      const value = await api(path + "/status");
      if (valid()) { setState(value); setError(""); }
      return value;
    } catch (e) {
      if (valid()) setError(e.message);
      return null;
    }
  };
  const run = async (action) => {
    setSubmitting(true);
    setError("");
    try {
      const job = await api(`${path}/${action}`, {});
      setJobID(job.id);
    } catch (e) {
      setError(e.message);
    } finally {
      setSubmitting(false);
    }
  };
  useEffect(() => {
    opened.current = false;
    setState(null);
  }, [path]);
  useEffect(() => {
    if (!visible || !windowVisible || !status?.vm_running || status?.job?.running) return;
    let disposed = false, timer;
    const poll = async () => {
      const value = await refresh(() => !disposed);
      if (disposed) return;
      if (value && !opened.current) {
        opened.current = true;
        if (value.installed && !value.running && !value.failed && !jobID) await run("start");
      }
      if (!disposed) timer = setTimeout(poll, 5000);
    };
    poll();
    return () => { disposed = true; clearTimeout(timer); };
  }, [visible, windowVisible, status?.vm_running, status?.job?.running, path]);
  useEffect(() => {
    if (!jobID || status?.job?.id !== jobID || status.job.running) return;
    setJobID("");
    if (status.job.error) {
      setError(status.job.error);
      return;
    }
    if (["desktop-app-install", "desktop-app-update"].includes(status.job.kind)) run("start");
    else refresh();
  }, [status?.job]);
  const appUploads = uploads.filter((item) => state?.id && item.desktop === state.id);
  const directoryRequest = appUploads.find((item) => item.directory && item.id !== closedChooser);
  const update = async () => {
    if (state?.running && !await onConfirm({ title: tr("更新 {0}？", app.title), body: tr("请先保存应用内的工作。更新会关闭应用，安装官方最新版本后重新打开；登录与设置继续保留。"), confirm: tr("更新应用") })) return;
    run("update");
  };
  const remove = async () => {
    if (!await onConfirm({ title: tr("卸载 {0}？", app.title), body: tr("卸载会删除应用文件。登录、设置和项目留在环境中，重新安装后继续使用。"), confirm: tr("卸载应用") })) return;
    run("remove");
  };
  return (
    <section className="claude-desktop-view plugin-desktop-app" aria-label={app.title}>
      {!nativeAvailable() && <BrowserUploads requests={appUploads} onStatus={onStatus} />}
      {directoryRequest && status && <ProjectDialog
        key={directoryRequest.id}
        status={status}
        activate={false}
        onClose={() => {
          setClosedChooser(directoryRequest.id);
          void api("/input/files/cancel", { target: "desktop", id: directoryRequest.id, desktop: directoryRequest.desktop }).catch((e) => onStatus?.(e.message));
        }}
        onOpened={async (workspace) => {
          await api("/input/files/directory", { target: "desktop", id: directoryRequest.id, desktop: directoryRequest.desktop, workspace });
          setClosedChooser(directoryRequest.id);
          await onProjectOpened?.(workspace);
        }}
      />}
      <header className="claude-desktop-toolbar">
        <Monitor size={18} />
        <strong>{app.title}</strong>
        <span className="desktop-version">{state?.version ? `v${state.version}` : tr("官方最新版")}</span>
        <span className="desktop-toolbar-spacer" />
        {state?.installed && <button className="quiet-button" disabled={busy} onClick={update}>{tr("更新应用")}</button>}
        {state?.installed && !state.running && <button className="quiet-button" disabled={busy} onClick={remove}><Trash2 size={15} />{tr("卸载")}</button>}
        {state?.running && <button className="quiet-button" disabled={busy} onClick={async () => {
          if (await onConfirm({ title: tr("关闭 {0}？", app.title), body: tr("请先保存应用内的工作。浏览器和终端会继续运行。"), confirm: tr("关闭应用") })) run("stop");
        }}><Power size={15} />{tr("关闭应用")}</button>}
        <button className="icon-button" aria-label={tr("刷新") + " " + app.title + " " + tr("状态")} disabled={busy} onClick={() => refresh()}>
          <RefreshCw size={15} />
        </button>
      </header>
      {(error || state?.failed) && (
        <div className="connection-banner" role="alert">
          <SafetyMessage message={error || (state?.failure_reason === "oom-kill"
            ? tr("环境内存不足，{0} 已被系统关闭。请在设置中检查计算资源后重新打开。", app.title)
            : tr("{0} 未能启动。可重新打开，或点“更新应用”安装官方最新版本；仍失败时可能需要升级插件。", app.title))} />
          {onDetails && <button onClick={onDetails}>{tr("操作详情")}</button>}
        </div>
      )}
      {busy && ownJob && status?.job?.message && (
        <div className="desktop-progress" role="status">
          <RefreshCw size={16} className="spin" />
          {status.job.message}
          {onDetails && <button className="text-button" onClick={onDetails}>{tr("查看进度")}</button>}
        </div>
      )}
      <div className="desktop-screen">
        {state?.running && status?.vm_running ? (
          <div className="account-desktop-panes">
            <DesktopPane
              account={{ id: state.id, label: app.title }}
              path={path}
              heading={app.title}
              inputLabel={tr("{0} 输入", app.title)}
              closeLabel="关闭应用"
              visible={visible}
              windowVisible={windowVisible}
              focused
              onFocus={() => {}}
              keyboard={keyboard}
              systemClipboard={systemClipboard}
              onStatus={onStatus}
              onFallback={onFallback}
              onControl={() => {}}
              busy={busy}
              close={() => run("stop")}
            />
          </div>
        ) : (
          <div className="desktop-empty">
            <Monitor size={36} />
            <h2>{app.title}</h2>
            <p>{tr("在环境中运行官方应用。应用从官方渠道安装，默认使用最新版本，登录与数据保留在环境里。")}</p>
            <div className="desktop-actions">
              {!state?.installed ? (
                <button className="primary" disabled={busy || !status?.vm_running} onClick={() => run("install")}>
                  <Download size={16} />{tr("下载并安装")}
                </button>
              ) : (
                <button className="primary" disabled={busy || !status?.vm_running} onClick={() => run("start")}>
                  <Monitor size={16} />{tr("打开应用")}
                </button>
              )}
            </div>
          </div>
        )}
      </div>
    </section>
  );
}
