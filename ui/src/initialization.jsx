import React, { useEffect, useRef, useState } from "react";
import { Check, ChevronLeft, LoaderCircle, ShieldCheck } from "lucide-react";
import { api, native, nativeAvailable } from "./api";
import { isRemoteClient } from "./client-context.mjs";
import { useAccount } from "./account";
import { GuestSystemPicker, validGuestChoice } from "./guest-system-picker";
import { InitializationStorage } from "./initialization-storage";
import { GuestProfileOptions, guestOptionsForSystem } from "./guest-profile";
import { ProxySettings } from "./proxy-settings";
import { InitializationDNS } from "./initialization-dns";
import { isInstallDNSFailure } from "./initialization-dns-state.mjs";
import { SetupProgress } from "./setup-progress";
import { DiagnosticSettings } from "./diagnostics.jsx";
import { InitializationDetails } from "./initialization-checks";
import { Modal } from "./modal";
import { SafetyMessage } from "./safety-message";
import { needsEnvironmentRestart, needsProxySetup } from "./onboarding-state.mjs";
import { beginInstall, formatSpeed, mirrorNames } from "./debian-mirrors.mjs";
import { networkState } from "./safety-state.mjs";
import { useSSHNavigation } from "./remote-hosts";
import logo from "./logo.png";
import "./initialization.css";
import { useTranslation } from "./interface-language.mjs";
import { LanguageSettings } from "./language-settings";

const steps = [["system", "系统与存储"], ["proxy", "网络代理"], ["install", "安装与检查"]];

function InitializationFrame({ children }) {
  const tr = useTranslation();
  return <main className="initialization">
    <header className="initialization-brand">
      <img src={logo} alt="" /><strong>Cage</strong><span>{tr("首次设置")}</span>
      <div className="startup-language-slot"><LanguageSettings compact /></div>
    </header>
    {children}
  </main>;
}

const setupApi = (path, body, method, headers = {}, options) =>
  api(path, body, method, { "X-Cage-Initiator": "setup", ...headers }, options);

export function InitializationGate({ children }) {
  const tr = useTranslation();
  const [state, setState] = useState(null);
  const [error, setError] = useState("");
  const [attempt, retry] = useState(0);
  const remote = isRemoteClient();
  useEffect(() => {
    if (remote) return;
    let live = true;
    setError("");
    (async () => {
      const next = await setupApi("/environment/initialization");
      if (next.required && !next.tracked && !next.started) await setupApi("/environment/initialization", { action: "begin" });
      if (live) setState(next);
    })().catch(error => { if (live) setError(error.message); });
    return () => { live = false; };
  }, [remote, attempt]);
  if (remote) return children;
  if (error) return <InitializationFrame><section className="initialization-content"><h1>{tr("继续准备 Cage")}</h1><p role="alert"><SafetyMessage message={error} /></p><button className="primary" onClick={() => retry(value => value + 1)}>{tr("重新连接")}</button><DiagnosticSettings /></section></InitializationFrame>;
  if (!state) return <InitializationFrame><section className="initialization-content"><h1>{tr("正在读取环境状态")}</h1><p role="status"><LoaderCircle size={18} className="spin" />{tr("正在检查已有配置和安装进度…")}</p></section></InitializationFrame>;
  return state.required ? <Initialization initial={state} onComplete={() => setState({ required: false })} /> : children;
}

