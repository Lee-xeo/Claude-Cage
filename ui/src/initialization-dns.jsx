import React, { useEffect, useRef, useState } from "react";
import { ChevronDown, LoaderCircle, ShieldCheck } from "lucide-react";
import { api } from "./api";
import { SafetyMessage } from "./safety-message";
import { useTranslation } from "./interface-language.mjs";
import {
  buildSettingsPayload,
  dnsDraftEqual,
  dnsEndpointStatusText,
  dnsHostSummary,
} from "./initialization-dns-state.mjs";

const setupApi = (path, body, method, headers = {}, options) =>
  api(path, body, method, { "X-Cage-Initiator": "setup", ...headers }, options);

export function InitializationDNS({
  proxy,
  busy = false,
  defaultOpen = false,
  focusNonce = 0,
  onDirty,
  onSaved,
  apiClient = setupApi,
}) {
  const tr = useTranslation();
  const [open, setOpen] = useState(Boolean(defaultOpen));
  const [saved, setSaved] = useState(null);
  const [draft, setDraft] = useState(null);
  const [loading, setLoading] = useState(true);
  const [testing, setTesting] = useState(false);
  const [saving, setSaving] = useState(false);
  const [result, setResult] = useState(null);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const containerRef = useRef(null);
  const alive = useRef(true);
  const dirtyRef = useRef(false);

  const proxyApplied = Boolean(proxy?.active && proxy?.running && !proxy?.route_error);
  const dirty = Boolean(saved && draft && !dnsDraftEqual(saved, draft));
  dirtyRef.current = dirty;

  useEffect(() => {
    onDirty?.(dirty);
    return () => onDirty?.(false);
  }, [dirty, onDirty]);

  useEffect(() => {
    if (defaultOpen) setOpen(true);
  }, [defaultOpen]);

  useEffect(() => {
    if (focusNonce > 0) {
      setOpen(true);
      containerRef.current?.scrollIntoView?.({ behavior: "smooth", block: "nearest" });
    }
  }, [focusNonce]);

  useEffect(() => {
    alive.current = true;
    let active = true;
    (async () => {
      try {
        const next = await apiClient("/settings");
        if (!active || !alive.current) return;
        setSaved(next);
        setDraft(current => (current && dirtyRef.current ? current : {
          dns_url: next.dns_url || "",
          dns_fallback_urls: Array.isArray(next.dns_fallback_urls) ? [...next.dns_fallback_urls] : [],
          dns_public_fallback: Boolean(next.dns_public_fallback),
        }));
      } catch (err) {
        if (active && alive.current) setError(err.message);
      } finally {
        if (active && alive.current) setLoading(false);
      }
    })();
    return () => { active = false; alive.current = false; };
  }, [apiClient, proxy?.revision]);

  function updateField(key, value) {
    setDraft(current => ({ ...(current || {}), [key]: value }));
    setResult(null);
    setError("");
    setNotice("");
  }

  async function runCheck() {
    if (!saved || !draft || testing || saving || busy) return;
    setTesting(true);
    setError("");
    setNotice("");
    try {
      const check = await apiClient("/settings/dns-check", buildSettingsPayload(saved, draft));
      if (alive.current) setResult(check);
    } catch (err) {
      if (alive.current) setError(err.message);
    } finally {
      if (alive.current) setTesting(false);
    }
  }

  async function waitForSettingsJob(accepted) {
    if (!accepted?.id) return;
    while (alive.current) {
      const next = await apiClient("/status");
      if (!alive.current) return;
      const current = next.job;
      if (current?.id !== accepted.id) return;
      if (current.error || current.canceled) {
        throw new Error(current.error || tr("操作已取消，请核对状态后重试。"));
      }
      if (!current.running) return;
      await new Promise(resolve => setTimeout(resolve, 300));
    }
  }

  async function saveDNS() {
    if (!saved || !draft || testing || saving || busy) return;
    setSaving(true);
    setError("");
    setNotice("");
    try {
      const accepted = await apiClient("/settings", buildSettingsPayload(saved, draft));
      await waitForSettingsJob(accepted);
      if (!alive.current) return;
      const reloaded = await apiClient("/settings");
      if (!alive.current) return;
      setSaved(reloaded);
      setDraft({
        dns_url: reloaded.dns_url || "",
        dns_fallback_urls: Array.isArray(reloaded.dns_fallback_urls) ? [...reloaded.dns_fallback_urls] : [],
        dns_public_fallback: Boolean(reloaded.dns_public_fallback),
      });
      setNotice(tr("已保存加密 DNS，可以继续安装。"));
      onSaved?.(reloaded);
    } catch (err) {
      if (alive.current) setError(err.message);
    } finally {
      if (alive.current) setSaving(false);
    }
  }

  const hostSummary = dnsHostSummary(draft?.dns_url);
  const disabled = busy || loading || testing || saving;

  return (
    <details
      id="initialization-dns"
      ref={containerRef}
      className="initialization-dns"
      open={open}
      onToggle={(event) => setOpen(event.currentTarget.open)}
    >
      <summary>
        <ShieldCheck size={15} />
        <span>{tr("加密 DNS")}</span>
        <small>{hostSummary}</small>
        <ChevronDown size={15} className="initialization-dns-chevron" />
      </summary>
      <div className="initialization-dns-body">
        {draft && (
          <div className="initialization-dns-fields">
            <label>
              {tr("加密 DNS")}
              <input
                value={draft.dns_url}
                onChange={(e) => updateField("dns_url", e.target.value)}
                disabled={disabled}
                spellCheck="false"
                placeholder="https://1.1.1.1/dns-query"
              />
            </label>
            <label>
              {tr("备用加密 DNS（每行一个，最多 4 个）")}
              <textarea
                rows={2}
                value={(draft.dns_fallback_urls ?? []).join("\n")}
                onChange={(e) => updateField("dns_fallback_urls", e.target.value.split(/\r?\n/))}
                disabled={disabled}
                spellCheck="false"
                placeholder="https://…/dns-query"
              />
            </label>
            <label className="initialization-dns-switch">
              <input
                role="switch"
                type="checkbox"
                checked={Boolean(draft.dns_public_fallback)}
                onChange={(e) => updateField("dns_public_fallback", e.target.checked)}
                disabled={disabled}
              />
              <span>{tr("以上端点都不可用时改用 Google 公共 DNS（8.8.8.8）")}</span>
            </label>
          </div>
        )}

        {!proxyApplied && (
          <p className="field-help">{tr("请先选择并应用代理节点，再测试加密 DNS。")}</p>
        )}

        <div className="initialization-dns-actions">
          <button
            type="button"
            disabled={disabled || !proxyApplied || !draft}
            onClick={runCheck}
          >
            {testing && <LoaderCircle size={14} className="spin" />}
            {testing ? tr("正在测试加密 DNS…") : tr("测试加密 DNS")}
          </button>
          <button
            type="button"
            className="primary"
            disabled={disabled || !dirty}
            onClick={saveDNS}
          >
            {saving && <LoaderCircle size={14} className="spin" />}
            {saving ? tr("正在保存加密 DNS…") : tr("保存设置")}
          </button>
        </div>

        {result && (
          <div className="initialization-dns-results" role="status">
            <p className="field-help">{tr("经所选节点解析 {0}", result.target)}</p>
            <ul>
              {(result.endpoints || []).map((endpoint, idx) => (
                <li key={`${endpoint.host}-${endpoint.role}-${idx}`} data-status={endpoint.status}>
                  <code>{endpoint.host}</code>
                  <span className={`initialization-dns-status initialization-dns-status--${endpoint.status}`}>
                    {dnsEndpointStatusText(endpoint, tr)}
                  </span>
                </li>
              ))}
            </ul>
          </div>
        )}

        {notice && <p className="initialization-dns-notice" role="status">{notice}</p>}
        {error && <p className="initialization-notice" role="alert"><SafetyMessage message={error} /></p>}
      </div>
    </details>
  );
}
