import React, { useEffect, useState } from "react";
import { api } from "./api";
import { SafetyMessage } from "./safety-message";
import { useTranslation } from "./interface-language.mjs";
import { useWindowVisibility } from "./window-visibility";

const gib = (bytes) => `${(bytes / 1073741824).toFixed(1)} GiB`;
const allocationFrom = (data) => ({
  automatic: data.automatic,
  memory_gib: data.memory_gib,
  cpus: data.cpus,
  disk_gib: data.disk_gib,
});

export function ResourceSettings({ status, onConfirm, onDirtyChange }) {
  const tr = useTranslation();
  const windowVisible = useWindowVisibility();
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [readError, setReadError] = useState("");
  const [starting, setStarting] = useState(false);
  const [draft, setDraft] = useState(null);
  const [saved, setSaved] = useState(false);
  useEffect(() => {
    if (data && data.runtime !== "container") setDraft((current) => current || allocationFrom(data));
  }, [data]);
  const save = async (event) => {
    event.preventDefault();
    setStarting(true);
    setSaved(false);
    try {
      await api("/environment/resources", { allocation: draft });
      const next = await api("/environment/resources");
      setData(next);
      setDraft(allocationFrom(next));
      setSaved(true);
      setError("");
    } catch (e) { setError(e.message); }
    finally { setStarting(false); }
  };
  useEffect(() => {
    if (!windowVisible) return;
    let live = true;
    let timer;
    const refresh = async () => {
      try {
        const next = await api("/environment/resources");
        if (live) { setData(next); setReadError(""); }
      } catch (e) { if (live) setReadError(e.message); }
      if (live) timer = setTimeout(refresh, 4000);
    };
    refresh();
    return () => { live = false; clearTimeout(timer); };
  }, [status.vm_running, status.job?.running, windowVisible]);
  const shrinkingDisk = Boolean(
    data?.configured_disk_bytes > 0 &&
      data?.disk_gib > 0 &&
      data.disk_gib * 1073741824 < data.configured_disk_bytes,
  );
  const apply = async () => {
    if (!(await onConfirm(shrinkingDisk ? {
      title: tr("应用配置并缩小虚拟硬盘？"),
      body: tr("正在运行的命令会结束，环境会重启以安全检查并缩小文件系统、释放多余主机磁盘空间。项目文件、账户和登录数据都会保留，失败会自动恢复原磁盘。"),
      confirm: tr("确认并调整硬盘"),
    } : {
      title: "重启并应用已保存的资源配置？",
      body: "正在运行的命令会结束，浏览器和编辑器会重启。请先保存内容；项目文件和登录数据保留。手动暂停的网络仍保持暂停。",
      confirm: "重启并应用",
    }))) return;
    setStarting(true);
    try { await api("/environment/resources", {}); setError(""); }
    catch (e) { setError(e.message); }
    finally { setStarting(false); }
  };
  const observation = data?.observation;
  const guest = observation?.guest;
  const fresh = observation && Date.now() - Date.parse(observation.observed_at) < 10000;
  const resourceError = error || readError || data?.resource_error;
  const resourcesVerified = data?.resources_verified === true && !resourceError;
  const busy = starting || status.job?.running;
  const dirty = draft && data && (draft.automatic !== data.automatic || (!draft.automatic && (draft.memory_gib !== data.memory_gib || draft.cpus !== data.cpus || draft.disk_gib !== data.disk_gib)));
  useEffect(() => {
    onDirtyChange?.(Boolean(dirty));
    return () => onDirtyChange?.(false);
  }, [dirty, onDirtyChange]);
  const edit = (key, value) => {
    setDraft((current) => ({ ...current, automatic: false, [key]: value === "" ? "" : Number(value) }));
    setSaved(false);
  };
  const reset = () => {
    const defaultDisk = data.configured_disk_bytes > 0
      ? Math.round(data.configured_disk_bytes / 1073741824)
      : Math.round((data.recommended_disk_bytes || 24 * 1073741824) / 1073741824);
    setDraft({
      automatic: true,
      memory_gib: data.recommended_memory_bytes / 1073741824,
      cpus: data.recommended_cpus,
      disk_gib: defaultDisk,
    });
    setSaved(false);
    setError("");
  };
  if (data?.runtime === "container") return <section className="environment-versions" aria-label={tr("计算资源")}>
    <h4>{tr("服务器容器资源上限")}</h4>
    <p className="field-help">{tr("CPU、内存与进程数量由服务器上的容器资源组限制。")}</p>
    <div className="setting-summary-row"><span>{tr("资源限制核验")}</span><strong>{data.running ? resourcesVerified ? tr("已核验") : tr("未通过核验") : tr("环境未运行")}</strong></div>
    <div className="setting-summary-row"><span>{tr("容器内存上限")}</span><strong>{data.running ? resourcesVerified ? gib(data.configured_memory_bytes) : tr("未核验") : gib(data.recommended_memory_bytes)}</strong></div>
    <div className="setting-summary-row"><span>{tr("容器 CPU 上限")}</span><strong>{data.running ? resourcesVerified ? tr("{0} 个逻辑核心", (data.configured_cpus)) : tr("未核验") : tr("{0} 个逻辑核心", (data.recommended_cpus))}</strong></div>
    <div className="setting-summary-row"><span>{tr("进程与线程总数上限")}</span><strong>{data.running ? resourcesVerified ? data.pids_limit : tr("未核验") : tr("运行后核验")}</strong></div>
    <p className="field-help">{data.running ? resourcesVerified ? tr("已从服务器核验容器所属资源组的实际限制。实时内存与 CPU 用量尚未提供。") : tr("无法确认运行中容器的实际资源限制，请查看下方错误或等待重新核验。") : tr("环境尚未运行，当前显示部署配置中的资源上限。")}</p>
    {resourceError && <p role="alert" className="error-message"><SafetyMessage message={resourceError} /></p>}
  </section>;
  return <section className="environment-versions resource-settings" aria-label={tr("计算资源")}>
    <h4>{tr("计算资源")}</h4>
    <p className="field-help resource-intro">{tr("默认使用主机一半的资源，可在下方调整。内存按需使用，持续空闲后逐步归还主机。CPU 按实际任务使用。")}</p>
    {draft ? <form className="resource-allocation" onSubmit={save}>
      <fieldset disabled={busy} className="resource-fields">
        <legend className="sr-only">{tr("资源配置")}</legend>
        <label><span>{tr("内存上限")}</span>
          <span className="resource-input"><input aria-label={tr("内存上限（GiB）")} aria-describedby="resource-memory-help" type="number" min={data.min_memory_gib || 2} max={data.max_memory_gib} step="1" required value={draft.memory_gib} onChange={(e) => edit("memory_gib", e.target.value)} /><span aria-hidden="true">GiB</span></span>
          <small id="resource-memory-help">{tr("默认") + " "}{gib(data.recommended_memory_bytes)}{" " + tr("· 可调") + " "}{data.min_memory_gib || 2}–{data.max_memory_gib} GiB</small>
        </label>
        <label><span>{tr("CPU 上限")}</span>
          <span className="resource-input"><input aria-label={tr("CPU 逻辑核心数")} aria-describedby="resource-cpu-help" type="number" min={data.min_cpus || 1} max={data.max_cpus} step="1" required value={draft.cpus} onChange={(e) => edit("cpus", e.target.value)} /><span aria-hidden="true">{tr("逻辑核心")}</span></span>
          <small id="resource-cpu-help">{tr("默认") + " "}{data.recommended_cpus}{" " + tr("个 · 可调") + " "}{data.min_cpus || 1}–{data.max_cpus}{" " + tr("个")}</small>
        </label>
        <label><span>{tr("硬盘上限")}</span>
          <span className="resource-input"><input aria-label={tr("硬盘上限（GiB）")} aria-describedby="resource-disk-help" type="number" min={data.min_disk_gib || 16} max={data.max_disk_gib || 256} step="1" required value={draft.disk_gib ?? ""} onChange={(e) => edit("disk_gib", e.target.value)} /><span aria-hidden="true">GiB</span></span>
          <small id="resource-disk-help">{tr("默认") + " "}{gib(data.recommended_disk_bytes || 24 * 1073741824)}{" " + tr("· 可调") + " "}{data.min_disk_gib || 16}–{data.max_disk_gib || 256} GiB</small>
        </label>
      </fieldset>
      <div className="resource-actions">
        <span className="resource-mode">{draft.automatic ? tr("默认配置") : tr("自定义配置")}{dirty ? " " + tr("· 未保存") : ""}</span>
        <button className="text-button" type="button" disabled={busy || draft.automatic} onClick={reset}>{tr("恢复默认")}</button>
        <button type="submit" disabled={busy || !dirty}>{starting ? tr("正在保存…") : tr("保存资源配置")}</button>
      </div>
      <p role={saved ? "status" : undefined} className="field-help resource-save-note">
        {saved ? tr("已保存，") : tr("保存后，")}{data.running ? tr("重启环境后生效。") : shrinkingDisk ? tr("点击下方按钮后生效。") : tr("下次启动环境时生效。")}
      </p>
      {!data.running && data.disk_change_pending && !dirty && (
        <div className="resource-actions">
          <button className="text-button" type="button" disabled={busy} onClick={apply}>
            {shrinkingDisk ? tr("立即应用并缩小硬盘") : tr("立即应用并启动环境")}
          </button>
        </div>
      )}
    </form> : !resourceError && <p role="status" className="field-help">{tr("正在读取资源配置…")}</p>}
    {data?.running && <div className="resource-runtime">
      <div className="resource-runtime-heading"><h5>{tr("当前运行")}</h5>{data.restart_required && <button className="text-button" disabled={busy} onClick={apply}>{tr("重启并应用已保存配置")}</button>}</div>
      <div className="setting-summary-row"><span>{tr("本次启动的内存上限")}</span><strong>{gib(data.configured_memory_bytes)}</strong></div>
      <div className="setting-summary-row"><span>{tr("本次启动的 CPU 上限")}</span><strong>{data.configured_cpus}{" " + tr("个逻辑核心")}</strong></div>
      {data.configured_disk_bytes > 0 && <div className="setting-summary-row"><span>{tr("本次启动的硬盘上限")}</span><strong>{gib(data.configured_disk_bytes)}</strong></div>}
      <div className="setting-summary-row"><span>{tr("环境内存用量")}</span><strong>{fresh && guest ? gib(guest.total_bytes - guest.available_bytes) : tr("等待采样")}</strong></div>
      <div className="setting-summary-row"><span>{tr("环境当前可用内存总量")}</span><strong>{fresh && guest ? gib(guest.total_bytes) : "—"}</strong></div>
      <div className="setting-summary-row"><span>{tr("CPU 使用率")}</span><strong>{fresh && Number.isFinite(observation.cpu_percent) ? tr("{0}% / {1} 个核心", (observation.cpu_percent.toFixed(0)), (observation.cpus)) : tr("等待采样")}</strong></div>
      {data.configured_disk_bytes > 0 && <div className="setting-summary-row"><span>{tr("环境硬盘用量")}</span><strong>{fresh && guest?.disk_total_bytes ? `${gib(guest.disk_used_bytes)} / ${gib(guest.disk_total_bytes)}` : tr("等待采样")}</strong></div>}
      {data.host_disk_allocated_bytes > 0 && <div className="setting-summary-row"><span>{tr("主机硬盘实际占用")}</span><strong>{gib(data.host_disk_allocated_bytes)}</strong></div>}
      {fresh && guest?.oom_kills > 0 && <p className="field-help" role="status">{tr("本次环境启动后，系统因内存不足终止过") + " "}{guest.oom_kills}{" " + tr("个进程。请降低重任务的并行数量，或使用内存更多的主机。")}</p>}
      {(observation?.pressure || observation?.host_pressure) && <p className="field-help" role="status">{observation.host_pressure ? tr("主机内存紧张，暂缓扩大分配。") : tr("环境内存紧张，正在上限内增加可用内存。达到上限后请减少同时运行的任务。")}</p>}
    </div>}
    {(resourceError || observation?.error) && <p role="alert" className="error-message"><SafetyMessage message={resourceError || observation.error} /></p>}
    {data?.running && <details className="resource-observation-note"><summary>{tr("资源统计说明")}</summary><p className="field-help">{tr("内存用量不含可回收缓存；这些数值反映环境内部状态，主机系统监视器还会统计虚拟化开销。")}</p></details>}
  </section>;
}
