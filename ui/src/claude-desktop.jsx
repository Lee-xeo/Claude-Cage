import React, { useEffect, useRef, useState } from "react";
import {
  Download,
  Monitor,
  PanelLeftOpen,
  RefreshCw,
} from "lucide-react";
import { BrowserUploads } from "./browser-uploads";
import { ProjectDialog } from "./project-dialog";
import { api, nativeAvailable } from "./api";
import { SafetyMessage } from "./safety-message";
import { RemoteView } from "./views";
import { useWindowVisibility } from "./window-visibility";
import "./claude-desktop.css";
import { useTranslation } from "./interface-language.mjs";

export function ClaudeDesktop({
  uploads = [],
  status,
  onProjectOpened,
  visible,
  remoteRef,
  onStatus,
  onFallback,
  onDetails,
  onExpand,
  sidebarCollapsed,
  refreshSignal = 0,
  autoStart = true,
  onRunningChange,
}) {
  const tr = useTranslation();
  const [closedChooser, setClosedChooser] = useState("");
  const defaultUploads = uploads.filter((item) => !item.desktop || item.desktop === "default");
  const directoryRequest = defaultUploads.find((item) => item.directory && item.id !== closedChooser);
  const windowVisible = useWindowVisibility();
  const screenVisible = visible && windowVisible;
  const [desktop, setDesktop] = useState(null);
  const [error, setError] = useState("");
  const [statusError, setStatusError] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [jobID, setJobID] = useState("");
  const opened = useRef(false);
  const statusRequest = useRef(null);
  const statusRevision = useRef(0);
  const lastRunning = useRef(null);
  const ownJob = status.job?.kind?.startsWith("claude-desktop-");
  const busy = submitting || Boolean(status.job?.running) || Boolean(jobID);
  const refresh = async (valid = () => true) => {
    const revision = statusRevision.current;
    const request = statusRequest.current ??= api("/claude-desktop");
    try {
      const value = await request;
      if (!valid() || revision !== statusRevision.current) return null;
      setDesktop(value);
      setStatusError("");
      return value;
    } catch (e) {
      if (valid() && revision === statusRevision.current) setStatusError(e.message);
      return null;
    } finally {
      if (statusRequest.current === request) statusRequest.current = null;
    }
  };
  const run = async (action) => {
    statusRevision.current++;
    statusRequest.current = null;
    setSubmitting(true);
    setError("");
    try {
      const job = await api(`/claude-desktop/${action}`, {});
      setJobID(job.id);
    } catch (e) {
      setError(e.message);
    } finally {
      setSubmitting(false);
    }
  };
  useEffect(() => {
    if (!desktop) return;
    const running = Boolean(desktop.running);
    if (lastRunning.current !== running) {
      lastRunning.current = running;
      onRunningChange?.(running);
    }
  }, [desktop, onRunningChange]);
  useEffect(() => {
    if (!refreshSignal || !status.vm_running) return;
    void refresh();
  }, [refreshSignal, status.vm_running]);
  useEffect(() => {
    if (!screenVisible || !status.vm_running || status.job?.running) return;
    let disposed = false;
    let timer;
    const poll = async () => {
      const value = await refresh(() => !disposed);
      if (disposed) return;
      if (value && !opened.current) {
        opened.current = true;
        if (autoStart && value.installed && !value.running && !value.failed && !jobID)
          await run("start");
      }
      if (!disposed) timer = setTimeout(poll, 5000);
    };
    poll();
    return () => {
      disposed = true;
      clearTimeout(timer);
    };
  }, [screenVisible, status.vm_running, status.job?.running]);
  useEffect(() => {
    if (!jobID || status.job?.id !== jobID || status.job.running) return;
    setJobID("");
    if (status.job.error) {
      setError(status.job.error);
      return;
    }
    if (["claude-desktop-install", "claude-desktop-update"].includes(status.job.kind)) run("start");
    else refresh();
  }, [status.job]);
  return (
    <section className="claude-desktop-view" aria-label="Claude Desktop">
      {sidebarCollapsed && (
        <div className="desktop-sidebar-reveal">
          <button
            className="icon-button"
            aria-label={tr("展开侧栏")}
            title={tr("展开侧栏")}
            onClick={onExpand}
          >
            <PanelLeftOpen size={15} />
          </button>
        </div>
      )}
      {!nativeAvailable() && <BrowserUploads requests={defaultUploads} onStatus={onStatus} />}
      {directoryRequest && <ProjectDialog
        key={directoryRequest.id}
        status={status}
        activate={false}
        onClose={() => {
          setClosedChooser(directoryRequest.id);
          void api("/input/files/cancel", { target: "desktop", id: directoryRequest.id }).catch((e) => onStatus(e.message));
        }}
        onOpened={async (workspace) => {
          await api("/input/files/directory", { target: "desktop", id: directoryRequest.id, workspace });
          setClosedChooser(directoryRequest.id);
          await onProjectOpened(workspace);
        }}
      />}
      {(error || statusError || desktop?.failed) && (
        <div className="connection-banner" role="alert">
          <SafetyMessage message={error || statusError || (desktop?.failure_reason === "oom-kill"
            ? "环境内存不足，Claude Desktop 已被系统关闭。请在设置中检查计算资源后重新打开。"
            : "Claude Desktop 未能启动，可重试打开或修复安装。")} />
          <button onClick={onDetails}>{tr("操作详情")}</button>
        </div>
      )}
      {desktop?.running && desktop.managed === false && !desktop.native_user_session && <div className="connection-banner" role="status">{tr("当前应用尚未接入 Cage 账户解锁。请先保存并关闭应用，再重新打开。")}</div>}
      {busy && ownJob && (
        <div className="desktop-progress" role="status">
          <RefreshCw size={16} className="spin" />
          {status.job.message}
          <button className="text-button" onClick={onDetails}>
            {tr("查看进度")}
          </button>
        </div>
      )}
      <div className="desktop-screen">
        {desktop?.running && status.vm_running ? (
          <RemoteView
            ref={remoteRef}
            desktop
            visible={visible}
            keyboard={status.keyboard}
            onStatus={onStatus}
            onFallback={onFallback}
          />
        ) : (
          <div className="desktop-empty">
            <Monitor size={36} />
            <h2>
              {desktop?.installed
                ? "Claude Desktop"
                : tr("在 Cage 中使用 Claude Desktop")}
            </h2>
            <p>
              {tr("Chat 和 Code 共用一个官方应用。在 Cage 内安装、登录并使用，项目文件来自已打开的文件夹。")}
            </p>
            {!desktop?.installed && (
              <p>
                {tr("首次使用时从 Anthropic 官方源下载，不增加 Cage 默认安装包体积。")}
              </p>
            )}
            <div className="desktop-actions">
              {desktop && (
                <button
                  className="primary"
                  disabled={busy || !status.vm_running}
                  onClick={() => run(desktop.installed ? "start" : "install")}
                >
                  {desktop.installed ? (
                    <Monitor size={16} />
                  ) : (
                    <Download size={16} />
                  )}
                  {desktop.installed ? tr("打开 Claude Desktop") : tr("下载并安装")}
                </button>
              )}
              {desktop?.installed && (
                <button
                  className="quiet-button"
                  disabled={busy}
                  onClick={() => run("install")}
                >
                  {tr("更新 / 修复安装")}
                </button>
              )}
              {!desktop && (
                <button
                  className="quiet-button"
                  disabled={busy}
                  onClick={refresh}
                >
                  {tr("重新读取状态")}
                </button>
              )}
            </div>
            {desktop?.installed && <p>{tr("Linux 版通过系统软件源更新。可在“设置 → 环境与恢复”中更新，或在 Cage 终端运行 sudo apt update && sudo apt install --only-upgrade claude-desktop。")}</p>}
            <p className="desktop-note">
              {tr("关闭应用后释放图形会话资源，登录状态保留在 Cage 环境中。需要额外虚拟机的 Cowork 功能暂未适配。")}
            </p>
          </div>
        )}
      </div>
    </section>
  );
}
