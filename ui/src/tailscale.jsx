import React, { useState } from "react";
import { LoaderCircle, Network } from "lucide-react";
import "./tailscale.css";

// Tailscale runs as a built-in plugin with its own page. Until it is
// installed, or until the environment's components carry its page, this is
// what the sidebar's Tailscale entry shows.
export function TailscaleSetup({ status, installed, onInstall }) {
  const [busy, setBusy] = useState(false), [error, setError] = useState("");
  const ready = status.ready && !status.job?.running;
  const install = async () => {
    setBusy(true); setError("");
    try { await onInstall(); } catch (e) { setError(e.message); }
    finally { setBusy(false); }
  };
  return <div className="tailscale-panel">
    <div className="tailscale-state" role="status">
      <div className="tailscale-network-icon"><Network size={24} /></div>
      <div><strong>{!ready ? "等待环境就绪" : installed ? "等待 Tailscale 页面就绪" : "尚未安装"}</strong></div>
    </div>
    <p className="field-help">{!ready ? "启动环境后，即可安装并登录 Tailscale。"
      : installed ? "Tailscale 已安装。更新环境组件后，这里会打开 Tailscale 页面。"
      : "安装 Tailscale，让这个环境加入你的私网，访问其他设备上的服务，或从其他设备 SSH 进来。"}</p>
    {ready && !installed && <div className="tailscale-actions">
      <button className="primary-button" disabled={busy} onClick={install}>{busy && <LoaderCircle size={15} className="spin" />}安装 Tailscale</button>
    </div>}
    {error && <p className="error-message" role="alert">{error}</p>}
  </div>;
}
