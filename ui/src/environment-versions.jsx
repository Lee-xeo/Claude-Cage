import React, { useEffect, useState } from "react";
import { api } from "./api";
import { SafetyMessage } from "./safety-message";
import { useTranslation } from "./interface-language.mjs";

export function EnvironmentVersions({ status, onConfirm }) {
  const tr = useTranslation();
  const [data, setData] = useState(null), [desktop, setDesktop] = useState(null), [error, setError] = useState(""), [loading, setLoading] = useState(false), [starting, setStarting] = useState(false);
  const readDesktop = () => status.vm_running ? api("/claude-desktop").catch(() => null) : Promise.resolve(null);
  const refresh = async () => {
    setLoading(true);
    try {
      const [versions, desktopState] = await Promise.all([api("/environment/versions"), readDesktop()]);
      setData(versions);
      setDesktop(desktopState?.installed ? desktopState : null);
      setError("");
    } catch (e) { setError(e.message); }
    finally { setLoading(false); }
  };
  useEffect(() => {
    let live = true;
    setLoading(true);
    Promise.all([api("/environment/versions"), readDesktop()])
      .then(([next, desktopState]) => {
        if (!live) return;
        setData(next);
        setDesktop(desktopState?.installed ? desktopState : null);
        setError("");
      })
      .catch((e) => { if (live) setError(e.message); })
      .finally(() => { if (live) setLoading(false); });
    return () => { live = false; };
  }, [status.vm_running, status.job?.running]);
  const busy = Boolean(status.job?.running) || starting;
  const update = async (tools) => {
    if (!(await onConfirm({
      title: tools ? "更新环境工具？" : "更新环境组件？",
      body: "请先保存浏览器和编辑器中的内容。浏览器、编辑器会短暂重启，编辑器内的终端可能结束；工作台终端、项目文件和登录状态会保留。",
      confirm: tools ? "更新工具" : "更新组件",
    }))) return;
    setStarting(true); setError("");
    try { await api(tools ? "/environment/tools" : "/environment/update", {}); }
    catch (e) { setError(e.message); }
    finally { setStarting(false); }
  };
  const updateDesktop = async () => {
    const wasRunning = Boolean(desktop?.running);
    if (wasRunning && !(await onConfirm({
      title: "更新 Claude Desktop？",
      body: "请先保存应用内的工作。更新会关闭 Claude Desktop，从官方软件源安装可用版本，再重新打开；登录资料继续保留。",
      confirm: "更新应用",
    }))) return;
    setStarting(true); setError("");
    try {
      let job = await api("/claude-desktop/update", {});
      while (job?.running) {
        await new Promise((resolve) => setTimeout(resolve, 500));
        job = await api("/agent/jobs/" + job.id);
      }
      if (job?.error || job?.canceled) throw new Error(job.error || tr("操作已取消"));
      if (wasRunning) {
        let startJob = await api("/claude-desktop/start", {});
        while (startJob?.running) {
          await new Promise((resolve) => setTimeout(resolve, 500));
          startJob = await api("/agent/jobs/" + startJob.id);
        }
        if (startJob?.error || startJob?.canceled) throw new Error(startJob.error || tr("操作已取消"));
      }
      await refresh();
    } catch (e) {
      setError(e.message);
      const latest = await readDesktop();
      setDesktop(latest?.installed ? latest : null);
    } finally {
      setStarting(false);
    }
  };
  const componentLabels = { current: "已同步", update_required: "需要更新", stopped: "环境已停止", unknown: "暂时无法确认" };
  const toolNames = { chromium: "浏览器", code_server: "文件编辑器", claude: "Claude Code", mise: "工具版本管理器", node: "Node.js", python: "Python", git: "Git", gh: "GitHub CLI", proxy: "代理核心" };
  const toolRows = data?.tools ? Object.entries(toolNames).filter(([key]) => data.tools[key]) : [];
  return (
    <section className="environment-versions" aria-label={tr("环境版本")}>
      <h4>{tr("版本与组件")}</h4>
      <div className="setting-summary-row"><span>{tr("控制服务")}</span><strong>{data?.controller?.version || "—"}</strong></div>
      <div className="setting-summary-row"><span>{tr("环境组件")}</span><strong>{componentLabels[data?.components?.status] ? tr(componentLabels[data.components.status]) : (loading ? tr("正在读取…") : tr("暂时无法确认"))}</strong></div>
      {(toolRows.length > 0 || desktop?.installed) && <table className="environment-tool-table">
        <thead><tr><th>{tr("工具")}</th><th>{tr("当前版本")}</th><th>{tr("受管版本")}</th></tr></thead>
        <tbody>
          {toolRows.map(([key, name]) => <tr key={key}><td>{tr(name)}</td><td>{data.tools[key]?.current || "—"}</td><td>{data.tools[key]?.required || "—"}</td></tr>)}
          {desktop?.installed && <tr key="claude-desktop"><td>Claude Desktop</td><td>{desktop.version ? `v${desktop.version}` : "—"}</td><td>{tr("官方软件源")}</td></tr>}
        </tbody>
      </table>}
      {(error || data?.error) && <p role="alert" className="error-message"><SafetyMessage message={error || data.error} /></p>}
      <div className="environment-version-actions">
        <button disabled={loading || busy} onClick={refresh}>{loading ? tr("正在读取…") : tr("刷新版本")}</button>
        {data?.components?.update_required && <button className="primary" disabled={busy || !status.vm_running} onClick={() => update(false)}>{tr("更新环境组件")}</button>}
        <button disabled={busy || !data?.tools_update_allowed} onClick={() => update(true)}>{tr("更新环境工具")}</button>
        {desktop?.installed && <button disabled={busy || !status.vm_running} onClick={updateDesktop}>{tr("更新 Claude Desktop")}</button>}
      </div>
      <p className="field-help">{tr("组件更新保留虚拟磁盘与终端。工具更新需先启动环境并同步组件，依赖通过已选代理下载；无需放行业务网络，也不会自动恢复已暂停的外网。")}</p>
    </section>
  );
}
