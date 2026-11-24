import React, { useCallback, useEffect, useRef, useState } from "react";
import { ArrowDownToLine, LoaderCircle, RefreshCw } from "lucide-react";
import { api, native, nativeAvailable } from "./api";
import { getClientContext } from "./client-context.mjs";
import { Modal } from "./modal";
import { stopAndExit } from "./quit";
import { activeInterfaceLanguage, useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";

const activePhases = new Set(["checking", "downloading", "downloaded", "verifying", "installing"]);
const checkInterval = 6 * 60 * 60 * 1000;
const retryInterval = 5 * 60 * 1000;

export function useAppUpdates() {
  const [state, setState] = useState({ phase: "idle", message: "" });
  const [startupFailure, setStartupFailure] = useState("");
  const [installationPrompt, setInstallationPrompt] = useState(false);
  const [retryClock, setRetryClock] = useState(Date.now);
  const retryAt = Date.parse(state.retryAt || "");
  const coolingDown = state.phase === "failed" && retryAt > Date.now();
  const current = useRef(state);
  current.current = state;
  const requestPending = useRef(false);
  const lastCheck = useRef(0);
  const available = nativeAvailable();
  const run = useCallback(async (operation) => {
    if (!available || requestPending.current || activePhases.has(current.current.phase)) return;
    if (operation === "install" && current.current.phase === "ready") {
      setInstallationPrompt(true);
      return;
    }
    if (["check", "install"].includes(operation) && Date.parse(current.current.retryAt || "") > Date.now()) return;
    requestPending.current = true;
    if (operation === "check") lastCheck.current = Date.now();
    if (operation === "install") setState(previous => ({ ...previous, phase: "checking" }));
    try {
      const next = await native("appUpdate", { operation });
      if (next?.phase) setState(next);
      return next;
    } catch (error) {
      const next = { ...current.current, phase: "failed", message: error.message, retryable: true };
      setState(next);
      return next;
    } finally { requestPending.current = false; }
  }, [available]);
  useEffect(() => {
    if (state.phase === "ready") setInstallationPrompt(true);
  }, [state.phase]);

  useEffect(() => {
    if (!coolingDown) return;
    const timer = setTimeout(() => setRetryClock(Date.now()), Math.min(60000, retryAt - Date.now()));
    return () => clearTimeout(timer);
  }, [coolingDown, retryAt, retryClock]);

  useEffect(() => {
    if (!available) return;
    let live = true;
    const receive = event => {
      if (event.detail?.phase) setState(event.detail);
    };
    const check = () => {
      if (document.visibilityState === "hidden" || window.cageWindowVisible === false) return;
      const phase = current.current.phase;
      const interval = ["failed", "waiting"].includes(phase) ? retryInterval : checkInterval;
      const deadline = phase === "failed" && Number.isFinite(Date.parse(current.current.retryAt || ""))
        ? Date.parse(current.current.retryAt) : lastCheck.current + interval;
      if (["idle", "current", "failed", "waiting", "available", "manual"].includes(phase) && Date.now() >= deadline) void run("check");
    };
    window.addEventListener("cage-app-update", receive);
    window.addEventListener("focus", check);
    window.addEventListener("cage-window-visibility", check);
    document.addEventListener("visibilitychange", check);
    (async () => {
      try {
        const next = await native("appUpdate", { operation: "status" });
        if (!live) return;
        if (next?.phase === "failed") setStartupFailure(next.message || "上次应用更新未完成");
        if (next?.phase) { current.current = next; setState(next); }
        if (!next || ["idle", "current", "failed", "waiting", "manual"].includes(next.phase)) void run("check");
      } catch (error) {
        if (live) setState({ phase: "failed", message: error.message, retryable: true });
      }
    })();
    const timer = setInterval(check, 60 * 1000);
    return () => {
      live = false;
      clearInterval(timer);
      window.removeEventListener("cage-app-update", receive);
      window.removeEventListener("focus", check);
      window.removeEventListener("cage-window-visibility", check);
      document.removeEventListener("visibilitychange", check);
    };
  }, [available, run]);
  useEffect(() => {
    if (!startupFailure) return;
    const timer = setTimeout(() => setStartupFailure(""), 20000);
    return () => clearTimeout(timer);
  }, [startupFailure]);
  return { state, run, available, startupFailure, coolingDown, installationPrompt, setInstallationPrompt };
}

export function AppUpdateDialog({ updates, onStatus }) {
  const tr = useTranslation();
  const { state, installationPrompt, setInstallationPrompt, run } = updates;
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState("");
  const attempt = useRef(null);
  useEffect(() => () => { attempt.current = null; }, []);
  const close = () => {
    attempt.current = null;
    setStopping(false);
    setError("");
    setInstallationPrompt(false);
  };
  const install = async () => {
    if (attempt.current) return;
    const request = { connection: getClientContext().connection_id };
    attempt.current = request;
    setStopping(true);
    setError("");
    try {
      // Only the local workbench can stop its own environment. A selected
      // remote host must never receive the desktop application's stop request.
      if (getClientContext().mode !== "local") throw new Error("请先切回本机工作台，再点击更新按钮继续安装。云端任务会继续运行。");
      await stopAndExit({
        stop: () => api("/stop", {}),
        readStatus: () => api("/status"),
        exit: async () => {
          const next = await run("complete");
          if (next?.phase !== "installing") throw new Error(next?.message || "安装尚未开始，请重试。");
        },
        isCancelled: () => attempt.current !== request || getClientContext().connection_id !== request.connection,
        onStatus,
        tr,
      });
      if (attempt.current === request) close();
    } catch (failure) {
      if (attempt.current === request) {
        attempt.current = null;
        setStopping(false);
        setError(failure.message);
      }
    }
  };
  if (!installationPrompt) return null;
  return <Modal title={tr("停止环境并安装更新？")} onClose={close} footer={<>
    <button onClick={close}>{stopping ? tr("稍后安装") : tr("取消")}</button>
    <button className="primary" disabled={stopping} onClick={install}>
      {stopping ? tr("正在停止环境…") : error ? tr("重试停止并安装") : tr("保存并停止环境后安装")}
    </button>
  </>}>
    <div className="modal-body">
      <p>{tr("更新已下载并验证。请先保存文件，点击下方按钮停止环境后安装并重启 Cage。当前终端任务会结束，项目、账户和登录状态会保留。")}</p>
      {state.officialUpgrade && <p>{tr("当前运行的是本地未签名构建。继续安装将切换为官方签名发行版，本地源码改动不会保留；配置、账户、项目和虚拟机磁盘会保留，后续将持续接收官方自动更新。")}</p>}
      <p>{tr("取消可稍后安装，已下载的更新会保留。停止开始后，取消只推迟安装，环境仍会停止。")}</p>
      {stopping && <p role="status"><LoaderCircle size={16} className="spin" />{" " + tr("正在等待环境停止，完成后继续安装…")}</p>}
      {error && <p className="error-message" role="alert">{localizeBackendMessage(error, tr)}</p>}
    </div>
  </Modal>;
}

export function AppUpdateButton({ updates }) {
  const tr = useTranslation();
  const { state, run, available, startupFailure, coolingDown } = updates;
  const busy = activePhases.has(state.phase);
  const failed = state.phase === "failed";
  // A platform that installs packages itself reports "manual" with the newer
  // version; the button then opens the release page instead of installing.
  const manual = state.phase === "manual";
  const visible = state.latestVersion && ["available", "ready", "failed", "manual", ...activePhases].includes(state.phase);
  const announcedFailure = useRef("");
  const [failure, setFailure] = useState("");
  useEffect(() => {
    if (failed && visible && state.message && announcedFailure.current !== state.message) {
      announcedFailure.current = state.message;
      setFailure(state.message);
    }
    if (!failed) { announcedFailure.current = ""; setFailure(""); }
  }, [failed, visible, state.message]);
  useEffect(() => {
    if (!failure) return;
    const timer = setTimeout(() => setFailure(""), 8000);
    return () => clearTimeout(timer);
  }, [failure]);
  if (!available || (!visible && !startupFailure)) return null;
  const downloading = state.phase === "downloading";
  const percent = state.totalBytes > 0
    ? Math.max(0, Math.min(100, Math.floor((state.receivedBytes || 0) / state.totalBytes * 100)))
    : null;
  const label = downloading ? percent === null ? tr("正在下载更新") : tr("正在下载更新 {0}%", percent)
    : state.phase === "verifying" || state.phase === "downloaded" ? tr("正在验证更新")
    : state.phase === "installing" ? tr("正在安装更新，完成后自动重启")
    : state.phase === "checking" ? tr("正在准备更新")
    : coolingDown ? tr("等待重试更新") : failed ? tr("重试更新") : manual ? tr("查看新版本") : state.phase === "ready" ? tr("安装已下载的更新") : tr("下载更新");
  const hint = coolingDown ? tr("{0}，可于 {1} 后重试", state.message ? localizeBackendMessage(state.message, tr) : tr("更新源请求受限"), new Date(state.retryAt).toLocaleString(activeInterfaceLanguage()))
    : failed ? tr("{0}，点击重试", state.message ? localizeBackendMessage(state.message, tr) : tr("更新未完成"))
    : busy ? label
    : manual ? tr("新版本 {0} 已发布。{1}点击打开发布页面。", state.latestVersion, state.message ? localizeBackendMessage(state.message, tr) + " " : "")
    : state.officialUpgrade ? tr("更新至 {0}。当前为本地未签名构建，更新后将切换为官方签名发行版（配置、账户、项目和虚拟机磁盘会保留，本地源码改动不会保留）。", state.latestVersion)
    : tr("更新至 {0}。先下载并验证，安装前会提示你保存工作并手动停止环境。", state.latestVersion);
  const circumference = 2 * Math.PI * 15;
  return (
    <>
      {visible && <button
        className={`app-update-button${busy ? " busy" : ""}${failed ? " failed" : ""}`}
        aria-label={label}
        title={hint}
        disabled={busy || coolingDown}
        onClick={() => {
          setFailure("");
          if (manual) void native("appReference", { kind: "releases" }).catch(error => setFailure(error.message));
          else void run("install");
        }}
      >
        {busy ? (
          <>
            <svg className={`app-update-ring${downloading && percent !== null ? "" : " indeterminate"}`}
              viewBox="0 0 36 36" role="progressbar" aria-label={downloading ? tr("更新下载进度") : label}
              aria-valuemin={0} aria-valuemax={100} aria-valuenow={downloading && percent !== null ? percent : undefined}>
              <circle className="app-update-ring-track" cx="18" cy="18" r="15" />
              <circle className="app-update-ring-value" cx="18" cy="18" r="15"
                strokeDasharray={circumference} strokeDashoffset={circumference * (1 - (downloading && percent !== null ? percent / 100 : 0.25))} />
            </svg>
            {downloading && percent !== null && <span className="app-update-percent">{percent}%</span>}
          </>
        ) : failed ? <RefreshCw size={20} /> : <ArrowDownToLine size={20} />}
      </button>}
      {(failure || startupFailure) && <div className="app-update-error" role="alert">{localizeBackendMessage(failure || startupFailure, tr)}{visible ? coolingDown ? tr("。请等待更新源恢复后重试。") : tr("。可点击更新按钮重试。") : ""}</div>}
    </>
  );
}
