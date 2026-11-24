import React, { useEffect, useRef, useState } from "react";
import { Check, Laptop, LoaderCircle, RefreshCw } from "lucide-react";
import { api } from "./api";
import "./local-config-import.css";
import { useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";

export function LocalConfigImport({ busy, onImport, initializing = false, hasNodes = false }) {
  const tr = useTranslation();
  const [configs, setConfigs] = useState([]), [loading, setLoading] = useState(true);
  const [error, setError] = useState(""), [imported, setImported] = useState({});
  const request = useRef(null), importing = useRef(false);
  async function detect() {
    request.current?.abort();
    const controller = new AbortController(); request.current = controller;
    setLoading(true); setError("");
    try {
      const result = await api("/proxy/local-configs", undefined, undefined, {}, { signal: controller.signal });
      if (!controller.signal.aborted) setConfigs(result.configs || []);
    } catch (error) { if (!controller.signal.aborted) setError(error.message); }
    finally { if (!controller.signal.aborted) setLoading(false); }
  }
  useEffect(() => { void detect(); return () => request.current?.abort(); }, []);
  async function importConfig(config) {
    if (busy || importing.current) return;
    importing.current = true;
    try {
      if (await onImport(config.id)) setImported(value => ({ ...value, [config.id]: true }));
    } finally { importing.current = false; }
  }
  const compactEmpty = initializing && !loading && !error && configs.length === 0;
  if (compactEmpty) {
    return <section className="local-config-import local-config-import--compact" aria-label={tr("导入本机代理配置")}>
      <div className="local-config-compact-row">
        <Laptop size={16} aria-hidden="true" />
        <p role="status">{tr("还没有找到可导入的配置，可使用下方方式导入节点。")}</p>
        <button type="button" disabled={busy || loading} onClick={detect}><RefreshCw size={14} />{tr("重新检测")}</button>
      </div>
    </section>;
  }
  return <section className={`local-config-import${initializing ? " local-config-import--setup" : ""}`} aria-label={tr("导入本机代理配置")}>
    <div className="local-config-heading"><h4>{tr("从已有代理软件导入节点")}</h4><button type="button" disabled={busy || loading} onClick={detect}><RefreshCw size={14} />{tr("重新检测")}</button></div>
    {(!initializing || !hasNodes) && <p>{tr("自动查找这台电脑里已保存的代理配置。导入后由 Cage 独立连接，原软件的配置会保留。")}</p>}
    {loading ? <p role="status"><LoaderCircle size={16} className="spin" />{tr("正在查找本机配置…")}</p> : error ? <p role="alert" className="local-config-error">{localizeBackendMessage(error, tr)}</p> : configs.length ? <ul className="local-config-list">{configs.map(config => <li key={config.id}>
      <div><strong>{config.client} · {tr(config.name)}</strong><p>{[config.current && tr("当前配置"), ...(config.error ? [localizeBackendMessage(config.error, tr)] : [tr("{0} 个可导入节点", config.nodes), config.skipped && tr("{0} 个节点不兼容", config.skipped), config.refreshable ? tr("保留订阅链接，可手动更新") : tr("本地副本")])].filter(Boolean).join(" · ")}</p>{config.warning && <p className="local-config-warning">{localizeBackendMessage(config.warning, tr)}</p>}</div>
      <button type="button" className={!hasNodes && !imported[config.id] && !config.error ? "primary" : ""} disabled={busy || Boolean(config.error) || imported[config.id]} onClick={() => importConfig(config)}>{imported[config.id] ? <><Check size={14} />{tr("已导入")}</> : tr("导入此配置")}</button>
    </li>)}</ul> : <p role="status">{tr("还没有找到可导入的配置。你可以使用下方“添加来源”，粘贴订阅链接或选择配置文件。")}</p>}
    {(!initializing || !hasNodes) && <p className="local-config-note">{tr("导入节点后，在下方选择出口并点击“应用代理”。原软件的分组、分流规则和本地监听端口由它自己管理。")}</p>}
  </section>;
}
