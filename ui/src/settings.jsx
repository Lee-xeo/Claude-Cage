import { MobilePairSettings } from "./mobile-pair-settings";
import { ArchivedSessions } from "./cli-sessions";
import { Smartphone } from "lucide-react";
import { PasswordVaultSettings } from "./password-vault";
import { KeyRound } from "lucide-react";
import { InlineSelect } from "./inline-select";
import { LanguageSettings } from "./language-settings";
import { clientKeyboard, isRemoteClient, saveClientKeyboard } from "./client-context.mjs";
import { shortcut, usesControl } from "./platform.mjs";
import React, { useEffect, useMemo, useRef, useState } from "react";
import {
  Bell,
  ChevronDown,
  Globe2,
  Clock3,
  FolderOpen,
  Check,
  LoaderCircle,
  RotateCcw,
  ArrowLeft,
  Settings,
  SlidersHorizontal,
  Palette,
  UserRound,
  Archive,
  Info,
} from "lucide-react";
import { api } from "./api";
import "./settings.css";
import { SafetyMessage } from "./safety-message";
import { AppearanceSettings } from "./appearance-settings";
import { NotificationSettings } from "./notification-settings";
import { ProxySettings } from "./proxy-settings";
import { ExitPolicyFields } from "./exit-policy";
import { canonicalExitPolicy } from "./safety-state.mjs";
import { EnvironmentVersions } from "./environment-versions";
import { EnvironmentStorage } from "./environment-storage";
import { EnvironmentCache } from "./environment-cache";
import { EnvironmentDeviceReset } from "./environment-device-reset";
import { ResourceSettings } from "./resources";
import { DiagnosticSettings } from "./diagnostics.jsx";
import { AccountSettings } from "./account";
import { AboutSettings } from "./about-settings";
import { useTranslation } from "./interface-language.mjs";
const sectionGroups = [
  [
    ["account", "账户", UserRound],
    ["vault", "密码保险箱", KeyRound],
    ["archived-sessions", "已归档会话", Archive],
  ],
  [
    ["keyboard", "通用", Settings],
    ["appearance", "外观", Palette],
    ["notifications", "通知", Bell],
    ["mobile", "远程连接", Smartphone],
  ],
  [
    ["network", "网络与代理", Globe2],
    ["region", "时区与语言", Clock3],
    ["project", "环境与恢复", RotateCcw],
    ["about", "关于", Info],
  ],
];
export function SettingsPanel({
  current,
  status,
  workspace = "",
  onClose,
  onSaved,
  onConfirm,
  initialSection = "keyboard",
}) {
  const tr = useTranslation();
  const activeWorkspace = workspace || status?.workspace || current.workspace || status?.projects?.[0] || "";
  const [localKeyboard, setLocalKeyboard] = useState(() => clientKeyboard(current.keyboard));
  const [accountDirty, setAccountDirty] = useState(false);
  const [proxyDirty, setProxyDirty] = useState(false);
  const [resourceDirty, setResourceDirty] = useState(false);
  const [draft, setDraft] = useState({ ...current }),
    [section, setSection] = useState(initialSection),
    [probe, setProbe] = useState(null),
    [probing, setProbing] = useState(false),
    [probeFailed, setProbeFailed] = useState(false),
    [error, setError] = useState(""),
    [staticDraftOffer, setStaticDraftOffer] = useState(false),
    [jobId, setJobId] = useState(""),
    [submitting, setSubmitting] = useState(false);
  const changed =
    JSON.stringify({
      ...draft,
      browser_home_url: undefined,
      exit_policy: canonicalExitPolicy(draft.exit_policy),
    }) !==
    JSON.stringify({
      ...current,
      browser_home_url: undefined,
      exit_policy: canonicalExitPolicy(current.exit_policy),
    });
  const environmentChanged = [
    "dns_url",
    "dns_fallback_urls",
    "dns_public_fallback",
    "dns_bootstrap_urls",
    "timezone",
    "locale",
    "browser_language",
    "auto_timezone",
    "auto_language",
    "exit_policy",
  ].some((k) =>
    k === "exit_policy"
      ? JSON.stringify(canonicalExitPolicy(draft[k])) !==
        JSON.stringify(canonicalExitPolicy(current[k]))
      : Array.isArray(draft[k]) || Array.isArray(current[k])
        ? JSON.stringify(draft[k] ?? []) !== JSON.stringify(current[k] ?? [])
        : draft[k] !== current[k],
  );
  const busy =
    Boolean(status.job?.running) || Boolean(status.storage_recovery?.required) || probing || submitting || Boolean(jobId);
  // Project folders of the other guest systems are always read directly.
  const update = (key, value) => {
    setDraft((p) => ({ ...p, [key]: value }));
    if (["dns_url", "dns_fallback_urls", "dns_public_fallback"].includes(key)) setProbe(null);
    setError("");
  };
  const close = async () => {
    if (submitting) return;
    if (status.job?.running && jobId) {
      onClose();
      return;
    }
    if (
      (!changed && !accountDirty && !proxyDirty && !resourceDirty) ||
      (await onConfirm({
        title: "放弃尚未保存的修改？",
        body: "当前环境不会改变。关闭后会丢弃这次表单修改。",
        confirm: "放弃修改",
      }))
    )
      onClose();
  };
  useEffect(() => {
    if (jobId && status.job?.id === jobId && !status.job.running) {
      setJobId("");
      if (status.job.error || status.job.canceled) {
        if (status.job.settings_saved) onSaved();
        setStaticDraftOffer(status.job.recovery === "static-ip-draft-available");
        setError(status.job.error || status.job.message);
      } else {
        setStaticDraftOffer(false);
        onSaved();
        if (!proxyDirty) onClose();
      }
    }
  }, [status.job, jobId]);
  const check = async () => {
    setProbing(true);
    setProbeFailed(false);
    setError("");
    try {
      const result = await api("/settings/probe", draft);
      setProbe(result);
      if (draft.auto_timezone)
        setDraft((previous) => ({ ...previous, timezone: result.timezone }));
      if (draft.auto_language && result.suggested_locale)
        setDraft((previous) => ({
          ...previous,
          locale: result.suggested_locale,
          browser_language: result.suggested_browser_language,
        }));
    } catch (e) {
      setError(e.message);
      setProbeFailed(true);
    } finally {
      setProbing(false);
    }
  };
  const save = async ({ saveStaticDraft = false } = {}) => {
    if (busy) return;
    if (saveStaticDraft) {
      if (
        !(await onConfirm({
          title: "离线保存待验证的静态 IP 草稿？",
          body: "仅保存静态 IP 基准，不会放行外网。待代理恢复并通过实时出口、国家与隔离检查后才允许联网。",
          confirm: "保存待验证草稿",
        }))
      )
        return;
    } else if (
      environmentChanged &&
      status.vm_running &&
      !(await onConfirm({
        title: "应用设置并重启环境？",
        body: "请先保存编辑器中的文件。当前终端会话会结束，浏览器的页面和登录状态将保留。",
        confirm: "保存并重启",
      }))
    )
      return;
    setError("");
    setStaticDraftOffer(false);
    setSubmitting(true);
    try {
      const payload = { ...draft };
      delete payload.browser_home_url;
      if (saveStaticDraft) payload.save_static_draft = true;
      const result = await api("/settings", payload);
      setJobId(result.id);
    } catch (e) {
      setError(e.message);
    } finally {
      setSubmitting(false);
    }
  };
  const zones = useMemo(
    () => [
      ...new Set([
        "Etc/UTC",
        current.timezone,
        ...(Intl.supportedValuesOf?.("timeZone") || []),
      ]),
    ],
    [],
  );
  const contentRef = useRef();
  useEffect(() => {
    contentRef.current?.scrollTo(0, 0);
  }, [section]);
  const currentLoc = probe || status.location;
  const closeRef = useRef(close);
  closeRef.current = close;
  useEffect(() => {
    const previous = document.activeElement;
    document.getElementById("settings-back")?.focus();
    const dismiss = (event) => {
      if (document.querySelector('[role="dialog"]')) return;
      if (event.type === "cage-close-modal" || event.key === "Escape") {
        event.preventDefault();
        closeRef.current();
      }
    };
    document.addEventListener("keydown", dismiss);
    document.addEventListener("cage-close-modal", dismiss);
    return () => {
      document.removeEventListener("keydown", dismiss);
      document.removeEventListener("cage-close-modal", dismiss);
      previous?.focus?.();
    };
  }, []);
  return (
    <section className="settings-page" aria-label={tr("设置")}>
      <aside className="settings-sidebar">
        <button
          id="settings-back"
          className="back-to-workspace"
          onClick={close}
        >
          <ArrowLeft size={17} />
          {tr("返回工作台")}
        </button>
        <h2>{tr("设置")}</h2>
        <nav aria-label={tr("设置分组")}>
          {sectionGroups.map((group, groupIndex) => (
            <div className="settings-nav-group" key={groupIndex}>
              {groupIndex > 0 && <div className="settings-nav-divider" aria-hidden="true" />}
              {group.map(([id, label, Icon]) => (
                <button
                  key={id}
                  className={section === id ? "selected" : ""}
                  aria-current={section === id ? "page" : undefined}
                  onClick={async () => {
                    if (id !== section && accountDirty && !(await onConfirm({ title: "放弃尚未保存的修改？", body: "已保存的资料和密码保持不变。", confirm: "放弃修改" }))) return;
                    if (id !== section && proxyDirty && !(await onConfirm({ title: "放弃尚未应用的代理修改？", body: "当前代理仍按已应用的链路运行。离开会丢弃这次节点选择。", confirm: "放弃修改" }))) return;
                    setSection(id);
                  }}
                >
                  <Icon size={16} />
                  {tr(label)}
                </button>
              ))}
            </div>
          ))}
        </nav>
      </aside>
      <div className="settings-main">
        <div className={section === "archived-sessions" ? "settings-content settings-content--archives" : section === "account" ? "settings-content settings-content--account" : section === "mobile" ? "settings-content settings-content--remote" : "settings-content"} ref={contentRef}>
          {section === "appearance" && <AppearanceSettings />}
          {section === "notifications" && <NotificationSettings />}
          {section === "about" && <AboutSettings />}
          {section === "archived-sessions" && <ArchivedSessions ready={status.ready && !busy} />}
          {section === "vault" && <PasswordVaultSettings onConfirm={onConfirm} onDirty={setAccountDirty} />}
          {section === "account" && <AccountSettings busy={busy} onDirty={setAccountDirty} onConfirm={onConfirm} />}
          <fieldset className="settings-fields" disabled={busy && !(section === "project" && status.storage_recovery?.required)}>
            {section === "mobile" && (isRemoteClient()
              ? <section className="section-title"><h3>{tr("连接这台电脑")}</h3><p>{tr("请先切换到这台电脑的本机环境，再生成或管理手机配对码。")}</p></section>
              : <MobilePairSettings relay={draft.mobile_relay ?? ""} savedRelay={current.mobile_relay ?? ""} onChange={value => update("mobile_relay",value)} onConfirm={onConfirm} />)}
            {section === "network" && (
              <>
                <ProxySettings
                  onDirty={setProxyDirty}
                  status={status}
                  onConfirm={onConfirm}
                  onApplied={async () => {
                    setProbe(null);
                    const applied = await api("/settings");
                    if (JSON.stringify(canonicalExitPolicy(applied.exit_policy)) !==
                        JSON.stringify(canonicalExitPolicy(current.exit_policy))) {
                      setDraft(previous => ({ ...previous, exit_policy: applied.exit_policy }));
                    }
                    await onSaved();
                  }}
                />
                <div className="settings-card settings-exit-card">
                  <div className="settings-row">
                    <div className="settings-row-copy">
                      <strong>{currentLoc ? `${currentLoc.country} · ${currentLoc.city}` : tr("验证出口")}</strong>
                      <small className="settings-exit-meta">
                        {currentLoc ? (
                          <>
                            <code>{currentLoc.ip}</code>
                            <span>{currentLoc.timezone}</span>
                            <span>{new Date(currentLoc.checked_at).toLocaleTimeString(document.documentElement.lang, { hour: "2-digit", minute: "2-digit" })}</span>
                            {probe && <span className="settings-ok-pill"><Check size={12} />{tr("验证通过")}</span>}
                          </>
                        ) : tr("通过当前节点查询 IP 和位置")}
                      </small>
                    </div>
                    <div className="settings-row-control">
                      <button disabled={busy} onClick={check}>
                        {probing ? <LoaderCircle size={15} className="spin" /> : <Globe2 size={15} />}
                        {tr("验证出口")}
                      </button>
                    </div>
                  </div>
                </div>
                <section className="network-exit-policy" aria-label={tr("IP 变化规则")}>
                  <ExitPolicyFields
                    value={draft.exit_policy}
                    onChange={(value) => update("exit_policy", value)}
                    observation={probe}
                    onProbe={check}
                    busy={busy}
                    probing={probing}
                  />
                </section>
                <details className="settings-disclosure-card">
                  <summary>
                    <SlidersHorizontal size={15} />
                    <span>{tr("加密 DNS")}</span>
                    <small>{draft.dns_url ? draft.dns_url.replace(/^https?:\/\//, "").split("/")[0] : ""}</small>
                    <ChevronDown size={15} className="settings-disclosure-chevron" />
                  </summary>
                  <div className="settings-disclosure-body">
                    <label>
                      {tr("加密 DNS")}
                      <input
                        value={draft.dns_url}
                        onChange={(e) => update("dns_url", e.target.value)}
                        spellCheck="false"
                      />
                    </label>
                    <label>
                      {tr("备用加密 DNS（每行一个，最多 4 个）")}
                      <textarea
                        rows={2}
                        value={(draft.dns_fallback_urls ?? []).join("\n")}
                        onChange={(e) => update("dns_fallback_urls", e.target.value.split(/\r?\n/))}
                        spellCheck="false"
                        placeholder="https://…/dns-query"
                      />
                    </label>
                    <label>
                      {tr("节点域名解析（每行一个数字 IP 的 DoH，留空使用默认）")}
                      <textarea
                        rows={2}
                        value={(draft.dns_bootstrap_urls ?? []).join("\n")}
                        onChange={(e) => update("dns_bootstrap_urls", e.target.value.split(/\r?\n/))}
                        spellCheck="false"
                        placeholder="https://223.5.5.5/dns-query"
                      />
                    </label>
                    <div className="settings-card settings-card--nested">
                      <label className="settings-row settings-switch-row" title={tr("业务 DNS、联网检查与内置核心使用同一组端点，均经所选节点发出，按顺序尝试。默认只使用上面的加密 DNS；开启公共 DNS 备用后，查询可能发给第二家服务商。")}>
                        <span className="settings-row-copy">
                          <strong>{tr("以上端点都不可用时改用 Google 公共 DNS（8.8.8.8）")}</strong>
                        </span>
                        <input
                          role="switch"
                          type="checkbox"
                          checked={Boolean(draft.dns_public_fallback)}
                          onChange={(e) => update("dns_public_fallback", e.target.checked)}
                        />
                      </label>
                      <label className="settings-row settings-switch-row" title={tr("默认关闭。仅当所选代理出口会中断 curl 的 TLS 1.3 握手、导致安装或工具下载失败时开启；不影响正常浏览与对话连接。")}>
                        <span className="settings-row-copy">
                          <strong>{tr("安装与工具下载兼容模式（限制 curl 最高使用 TLS 1.2）")}</strong>
                        </span>
                        <input
                          role="switch"
                          type="checkbox"
                          checked={Boolean(draft.install_tls12_compat)}
                          onChange={(e) => update("install_tls12_compat", e.target.checked)}
                        />
                      </label>
                      <label className="settings-row settings-switch-row" title={tr("默认关闭。仅当所选代理链路在 TLS 握手阶段返回 EOF 或间歇性提示“出口待确认”时开启：保留完整后量子加密握手与代理隔离，仅对超过 1000 字节的 TLS ClientHello 分段发送。")}>
                        <span className="settings-row-copy">
                          <strong>{tr("TLS ClientHello 分段传输兼容模式")}</strong>
                        </span>
                        <input
                          role="switch"
                          type="checkbox"
                          checked={Boolean(draft.tls_client_hello_split)}
                          onChange={(e) => update("tls_client_hello_split", e.target.checked)}
                        />
                      </label>
                      <label className="settings-row settings-switch-row" title={tr("默认关闭。开启后，仅当默认的五家位置服务都失败时，经所选节点把已测得的出口 IP 发给 ipinfo.io 查询国家或地区；其结果只用于国家级判断，不用于绑定或核对州、省和城市。")}>
                        <span className="settings-row-copy">
                          <strong>{tr("出口位置查询备用服务 ipinfo.io")}</strong>
                        </span>
                        <input
                          role="switch"
                          type="checkbox"
                          checked={Boolean(draft.location_ipinfo_fallback)}
                          onChange={(e) => update("location_ipinfo_fallback", e.target.checked)}
                        />
                      </label>
                    </div>
                  </div>
                </details>
              </>
            )}
            {section === "region" && (
              <>
                <div className="section-title">
                  <h3>{tr("时区与语言")}</h3>
                  <p>{tr("这些设置作用于 Cage 内的系统、CLI 和浏览器。")}</p>
                </div>
                <div className="settings-card">
                  <label className="settings-row settings-switch-row">
                    <span className="settings-row-copy">
                      <strong>{tr("跟随出口 IP 的时区")}</strong>
                      <small>{probe && draft.auto_timezone ? probe.timezone : tr("在启动或应用设置时重新查询，不会静默重启正在运行的任务。")}</small>
                    </span>
                    <span className="settings-row-inline">
                      {draft.auto_timezone && (
                        <button type="button" className="quiet-button" onClick={check} disabled={busy}>
                          {probing ? <LoaderCircle size={13} className="spin" /> : <Clock3 size={13} />}
                          {tr("现在检测")}
                        </button>
                      )}
                      <input
                        role="switch"
                        type="checkbox"
                        checked={draft.auto_timezone}
                        onChange={(e) => update("auto_timezone", e.target.checked)}
                      />
                    </span>
                  </label>
                  <div className="settings-row">
                    <div className="settings-row-copy">
                      <strong>{draft.auto_timezone ? tr("将应用的时区") : tr("系统时区")}</strong>
                    </div>
                    <div className="settings-row-control settings-row-control--wide">
                      <InlineSelect editable
                        aria-label={draft.auto_timezone ? tr("将应用的时区") : tr("系统时区")}
                        value={draft.timezone}
                        disabled={draft.auto_timezone}
                        onValueChange={(next) => update("timezone", next)}
                      >
                        {zones.map((zone) => (
                          <option key={zone} value={zone} />
                        ))}
                      </InlineSelect>
                    </div>
                  </div>
                </div>
                <div className="settings-card">
                  <label className="settings-row settings-switch-row">
                    <span className="settings-row-copy">
                      <strong>{tr("跟随出口地区的语言建议")}</strong>
                      {draft.auto_language && currentLoc?.suggested_locale && (
                        <small>{`${currentLoc.suggested_locale} / ${currentLoc.suggested_browser_language}`}</small>
                      )}
                    </span>
                    <span className="settings-row-inline">
                      {draft.auto_language && (
                        <button type="button" className="quiet-button" onClick={check} disabled={busy}>
                          {probing ? <LoaderCircle size={13} className="spin" /> : <Globe2 size={13} />}
                          {tr("现在检测")}
                        </button>
                      )}
                      <input
                        role="switch"
                        type="checkbox"
                        checked={Boolean(draft.auto_language)}
                        onChange={(e) => update("auto_language", e.target.checked)}
                      />
                    </span>
                  </label>
                  <div className="settings-row">
                    <div className="settings-row-copy">
                      <strong>{tr("系统语言")}</strong>
                    </div>
                    <div className="settings-row-control">
                      <InlineSelect
                        aria-label={tr("系统语言")}
                        value={draft.locale}
                        disabled={draft.auto_language}
                        onValueChange={(next) => update("locale", next)}
                      >
                        {[
                          ["en_US.UTF-8", "English (United States)"],
                          ["zh_CN.UTF-8", "简体中文"],
                          ["zh_TW.UTF-8", "繁體中文"],
                          ["ja_JP.UTF-8", "日本語"],
                          ["de_DE.UTF-8", "Deutsch"],
                          ["fr_FR.UTF-8", "Français"],
                        ].map(([value, label]) => (
                          <option key={value} value={value}>
                            {tr(label)}
                          </option>
                        ))}
                        {![
                          "en_US.UTF-8",
                          "zh_CN.UTF-8",
                          "zh_TW.UTF-8",
                          "ja_JP.UTF-8",
                          "de_DE.UTF-8",
                          "fr_FR.UTF-8",
                        ].includes(draft.locale) && (
                          <option value={draft.locale}>{draft.locale}</option>
                        )}
                      </InlineSelect>
                    </div>
                  </div>
                  <div className="settings-row">
                    <div className="settings-row-copy">
                      <strong>{tr("网页首选语言")}</strong>
                    </div>
                    <div className="settings-row-control">
                      <InlineSelect
                        aria-label={tr("网页首选语言")}
                        value={draft.browser_language}
                        disabled={draft.auto_language}
                        onValueChange={(next) => update("browser_language", next)}
                      >
                        {[
                          ["en-US", "English (United States)"],
                          ["zh-CN", "简体中文"],
                          ["zh-TW", "繁體中文"],
                          ["ja-JP", "日本語"],
                          ["de-DE", "Deutsch"],
                          ["fr-FR", "Français"],
                        ].map(([value, label]) => (
                          <option key={value} value={value}>
                            {tr(label)}
                          </option>
                        ))}
                        {![
                          "en-US",
                          "zh-CN",
                          "zh-TW",
                          "ja-JP",
                          "de-DE",
                          "fr-FR",
                        ].includes(draft.browser_language) && (
                          <option value={draft.browser_language}>
                            {draft.browser_language}
                          </option>
                        )}
                      </InlineSelect>
                    </div>
                  </div>
                </div>
              </>
            )}
            {section === "keyboard" && (
              <>
                <div className="section-title">
                  <h3>{tr("通用")}</h3>
                </div>
                <div className="settings-card">
                  <LanguageSettings row />
                  <div className="settings-row">
                    <div className="settings-row-copy">
                      <strong>{tr("时间显示格式")}</strong>
                    </div>
                    <div className="settings-segmented" role="group" aria-label={tr("时间显示格式")}>
                      <button
                        type="button"
                        aria-pressed={!draft.clock_12_hour}
                        onClick={() => update("clock_12_hour", false)}
                      >
                        {tr("24 小时制")}
                      </button>
                      <button
                        type="button"
                        aria-pressed={Boolean(draft.clock_12_hour)}
                        onClick={() => update("clock_12_hour", true)}
                      >
                        {tr("12 小时制（AM/PM）")}
                      </button>
                    </div>
                  </div>
                  <div className="settings-row">
                    <div className="settings-row-copy">
                      <strong>{tr("Claude 终端启动命令")}</strong>
                    </div>
                    <div className="settings-row-control settings-row-control--wide">
                      <input
                        className="settings-mono-input"
                        aria-label={tr("Claude 终端启动命令")}
                        value={draft.claude_terminal_command ?? "claude"}
                        onChange={(e) => update("claude_terminal_command", e.target.value)}
                        placeholder="claude"
                        maxLength={4096}
                        spellCheck="false"
                        autoComplete="off"
                      />
                    </div>
                  </div>
                </div>
                <div className="settings-card">
                  {typeof current.reuse_claude_login === "boolean" && <label className="settings-row settings-switch-row" title={tr("每次授权使用独立的浏览器会话。关闭后可复用登录状态。")}>
                    <span className="settings-row-copy">
                      <strong>{tr("使用无痕标签页登录 Claude")}</strong>
                      <small>{tr("每次授权使用独立的浏览器会话。关闭后可复用登录状态。")}</small>
                    </span>
                    <input type="checkbox" role="switch" checked={!draft.reuse_claude_login}
                      onChange={(event) => update("reuse_claude_login", !event.target.checked)} />
                  </label>}
                  {typeof current.balanced_display === "boolean" && <label className="settings-row settings-switch-row" title={tr("降低浏览器和 Claude Desktop 的清晰度，Linux 环境同时减少 Claude Desktop 的动效。下次启动环境时生效。")}>
                    <span className="settings-row-copy">
                      <strong>{tr("优先流畅")}</strong>
                      <small>{tr("下次启动环境时生效。")}</small>
                    </span>
                    <input type="checkbox" role="switch" checked={draft.balanced_display}
                      onChange={(event) => update("balanced_display", event.target.checked)} />
                  </label>}
                  {typeof current.project_file_cache === "boolean" && <label className="settings-row settings-switch-row" title={tr("减少读取项目文件时与本机的往返。在本机上所做的改动最多 20 秒后在环境中可见，也可以在项目栏点“刷新项目文件”立即同步。下次启动环境时生效。")}>
                    <span className="settings-row-copy">
                      <strong>{tr("项目文件缓存")}</strong>
                      <small>{tr("下次启动环境时生效。")}</small>
                    </span>
                    <input type="checkbox" role="switch" checked={draft.project_file_cache}
                      onChange={(event) => update("project_file_cache", event.target.checked)} />
                  </label>}
                  <label className="settings-row settings-switch-row" title={tr("支持的格式始终自动清理。开启后，无法安全清理的照片和音视频可通过上传和文件剪贴板原样传递，可能保留 GPS 定位、时区和地区元数据。保存后对新的传递生效；共享项目目录始终按原文件读写。")}>
                    <span className="settings-row-copy">
                      <strong>{tr("允许未支持格式原样传递")}</strong>
                    </span>
                    <input type="checkbox" role="switch" checked={Boolean(draft.transfer_allow_unclean)}
                      onChange={(event) => update("transfer_allow_unclean", event.target.checked)} />
                  </label>
                </div>
                <h4>{tr("键盘习惯")}</h4>
                <div className="choice-list choice-list--grid">
                  {[
                    [
                      "mac",
                      "Mac 快捷键",
                      "使用 ⌘ 操作网页和编辑器。Linux 客机使用对应的 Ctrl 快捷键。",
                    ],
                    [
                      "native",
                      "客机原生快捷键",
                      "按客机系统的习惯使用 Control、Command 和其他组合键。",
                    ],
                  ].map(([value, title, help]) => (
                    <label
                      className={`choice ${(isRemoteClient() ? localKeyboard : draft.keyboard) === value ? "checked" : ""}`}
                      key={value}
                    >
                      <input
                        type="radio"
                        name="keyboard"
                        value={value}
                        checked={(isRemoteClient() ? localKeyboard : draft.keyboard) === value}
                        onChange={() => {
                          if (!isRemoteClient()) { update("keyboard", value); return; }
                          try { saveClientKeyboard(value); setLocalKeyboard(value); setError(""); }
                          catch { setError("未能保存本机键盘习惯，请检查本地存储空间。"); }
                        }}
                      />
                      <span>
                        <strong>{tr(title)}</strong>
                        <small>{tr(help)}</small>
                      </span>
                    </label>
                  ))}
                </div>
                <h4>{tr("工作台快捷键")}</h4>
                <div className="shortcut-list shortcut-list--grid">
                  {[
                    ["打开项目", "⌘O"],
                    ["新建网页", "⌘T"],
                    ["新建终端", "⌘⇧↵"],
                    ["地址栏", "⌘L"],
                    ["关闭当前标签", "⌘W"],
                    ["切换标签", "⌘1–9"],
                    ["设置", "⌘,"],
                    ["终端复制粘贴", usesControl ? "Ctrl+Shift+C / V" : "⌘C / ⌘V"],
                  ].map(([label, key]) => (
                    <div key={label}>
                      <span>{tr(label)}</span>
                      <kbd>{shortcut(key)}</kbd>
                    </div>
                  ))}
                </div>
              </>
            )}
            {section === "project" && (
              <>
                <div className="section-title">
                  <h3>{tr("环境与恢复")}</h3>
                  <p>{isRemoteClient() ? tr("项目位于云端，Cage 中的改动会直接保存到云端文件。") : tr("所有项目共用一个 Cage 环境")}</p>
                </div>
                <div className="settings-card">
                  <div className="settings-row">
                    <div className="settings-row-copy">
                      <strong>{tr("当前项目")}</strong>
                      <small className="settings-mono-path" title={activeWorkspace}>
                        {activeWorkspace || tr("尚未选择项目")}
                      </small>
                    </div>
                    <FolderOpen size={18} className="settings-row-icon" />
                  </div>
                </div>
                {status.local_vm_storage && !isRemoteClient() && <EnvironmentStorage status={status} onConfirm={onConfirm} />}
                {!status.storage_recovery?.required && <EnvironmentVersions status={status} onConfirm={onConfirm} />}
                {!status.storage_recovery?.required && <EnvironmentCache status={status} onConfirm={onConfirm} />}
                {(!status.storage_recovery?.required || (status.local_vm_storage && !isRemoteClient())) && (
                  <EnvironmentDeviceReset
                    status={status}
                    onConfirm={onConfirm}
                    onOpenProxy={() => setSection("network")}
                  />
                )}
                {!status.storage_recovery?.required && <ResourceSettings status={status} onConfirm={onConfirm} onDirtyChange={setResourceDirty} />}
                <DiagnosticSettings />
                {status.can_restore && (
                  <button
                    className="text-button"
                    disabled={busy}
                    onClick={async () => {
                      if (
                        await onConfirm({
                          title: "恢复上一份配置？",
                          body: "会停止当前环境并恢复上次应用前的设置。已经结束的终端进程无法恢复。",
                          confirm: "恢复并重启",
                        })
                      ) {
                        try {
                          const result = await api("/restore", {});
                          setJobId(result.id);
                        } catch (e) {
                          setError(e.message);
                        }
                      }
                    }}
                  >
                    <RotateCcw size={14} />
                    {tr("恢复上一份配置")}
                  </button>
                )}
              </>
            )}
          </fieldset>
          {error && (
            <div role="alert" className="error-message">
              <SafetyMessage message={error} />
              {probeFailed && (draft.auto_timezone || draft.auto_language) && (
                <button
                  className="text-button"
                  disabled={busy}
                  onClick={() => {
                    setDraft((previous) => ({
                      ...previous,
                      auto_timezone: false,
                      auto_language: false,
                    }));
                    setProbeFailed(false);
                    setError("");
                    setSection("region");
                  }}
                >
                  {tr("手动设置时区和语言")}
                </button>
              )}
              {(staticDraftOffer || probeFailed) &&
                draft.exit_policy?.mode === "static" &&
                Boolean(draft.exit_policy?.ip?.trim()) && (
                  <button
                    className="text-button"
                    disabled={busy}
                    onClick={() => save({ saveStaticDraft: true })}
                  >
                    {tr("离线保存待验证的静态 IP 草稿")}
                  </button>
                )}
            </div>
          )}
        </div>
        {(!["appearance", "notifications", "browser", "account", "vault", "mobile", "archived-sessions", "project", "about"].includes(section) || changed) && (
          <footer className="settings-footer">
            {" "}
            <>
              <span className="save-note">
                {status.job?.running
                  ? status.job.message
                  : proxyDirty
                    ? tr("代理修改待应用，请点击「应用代理」")
                    : changed
                    ? environmentChanged && status.vm_running
                      ? tr("网络和系统设置需要重启")
                      : tr("有尚未保存的修改")
                    : tr("没有未保存的修改")}
              </span>
              <button onClick={close}>{tr("返回工作台")}</button>
              <button
                className="primary"
                disabled={!changed || busy}
                onClick={() => save()}
              >
                {busy ? (
                  <LoaderCircle size={15} className="spin" />
                ) : (
                  <Check size={15} />
                )}{" "}
                {environmentChanged && status.vm_running
                  ? tr("保存并重启")
                  : tr("保存设置")}
              </button>
            </>
          </footer>
        )}
      </div>
    </section>
  );
}
