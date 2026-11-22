import React, { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { Check, Globe2, Layers, Pencil, Plus, RefreshCw, SlidersHorizontal, TerminalSquare, Timer, Trash2 } from "lucide-react";
import { api } from "./api";
import { InlineSelect } from "./inline-select";
import crabIcon from "./claude-account-crab.svg";
import "./claude-accounts.css";
import { activeInterfaceLanguage, translate, useTranslation } from "./interface-language.mjs";

// Ordinary reads never start an upstream usage query.
export function useClaudeAccounts() {
  const [registry, setRegistry] = useState(null);
  const [error, setError] = useState("");
  const generation = useRef(0);
  const pendingMutation = useRef(0);
  const polling = useRef(false);
  const load = useCallback(async (change, visiblePanel = false) => {
    if (!change && (pendingMutation.current || (visiblePanel && polling.current))) return;
    const request = ++generation.current;
    if (change) pendingMutation.current++;
    else if (visiblePanel) polling.current = true;
    try {
      const value = await api(visiblePanel ? "/claude-accounts?panel=visible" : "/claude-accounts", change);
      if (!Array.isArray(value?.accounts)) throw new Error("无法读取 Claude 账号列表");
      if (generation.current === request) {
        setRegistry(value);
        setError("");
      }
      return value.accounts;
    } catch (e) {
      if (generation.current === request) setError(e.message);
      if (change) throw e;
    } finally {
      if (change) pendingMutation.current--;
      else if (visiblePanel) polling.current = false;
    }
  }, []);
  useEffect(() => { load(); return () => { generation.current++; }; }, [load]);
  return { accounts: registry?.accounts ?? null, detected: registry?.detected,
    detectionStatus: registry?.detection_status, detectionError: registry?.detection_error,
    usageRunning: registry?.usage_panel_running, usageUnavailable: registry?.usage_unavailable_reason,
    scheduling: registry?.scheduling || {mode:"balanced",switch_policy:"ask"}, loginNotice: registry?.login_notice, error, load };
}

const statusLabels = {
  pending: "等待登录", ready: "已登录", signed_out: "需要登录",
  duplicate: "账号已在列表中", changed: "登录身份已改变",
};
const localTimestampCache = new Map();
const timeLabel = (seconds) => {
  const lang = activeInterfaceLanguage();
  let formatter = localTimestampCache.get(lang);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat(lang, {
      month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit",
    });
    localTimestampCache.set(lang, formatter);
  }
  return formatter.format(seconds * 1000);
};

const usageUpdatedAt = (profile) => Math.max(profile.quota?.five_hour?.observed_at || 0,
  profile.quota?.seven_day?.observed_at || 0) || profile.usage_checked_at || 0;
const ageUnit = (elapsed) => elapsed < 3600 ? 60 : elapsed < 86400 ? 3600 : 86400;
function relativeTimeLabel(seconds, now) {
  const elapsed = Math.max(0, now - seconds);
  if (elapsed < 60) return translate("刚刚");
  if (elapsed < 3600) return translate("{0} 分钟前", Math.floor(elapsed / 60));
  if (elapsed < 86400) return translate("{0} 小时前", Math.floor(elapsed / 3600));
  return translate("{0} 天前", Math.floor(elapsed / 86400));
}

const QuotaTime = createContext(0);

// All rows share a clock. Only quota readings subscribe; a tick doesn't render
// the account forms or fetch data. Sleep until the next visible unit changes.
function QuotaClock({ accounts, children }) {
  const [now, setNow] = useState(() => Math.floor(Date.now() / 1000));
  useEffect(() => {
    let timer;
    const update = () => {
      clearTimeout(timer);
      if (document.hidden) return;
      const millis = Date.now();
      const seconds = Math.floor(millis / 1000);
      setNow(seconds);
      let delay = Infinity;
      for (const account of accounts || []) {
        const updated = usageUpdatedAt(account);
        if (updated > 0) {
          const elapsed = Math.max(0, seconds - updated);
          const unit = ageUnit(elapsed);
          delay = Math.min(delay, unit - elapsed % unit);
        }
        for (const window of [account.quota?.five_hour, account.quota?.seven_day]) {
          const left = window?.resets_at - seconds;
          if (left > 0) delay = Math.min(delay, left <= 60 ? 1 : (left - 1) % 60 + 1);
          const seen = window?.observed_at;
          if (window && seen && seen + 901 > seconds) delay = Math.min(delay, seen + 901 - seconds);
        }
      }
      if (Number.isFinite(delay)) timer = setTimeout(update, delay * 1000 - millis % 1000);
    };
    update();
    document.addEventListener("visibilitychange", update);
    return () => { clearTimeout(timer); document.removeEventListener("visibilitychange", update); };
  }, [accounts]);
  return <QuotaTime.Provider value={now}>{children}</QuotaTime.Provider>;
}

