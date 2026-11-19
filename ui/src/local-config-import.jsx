import React, { useEffect, useRef, useState } from "react";
import { Check, LoaderCircle, RefreshCw } from "lucide-react";
import { api } from "./api";
import "./local-config-import.css";

export function LocalConfigImport({ busy, onImport }) {
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
  return <section className="local-config-import" aria-label="导入本机代理配置">
    <div className="local-config-heading"><h4>从已有代理软件导入</h4><button type="button" disabled={busy || loading} onClick={detect}><RefreshCw size={14} />重新检测</button></div>
    <p>自动查找这台电脑里已保存的代理配置。导入后由 Cage 独立连接，原软件的配置会保留。</p>
    {loading ? <p role="status"><LoaderCircle size={16} className="spin" />正在查找本机配置…</p> : error ? <p role="alert" className="local-config-error">{error}</p> : configs.length ? <ul className="local-config-list">{configs.map(config => <li key={config.id}>
      <div><strong>{config.client} · {config.name}</strong><p>{config.current && "当前配置 · "}{config.error || `${config.nodes} 个可导入节点${config.skipped ? ` · ${config.skipped} 个节点不兼容` : ""}${config.refreshable ? " · 保留订阅链接，可手动更新" : " · 本地副本"}`}</p>{config.warning && <p className="local-config-warning">{config.warning}</p>}</div>
      <button type="button" disabled={busy || Boolean(config.error) || imported[config.id]} onClick={() => importConfig(config)}>{imported[config.id] ? <><Check size={14} />已导入</> : "导入此配置"}</button>
    </li>)}</ul> : <p role="status">还没有找到可导入的配置。你可以使用下方“添加来源”，粘贴订阅链接或选择配置文件。</p>}
    <p className="local-config-note">导入节点后，在下方选择出口并点击“应用代理”。原软件的分组、分流规则和本地监听端口由它自己管理。</p>
  </section>;
}
