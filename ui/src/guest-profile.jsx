import React, { useEffect, useState } from "react";
import { api } from "./api";
import { SafetyMessage } from "./safety-message";

const textFields = [
  ["hostname", "主机名", "留空使用随机 PC 名称", 15],
  ["username", "Linux 工作账户", "留空随机生成", 24],
  ["browser_username", "Linux 浏览器账户", "留空随机生成，须与工作账户不同", 24],
  ["audio_name", "Linux 音频设备名", "留空随机生成", 48],
  ["audio_description", "Linux 音频设备描述", "留空随机选择", 64],
  ["extension_name", "Linux 控制扩展名称", "留空随机选择", 64],
];
const dimensions = [["display", "Linux 虚拟显示"], ["browser", "浏览器初始窗口"]];

export function GuestProfileOptions({ value, onChange, disabled, system }) {
  const [saved, setSaved] = useState(null);
  const [loaded, setLoaded] = useState(false);
  const [editable, setEditable] = useState(false);
  const [error, setError] = useState("");
  const [generating, setGenerating] = useState(false);
  useEffect(() => {
    let live = true;
    api("/environment/guest-profile").then(result => {
      if (!live) return;
      setSaved(result.profile); setEditable(result.editable); setLoaded(true);
      if (!result.editable) onChange(null);
    }).catch(error => { if (live) setError(error.message); });
    return () => { live = false; };
  }, [onChange]);
  const native = system === "macos" || system === "windows-11";
  const options = saved || value || {};
  const locked = disabled || !editable || generating;
  const applicable = input => native ? Object.fromEntries(Object.entries(input).filter(([key]) => ["hostname", "browser_width", "browser_height", "webgl", "canvas"].includes(key))) : input;
  const set = (key, next) => onChange(applicable({ ...(value || {}), [key]: next }));
  async function randomize() {
    setGenerating(true); setError("");
    try { onChange(applicable(await api("/environment/guest-profile/preview", { webgl: value?.webgl || "native", canvas: value?.canvas || "native" }))); }
    catch (error) { setError(error.message); }
    finally { setGenerating(false); }
  }
  return <details className="guest-profile-options"><summary>客机名称与初始设备配置</summary>
    <p>留空的名称和尺寸会在创建时随机生成，并随环境保存。重新启动会复用原值。</p>
    {error && <p role="alert"><SafetyMessage message={error} /></p>}
    {loaded && !editable && <p>{saved ? "此环境的创建配置已保存，继续安装将复用以下设置。" : "已有环境沿用原配置；这些选项用于创建独立的新环境。"}</p>}
    {loaded && (editable || saved) && <fieldset disabled={locked}>
      {textFields.filter(([key]) => !native || key === "hostname").map(([key, label, placeholder, maxLength]) => <label key={key}>{label}<input value={options[key] || ""} maxLength={maxLength} placeholder={placeholder} onChange={event => set(key, event.target.value)} autoComplete="off" /></label>)}
      {dimensions.filter(([key]) => !native || key === "browser").map(([key, label]) => <label key={key}>{label}<span className="guest-profile-size">{["width", "height"].map((axis, i) => <React.Fragment key={axis}>{i > 0 && " × "}<input type="number" min={i ? 600 : 800} max={i ? 2160 : 3840} placeholder={i ? "高（随机）" : "宽（随机）"} value={options[`${key}_${axis}`] || ""} onChange={event => set(`${key}_${axis}`, event.target.value === "" ? 0 : Number(event.target.value))} /></React.Fragment>)}</span></label>)}
      <label>WebGL<select value={options.webgl || "native"} onChange={event => set("webgl", event.target.value)}><option value="native">使用客机原生图形能力</option><option value="disabled">关闭网页 3D 图形能力</option></select></label>
      <label>Canvas 图像读取<select value={options.canvas || "native"} onChange={event => set("canvas", event.target.value)}><option value="native">允许网页读取 Canvas 图像</option><option value="blocked">阻止网页读取 Canvas 图像</option></select></label>
      <p>关闭网页 3D 图形会影响依赖 WebGL/WebGPU 的网站。阻止 Canvas 读取会影响截图、图片编辑等网站功能。这些选项不修改虚拟 GPU 型号。</p>
      <button type="button" onClick={randomize}>{generating ? "正在生成…" : "重新随机生成名称和尺寸"}</button>
    </fieldset>}
  </details>;
}
