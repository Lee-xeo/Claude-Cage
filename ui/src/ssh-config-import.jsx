import React, { useEffect, useState } from "react";
import { api } from "./api";
import { Modal } from "./modal";
import { useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";

export function SSHConfigImport({ platform, onSaved, onClose }) {
  const tr = useTranslation();
  const configPath = platform === "windows" ? "%USERPROFILE%\\.ssh\\config" : "~/.ssh/config";
  const [preview, setPreview] = useState(null);
  const [selected, setSelected] = useState(new Set());
  const [reload, setReload] = useState(0);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState(null);
  useEffect(() => {
    const controller = new AbortController();
    setPreview(null);
    setError("");
    api("/client/ssh-config", undefined, "GET", {}, { signal: controller.signal })
      .then((value) => {
        if (controller.signal.aborted) return;
        setPreview(value);
        setSelected(new Set(value.hosts.map((host) => host.name)));
      })
      .catch((e) => { if (!controller.signal.aborted) setError(e.message); });
    return () => controller.abort();
  }, [reload]);
  const save = async () => {
    if (busy || !preview || !selected.size) return;
    setBusy(true);
    setError("");
    try {
      const value = await api("/client/ssh-config", {
        revision: preview.revision,
        aliases: [...selected],
      });
      // Record success before refreshing the sidebar; a refresh failure must
      // not offer to repeat an already accepted import.
      setResult(value);
      await onSaved(value.imported);
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal title={tr("导入本机 SSH 配置")} onClose={busy ? undefined : onClose}>
      <div className="ssh-dialog-body ssh-config-import">
        <p>{tr("从 {0} 读取主机，使用本机 SSH agent。请先用 ssh-add 添加密钥。", configPath)}</p>
        <p>{tr("私钥、转发和其他 SSH 选项不会导入。已有主机不会覆盖，首次连接仍需核对指纹。")}</p>
        {error && <p className="error-message" role="alert">{localizeBackendMessage(error, tr)}</p>}
        {result ? (
          <p role="status">{tr("已导入 {0} 台主机，跳过 {1} 台已有主机。", result.imported.length, result.skipped)}</p>
        ) : <>
          {!preview && !error && <p role="status">{tr("正在读取…")}</p>}
          {preview?.hosts.length === 0 && <p>{tr("没有可导入的主机。")}</p>}
          <div className="ssh-config-hosts">
            {preview?.hosts.map((host) => (
              <label key={host.name}>
                <input type="checkbox" checked={selected.has(host.name)} disabled={busy}
                  onChange={(event) => setSelected((old) => {
                    const next = new Set(old);
                    if (event.target.checked) next.add(host.name); else next.delete(host.name);
                    return next;
                  })} />
                <span><strong>{host.name}</strong><small>{host.user}@{host.host}:{host.port}</small></span>
              </label>
            ))}
          </div>
          {Boolean(preview?.skipped.length) && <details>
            <summary>{tr("已跳过的主机")} ({preview.skipped.length})</summary>
            <ul>{preview.skipped.map((host) => <li key={host.name}><strong>{host.name}</strong>: {localizeBackendMessage(host.reason, tr)}</li>)}</ul>
          </details>}
        </>}
        <div className="remote-button-row">
          {result ? <button className="primary" disabled={busy} onClick={onClose}>{tr("完成并关闭")}</button> : <>
            <button className="primary" disabled={busy || !preview || !selected.size} onClick={save}>
              {busy ? tr("正在导入…") : tr("导入所选主机（{0}）", selected.size)}
            </button>
            <button disabled={busy} onClick={() => setReload((old) => old + 1)}>{tr("重新读取")}</button>
            <button disabled={busy} onClick={onClose}>{tr("取消")}</button>
          </>}
        </div>
      </div>
    </Modal>
  );
}