function resetCountdown(seconds) {
  if (seconds < 60) return translate("{0} 秒后", seconds);
  const minutes = Math.ceil(seconds / 60);
  if (minutes < 60) return translate("{0} 分钟后", minutes);
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return translate("{0} 小时 {1} 分钟后", hours, minutes % 60);
  return translate("{0} 天 {1} 小时后", Math.floor(hours / 24), hours % 24);
}

const usageLabels = {
  checking: "正在查询额度…", reported: "已获取额度", partial: "本次只返回了部分额度",
  cached: "本次只有缓存读数", unavailable: "本次未返回额度", error: "额度查询失败",
  not_applicable: "当前登录不提供订阅额度",
  identity_changed: "登录身份已改变，本次读数已丢弃", offline: "环境离线，无法查询",
  update_required: "需同步环境组件后才能查询", blocked: "暂时无法查询额度",
  signed_out: "登录后可查询额度", local: "已有会话读数", unknown: "尚未查询额度",
};

function useUsageCheck(profile, environment, perform, change) {
  const [manual, setManual] = useState(null);
  const check = () => perform(async () => {
    setManual({ state: "checking" });
    try {
      await change(profile.id ? { action: "refresh", id: profile.id } :
        { action: "refresh_default", identity: profile.identity });
      setManual(null);
    } catch (e) {
      setManual({ state: "error", previousCheck: profile.usage_checked_at, previousStatus: profile.usage_status });
      await change(); // A failed identity check can still change which default account the panel should show.
      throw e;
    }
  });
  let state = profile.usage_status || (profile.quota ? "local" : "unknown");
  if (profile.status === "changed" || profile.status === "duplicate") state = "identity_changed";
  if (profile.status === "signed_out" || profile.status === "pending") state = "signed_out";
  if (manual?.state === "error" && manual.previousCheck === profile.usage_checked_at && manual.previousStatus === profile.usage_status) state = "error";
  if (profile.usage_checking || manual?.state === "checking") state = "checking";
  if (environment) state = environment;
  return { state, check };
}

function QuotaWindow({ label, snapshot, state }) {
  const tr = useTranslation();
  const now = useContext(QuotaTime);
  const reset = snapshot?.resets_at;
  const remaining = reset ? reset - now : null;
  const expired = remaining !== null && remaining <= 0;
  const hasReading = Number.isFinite(snapshot?.used_percentage);
  const known = !expired && hasReading;
  const lastSeen = snapshot?.observed_at;
  const stale = known && (!lastSeen || now - lastSeen > 15 * 60 ||
    !["reported", "partial"].includes(state));
  const countdown = expired ? tr("等待更新") : remaining !== null ? resetCountdown(remaining) :
    known ? tr("重置时间未知") : state === "checking" ? tr("查询中") : tr("暂无读数");
  return <div className="claude-quota-row" role="group" aria-label={label + " " + tr("额度")}>
    <span className="claude-quota-period">{label}</span>
    {known
      ? <progress aria-label={label + " " + tr("已用额度")} value={snapshot.used_percentage} max={100} />
      : <span className="claude-quota-unknown" aria-hidden="true" />}
    <div className="claude-quota-reading">
      {stale && <span className="claude-quota-stale" title={lastSeen ? tr("历史读数 · {0}", (timeLabel(lastSeen))) : tr("历史读数")}>{tr("历史读数")}</span>}
      {known && <strong title={lastSeen ? tr("已用额度 · 读取于 {0}", (timeLabel(lastSeen))) : tr("已用额度")}>{snapshot.used_percentage}%</strong>}
      <span className="claude-quota-reset" title={reset ? tr("{0} 重置", (timeLabel(reset))) : tr("尚无重置时间")}>
        {remaining > 0 && <Timer size={12} aria-hidden="true" />}{countdown}
      </span>
    </div>
    {hasReading && (expired || stale || state === "partial") && <small className="claude-quota-history">
      {expired && tr("上次 {0}% ·", (snapshot.used_percentage)) + " "}
      {lastSeen > 0 ? <>{!expired && tr("读取于") + " "}<time dateTime={new Date(lastSeen * 1000).toISOString()}>{timeLabel(lastSeen)}</time></> : tr("读数时间未知")}
    </small>}
  </div>;
}

