import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createRoot } from "react-dom/client";
import { Archive, ArrowLeft, ArrowUp, LoaderCircle, Monitor, RefreshCw, Settings, Square, Terminal, X } from "lucide-react";
import { RemoteDisplay } from "./remote-browser";
import { CodeNavigation, ProjectTree, WorkspaceDrawer, workspaceTools } from "./workspace-drawer";
import { deviceCommand } from "./device-navigation.mjs";
import cageLogo from "../logo.png";
import { accessDenied, request, sessionPath } from "./transport.mjs";
import { recoverPendingMessage, retryableSessionError, sessionFailureMessage, sessionRetryDelay, sessionRetryLimit } from "./session-recovery.mjs";
import { activeTurn as active, activeTurnIdForQueue, canDrainQueue, canSendImmediately, dequeueMessage, enqueueMessage, reconcileDispatchError, reconcileQueueWithSnapshot, removeQueuedMessage } from "./message-queue.mjs";
import { SessionActionDialog, sortSessions } from "./session-row";
import { ArchivedSessions } from "../cli-sessions";
import { SessionAccount } from "../session-account";
import "./mobile.css";
import { IconHints } from "../icon-hints.jsx";
import { MessageBody, ToolActivity, TranscriptContext } from "./transcript";

const labels = {
	queued: "消息已送达",
  running: "正在运行", stopping: "正在停止", completed: "已完成",
  failed: "运行失败", stopped: "已停止", interrupted: "运行中断",
};
const nativePhone = navigator.userAgent.includes("CageMobile/1");
const phoneWorkspace = nativePhone || location.pathname === "/";
const dateFormat = new Intl.DateTimeFormat("zh-CN", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit" });
function displayTime(value) {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? "" : dateFormat.format(date);
}
function route() {
  const value = history.state?.cageCLI || (new URLSearchParams(location.search).get("page") === "archived" ? {tool:"archived"} : null);
  const id = value?.displayID || value?.browser;
  const displayID = typeof id === "string" && /^[a-f0-9-]{36}$/.test(id) ? id : "";
  const tool = ["settings", "archived"].includes(value?.tool) ? value.tool : displayID && ["browser", "claude", "desktop"].includes(value.tool) ? value.tool : displayID ? "browser" : "code";
  return {tool, displayID, project: typeof value?.project === "string" ? value.project : "", session: typeof value?.session === "string" ? value.session : ""};
}
function saveRoute(next, push) {
  history[push ? "pushState" : "replaceState"]({ ...history.state, cageCLI: next }, "");
}

function Status({ value, awaitingApproval }) {
  const tone = active(value) ? "live" : value === "failed" || value === "interrupted" ? "bad" : "";
  return <span className={"status-label " + tone} role="status"><i className="status-dot" />{awaitingApproval ? "等待审批" : labels[value] || "就绪"}</span>;
}
function Notice({ children, onRetry, label = "重新读取" }) {
  return <div className="notice" role="alert">{children}
    {onRetry && <button type="button" className="action secondary" onClick={onRetry}><RefreshCw size={15} />{label}</button>}
  </div>;
}
function Empty({ icon: Icon, title, children }) {
  return <div className="center-message"><Icon size={25} strokeWidth={1.6} /><strong>{title}</strong>{children && <p className="note">{children}</p>}</div>;
}

function App() {
  const [current, setCurrent] = useState(route);
  const [addressSlot, setAddressSlot] = useState(null);
  const [drawer, setDrawer] = useState(() => phoneWorkspace && !history.state?.cageCLI && route().tool === "code");
  const drafts = useRef(new Map());
  const [expanded, setExpanded] = useState(() => new Map());
  const [catalog, setCatalog] = useState(null);
  const [error, setError] = useState("");
  const [gate, setGate] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [visible, setVisible] = useState(!document.hidden);
  const [creating, setCreating] = useState(false);
  const [sessionAction, setSessionAction] = useState(null);
  const [actionBusy, setActionBusy] = useState(false);
  const [sessionActionError, setSessionActionError] = useState("");
  const mutationBusy = useRef(false);
  const catalogGeneration = useRef(0);
  const archivedSessions = useRef(new Set());
  const [devices, setDevices] = useState(() => window.__cageDevices || { current: "", devices: [] });
  const createRequest = useRef(null);
  const openBrowserRequest = useRef(null);
  const browserControls = useRef(null);
  const createBusy = useRef(false);
  const pageGeneration = useRef(0);
  const accessGeneration = useRef(0);
  const refreshAll = useCallback(() => setRefresh((n) => n + 1), []);
  useEffect(() => {
    const update = event => setDevices(event.detail);
    window.addEventListener("cage-devices", update);
    // Native bridges may become available after the document starts loading.
    if (window.__cageDevices) setDevices(window.__cageDevices);
    return () => window.removeEventListener("cage-devices", update);
  }, []);
  useEffect(() => {
    if (catalog?.host && devices.current) deviceCommand("name", { id: devices.current, name: catalog.host });
  }, [catalog?.host, devices.current]);
  const restricted = useCallback((error) => {
    pageGeneration.current++;
    accessGeneration.current++;
    drafts.current.clear();
    setSessionAction(null);
    if (route().displayID) {
      const next = {...route(), tool: "code", displayID: ""};
      saveRoute(next, false);
      setCurrent(next);
    }
    setGate(error.message); setCatalog(null); setError("");
  }, []);
  useEffect(() => {
    const goBack = () => { openBrowserRequest.current = null; pageGeneration.current++; setCurrent(route()); setDrawer(phoneWorkspace && !history.state?.cageCLI); setError(""); };
    const visibility = () => setVisible(!document.hidden);
    const viewport = window.visualViewport;
    const resize = () => {
      if (viewport?.scale === 1) document.documentElement.style.setProperty("--mobile-height", viewport.height + "px");
    };
    resize();
    window.addEventListener("popstate", goBack);
    window.addEventListener("online", refreshAll);
    document.addEventListener("visibilitychange", visibility);
    viewport?.addEventListener("resize", resize);
    return () => {
      window.removeEventListener("popstate", goBack);
      window.removeEventListener("online", refreshAll);
      document.removeEventListener("visibilitychange", visibility);
      viewport?.removeEventListener("resize", resize);
    };
  }, [refreshAll]);
  useEffect(() => {
    if (!visible) return;
    const controller = new AbortController();
    const options = { signal: controller.signal };
    const expectedAccess = accessGeneration.current;
    let timer;
    const load = async () => {
      const expectedCatalog = catalogGeneration.current;
      try {
        const [info, list] = await Promise.all([request("/projects", options), request("/sessions", options)]);
        if (controller.signal.aborted || expectedAccess !== accessGeneration.current || expectedCatalog !== catalogGeneration.current) return;
        for (const session of list.sessions || []) archivedSessions.current.delete(session.id);
        setCatalog({ ...info, sessions: (list.sessions || []).map(session => list.notice ? {...session, active:false} : session) }); setGate(""); setError(list.notice || "");
      } catch (error) {
        if (controller.signal.aborted) return;
        if (accessDenied(error)) { restricted(error); return; }
        setError(error.message);
        setCatalog(previous => previous && ({...previous, sessions:previous.sessions.map(session => ({...session, active:false}))}));
      }
      if (!controller.signal.aborted && (drawer || current.tool !== "code" || !current.session)) timer = setTimeout(load, 5000);
    };
    void load();
    return () => { clearTimeout(timer); controller.abort(); };
  }, [refresh, visible, restricted, current.session, current.tool, drawer]);
  const open = (project, session = "", push = false) => {
    const next = {tool: "code", displayID: "", project, session};
    pageGeneration.current++;
    saveRoute(next, push); setCurrent(next); setError("");
  };
  const updateSession = useCallback((snapshot) => {
    setCatalog((old) => {
      if (!old) return old;
      const { id, workspace, title, revision, updated_at, generation, pinned, custom_title, metadata_set, active } = snapshot;
      if (archivedSessions.current.has(id)) return old;
      const previous = old.sessions.find((s) => s.id === id);
      if (previous && previous.generation === generation && previous.revision >= revision) return old;
      return { ...old, sessions: [{ id, workspace, title, revision, updated_at, generation, pinned, custom_title, metadata_set, active }, ...old.sessions.filter((s) => s.id !== id)] };
    });
  }, []);
  const projects = catalog?.projects || [];
  const project = projects.find((p) => p.path === current.project || p.folders?.includes(current.project)) || projects[0];
  const sessions = catalog?.sessions.filter((s) => (project?.folders || [project?.path]).includes(s.workspace))
    .sort(sortSessions) || [];
  const missingSession = useCallback(() => {
    const selected = route();
    drafts.current.delete(selected.session);
    const next = {...selected, session:""};
    saveRoute(next, false); setCurrent(next); setDrawer(phoneWorkspace); refreshAll();
  }, [refreshAll]);
  const mutateSession = async (kind, target, body) => {
    if (mutationBusy.current) return;
    mutationBusy.current = true; setActionBusy(true); setSessionActionError("");
    catalogGeneration.current++;
    try {
      const result = await request(sessionPath(target.id) + (kind === "archive" ? "/archive" : "/metadata"), {body});
      if (kind === "archive") {
        archivedSessions.current.add(target.id);
        drafts.current.delete(target.id);
        setCatalog(old => old && ({...old, sessions:old.sessions.filter(s => s.id !== target.id)}));
        if (route().session === target.id) missingSession();
      } else updateSession(result);
      setSessionAction(null);
    } catch (error) {
      if (accessDenied(error)) restricted(error);
      else if (kind === "pin") setError(error.message);
      else setSessionActionError(error.message);
    } finally {
      catalogGeneration.current++;
      mutationBusy.current = false; setActionBusy(false); refreshAll();
    }
  };
  const chooseSessionAction = (kind, target) => {
    if (mutationBusy.current) return;
    if (kind === "pin") void mutateSession(kind, target, {pinned:!target.pinned});
    else { setSessionActionError(""); setSessionAction({kind, session:target}); }
  };
  const start = async target => {
    if (!target || createBusy.current || !catalog.ready) return;
    createBusy.current = true; setCreating(true); setError("");
    const generation = pageGeneration.current;
    try {
      if (createRequest.current?.workspace !== target.path) createRequest.current = {
        id: crypto.randomUUID(), workspace: target.path, ...(catalog.shared_sessions ? {shared:true} : {}),
      };
      const session = await request("/sessions", { body: createRequest.current });
      createRequest.current = null;
      if (generation !== pageGeneration.current) return;
      updateSession(session);
      setExpanded(previous => new Map(previous).set(target.path, true));
      open(target.path, session.id, true); setDrawer(false);
    } catch (error) {
      if (generation !== pageGeneration.current) return;
      if (error.status && error.status < 500) createRequest.current = null;
      if (accessDenied(error)) restricted(error); else setError(error.message);
    } finally { createBusy.current = false; setCreating(false); }
  };
  const session = current.session;
  const selectTool = (tool, push = true, url = "") => {
    if (tool !== current.tool || !push) {
      const id = crypto.randomUUID();
      openBrowserRequest.current = {id, url};
      const next = {...current, tool, displayID:id};
      pageGeneration.current++;
      saveRoute(next, push); setCurrent(next);
    }
    setDrawer(false);
  };
  const leaveDisplay = () => {
    browserControls.current?.close();
    open(current.project, session);
    setDrawer(!openBrowserRequest.current?.url);
  };
  const exitWorkspace = () => {
    if (current.displayID) leaveDisplay();
    return deviceCommand("home");
  };
  const codeNavigation = <CodeNavigation catalog={catalog} project={project} sessions={sessions} current={session}
    creating={creating || actionBusy} onAction={chooseSessionAction} onProject={path => open(path)} onNew={() => start(project)} formatTime={displayTime}
    onSession={id => {open(sessions.find(session => session.id === id)?.workspace || project.path, id, true); setDrawer(false);}} />;
  const activeTool = workspaceTools.find(entry => entry.id === current.tool);
  const ActiveToolIcon = ["settings", "archived"].includes(current.tool) ? Settings : activeTool?.icon || Terminal;
  const settingsPage = tool => {
    browserControls.current?.close();
    const next = {tool, displayID:"", project:current.project, session:""};
    pageGeneration.current++;
    saveRoute(next, true); setCurrent(next); setDrawer(false);
  };
  const leaveSettings = () => {
    if (current.tool === "archived") settingsPage("settings");
    else { open(current.project); setDrawer(true); }
  };
  useEffect(() => {
    const openArchives = () => settingsPage("archived");
    window.addEventListener("cage-open-archives", openArchives);
    return () => window.removeEventListener("cage-open-archives", openArchives);
  }, [current.project]);
  return <main className={["mobile-app", phoneWorkspace && "phone-workspace", nativePhone && "native-phone"].filter(Boolean).join(" ")}>
    <header className="topbar workspace-topbar">
      {phoneWorkspace
        ? <button type="button" className="icon-button" aria-haspopup="dialog" aria-expanded={drawer} aria-label="展开侧栏"
          onClick={() => setDrawer(true)}><img className="brand-mark" src={cageLogo} alt="Cage" /></button>
        : <a className="icon-button" href="/" aria-label="返回工作台"><img className="brand-mark" src={cageLogo} alt="Cage" /></a>}
      {phoneWorkspace && (current.tool === "browser" || current.tool === "claude") && <div className="workspace-address-slot" ref={setAddressSlot} />}
      <div className="top-actions">{phoneWorkspace
        ? <span className="workspace-tool-icon" role="img" aria-label={activeTool?.name || "Claude Code"}><ActiveToolIcon size={19} aria-hidden="true" /></span>
        : <div className="connection-host"><Monitor size={16} /><span>{catalog?.host || "连接电脑"}</span></div>}</div>
    </header>
    {gate ? <Notice onRetry={refreshAll}>{gate}</Notice> : !catalog
      ? <section className="page-scroll">{error ? <Notice onRetry={refreshAll}>{error}</Notice> : <Empty icon={LoaderCircle} title="正在读取项目…" />}</section>
      : current.tool === "settings" || current.tool === "archived"
        ? <section className="page-scroll mobile-session-settings" aria-label="会话设置">
          <button className="action" onClick={leaveSettings}><ArrowLeft size={17} />{current.tool === "archived" ? "返回设置" : "返回会话"}</button>
          {current.tool === "archived" ? <ArchivedSessions request={request} ready={catalog.ready}
            onRestored={id => {archivedSessions.current.delete(id); refreshAll();}} />
            : <><h1>设置</h1><button className="action secondary" onClick={() => settingsPage("archived")}><Archive size={18} />已归档会话</button></>}
        </section>
      : current.displayID
        ? <RemoteDisplay controls={browserControls} key={current.displayID} id={current.displayID} tool={current.tool} addressSlot={addressSlot}
          start={openBrowserRequest.current?.id === current.displayID} initialURL={openBrowserRequest.current?.url || ""} visible={visible}
          onBack={leaveDisplay} onReopen={() => selectTool(current.tool, false, openBrowserRequest.current?.url)} />
      : session
        ? <Chat key={session} id={session} drafts={drafts.current} ready={catalog.ready} visible={visible} refresh={refresh}
          onOpenWeb={url => selectTool("browser", true, url)} onDenied={restricted} onMissing={missingSession} onSnapshot={updateSession} onBack={phoneWorkspace ? undefined : () => open(current.project)} />
      : phoneWorkspace
        ? <div className="workspace-placeholder"><Terminal size={28} /><p>{project ? "在侧栏选择或新建会话" : "请选择远程工具"}</p></div>
        : <section className="page-scroll">{error && <Notice onRetry={refreshAll}>{error}</Notice>}{codeNavigation}</section>}
    {phoneWorkspace && <WorkspaceDrawer open={drawer} active={current.tool} name={catalog?.host || devices.devices.find(d => d.id === devices.current)?.name}
      onClose={() => setDrawer(false)} onTool={selectTool} onSettings={() => settingsPage("settings")} nativePhone={nativePhone} onExit={exitWorkspace}>
      {error && <Notice onRetry={refreshAll}>{error}</Notice>}
      {gate ? <Notice onRetry={refreshAll}>{gate}</Notice> : <ProjectTree catalog={catalog} projectPath={current.project}
        sessionID={current.tool === "code" ? session : ""} creating={creating || actionBusy} onNew={start} onAction={chooseSessionAction}
        onSession={(path,id) => {open(path,id,true); setDrawer(false);}} expanded={expanded}
        toggle={(path,next) => setExpanded(previous => new Map(previous).set(path,next))} />}
    </WorkspaceDrawer>}
    {sessionAction && <SessionActionDialog key={sessionAction.kind + sessionAction.session.id} action={sessionAction} busy={actionBusy}
      error={sessionActionError} onCancel={() => setSessionAction(null)} onSubmit={body => mutateSession(sessionAction.kind, sessionAction.session, body)}/>}
  </main>;
}

function Chat({ id, drafts, ready, visible, refresh, onOpenWeb, onDenied, onMissing, onSnapshot, onBack }) {
  const [session, setSession] = useState(null);
  const [readError, setReadError] = useState("");
  const [connectionError, setConnectionError] = useState("");
  const [actionError, setActionError] = useState("");
  const [readProgress, setReadProgress] = useState("");
  const [connectionProgress, setConnectionProgress] = useState("");
  const [sendProgress, setSendProgress] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [draft, setDraft] = useState(() => drafts.get(id)?.text || "");
  const [queue, setQueue] = useState(() => drafts.get(id)?.queue || []);
  const [queuePaused, setQueuePaused] = useState(() => Boolean(drafts.get(id)?.paused && drafts.get(id)?.queue?.length));
  const [waitingSync, setWaitingSync] = useState(false);
  const [pendingMessage, setPendingMessage] = useState(() => drafts.get(id)?.pending || null);
  const [busy, setBusy] = useState(false);
  const [uncertain, setUncertain] = useState(() => !!drafts.get(id)?.pending);
  const [reload, setReload] = useState(0);
  const [approvalFeedback, setApprovalFeedback] = useState(null);
  const checkApproval = useRef(null);
  const readFailures = useRef(0);
  const live = useRef(true);
  const latest = useRef(null);
  const draftRef = useRef(draft);
  const queueRef = useRef(queue);
  const queuePausedRef = useRef(queuePaused);
  const waitingTurnIdRef = useRef(drafts.get(id)?.queue?.length && !drafts.get(id)?.paused ? drafts.get(id)?.waitingTurnId || null : null);
  const conflictIdRef = useRef(null);
  const submission = useRef(drafts.get(id)?.pending || null);
  const actionBusy = useRef(false);
  const transcript = useRef(null);
  const transcriptContext = useMemo(() => ({sessionID:id, openWeb:onOpenWeb}), [id, onOpenWeb]);
  const composerInput = useRef(null);
  const follow = useRef(true);
  const last = session?.turns?.at(-1);
  const running = active(last?.status);
  const persistDraftState = useCallback((nextText, nextPending, nextQueue, nextPaused, nextWaitingTurnId) => {
    const queueList = nextQueue || [];
    const paused = Boolean(nextPaused && queueList.length > 0);
    const waitingTurnId = queueList.length > 0 && !paused ? nextWaitingTurnId || null : null;
    if (nextText || nextPending || queueList.length > 0) {
      drafts.set(id, { text: nextText, pending: nextPending || null, queue: queueList, paused, waitingTurnId });
    } else {
      drafts.delete(id);
    }
  }, [id, drafts]);
  useEffect(() => {
    live.current = true;
    return () => { live.current = false; };
  }, []);
  const accept = useCallback((snapshot, expectedGeneration) => {
    if (!live.current || snapshot?.id !== id) return;
    const current = latest.current;
    if (current && snapshot.generation !== current.generation && expectedGeneration !== current.generation) return;
    const stale = Boolean(current && snapshot.generation === current.generation && snapshot.revision < current.revision);
    const acked = Boolean(submission.current && (snapshot.acknowledged_request_id === submission.current.request_id || snapshot.turns?.some((turn) => turn.id === submission.current.request_id)));
    if (acked) {
      submission.current = null;
      conflictIdRef.current = null;
      setPendingMessage(null);
      setUncertain(false); setActionError(""); setSendProgress("");
    }
    let nextPaused = queuePausedRef.current;
    let nextWaitingTurnId = waitingTurnIdRef.current;
    let queueStateChanged = false;
    if (!stale) {
      const reconciled = reconcileQueueWithSnapshot({
        prevTurn: current?.turns?.at(-1) || null,
        snapshot,
        queueLength: queueRef.current.length,
        paused: queuePausedRef.current,
        waitingTurnId: waitingTurnIdRef.current,
        pendingRequestId: submission.current?.request_id || null,
      });
      if (reconciled.paused !== queuePausedRef.current) {
        nextPaused = reconciled.paused;
        queuePausedRef.current = reconciled.paused;
        setQueuePaused(reconciled.paused);
        queueStateChanged = true;
      }
      if (reconciled.waitingTurnId !== waitingTurnIdRef.current) {
        nextWaitingTurnId = reconciled.waitingTurnId;
        waitingTurnIdRef.current = reconciled.waitingTurnId;
        queueStateChanged = true;
      }
    }
    if (acked || queueStateChanged) {
      persistDraftState(draftRef.current, submission.current, queueRef.current, nextPaused, nextWaitingTurnId);
    }
    if (stale) return;
    latest.current = snapshot; setSession(snapshot); onSnapshot(snapshot);
    const pending = snapshot.turns?.at(-1)?.approval?.id;
    if (checkApproval.current && pending !== checkApproval.current.id) checkApproval.current = null;
    setApprovalFeedback((old) => old?.id === pending ? old : null);
  }, [id, persistDraftState, onSnapshot]);
  const fetchSession = useCallback(async (signal, incremental) => {
    const checking = checkApproval.current;
    const generation = latest.current?.generation;
    const after = incremental && !checking && latest.current ? "?after=" + encodeURIComponent(latest.current.revision)
      + (generation ? "&epoch=" + encodeURIComponent(generation) : "") : "";
    try {
      const snapshot = await request(sessionPath(id) + after, { signal });
      if (signal.aborted || !live.current) return;
      if (snapshot) accept(snapshot, generation);
      if (snapshot && checking && checkApproval.current === checking && latest.current?.turns?.at(-1)?.approval?.id === checking.id) {
        checkApproval.current = null;
        setApprovalFeedback({ id: checking.id, message: "主机仍在等待这次审批。请核对参数后再次选择。" });
      }
      setWaitingSync(false);
      setReadError("");
      readFailures.current = 0; setReadProgress("");
      return active(latest.current?.turns?.at(-1)?.status) ? 1000 : 4000;
    } catch (error) {
      if (signal.aborted || !live.current) return;
      if (accessDenied(error)) onDenied(error); else if (error.status === 404 || error.status === 410) onMissing();
      else if (retryableSessionError(error) && ++readFailures.current <= sessionRetryLimit) {
        setReadError(""); setReadProgress(`${sessionFailureMessage(error)} 正在自动重连（${readFailures.current}/${sessionRetryLimit}）…`);
        return sessionRetryDelay(readFailures.current);
      } else {
        setReadProgress("");
        setReadError(sessionFailureMessage(error) + (retryableSessionError(error) ? " 已自动重连5次，仍未恢复。可以稍后再次连接。" : ""));
      }
      return null;
    }
  }, [id, accept, onDenied, onMissing]);
  useEffect(() => {
    if (!visible || uncertain) return;
    const controller = new AbortController();
    let timer, incremental = false;
    const poll = async () => {
      const delay = await fetchSession(controller.signal, incremental);
      incremental = true;
      if (!controller.signal.aborted && delay != null) timer = setTimeout(poll, delay);
    };
    void poll();
    return () => { clearTimeout(timer); controller.abort(); };
  }, [fetchSession, visible, refresh, reload, uncertain]);
  useEffect(() => {
    if (!uncertain || !visible || !ready) return;
    const controller = new AbortController();
    setConfirming(true); setActionError(""); setReadError(""); setReadProgress("");
    const recovering = recoverPendingMessage({
      signal:controller.signal, pending:() => submission.current,
      progress:attempt => setSendProgress(`正在重连并确认消息是否送达（${attempt}/${sessionRetryLimit}）…`),
      read:async () => {
        const generation = latest.current?.generation;
        const snapshot = await request(sessionPath(id), {signal:controller.signal});
        if (!controller.signal.aborted) { accept(snapshot, generation); setReadError(""); setReadProgress(""); readFailures.current = 0; }
        return latest.current;
      },
      send:async message => {
        const generation = latest.current?.generation;
        const snapshot = await request(sessionPath(id) + "/messages", {body:message, signal:controller.signal});
        if (!controller.signal.aborted) accept(snapshot, generation);
      },
    });
    void recovering.then(confirmed => {
      if (!controller.signal.aborted && !confirmed) setActionError("消息是否送达仍未确认。已自动重连5次，原消息已保留。再次连接后会继续确认，避免重复执行。");
    }).catch(error => {
      if (controller.signal.aborted) return;
      if (accessDenied(error)) onDenied(error); else if ([404,410].includes(error.status)) onMissing(); else setActionError(sessionFailureMessage(error));
    }).finally(() => { if (!controller.signal.aborted) { setConfirming(false); setSendProgress(""); } });
    return () => { controller.abort(); setConfirming(false); setSendProgress(""); };
  }, [id, uncertain, visible, ready, accept, onDenied, onMissing, reload]);
  const needsConnection = ready && visible && session?.terminal_available && session.can_send === false;
  useEffect(() => {
    if (!needsConnection) { setConnectionError(""); setConnectionProgress(""); return; }
    setConnectionError("");
    const controller = new AbortController();
    let timer;
    let started = Date.now();
    let failures = 0;
    const connect = async () => {
      let delay = 1000;
      try {
        const generation = latest.current?.generation;
        const snapshot = await request(sessionPath(id) + "/open", { body: {automatic:true}, signal:controller.signal });
        if (controller.signal.aborted) return;
        if (snapshot) accept(snapshot, generation);
        failures = 0; setConnectionProgress("");
        if (snapshot?.can_send) return;
        if (snapshot?.connection_waiting) started = Date.now();
        if (!snapshot?.connection_waiting && Date.now() - started > 60000) {
          setConnectionError("会话连接暂未就绪，请重试。");
          return;
        }
      } catch (error) {
        if (controller.signal.aborted) return;
        if (accessDenied(error)) { onDenied(error); return; }
        if (retryableSessionError(error) && ++failures <= sessionRetryLimit) {
          setConnectionProgress(`正在恢复会话连接（${failures}/${sessionRetryLimit}）…`);
          delay = sessionRetryDelay(failures);
        } else if (error.status !== 409 || Date.now() - started > 60000) {
          setConnectionProgress("");
          setConnectionError(sessionFailureMessage(error) + (retryableSessionError(error) ? " 已自动重连5次，可以稍后再次连接。" : "")); return;
        }
      }
      timer = setTimeout(connect, delay);
    };
    void connect();
    return () => { controller.abort(); clearTimeout(timer); };
  }, [id, needsConnection, accept, onDenied, reload]);
  useEffect(() => {
    if (follow.current && transcript.current) transcript.current.scrollTop = transcript.current.scrollHeight;
  }, [session?.revision, queue.length, pendingMessage?.request_id]);
  useEffect(() => {
    const input = composerInput.current;
    if (!input) return;
    input.style.height = "24px";
    input.style.height = Math.min(input.scrollHeight, 144) + "px";
  }, [draft]);
  const dispatchMessage = useCallback(async (item) => {
    if (actionBusy.current) return;
    actionBusy.current = true; setBusy(true); setActionError("");
    setSendProgress("正在发送消息…");
    try {
      follow.current = true;
      const generation = latest.current?.generation;
      submission.current = item;
      setPendingMessage(item);
      if (queueRef.current.length > 0 && !queuePausedRef.current) {
        waitingTurnIdRef.current = item.request_id;
      }
      persistDraftState(draftRef.current, item, queueRef.current, queuePausedRef.current, waitingTurnIdRef.current);
      const snapshot = await request(sessionPath(id) + "/messages", { body: item });
      if (live.current) { conflictIdRef.current = null; accept(snapshot, generation); setReload(n => n + 1); }
    } catch (error) {
      if (!live.current) return;
      if (accessDenied(error)) onDenied(error);
      else if (submission.current) {
        const next = reconcileDispatchError({
          error,
          item,
          draft: draftRef.current,
          queue: queueRef.current,
          previousConflictId: conflictIdRef.current,
        });
        conflictIdRef.current = next.conflictId;
        if (!next.keepPending) {
          submission.current = null;
          setPendingMessage(null);
        }
        if (next.draft !== draftRef.current) {
          draftRef.current = next.draft;
          setDraft(next.draft);
        }
        if (next.queue !== queueRef.current) {
          queueRef.current = next.queue;
          setQueue(next.queue);
        }
        const nextPaused = next.paused || queuePausedRef.current;
        if (next.paused && !queuePausedRef.current) {
          queuePausedRef.current = true;
          setQueuePaused(true);
        }
        if (nextPaused || queueRef.current.length === 0) {
          waitingTurnIdRef.current = null;
        } else if (next.keepPending) {
          waitingTurnIdRef.current = item.request_id;
        } else {
          waitingTurnIdRef.current = activeTurnIdForQueue(latest.current, submission.current);
        }
        setWaitingSync(next.waitingSync);
        persistDraftState(draftRef.current, submission.current, queueRef.current, nextPaused, waitingTurnIdRef.current);
        setUncertain(next.uncertain);
        setActionError(next.actionError);
        if (!next.uncertain) setSendProgress("");
        if (next.reload) setReload((n) => n + 1);
      }
    } finally { actionBusy.current = false; if (live.current) setBusy(false); }
  }, [id, accept, onDenied, persistDraftState]);
  const removeQueued = useCallback((requestId) => {
    const nextQueue = removeQueuedMessage(queueRef.current, requestId);
    queueRef.current = nextQueue;
    setQueue(nextQueue);
    let nextPaused = queuePausedRef.current;
    if (nextQueue.length === 0) {
      nextPaused = false;
      queuePausedRef.current = false;
      waitingTurnIdRef.current = null;
      conflictIdRef.current = null;
      setQueuePaused(false);
      setWaitingSync(false);
    }
    persistDraftState(draftRef.current, submission.current, nextQueue, nextPaused, waitingTurnIdRef.current);
  }, [persistDraftState]);
  const resumeQueue = useCallback(() => {
    conflictIdRef.current = null;
    queuePausedRef.current = false;
    waitingTurnIdRef.current = queueRef.current.length > 0 ? activeTurnIdForQueue(latest.current, submission.current) : null;
    setQueuePaused(false);
    setWaitingSync(false);
    setActionError("");
    persistDraftState(draftRef.current, submission.current, queueRef.current, false, waitingTurnIdRef.current);
  }, [persistDraftState]);
  useEffect(() => {
    if (!canDrainQueue({
      visible,
      ready,
      session,
      running,
      busy: busy || actionBusy.current,
      confirming,
      uncertain,
      readProgress,
      readError,
      waitingSync,
      queueLength: queue.length,
      queuePaused,
    })) return;
    const { head, rest } = dequeueMessage(queueRef.current);
    if (!head) return;
    queueRef.current = rest;
    setQueue(rest);
    waitingTurnIdRef.current = rest.length > 0 ? head.request_id : null;
    persistDraftState(draftRef.current, submission.current, rest, queuePausedRef.current, waitingTurnIdRef.current);
    void dispatchMessage(head);
  }, [visible, ready, session, running, busy, confirming, uncertain, readProgress, readError, waitingSync, queue, queuePaused, dispatchMessage, persistDraftState]);
  const send = async (event) => {
    event?.preventDefault();
    const text = draft.trim();
    if (!text || !ready || !session || session.single_turn || session.can_send === false) return;
    const item = { request_id: crypto.randomUUID(), text };
    draftRef.current = "";
    setDraft("");
    follow.current = true;
    if (canSendImmediately({
      running,
      busy: actionBusy.current || busy,
      confirming,
      uncertain,
      readProgress,
      readError,
      waitingSync,
      queueLength: queueRef.current.length,
      queuePaused: queuePausedRef.current,
    })) {
      await dispatchMessage(item);
      return;
    }
    const nextQueue = enqueueMessage(queueRef.current, item);
    queueRef.current = nextQueue;
    setQueue(nextQueue);
    if (!waitingTurnIdRef.current && !queuePausedRef.current) {
      waitingTurnIdRef.current = activeTurnIdForQueue(latest.current, submission.current);
    }
    persistDraftState("", submission.current, nextQueue, queuePausedRef.current, waitingTurnIdRef.current);
  };
  const stop = async () => {
    if (actionBusy.current || last?.status !== "running") return;
    if (queueRef.current.length > 0 && !queuePausedRef.current) {
      queuePausedRef.current = true;
      waitingTurnIdRef.current = null;
      setQueuePaused(true);
      persistDraftState(draftRef.current, submission.current, queueRef.current, true, null);
    }
    actionBusy.current = true; setBusy(true); setActionError("");
    const generation = latest.current?.generation;
    try { accept(await request(sessionPath(id) + "/stop", { body: { request_id: last.id } }), generation); }
    catch (error) {
      if (!live.current) return;
      if (accessDenied(error)) onDenied(error);
      else { setActionError(error.message); setReload((n) => n + 1); }
    } finally { actionBusy.current = false; if (live.current) setBusy(false); }
  };
  const retry = () => { readFailures.current = 0; setReadError(""); setConnectionError(""); setReload((n) => n + 1); };
  const decide = async (turnId, approvalId, allow) => {
    const turn = latest.current?.turns?.at(-1);
    if (actionBusy.current || checkApproval.current?.id === approvalId || turn?.id !== turnId || turn?.status !== "running" || turn.approval?.id !== approvalId) return;
    actionBusy.current = true; setBusy(true); setApprovalFeedback(null);
    try {
      const generation = latest.current?.generation;
      accept(await request(sessionPath(id) + "/approval", { body: { request_id: turnId, approval_id: approvalId, allow } }), generation);
    } catch (error) {
      if (!live.current) return;
      if (accessDenied(error)) onDenied(error);
      else if (latest.current?.turns?.at(-1)?.approval?.id === approvalId) {
        const checking = { id: approvalId, checking: true, message: error.status === 409 ? "这次请求的状态已变化，正在重新读取。" : "审批结果尚未确认，正在重新读取主机状态。" };
        checkApproval.current = checking; setApprovalFeedback(checking); retry();
      }
    } finally { actionBusy.current = false; if (live.current) setBusy(false); }
  };
  const showPending = Boolean(pendingMessage && !session?.turns?.some((turn) => turn.id === pendingMessage.request_id));
  const willQueue = !canSendImmediately({
    running,
    busy,
    confirming,
    uncertain,
    readProgress,
    readError,
    waitingSync,
    queueLength: queue.length,
    queuePaused,
  });
  return <>
    <section className="chat-header">
      <div className="title-line">{onBack && <button type="button" className="icon-button" aria-label="返回会话列表" onClick={onBack}><ArrowLeft size={18} /></button>}<h1>{session?.title || "Claude Code 会话"}</h1>{session && <Status value={last?.status} awaitingApproval={last?.status === "running" && last.approval} />}</div>
      {session && <p className="note">{session.workspace}</p>}
    </section>
    {!uncertain && (readError || connectionError) && <Notice onRetry={retry} label="再次连接">{readError || connectionError}</Notice>}
    {session && <SessionAccount id={id} request={request} onChanged={()=>setReload(n=>n+1)} compact />}
    {(sendProgress || connectionProgress || readProgress) && <p className="note" role="status">{sendProgress || connectionProgress || readProgress}</p>}
    {session?.connection_notice && <Notice>{session.connection_notice}</Notice>}
    {session?.history_truncated && <p className="note">手机显示的历史已截断，完整内容保存在电脑 Claude Code 中。</p>}
    <section className="transcript" ref={transcript} aria-label="会话消息" onScroll={(e) => {
      const box = e.currentTarget; follow.current = box.scrollHeight - box.scrollTop - box.clientHeight < 64;
    }}>
      {!session && !readError && <Empty icon={LoaderCircle} title="正在读取会话…" />}
      {session && !session.turns?.length && !showPending && !queue.length && <Empty icon={Terminal} title="发送第一条消息">Claude Code 会在所选项目中处理你的请求。</Empty>}
      <TranscriptContext.Provider value={transcriptContext}>{session?.turns?.map((turn) => <React.Fragment key={turn.id}>
        <article className="message user" aria-label="你的消息"><MessageBody text={turn.prompt} /></article>
        <article className="message" aria-label="回复">
          <ToolActivity tools={turn.tools} />
          {turn.text && <MessageBody text={turn.text} />}
          {turn.truncated && <p className="note">本轮输出超过记录上限，仅显示已保存的部分。</p>}
          {!turn.text && !turn.approval && active(turn.status) && <p className="note">等待输出…</p>}
          {turn.id === last.id && turn.status === "running" && turn.approval?.id && <Approval
            key={turn.approval.id} approval={turn.approval} busy={busy} feedback={approvalFeedback?.id === turn.approval.id ? approvalFeedback : null}
            onDecide={(allow) => decide(turn.id, turn.approval.id, allow)} />}
          {turn.error && <div className="turn-error">{turn.error}</div>}
          {turn.denied_tools?.length > 0 && <div className="turn-tools">以下工具需要授权，本次调用已拒绝。<span>{turn.denied_tools.join("、")}</span></div>}
          {["failed", "interrupted", "stopped"].includes(turn.status) && <p className="note">{labels[turn.status]}</p>}
        </article>
      </React.Fragment>)}
      {showPending && <article className="message user queued" aria-label="正在发送的消息">
        <div className="queued-meta">
          <span>{uncertain ? (confirming ? "确认送达中…" : "送达未确认") : "发送中…"}</span>
        </div>
        <MessageBody text={pendingMessage.text} />
      </article>}
      {queue.map((item, index) => <article key={item.request_id} className="message user queued" aria-label="排队待发消息">
        <div className="queued-meta">
          <span>{queuePaused ? `已暂停排队 · 第 ${index + 1} 条` : `排队中 · 第 ${index + 1} 条`}</span>
          <div className="queued-actions">
            {queuePaused && index === 0 && <button type="button" className="queued-resume" onClick={resumeQueue}>继续发送</button>}
            <button type="button" className="queued-cancel" aria-label="取消排队消息" title="取消发送" onClick={() => removeQueued(item.request_id)}><X size={14} /></button>
          </div>
        </div>
        <MessageBody text={item.text} />
      </article>)}</TranscriptContext.Provider>
    </section>
    <form className="compose" onSubmit={send}>
      {actionError && <Notice onRetry={uncertain ? retry : undefined} label="再次连接">{actionError}</Notice>}
      {session?.single_turn && <p className="note">当前凭据或路由只能验证本轮。请返回会话列表，新建会话后继续。</p>}
      {!ready && <p className="note">请在电脑上启动 Cage 环境。</p>}
      <div className="composer-field">
        <textarea ref={composerInput} aria-label="消息" rows="1" placeholder={running ? "正在运行，可继续输入排队发送" : willQueue ? "可继续输入，将按顺序排队发送" : "给 Claude Code 发消息"} value={draft} disabled={!session || session.single_turn || !ready}
          onChange={(e) => {
            const value = e.target.value;
            draftRef.current = value;
            persistDraftState(value, submission.current, queueRef.current, queuePausedRef.current, waitingTurnIdRef.current);
            setDraft(value);
          }} onKeyDown={(e) => {
            if (e.key === "Enter" && !e.nativeEvent.isComposing && (e.ctrlKey || e.metaKey)) { e.preventDefault(); void send(); }
          }} />
        {running && <button type="button" className={draft.trim() ? "composer-action secondary" : "composer-action"} aria-label={last.status === "stopping" ? "正在停止" : "停止运行"} title={last.status === "stopping" ? "正在停止" : "停止运行"} disabled={busy || last.status !== "running"} onClick={stop}><Square size={17} /></button>}
        {(!running || draft.trim()) && <button type="submit" className="composer-action" aria-label={willQueue ? "加入待发队列" : "发送"} title={willQueue ? "加入待发队列" : "发送"} disabled={!ready || !session || session.single_turn || session.can_send === false || !draft.trim()}><ArrowUp size={21} /></button>}
      </div>
    </form>
  </>;
}

function Approval({ approval, busy, feedback, onDecide }) {
  let input = null;
  if (approval.input && typeof approval.input === "object" && !Array.isArray(approval.input)) {
    try {
      // Keep direction-control characters visible when reviewing command text.
      input = JSON.stringify(approval.input, null, 2).replace(/[\u061c\u200e\u200f\u202a-\u202e\u2066-\u2069]/g, c => "\\u" + c.charCodeAt(0).toString(16).padStart(4, "0"));
    } catch { /* An unreadable argument object cannot be approved. */ }
  }
  return <section className="approval" aria-label="工具审批">
    <h2>允许使用 {approval.tool}？</h2>
    {input === null ? <p className="note">无法显示完整参数，请拒绝或重新读取。</p>
      : <pre tabIndex={0} aria-label={approval.preview ? "工具参数预览" : "完整工具参数"}>{input}</pre>}
    {approval.preview && <p className="note">参数预览由 Claude Code 提供。若显示省略内容，请在电脑查看完整参数后处理。</p>}
    {feedback && <p className="note" role="status">{feedback.message}</p>}
    <div className="approval-actions">
      <button type="button" className="action secondary" disabled={busy || feedback?.checking} onClick={() => onDecide(false)}>拒绝</button>
      <button type="button" className="action primary" disabled={busy || feedback?.checking || input === null} onClick={() => onDecide(true)}>允许一次</button>
    </div>
  </section>;
}

createRoot(document.getElementById("root")).render(<><IconHints /><App /></>);
