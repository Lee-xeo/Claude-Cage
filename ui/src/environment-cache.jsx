import React, { useState } from "react";
import { api } from "./api";
import "./environment-cache.css";

function size(bytes) {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
}

export function EnvironmentCache({ status, onConfirm }) {
  const [inventory, setInventory] = useState(null);
  const [selected, setSelected] = useState([]);
  const [working, setWorking] = useState(false);
  const [error, setError] = useState("");
  const [result, setResult] = useState(null);
  const disabled = working || !status.ready || status.job?.running;

  async function inspect() {
    const next = await api("/environment/cache/inventory", {});
    setInventory(next);
    setSelected([]);
  }

  async function refresh() {
    setWorking(true);
    setError("");
    setResult(null);
    try { await inspect(); }
    catch (failure) { setInventory(null); setSelected([]); setError(failure.message); }
    finally { setWorking(false); }
  }

  async function mutate(action, batch) {
    setWorking(true);
    setError("");
    try {
      const cleaning = action === "clean";
      const choices = inventory.items.filter(item => selected.includes(item.id));
      const confirmed = await onConfirm({
        title: cleaning ? "清理所选缓存？" : "恢复这批缓存？",
        body: cleaning
          ? `将移动 ${choices.length} 项缓存（${size(choices.reduce((total, item) => total + item.bytes, 0))}）到恢复批次。\n${choices.map(item => item.title).join("、")}。\n账户、登录、会话、项目和持久磁盘保留。恢复副本仍占用空间，客户端下次使用时可能重新生成缓存。`
          : "只恢复这批缓存。正在使用的缓存和原位置新生成的数据都会保留，恢复不会覆盖它们。",
        confirm: cleaning ? "清理缓存" : "恢复缓存",
      });
      if (!confirmed) return;
      const outcome = await api(`/environment/cache/${action}`, cleaning
        ? { plan_id: inventory.plan_id, items: selected }
        : { batch });
      setResult({ ...outcome, action, titles: Object.fromEntries(inventory.items.map(item => [item.id, item.title])) });
      // Refresh only as part of the explicit user operation, never on mount or a timer.
      await inspect();
    } catch (failure) {
      setInventory(null);
      setSelected([]);
      setError(failure.message);
    } finally { setWorking(false); }
  }

  return <section className="environment-cache" aria-label="环境清理" aria-busy={working}>
    <h4>环境清理</h4>
    <p className="field-help">手动盘点当前环境的缓存和受保护资料。清理仅移动允许清单中的缓存，保留登录、会话、项目、设置和持久磁盘。</p>
    <div className="environment-version-actions">
      <button disabled={disabled} onClick={refresh}>{working ? "正在处理…" : inventory ? "重新盘点" : "只读盘点"}</button>
      {inventory && <button disabled={disabled || !selected.length} onClick={() => mutate("clean")}>清理所选缓存{selected.length ? `（${selected.length}）` : ""}</button>}
    </div>
    {!status.ready && <p className="field-help">请先启动当前环境。此操作不会扫描本机其他应用的数据。</p>}
    {error && <p role="alert" className="error-message">{error}</p>}
    {result && <div role="status" className="cache-result">
      <strong>{result.status === "partial" ? "部分项目未完成，请查看原因" : result.action === "clean" ? "缓存清理完成，恢复副本已保留" : "缓存恢复完成"}</strong>
      {result.outcomes.map(item => <p key={item.id}>{result.titles[item.id] || item.id}：{item.reason}</p>)}
    </div>}
    {inventory && <>
      <p className="field-help">{inventory.notice}</p>
      <div className="cache-items">
        {inventory.items.filter(item => item.state !== "absent").map(item => <label className="check-line cache-item" key={item.id}>
          <input type="checkbox" aria-label={item.title} checked={selected.includes(item.id)} disabled={disabled || item.state !== "ready"}
            onChange={event => setSelected(previous => event.target.checked ? [...previous, item.id] : previous.filter(id => id !== item.id))} />
          <span><strong>{item.title}</strong><small>{item.location}</small><small>{item.reason}</small></span>
          <span className="cache-size">{size(item.bytes)}<small>{item.files} 个文件</small></span>
        </label>)}
      </div>
      {!inventory.items.some(item => item.state !== "absent") && <p>未发现允许清单中的缓存。</p>}
      <p className="field-help">运行中的客户端、无法确认归属或含链接的目录会保留。清理前会再次核对，预览有效期为 10 分钟。</p>
      <div className="cache-protected" aria-label="受保护资料">
        <strong>以下资料保留</strong>
        {inventory.protected.map(item => <p key={item.title}>{item.title}：发现 {item.present} 处{item.unknown ? `，${item.unknown} 处无法确认` : ""}</p>)}
        <p>系统凭据库、项目内容和持久磁盘不参与扫描或清理。存在状态不能判断是否已登录，以账户页和客户端实际显示为准。</p>
      </div>
      <div className="cache-recovery" aria-label="缓存恢复批次">
        <h4>缓存恢复批次</h4>
        <p className="field-help">副本仍占用磁盘空间，不会自动删除。恢复不覆盖新生成的数据。</p>
        {!inventory.batches.length && <p className="field-help">暂无可恢复批次。</p>}
        {inventory.batches.map(batch => <div className="setting-summary-row" key={batch.id}>
          <span>{new Date(batch.created_at * 1000).toLocaleString()} · {batch.count} 项缓存</span>
          <button disabled={disabled} onClick={() => mutate("restore", batch.id)}>恢复此批缓存</button>
        </div>)}
      </div>
    </>}
    <p className="field-help">盘点、保护资料与可恢复清理的流程参考了 claude-post-ban-cleanup 和 Claude-Guardian，由 Cage 独立实现。</p>
  </section>;
}
