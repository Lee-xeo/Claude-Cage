import React, { useEffect, useState } from "react";
import { api } from "./api";
import "./guest-system-picker.css";

// New environments always use Debian 13. The other names only label an
// installation that an earlier version recorded.
const names = { "debian-12": "Debian 12", "debian-13": "Debian 13", "ubuntu-22.04": "Ubuntu 22.04", "ubuntu-24.04": "Ubuntu 24.04", macos: "macOS", "windows-11": "Windows 11" };
const newEnvironment = { guest_system: "debian-13" };

// GuestSystemPicker reports the system the next start will use. It shows
// nothing for a new environment; for an installation recorded by an earlier
// version it shows that record and, while nothing was created for it, lets
// the user withdraw it.
export function GuestSystemPicker({ system, onChange, onReselect, preserveOnUnmount = false }) {
  const [view, setView] = useState(null);
  const [error, setError] = useState("");
  const [attempt, retry] = useState(0);
  useEffect(() => {
    let active = true;
    setError("");
    api("/environment/guest-systems").then((next) => {
      if (!active) return;
      setView(next);
      if (next.pending) onChange({ guest_system: next.pending.system, pending: true, resumable: Boolean(next.available) });
      else onChange(next.fresh ? newEnvironment : null);
    }).catch((e) => {
      if (!active) return;
      if (e.status === 403 || e.status === 404) onChange(null);
      else setError(e.message);
    });
    return () => { active = false; if (!preserveOnUnmount) onChange(undefined); };
  }, [onChange, attempt, preserveOnUnmount]);
  if (!view || !system) return error ? <div className="guest-system-picker"><span role="alert">{error}</span>
    <button type="button" onClick={() => retry((n) => n + 1)}>重新检查安装状态</button></div> : null;
  if (!view.pending) return null;
  return (
    <div className="guest-system-picker">
      <span>原安装选择</span>
      <strong>{names[view.pending.system] || view.pending.system}</strong>
      {view.available && <small className="guest-install-fields">此环境保留着早期版本选择的系统镜像，可以继续原安装。镜像通过主机网络下载，遵循主机系统代理或 TUN 设置，不使用 Cage 的客机代理。</small>}
      {view.reason && <small className="guest-install-fields">{view.reason}</small>}
      {view.reselect && <button type="button" className="guest-install-fields" onClick={async () => {
        try {
          await api("/environment/guest-systems", { id: view.pending.id }, "DELETE");
          const next = await api("/environment/guest-systems");
          setView(next);
          onChange(next.fresh ? newEnvironment : null);
          onReselect?.();
        } catch (e) { setError(e.message); }
      }}>撤销原安装选择，改用 Debian 13</button>}
      {error && <p role="alert" className="guest-install-fields">{error}</p>}
    </div>
  );
}

// validGuestChoice reports whether a start may be requested: a new Debian 13
// environment, an existing environment, or an installation that can continue.
export function validGuestChoice(value) {
  if (!value) return true;
  return value.pending ? Boolean(value.resumable) : value.guest_system === "debian-13";
}
