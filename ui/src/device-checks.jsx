import React, { useState } from "react";
import { api } from "./api";
import { Modal } from "./modal";
import { SafetyMessage } from "./safety-message";
import "./device-checks.css";

const labels = { passed: "通过", failed: "未通过", unknown: "未观测", warning: "需关注", observed: "已观测" };
const renderers = { software: "软件渲染", "virtual-gpu": "虚拟 GPU", "driver-reported": "驱动报告的渲染器", unobserved: "未观测" };

export function DeviceChecks({ target, disabled = false }) {
  const [report, setReport] = useState(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function run(selected) {
    setBusy(true); setError(""); setReport(null);
    try { setReport(await api("/environment/device-check", { target: selected })); }
    catch (error) { setError(error.message); }
    finally { setBusy(false); }
  }
  return <section className="device-checks" aria-label="设备配置与渲染检测">
    <h4>设备配置与渲染检测</h4>
    <p>核对实际配置、浏览器身份和图形能力。结果用于排查不一致，不作为物理设备或匿名性认证。</p>
    <div className="device-check-actions">
      {(!target || target === "browser") && <button disabled={busy || disabled} onClick={() => run("browser")}>{busy ? "正在检测…" : "检测浏览器"}</button>}
      {(!target || target === "desktop") && <button disabled={busy || disabled} onClick={() => run("desktop")}>{busy ? "正在检测…" : "检测 Claude Desktop"}</button>}
    </div>
    {error && <p role="alert"><SafetyMessage message={error} /></p>}
    {report && <div aria-live="polite">
      <p>{report.target === "desktop" ? "Claude Desktop" : "浏览器"} · {new Date(report.checked_at).toLocaleString()}</p>
      {report.renderer_class && <p>实际图形环境：{renderers[report.renderer_class] || "未观测"}</p>}
      {report.browser && <details><summary>浏览器实际观测值</summary><dl>
        <dt>User Agent</dt><dd>{report.browser.user_agent}</dd>
        <dt>图形渲染器</dt><dd>{report.browser.webgl.renderer || "未观测"}</dd>
        <dt>显示尺寸</dt><dd>{report.browser.screen.width} × {report.browser.screen.height}，像素比 {report.browser.screen.pixel_ratio}</dd>
        <dt>诊断文档视口</dt><dd>{report.browser.viewport.width} × {report.browser.viewport.height}</dd>
      </dl></details>}
      <ul className="device-check-results">{report.checks.map(check => <li key={check.id} data-state={check.state}><strong>{check.title}</strong><span>{labels[check.state] || "未观测"}</span><p>{check.evidence}</p></li>)}</ul>
    </div>}
  </section>;
}

export function DeviceCheckButton({ target = "desktop", disabled = false }) {
  const [open, setOpen] = useState(false);
  return <><button disabled={disabled} onClick={() => setOpen(true)}>检测环境</button>{open && <Modal title="设备配置与渲染检测" onClose={() => setOpen(false)}><div className="modal-body"><DeviceChecks target={target} /></div></Modal>}</>;
}