function ProfileUsage({ quota, state }) {
  return <div className="claude-quota-grid">
    <QuotaWindow label="5h" snapshot={quota?.five_hour} state={state} />
    <QuotaWindow label="7d" snapshot={quota?.seven_day} state={state} />
  </div>;
}

function UsageNote({ profile, state }) {
  const tr = useTranslation();
  const now = useContext(QuotaTime);
  const updated = usageUpdatedAt(profile);
  const statusText = usageLabels[state] ? tr(usageLabels[state]) : tr("暂无额度信息");
  return <small className="claude-quota-age" title={updated > 0 ? `${statusText} · ${timeLabel(updated)}` : statusText}>
    {updated > 0 ? <time dateTime={new Date(updated * 1000).toISOString()}>{relativeTimeLabel(updated, now)}</time> : <span>{statusText}</span>}
  </small>;
}

function DetectedProfile({ profile, usageEnvironment, busy, join, perform, change }) {
  const tr = useTranslation();
  const usage = useUsageCheck(profile, usageEnvironment, perform, change);
  return <article className="claude-profile claude-detected" aria-label={profile.email}>
    <div className="claude-profile-heading">
      <span className="claude-profile-avatar" aria-hidden="true"><img src={crabIcon} alt="" /></span>
      <div className="claude-profile-identity">
        <div className="claude-profile-name">
          <h4 title={profile.email}>{profile.email}</h4>
          {profile.plan && <span className="claude-profile-plan">{profile.plan}</span>}
        </div>
        <div className="claude-profile-meta">
          <span className="claude-profile-status">{tr("普通终端 · 已发现，未加入")}</span>
          {profile.organization && <span className="claude-profile-email" title={profile.organization}>{profile.organization}</span>}
        </div>
      </div>
    </div>
    <ProfileUsage quota={profile.quota} state={usage.state} />
    <div className="claude-profile-actions">
      <button className="quiet-button" disabled={busy} onClick={join}><Plus size={14} />{tr("加入调度")}</button>
      <button className="quiet-button" disabled={busy || !!usageEnvironment || usage.state === "checking"} onClick={usage.check}>
        <RefreshCw size={14} />{tr("检查状态")}</button>
      <UsageNote profile={profile} state={usage.state} />
    </div>
  </article>;
}

// The guest account helper reports configuration conflicts it met while
// preparing this account. Each notice disappears once it is resolved.
const noticeTexts = (tr) => ({
  resource_conflict: [tr("同名配置冲突"), tr("此账号和共享位置各有一份同名内容，两份都已保留。请把此账号中的内容合并到共享文件，再删除此账号中的这一份。")],
  link_conflict: [tr("链接指向共享位置之外"), tr("此账号中的这一项链接到共享位置之外，未作改动。确认不再需要后，删除该链接即可改用共享内容。")],
  memory_conflict: [tr("项目记忆冲突"), tr("项目记忆不一致且无法自动合并，此账号本次使用自己的项目记忆。请手动合并两份内容。")],
  memory_merged: [tr("项目记忆已自动合并"), tr("项目记忆已自动合并。此账号原来的内容已另存为带后缀的文件，备份也已保留。")],
  plugins_separate: [tr("插件单独保留"), tr("插件市场来源不同，此账号的插件单独保留。来源一致后会恢复共享。")],
  removal_blocked: [tr("已恢复未确认的删除"), tr("这些条目消失时没有卸载记录，已恢复。如确需删除，请再执行一次删除。")],
  registry_corrupt: [tr("插件登记已重建"), tr("无法读取的插件登记已移到一旁并重新建立。如有插件缺失，请重新安装。")],
  mcp_kept: [tr("已保留此账号的 MCP 服务器"), tr("共享的 MCP 服务器在没有删除记录的情况下消失，已保留此账号中的副本。")],
});

