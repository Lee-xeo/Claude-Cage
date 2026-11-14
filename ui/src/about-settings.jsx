import React, { useEffect, useRef, useState } from "react";
import { ExternalLink, LoaderCircle, RefreshCw } from "lucide-react";
import { api, native, nativeAvailable } from "./api";
import "./about-settings.css";

const repository = "https://github.com/Lee-xeo/Claude-Cage";
const sourceMessages = {
  current: "已与主分支同步",
  available: "主分支有新提交",
  ahead: "当前构建的提交领先于主分支",
  diverged: "当前构建与主分支存在分叉，请核对差异",
  unknown: "此构建缺少提交信息，无法核对是否最新",
};
const releaseMessages = {
  current: "已是最新发行版（含预发行）",
  available: "发现新发行版",
  ahead: "当前版本高于最新发行版",
  unknown: "当前版本无法比较，请核对发行说明",
  missing: "暂无可比较的公开发行版",
};

function RepositoryLink({ kind, onError, children }) {
  const url = `${repository}/${kind === "source" ? "commits/main" : "releases"}`;
  return <a href={url} target="_blank" rel="noopener noreferrer" onClick={async event => {
    if (!nativeAvailable()) return;
    event.preventDefault();
    try { await native("appReference", { kind }); }
    catch (error) { onError(error.message); }
  }}>{children}<ExternalLink size={13} aria-hidden="true" /></a>;
}

function CheckResult({ label, item, source, onError }) {
  return <section className="about-result" aria-label={label}>
    <h4>{label}</h4>
    {!item ? <p className="footnote">尚未检查</p> : <>
      <p className={item.error ? "error-message" : "about-result-status"}>
        {item.error || (source ? sourceMessages : releaseMessages)[item.status]}
      </p>
      {item.retryAt && <p className="footnote">可于 {new Date(item.retryAt).toLocaleString()} 后重试</p>}
      {source && item.commit && <p className="about-result-detail">主分支提交 <code title={item.commit}>{item.commit.slice(0, 12)}</code></p>}
      {source && item.status === "available" && item.aheadBy > 0 && <p className="footnote">主分支新增 {item.aheadBy} 个提交</p>}
      {!source && item.version && <p className="about-result-detail">{item.version} · {item.prerelease ? "预发行版" : "正式版"}</p>}
      <RepositoryLink kind={source ? "source" : "releases"} onError={onError}>{source ? "查看源码提交" : "查看发行说明与下载"}</RepositoryLink>
    </>}
  </section>;
}

export function AboutSettings() {
  const [build, setBuild] = useState(null);
  const [result, setResult] = useState(null);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [clock, setClock] = useState(Date.now);
  const retryAt = Math.max(Date.parse(result?.source?.retryAt || "") || 0, Date.parse(result?.release?.retryAt || "") || 0);
  const coolingDown = retryAt > clock;
  useEffect(() => {
    if (!coolingDown) return;
    const timer = setTimeout(() => setClock(Date.now()), Math.min(60000, retryAt - Date.now()));
    return () => clearTimeout(timer);
  }, [clock, coolingDown, retryAt]);
  const request = useRef(null);
  useEffect(() => {
    const controller = new AbortController();
    api("/client/about", undefined, undefined, {}, { signal: controller.signal })
      .then(setBuild).catch(error => { if (!controller.signal.aborted) setError(error.message); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => { controller.abort(); request.current?.abort(); };
  }, []);
  const check = async () => {
    if (request.current || retryAt > Date.now()) return;
    const controller = new AbortController();
    request.current = controller;
    setBusy(true); setError(""); setResult(null);
    try {
      const next = await api("/client/about", {}, undefined, {}, { signal: controller.signal });
      setBuild(next.build); setResult(next); setClock(Date.now());
    } catch (error) { if (!controller.signal.aborted) setError(error.message); }
    finally {
      request.current = null;
      if (!controller.signal.aborted) setBusy(false);
    }
  };
  return <section className="about-settings" aria-label="关于 Cage">
    <div className="section-title"><h3>关于</h3><p>Cage 的版本与更新信息</p></div>
    <dl className="about-build">
      <div><dt>软件版本</dt><dd>{loading ? "正在读取…" : build?.version || "未知"}</dd></div>
      <div><dt>构建提交</dt><dd>{build?.commit ? <code title={build.commit}>{build.commit.slice(0, 12)}</code> : loading ? "正在读取…" : "未记录"}</dd></div>
    </dl>
    {build?.modified && <p className="footnote">此构建包含未提交的修改，提交核对仅反映已记录的提交。</p>}
    <div className="about-check-actions">
      <button className="secondary-button" onClick={check} disabled={busy || coolingDown}>
        {busy ? <LoaderCircle size={15} className="spin" /> : <RefreshCw size={15} />}
        {busy ? "正在检查…" : coolingDown ? "等待重试" : "检测更新"}
      </button>
      {result?.checkedAt && <span className="footnote">检查时间：{new Date(result.checkedAt).toLocaleString()}</span>}
    </div>
    <p className="footnote">通过本机所选代理检查主分支提交和最新发行版，包含预发行版。检查后可前往发布页下载。</p>
    {busy && <p role="status">正在核对源码提交与发行版…</p>}
    {error && <p className="error-message" role="alert">{error}</p>}
    <div className="about-results" aria-live="polite" aria-busy={busy}>
      <CheckResult label="源码更新" item={result?.source} source onError={setError} />
      <CheckResult label="发行版更新（含预发行）" item={result?.release} onError={setError} />
    </div>
  </section>;
}