function Initialization({ initial, onComplete }) {
  const tr = useTranslation();
  const { account, refresh } = useAccount();
  const navigation = useSSHNavigation();
  const [step, setStep] = useState(initial.started || account?.guest_system ? "install" : "system");
  const [status, setStatus] = useState(null);
  const [proxy, setProxy] = useState(null);
  const [choice, setChoice] = useState(undefined);
  const [guestProfile, setGuestProfile] = useState(null);
  const [storage, setStorage] = useState(null);
  const [pending, setPending] = useState(null);
  const [canReselect, setCanReselect] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [entering, setEntering] = useState(false);
  const [error, setError] = useState("");
  const [connectionError, setConnectionError] = useState("");
  const [mirrors, setMirrors] = useState(null);
  const [mirrorChoice, setMirrorChoice] = useState("");
  const [measuring, setMeasuring] = useState(false);
  const [details, setDetails] = useState(false);
  const [help, setHelp] = useState(false);
  const [dialog, setDialog] = useState(null);
  const [proxyDirty, setProxyDirty] = useState(false);
  const [dnsDirty, setDnsDirty] = useState(false);
  const [dnsOpen, setDnsOpen] = useState(false);
  const [dnsFocusNonce, setDnsFocusNonce] = useState(0);
  const alive = useRef(true);
  const pollNow = useRef(null);
  const actionPending = useRef(false);
  const restored = useRef(false);
  const job = status?.job || {};
  const busy = submitting || Boolean(job.running);
  const network = networkState(status);
  const needsRestart = needsEnvironmentRestart(status);
  const showReady = entering || Boolean(status?.ready && status.vm_running && !busy && !job.error && !needsRestart && network.mode === "open");
  const confirm = options => new Promise(resolve => setDialog({ ...options, resolve }));
  const settleDialog = value => { dialog?.resolve(value); setDialog(null); };

  useEffect(() => {
    alive.current = true;
    let timer, polling = false;
    async function poll() {
      if (polling || !alive.current) return;
      clearTimeout(timer);
      polling = true;
      try {
        const next = await setupApi("/status");
        if (!alive.current) return;
        setStatus(next);
        const results = await Promise.allSettled([
          setupApi("/proxy"),
          account?.guest_system ? Promise.resolve(null) : setupApi("/environment/guest-systems"),
        ]);
        if (!alive.current) return;
        // A successful status read does not mean the related configuration
        // reads recovered. Keep the notice mounted until the full poll succeeds.
        const failedRead = results.find(result => result.status === "rejected");
        setConnectionError(failedRead ? failedRead.reason.message : "");
        if (results[0].status === "fulfilled") setProxy(results[0].value);
        if (results[1].status === "fulfilled") {
          const install = results[1].value;
          setPending(install?.pending || null);
          setCanReselect(Boolean(install?.reselect));
          if (!restored.current) {
            restored.current = true;
            if (initial.started && !initial.prepared && !account?.guest_system && !install?.pending && !next.job?.running && !next.job?.setup_progress && !next.ready) setStep("system");
          }
        }
        if (next.job?.running && ["start", "guest-install", "safety"].includes(next.job.kind)) setStep("install");
      } catch (error) { if (alive.current) setConnectionError(error.message); }
      finally {
        polling = false;
        if (alive.current) timer = setTimeout(poll, 1200);
      }
    }
    pollNow.current = poll;
    poll();
    if (nativeAvailable()) native("workbenchReady").catch(() => {});
    window.cageWorkbenchAction = async action => { if (action === "quit") await native("quitNow"); };
    return () => { alive.current = false; clearTimeout(timer); pollNow.current = null; delete window.cageWorkbenchAction; };
  }, [account?.guest_system]);

  async function run(operation) {
    if (actionPending.current) return;
    actionPending.current = true;
    setSubmitting(true); setError("");
    try { await operation(); }
    catch (error) { if (alive.current) setError(error.message); }
    finally { actionPending.current = false; if (alive.current) { setSubmitting(false); pollNow.current?.(); } }
  }
  function startOptions() {
    if (account?.guest_system) return {};
    if (pending) return { guest_system: pending.system };
    const preferences = guestProfile ? { guest_profile: guestOptionsForSystem(guestProfile, choice?.guest_system) } : {};
    if (!choice) return job.setup_progress?.system ? { guest_system: job.setup_progress.system } : preferences;
    return { guest_system: choice.guest_system, ...preferences };
  }
  async function install(mirrorChoice) {
    if (needsProxySetup(null, status, proxy)) { setError("请先选择并应用代理节点。"); setStep("proxy"); return; }
    // Before the first install, measure the Debian sources through the proxy
    // once; the saved choice is reused by later updates and maintenance.
    const checkMirrors = !status?.vm_running && !job.setup_progress;
    await run(async () => {
      setMeasuring(checkMirrors && typeof mirrorChoice !== "string");
      let outcome;
      try { outcome = await beginInstall(setupApi, { mirrorChoice: typeof mirrorChoice === "string" ? mirrorChoice : "", checkMirrors, startOptions: startOptions() }); }
      finally { if (alive.current) setMeasuring(false); }
      if (!alive.current) return;
      if (outcome.mirrors) { setMirrors(outcome.mirrors); setMirrorChoice(outcome.mirrors.preference); setStep("install"); return; }
      setMirrors(null);
      const { accepted } = outcome;
      setStep("install");
      setStatus(current => ({ ...current, job: { ...accepted, running: true, kind: accepted.kind || "start", message: accepted.message || tr("正在准备客机环境…") } }));
    });
  }
  async function stop() {
    if (!await confirm({ title: "停止当前安装？", body: "安装进度、已下载文件和虚拟磁盘会保留。你可以在此界面继续安装。", confirm: "停止安装" })) return;
    await run(async () => { const accepted = await setupApi("/stop", {}); setStatus(current => ({ ...current, job: { ...accepted, running: true } })); });
  }
  async function waitForJob(accepted, kind) {
    if (!accepted.id) throw new Error(tr("操作未返回任务编号，请核对状态后重试。"));
    while (alive.current) {
      const next = await setupApi("/status");
      if (!alive.current) break;
      setStatus(next);
      const current = next.job;
      if (current?.id !== accepted.id || current.kind !== kind) throw new Error(tr("操作状态已变化，请核对状态后重试。"));
      if (current.error || current.canceled) throw new Error(current.error || tr("操作已取消，请核对状态后重试。"));
      if (!current.running) return next;
      await new Promise(resolve => setTimeout(resolve, 300));
    }
    throw new Error(tr("初始化页面已关闭，操作未继续。"));
  }
  // A changed exit or environment configuration only takes effect after a full
  // stop. Confirm first, then start only after the accepted stop has finished.
  async function restart() {
    await run(async () => {
      const running = status.vm_running;
      if (!await confirm({ title: running ? tr("重启环境并重新检测？") : tr("启动环境并重新检测？"), body: tr("请先保存文件。当前终端会话会结束，浏览器登录和项目文件会保留。重新检测通过前，外网保持关闭。"), confirm: running ? tr("重启并检查") : tr("启动并检测") })) return;
      const current = await setupApi("/status");
      if (!alive.current) return;
      if (current.job?.running) throw new Error(tr("其他操作仍在进行，请等待完成后重试。"));
      if (current.vm_running) {
        const stopped = await waitForJob(await setupApi("/stop", {}), "stop");
        if (stopped.vm_running) throw new Error(tr("环境尚未停止，请核对状态后重试。"));
      }
      if (!alive.current) return;
      const accepted = await setupApi("/start", startOptions());
      if (!accepted.id) throw new Error(tr("启动请求未返回任务编号，请核对状态后重试。"));
      setStatus(current => ({ ...current, job: { ...accepted, running: true, kind: "start", message: accepted.message || tr("正在启动并检查环境…") } }));
    });
  }
  async function enter() {
    await run(async () => {
      setEntering(true);
      try {
        await setupApi("/environment/initialization", { action: "complete" });
        await refresh();
        if (alive.current) onComplete();
      } finally {
        if (alive.current) setEntering(false);
      }
    });
  }
  async function checkNetwork() {
    await run(async () => {
      const accepted = await setupApi("/safety/check", {});
      setStatus(current => ({ ...current, job: { ...accepted, running: true, kind: "safety", message: tr("正在检查代理出口和环境隔离…") } }));
    });
  }
  async function back() {
    if (step === "proxy" && proxyDirty && !await confirm({ title: "放弃尚未应用的代理修改？", body: "已经保存并应用的代理会保留，当前未应用的表单修改会丢弃。", confirm: "放弃修改" })) return;
    if (step === "proxy" && !proxyDirty && dnsDirty && !await confirm({ title: "放弃尚未保存的修改？", body: "当前环境不会改变。关闭后会丢弃这次表单修改。", confirm: "放弃修改" })) return;
    setProxyDirty(false);
    setDnsDirty(false);
    setStep(step === "install" ? "proxy" : pending && !canReselect || account?.guest_system ? "install" : "system");
  }
  function openDNSSettings() {
    setDnsOpen(true);
    setDnsFocusNonce(value => value + 1);
    setStep("proxy");
  }
  async function nextFromSystem() {
    await run(async () => {
      const directory = storage.directory.trim();
      if (directory !== storage.location.directory) {
        const accepted = await setupApi("/environment/storage", { directory, previous: storage.location.directory, migrate: false });
        let job;
        do {
          await new Promise(resolve => setTimeout(resolve, 300));
          if (!alive.current) return;
          const next = await setupApi("/status");
          setStatus(next); job = next.job;
          if (job.id !== accepted.id) throw new Error("存储操作状态已变化，请核对目录后重试。");
        } while (job.running);
        if (job.error) throw new Error(job.error);
        const location = await setupApi("/environment/storage");
        if (location.directory !== directory) throw new Error("存储目录尚未保存，请重新选择后重试。");
        setStorage({ location, directory: location.directory });
      }
      setStep("proxy");
    });
  }

  const failed = Boolean(job.error || error);
  const dnsFailed = isInstallDNSFailure(job.error || error);
  const stopped = !busy && !status?.ready && (job.kind === "stop" || job.canceled);
  const networkPending = Boolean(status?.ready && status.vm_running && network.mode !== "open");
  const progress = !status?.ready && job.setup_progress ? { ...job, setup_progress: job.setup_progress } : null;
  const title = step === "system" ? "为 Cage 准备独立环境" : step === "proxy" ? "连接你的网络代理" : showReady ? "Cage 已准备就绪" : !busy && networkPending ? "系统已安装，完成使用检查" : stopped ? "安装已停止，可以继续" : failed ? "继续完成环境安装" : busy ? "正在准备你的环境" : "安装并检查客机环境";
  const index = steps.findIndex(([key]) => key === step);
  return <InitializationFrame>
    <section className="initialization-content">
      <ol className="initialization-steps" aria-label={tr("初始化步骤")}>{steps.map(([key, label], position) => <li key={key} aria-current={step === key ? "step" : undefined}><span>{position < index ? <Check size={14} /> : position + 1}</span>{tr(label)}</li>)}</ol>
      <div className="initialization-title"><h1>{tr(title)}</h1><p>{step === "system" ? tr("Cage 会在这台电脑里创建一套独立的 Debian 系统，供终端、浏览器和编辑器使用。不需要更换磁盘时，可以保留默认存储位置。") : step === "proxy" ? tr("导入具体节点。使用代理链时，在 Cage 中依次选择中转和最终出口。") : showReady ? tr("系统、开发组件和网络检查均已完成，可以进入工作台开始使用。") : networkPending ? tr("系统已经装好，无需重新安装。按下方指引完成检查，就能进入工作台。") : tr("Cage 会自动下载系统、安装工具，再引导你完成授权。需要你操作时会显示具体步骤；中断后可以继续原任务。")}</p></div>
      {connectionError && <div className="initialization-notice" role="alert"><SafetyMessage message={connectionError} /><button onClick={() => pollNow.current?.()}>{tr("重新连接")}</button></div>}
      {!status ? <p role="status"><LoaderCircle size={18} className="spin" />{tr("正在读取安装状态…")}</p> : <>
        <div className="initialization-stage" key={step}>
          {step === "system" && <>
            <GuestSystemPicker system={choice} onChange={setChoice} onReselect={() => { setPending(null); setCanReselect(false); }} preserveOnUnmount />
            {pending ? <p className="field-help">{tr("撤销原安装选择后，Cage 会改为安装 Debian 13；已下载的镜像和已保存的代理配置会保留。")}</p> : <>
              <InitializationStorage value={storage} onChange={setStorage} disabled={busy} />
              <GuestProfileOptions value={guestProfile} onChange={setGuestProfile} disabled={busy} system={choice?.guest_system} />
            </>}
          </>}
          {step === "proxy" && <>
            <ProxySettings initializing status={status} onConfirm={confirm} onDirty={setProxyDirty} onApplied={async () => { setProxy(await api("/proxy")); pollNow.current?.(); }} />
            <InitializationDNS proxy={proxy} busy={busy} defaultOpen={dnsOpen || dnsFailed} focusNonce={dnsFocusNonce} onDirty={setDnsDirty} onSaved={() => pollNow.current?.()} apiClient={setupApi} />
          </>}
          {step === "install" && (showReady ? <div className="initialization-ready"><ShieldCheck size={36} /><h2>{tr(entering ? "正在进入工作台" : "环境已就绪")}</h2><p role={entering ? "status" : undefined}>{tr(entering ? "正在等待后台检查结束并确认环境状态，请稍候…" : "独立系统已安装，工具、文件和登录状态会持续保存。以后打开 Cage 可直接进入工作台。")}</p></div> : <>
            {needsRestart && <section className="initialization-notice" aria-label={tr("需要重启环境")}><h2>{tr("下一步：重启环境并重新检测")}</h2><p>{tr("环境设置已变化。请先保存文件，再点击下方按钮。重启并通过检查前，外网保持关闭。")}</p><button className="primary" disabled={busy || Boolean(connectionError)} onClick={restart}>{status.vm_running ? tr("重启环境并重新检测") : tr("启动环境并重新检测")}</button></section>}
            {progress ? <SetupProgress job={progress} onDetails={() => setDetails(true)} onStop={stop} onRetry={needsRestart ? restart : install} onDNS={dnsFailed ? openDNSSettings : undefined} disconnected={Boolean(connectionError) || submitting} /> : <div className="initialization-operation" role="status">{busy && <LoaderCircle size={22} className="spin" />}<p>{measuring ? tr("正在经所选代理测量 Debian 软件源的下载速度，最多约 20 秒…") : stopped ? tr("安装已停止，已有下载和磁盘会保留。点击「继续安装」接着完成。") : job.message || (status.vm_running ? tr("请完成客机连接和网络检查。") : tr("准备继续原安装，已有文件和磁盘会保留。"))}</p>{!busy && !job.error && !mirrors && !needsRestart && <button className="primary" disabled={Boolean(connectionError)} onClick={install}>{status.ready ? tr("重新启动并检查") : tr("继续安装")}</button>}{busy && !measuring && <button onClick={stop} disabled={submitting}>{tr("停止安装")}</button>}</div>}
            {job.error && !progress && <div className="initialization-notice" role="alert"><SafetyMessage message={job.error} />{!networkPending && !needsRestart && <button className="primary" disabled={busy || Boolean(connectionError)} onClick={install}>{tr("继续安装")}</button>}{dnsFailed && <button disabled={busy} onClick={openDNSSettings}>{tr("测试或修改加密 DNS")}</button>}</div>}
            {status.ready && network.mode !== "open" && !busy && !needsRestart && <>
              <div className="initialization-network"><p>{status.safety?.error || job.error || status.network_error || tr("客机已经连接，请完成网络和隔离检查后进入工作台。")}</p><button disabled={Boolean(connectionError)} onClick={checkNetwork}>{tr("运行网络与隔离检查")}</button></div>
            </>}
            {(progress?.setup_progress.phase === "image" || (failed && !networkPending)) && <p>{tr("系统镜像遵循主机代理设置。下载失败时，可在操作详情中查看当前文件和重试记录。客机内的联网请求使用 Cage 代理。")}</p>}
            <div className="initialization-recovery">{canReselect && <button disabled={busy} onClick={() => setStep("system")}>{tr("查看或撤销原安装选择")}</button>}<button onClick={() => { if (failed) setDnsOpen(true); setStep("proxy"); }} disabled={busy}>{tr("检查 Cage 代理配置")}</button>{!progress && <button onClick={() => setDetails(true)}>{tr("查看操作详情")}</button>}</div>
          </>)}
        </div>
        {mirrors && step === "install" && <section className="initialization-notice" aria-label={tr("Debian 软件源")}><h2>{tr("安装前，选择 Debian 软件源")}</h2><p>{tr("已经所选代理依次测量各软件源的下载速度。国内直连时国内镜像通常更快；出口在海外时官方 CDN 更快。")}</p>
          <table className="mirror-samples"><tbody>{mirrors.samples.map(sample => <tr key={sample.id}><td>{tr(sample.name)}</td><td><code>{sample.host}</code></td><td>{sample.error ? <span className="mirror-error">{tr(sample.error)}</span> : formatSpeed(sample.bytes_per_second)}</td></tr>)}</tbody></table>
          <label className="mirror-option"><input type="radio" name="debian-mirror" value="auto" checked={mirrorChoice === "auto"} onChange={() => setMirrorChoice("auto")} />{tr("Debian 官方源优先（deb.debian.org，不可达时改用国内镜像）")}{mirrors.recommended === "auto" && <em>{tr("推荐")}</em>}</label>
          <label className="mirror-option"><input type="radio" name="debian-mirror" value={mirrors.domestic} checked={mirrorChoice === mirrors.domestic} onChange={() => setMirrorChoice(mirrors.domestic)} />{tr("国内镜像优先（{0}首选，另一镜像其次，都不可达时用官方源）", tr(mirrorNames[mirrors.domestic] ?? mirrors.domestic))}{mirrors.recommended === mirrors.domestic && <em>{tr("推荐")}</em>}</label>
          <p>{tr("选择保存在设置中，之后的安装、更新和维护沿用；也可以在环境的") + " "}<code>/etc/claude-cage/debian-mirror</code>{" " + tr("中修改。")}</p>
          <button className="primary" disabled={busy || !mirrorChoice || Boolean(connectionError)} onClick={() => install(mirrorChoice)}>{tr("使用所选软件源并开始安装")}</button></section>}
        {error && <p className="initialization-notice" role="alert"><SafetyMessage message={error} /></p>}
        <footer className="initialization-actions">
          {step === "system" ? <span /> : <button className="text-button" disabled={busy} onClick={back}><ChevronLeft size={16} />{tr("上一步")}</button>}
          {step === "system" && <button className="primary" disabled={busy || !storage?.directory.trim() || choice === undefined || !validGuestChoice(choice) || Boolean(connectionError)} onClick={nextFromSystem}>{submitting && <LoaderCircle size={16} className="spin" />}{tr("下一步：配置代理")}</button>}
          {step === "proxy" && <button className="primary" disabled={busy || proxyDirty || dnsDirty || !account?.guest_system && !pending && !validGuestChoice(choice) || needsProxySetup(null, status, proxy) || Boolean(connectionError)} onClick={install}>{submitting ? <LoaderCircle size={16} className="spin" /> : null}{tr("开始安装")}</button>}
          {step === "install" && showReady && <button className="primary" disabled={busy || Boolean(connectionError)} aria-busy={entering} onClick={enter}>{entering && <LoaderCircle size={16} className="spin" />}{tr(entering ? "正在进入…" : "进入工作台")}</button>}
        </footer>
      </>}
      <div className="initialization-support"><button className="text-button" aria-expanded={help} onClick={() => setHelp(!help)}>{tr("日志与诊断")}</button>{navigation && !busy && <button className="text-button" onClick={() => navigation.open()}>{tr("只连接 SSH 主机")}</button>}</div>
      {help && <DiagnosticSettings />}
    </section>
    {details && <Modal title={tr("初始化操作详情")} onClose={() => setDetails(false)}><InitializationDetails status={status} /></Modal>}
    {dialog && <Modal title={dialog.title} onClose={() => settleDialog(false)} footer={<><button onClick={() => settleDialog(false)}>{tr("取消")}</button><button className="primary" onClick={() => settleDialog(true)}>{tr(dialog.confirm)}</button></>}><div className="modal-body"><p>{tr(dialog.body)}</p></div></Modal>}
  </InitializationFrame>;
}