function ProfileNotices({ notices }) {
  const tr = useTranslation();
  if (!notices?.length) return null;
  const texts = noticeTexts(tr);
  return notices.map((notice) => {
    const [title, advice] = texts[notice.kind] || [tr("账号配置提示"), notice.message];
    return <p className="field-help claude-profile-notice" role="status" key={notice.id}
      title={notice.since ? timeLabel(notice.since) : undefined}>
      <strong>{title}</strong>{notice.item && <>{" · "}<code>{notice.item}</code></>}
      {notice.local && <><br />{tr("此账号：")}<code>{notice.local}</code></>}
      {notice.shared && <><br />{tr("共享位置：")}<code>{notice.shared}</code></>}
      {(advice || notice.message) && <><br />{advice || notice.message}</>}
    </p>;
  });
}

function accountCaption(profile) {
  const label = profile.label || profile.email;
  return profile.number ? `#${String(profile.number).padStart(2, "0")} ${label}` : label;
}

function ProfileCard({ proxySources, desktopNetwork, profile, environment, usageEnvironment, busy, active, defaultActive, perform, change, login, cancelDraft, removeAccount }) {
  const tr = useTranslation();
  const [renaming, setRenaming] = useState(false);
  const [configOpen, setConfigOpen] = useState(false);
  const [label, setLabel] = useState("");
  const [group, setGroup] = useState(profile.handoff_group || "");
  useEffect(() => { setGroup(profile.handoff_group || ""); }, [profile.handoff_group]);
  const displayName = profile.label || profile.email;
  const fromTerminal = profile.source === "default";
  const isDraft = !fromTerminal && !profile.email && ["pending", "signed_out", "duplicate"].includes(profile.status);
  const savedLabel = label.trim() || profile.email;
  const usage = useUsageCheck(profile, usageEnvironment, perform, change);
  const codeStatusTitle = profile.code_unlinked ? tr("Code 待授权") : "Code · " + (statusLabels[profile.status] ? tr(statusLabels[profile.status]) : tr("状态未知"));
  const desktopStatusTitle = profile.desktop_status === "ready" ? tr("Desktop 已授权") : profile.desktop_instance_id ? tr("Desktop 待登录") : tr("Desktop 首次使用时授权");
  const boundNodeName = useMemo(() => {
    if (!profile.proxy_node_id) return profile.desktop_dedicated_network ? tr("已有手动网络") : "";
    for (const source of proxySources) {
      const found = source.nodes.find(node => node.id === profile.proxy_node_id);
      if (found) return found.name;
    }
    return tr("已绑定节点不可用");
  }, [profile.proxy_node_id, profile.desktop_dedicated_network, proxySources, tr]);
  const hasRoutingConfig = Boolean((desktopNetwork && boundNodeName) || profile.handoff_group);
  return <article className="claude-profile" aria-label={accountCaption(profile)} data-enabled={profile.enabled}>
    <div className="claude-profile-heading">
      <span className="claude-profile-avatar" aria-hidden="true"><img src={crabIcon} alt="" /></span>
      <div className="claude-profile-identity">
        {renaming ? <form className="claude-profile-rename" onSubmit={(e) => {
          e.preventDefault();
          perform(async () => { await change({ action: "update", id: profile.id, label: savedLabel }); setRenaming(false); });
        }}>
          <input autoFocus aria-label={tr("账号备注")} maxLength={64} placeholder={profile.email || tr("账号名称")} value={label} onChange={(e) => setLabel(e.target.value)} />
          <button disabled={busy || !savedLabel} aria-label={tr("保存备注")}><Check size={15} /></button>
          <button type="button" disabled={busy} onClick={() => setRenaming(false)}>{tr("取消")}</button>
        </form> : <div className="claude-profile-name">
          <h4 title={accountCaption(profile)}>{accountCaption(profile)}</h4>
          {profile.plan && <span className="claude-profile-plan">{profile.plan}</span>}
          {active > 0 && <span className="claude-profile-active-chip" title={active + " " + tr("个终端仍在运行")}>{active} {tr("运行中")}</span>}
          <button type="button" className="icon-button claude-profile-edit" disabled={busy} aria-label={tr("修改 {0} 的备注", (displayName))}
            title={tr("账号备注")}
            onClick={() => { setLabel(profile.label === profile.email ? "" : profile.label || ""); setRenaming(true); }}><Pencil size={13} /></button>
        </div>}
        <div className="claude-profile-meta">
          <span className="claude-status-pill" data-ready={profile.status === "ready"} title={codeStatusTitle}>Code</span>
          <span className="claude-status-pill" data-ready={profile.desktop_status === "ready"} title={desktopStatusTitle}>Desktop</span>
          {profile.email && (profile.email !== displayName || profile.organization) && <span className="claude-profile-email" title={[profile.email, profile.organization].filter(Boolean).join(" · ")}>
            {[profile.email !== displayName && profile.email, profile.organization].filter(Boolean).join(" · ")}
          </span>}
        </div>
      </div>
      <label className="check-line claude-participation" title={profile.enabled ? tr("参与分配") : tr("仅手动选择")}>
        <input type="checkbox" aria-label={tr("参与自动分配")} checked={profile.enabled} disabled={busy}
          onChange={(e) => { const enabled = e.target.checked; perform(() => change({ action: "update", id: profile.id, enabled })); }} />
        <span className="claude-switch-track" aria-hidden="true" />
      </label>
    </div>
    <ProfileUsage quota={profile.quota} state={usage.state} />
    {hasRoutingConfig && !configOpen && (
      <div className="claude-config-chips">
        {desktopNetwork && boundNodeName && (
          <button type="button" className="claude-config-chip" onClick={() => setConfigOpen(true)} title={tr("账号出口")}>
            <Globe2 size={12} /><span>{boundNodeName}</span>
          </button>
        )}
        {profile.handoff_group && (
          <button type="button" className="claude-config-chip" onClick={() => setConfigOpen(true)} title={tr("接续分组")}>
            <Layers size={12} /><span>{profile.handoff_group}</span>
          </button>
        )}
      </div>
    )}
    {configOpen && (
      <div className="claude-profile-config">
        {desktopNetwork && <label className="claude-config-field"><span>{tr("账号出口")}</span>
          <InlineSelect inline aria-label={displayName + " " + tr("的代理节点")}
            value={profile.proxy_node_id || (profile.desktop_dedicated_network ? "legacy" : "")} disabled={busy}
            onValueChange={node => perform(() => change({ action: "update", id: profile.id, proxy_node_id: node }))}>
            <option value="">{tr("跟随环境出口")}</option>
            {profile.desktop_dedicated_network && <option value="legacy" disabled>{tr("已有手动网络")}</option>}
            {profile.proxy_node_id && !proxySources.some(source => source.nodes.some(node => node.id === profile.proxy_node_id)) &&
              <option value={profile.proxy_node_id} disabled>{tr("已绑定节点不可用")}</option>}
            {proxySources.map(source => <optgroup label={source.name} key={source.id}>
              {source.nodes.map(node => <option value={node.id} key={node.id} disabled={Boolean(node.blocked_reason)}>{node.blocked_reason ? tr("{0}（需迁移）", node.name) : node.name}</option>)}
            </optgroup>)}
          </InlineSelect>
        </label>}
        <form className="claude-handoff-group" onSubmit={event => { event.preventDefault(); perform(() => change({action:"update",id:profile.id,handoff_group:group})); }}>
          <label><span>{tr("接续分组")}</span><input aria-label={tr("{0} 的接续分组", (displayName))} value={group} maxLength={32} placeholder={tr("未分组，不跨账号接续")} onChange={event => setGroup(event.target.value)} /></label>
          <button disabled={busy || group === (profile.handoff_group || "")}>{tr("保存")}</button>
        </form>
      </div>
    )}
    {!fromTerminal && defaultActive > 0 && <p className="field-help">
      {tr("其中") + " "}{defaultActive}{" " + tr("个工作终端仍使用普通终端的登录。若普通终端已换号，请重新打开这些会话确认身份。")}
    </p>}
    {fromTerminal && ["changed", "signed_out"].includes(profile.status) && <p className="field-help">
      {profile.status === "changed" ? tr("普通终端已换号，此账号暂不参与分配。新身份将单独归档。") : tr("普通终端已退出。要继续使用此账号，请先重新登录原身份。")}
      {active > 0 && " " + tr("原工作终端仍保留首次分配的标签。请重新打开工作会话确认身份。")}
    </p>}
    {!fromTerminal && profile.status === "changed" && <p className="field-help">{tr("请用原账号重新登录。")}</p>}
    {profile.status === "duplicate" && <p className="field-help">{tr("同一订阅只参与一次分配。请在此位置登录另一个账号。")}</p>}
    <ProfileNotices notices={profile.notices} />
    <div className="claude-profile-actions">
      {fromTerminal ? <span className="claude-profile-source">{tr("普通终端账号")}</span> :
        <button className="quiet-button" disabled={busy || !!environment || active > 0} onClick={() => perform(() => login(profile.id))}>
          <TerminalSquare size={14} />{profile.status === "ready" ? tr("重新授权 Code") : tr("授权 Code")}</button>}
      <button className="quiet-button" disabled={busy || !!usageEnvironment || usage.state === "checking"} onClick={usage.check}>
        <RefreshCw size={14} className={usage.state === "checking" ? "spin" : undefined} />{tr("检查状态")}</button>
      <button type="button" className={`quiet-button claude-config-toggle ${configOpen ? "open" : ""}`} aria-expanded={configOpen} onClick={() => setConfigOpen(!configOpen)}>
        <SlidersHorizontal size={13} />{tr("设置")}
        {hasRoutingConfig && <i className="claude-config-dot" aria-hidden="true" />}
      </button>
      <UsageNote profile={profile} state={usage.state} />
      {isDraft ? (
        !active && <button className="text-button" disabled={busy} onClick={() => perform(() => cancelDraft(profile))}>{tr("撤销添加")}</button>
      ) : (
        <button
          type="button"
          className="icon-button claude-profile-delete"
          disabled={busy || active > 0}
          aria-label={tr("删除账号")}
          title={active > 0 ? tr("请先关闭该账号的终端，再删除账号") : tr("删除账号")}
          onClick={(event) => perform(() => removeAccount(profile, event.currentTarget))}
        >
          <Trash2 size={14} />
        </button>
      )}
    </div>
  </article>;
}

export function ClaudeAccountsPanel({ proxySources, desktopNetwork, store, running, environmentBusy, tabs, login, enrollmentID, cancelLogin, onConfirm }) {
  const tr = useTranslation();
  const { accounts, detected, detectionStatus, detectionError, loginNotice, usageRunning, usageUnavailable, load, error } = store;
  const [busy, setBusy] = useState(false);
  const [adding, setAdding] = useState(false);
  const [schedulingOpen, setSchedulingOpen] = useState(false);
  const [node, setNode] = useState("");
  const nodeAvailable = !node || proxySources.some(source => source.nodes.some(n => n.id === node && !n.blocked_reason));
  const [requestingUsage, setRequestingUsage] = useState(false);
  const [actionError, setActionError] = useState("");
  const visibleProfiles = useMemo(() => detected ? [detected, ...(accounts || [])] : accounts, [accounts, detected]);
  const environment = !running || detectionStatus === "offline" ? "offline" :
    detectionStatus === "update_required" ? "update_required" : "";
  const usageEnvironment = environment || (usageUnavailable ? "blocked" : "");
  const checking = !usageEnvironment && (usageRunning || visibleProfiles?.some((profile) => profile.usage_checking));
  const activeCounts = useMemo(() => {
    const counts = new Map();
    for (const tab of tabs) {
      if (!tab.claude_account_id || tab.closed) continue;
      const count = counts.get(tab.claude_account_id) || { all: 0, fromDefault: 0 };
      count.all++;
      if (tab.claude_account_source === "default") count.fromDefault++;
      counts.set(tab.claude_account_id, count);
    }
    return counts;
  }, [tabs]);
  useEffect(() => {
    let requested = false, mounted = true;
    const openPanel = () => {
      if (requested || document.hidden) return;
      requested = true;
      setRequestingUsage(running);
      if (running) {
        load({ action: "usage_panel" })
          .catch(() => {})
          .finally(() => { if (mounted) setRequestingUsage(false); });
      } else load();
    };
    openPanel();
    document.addEventListener("visibilitychange", openPanel);
    return () => { mounted = false; document.removeEventListener("visibilitychange", openPanel); };
  }, [load, running]);
  useEffect(() => {
    const timer = setInterval(() => { if (!document.hidden) load(undefined, true); }, checking ? 2000 : 12000);
    return () => clearInterval(timer);
  }, [load, checking]);
  const perform = async (operation) => {
    if (busy) return;
    setBusy(true);
    setActionError("");
    try { await operation(); }
    catch (e) { setActionError(e.message); }
    finally { setBusy(false); }
  };
  const cancelDraft = async (profile) => {
    const last = accounts.length === 1;
    if (await onConfirm({ title: translate("撤销添加「{0}」？", profile.label),
      body: last
        ? "这是列表中的最后一个账号。撤销后，新建 Claude 终端会沿用环境原有的登录。虚拟机中的资料会保留。"
        : "此账号会从列表中移除，虚拟机中的资料会保留。",
      confirm: "撤销添加" })) {
      await load({ action: "cancel", id: profile.id });
    }
  };
  const removeAccount = async (profile, anchor) => {
    const last = accounts.length === 1;
    const displayName = profile.label || profile.email;
    const hasIsolatedDesktop = Boolean(profile.desktop_instance_id && profile.desktop_instance_id !== "default");
    await onConfirm({
      title: translate("删除账号「{0}」？", displayName),
      body: last
        ? translate("这是列表中的最后一个账号。删除后会从账号管理与调度中移除，新建 Claude 终端将沿用环境原有的登录。虚拟机内的会话历史和资料会保留。")
        : hasIsolatedDesktop
          ? translate("删除后会从账号管理、调度和 Desktop 列表中移除此账号。请先关闭该账号的终端与 Desktop；虚拟机内的会话历史和资料会保留。")
          : translate("删除后会从账号管理与调度中移除此账号。虚拟机内的会话历史和资料会保留。"),
      confirm: translate("删除账号"),
      pendingLabel: translate("正在删除…"),
      danger: true,
      anchor,
      submit: async () => {
        await load({ action: "delete", id: profile.id });
      },
    });
  };
  const joinDetected = async () => {
    try { await load({ action: "join", identity: detected.identity }); }
    catch (e) {
      await load(); // The identity shown when the user clicked may no longer be signed in.
      throw e;
    }
  };
  return <section className="claude-profiles" aria-label={tr("Claude 账号管理")}>
    <div className="claude-toolbar">
      <div className="claude-toolbar-title">
        <h4>{tr("账号列表")}<span>{visibleProfiles?.length ?? "—"}</span></h4>
        {requestingUsage || checking ? (
          <small className="claude-toolbar-status" role="status"><RefreshCw size={12} className="spin" />{tr("正在查询账号用量…")}</small>
        ) : accounts?.length > 0 && (
          <small className="claude-toolbar-status">{accounts.filter((a) => a.enabled).length}{" " + tr("个启用调度")}</small>
        )}
      </div>
      <div className="claude-toolbar-actions">
        <button type="button" className={`claude-scheduling-trigger ${schedulingOpen ? "open" : ""}`} aria-expanded={schedulingOpen} onClick={() => setSchedulingOpen(!schedulingOpen)}>
          <SlidersHorizontal size={14} />
          <span>{store.scheduling.mode === "sequential" ? tr("顺序优先") : tr("均衡分配")}</span>
        </button>
        <button className="primary" disabled={busy || !!enrollmentID || environmentBusy || !!environment}
          aria-expanded={adding} onClick={() => setAdding(!adding)}><Plus size={15} />{tr("添加账号")}</button>
      </div>
    </div>
    {schedulingOpen && (
      <div className="claude-scheduling-card">
        <div className="claude-scheduling-grid">
          <label>{tr("新会话分配")}<InlineSelect inline aria-label={tr("新会话分配")} value={store.scheduling.mode} disabled={busy}
            onValueChange={mode => perform(() => load({action:"scheduling",scheduling:{...store.scheduling,mode}}))}>
            <option value="balanced">{tr("均衡分配")}</option><option value="sequential">{tr("按列表顺序优先")}</option>
          </InlineSelect></label>
          <label>{tr("新会话切换授权")}<InlineSelect inline aria-label={tr("新会话切换授权")} value={store.scheduling.switch_policy} disabled={busy}
            onValueChange={switch_policy => perform(() => load({action:"scheduling",scheduling:{...store.scheduling,switch_policy}}))}>
            <option value="ask">{tr("每次询问")}</option><option value="allow">{tr("额度耗尽后自动接续")}</option><option value="wait">{tr("始终等待原账号")}</option>
          </InlineSelect></label>
        </div>
        {store.scheduling.mode === "sequential" && accounts?.length > 1 && <div className="claude-account-order" aria-label={tr("账号优先顺序")}>
          {accounts.map((profile,index) => <div key={profile.id}><span>{index+1}. {accountCaption(profile)}</span>
            <button disabled={busy || index===0} aria-label={tr("提高 {0} 的优先级", (profile.label))} onClick={() => {
              const order=accounts.map(a=>a.id); [order[index-1],order[index]]=[order[index],order[index-1]];
              perform(() => load({action:"scheduling",scheduling:store.scheduling,order}));
            }}>{tr("上移")}</button></div>)}
        </div>}
      </div>
    )}
    {adding && <form className="claude-add-form" onSubmit={e=>{
      e.preventDefault();
      if (nodeAvailable) perform(async()=>{await login(undefined,node);setAdding(false);});
    }}>
      {desktopNetwork && !!proxySources.length && <label>{tr("账号出口")}<InlineSelect inline aria-label={tr("新账号出口")} value={node} onValueChange={setNode}>
        <option value="">{tr("跟随环境出口")}</option>
        {proxySources.map(source=><optgroup key={source.id} label={source.name}>{source.nodes.map(n=><option value={n.id} key={n.id} disabled={Boolean(n.blocked_reason)}>{n.blocked_reason ? tr("{0}（需迁移）", n.name) : n.name}</option>)}</optgroup>)}
      </InlineSelect></label>}
      <div><button className="primary" disabled={busy || !!environment || !nodeAvailable}>{tr("开始授权")}</button>
        <button type="button" onClick={()=>setAdding(false)}>{tr("取消")}</button></div>
    </form>}
    {enrollmentID && <div className="claude-add-profile"><span role="status">{tr("正在等待 Claude 授权…")}</span><button onClick={() => perform(cancelLogin)}>{tr("取消授权")}</button></div>}
    {(actionError || error) && <p className="account-error" role="alert">{actionError || error}</p>}
    {loginNotice && <p className="claude-profile-note" role="status">{loginNotice}</p>}
    {detectionError && <p className="account-error" role="alert">{detectionError}</p>}
    {usageUnavailable && !environment && <p className="claude-profile-note" role="status">{usageUnavailable}</p>}
    {!running ? <p className="claude-profile-note">{tr("启动 Cage 环境后可检测普通终端的登录，也可登录新账号。")}</p> :
      detectionStatus === "offline" ? <p className="claude-profile-note">{tr("工作环境就绪后可检测和登录账号。")}</p> :
      detectionStatus === "signed_out" && !accounts?.length && <p className="claude-profile-note">{tr("普通终端尚未登录 Claude 订阅。可在其中运行") + " "}<code>claude auth login</code>{tr("，登录后会在这里出现。")}</p>}
    <div className="claude-profile-list" role="region" aria-label={tr("账号列表")} tabIndex={0}>
      {accounts === null && !error && <p role="status">{tr("正在读取账号…")}</p>}
      {accounts?.length === 0 && !detected && <div className="claude-profile-empty">
        <img src={crabIcon} alt="" /><p>{tr("尚未添加账号")}</p><small>{tr("登录成功后，账号会出现在这里。")}</small>
      </div>}
      <QuotaClock accounts={visibleProfiles}>
        {detected && <DetectedProfile key={detected.identity} profile={detected} usageEnvironment={usageEnvironment} busy={busy || environmentBusy || !!environment}
          perform={perform} change={load}
          join={() => perform(joinDetected)} />}
        {accounts?.map((profile) => <ProfileCard proxySources={proxySources} desktopNetwork={desktopNetwork} key={profile.id} profile={profile} environment={environment} usageEnvironment={usageEnvironment}
          busy={busy || environmentBusy} perform={perform} change={load} login={login} cancelDraft={cancelDraft} removeAccount={removeAccount}
          active={activeCounts.get(profile.id)?.all || 0} defaultActive={activeCounts.get(profile.id)?.fromDefault || 0} />)}
      </QuotaClock>
    </div>
  </section>;
}
