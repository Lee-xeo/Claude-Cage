import React, { useEffect, useState } from "react";
import { api } from "./api";
import { SafetyMessage } from "./safety-message";

const textFields = [
  ["hostname", "主机名", "留空使用随机 PC 名称", 15],
  ["username", "Linux 工作账户", "留空随机生成", 24],
  ["work_home", "工作账户主目录", "留空使用 /home/<工作账户>", 69],
  ["browser_username", "Linux 浏览器账户", "留空随机生成，须与工作账户不同", 24],
  ["browser_home", "浏览器账户主目录", "留空使用 /home/<浏览器账户>", 69],
  ["extension_name", "Linux 控制扩展名称", "留空随机选择", 64],
];
const dimensions = [["display", "Linux 虚拟显示"], ["browser", "浏览器初始窗口"]];

export function GuestProfileOptions({ value, onChange, disabled, system }) {
  const [saved, setSaved] = useState(null);
  const [loaded, setLoaded] = useState(false);
  const [editable, setEditable] = useState(false);
  const [error, setError] = useState("");
  const [generating, setGenerating] = useState(false);
  const [capabilities, setCapabilities] = useState(null);
  useEffect(() => {
    let live = true;
    api("/environment/guest-profile").then(result => {
      if (!live) return;
      setSaved(result.profile); setEditable(result.editable); setLoaded(true);
      setCapabilities(result);
      if (!result.editable) onChange(null);
    }).catch(error => { if (live) setError(error.message); });
    return () => { live = false; };
  }, [onChange]);
  const native = system === "macos" || system === "windows-11";
  const options = saved || value || {};
  const locked = disabled || !editable || generating;
  const applicable = input => native ? Object.fromEntries(Object.entries(input).filter(([key]) => ["hostname", "browser_width", "browser_height", "webgl", "canvas", "graphics_backend"].includes(key))) : input;
  const set = (key, next) => {
    const updated = { ...(value || {}), [key]: next };
    if (key === "webgl" && next === "disabled") updated.graphics_backend = "auto";
    if (key === "gpu_backend" && next === "virgl" && updated.graphics_backend === "vulkan") updated.graphics_backend = "opengl";
    onChange(applicable(updated));
  };
  async function randomize() {
    setGenerating(true); setError("");
    try { onChange(applicable(await api("/environment/guest-profile/preview", { webgl: value?.webgl || "native", canvas: value?.canvas || "native", graphics_backend: value?.graphics_backend || "auto", gpu_backend: value?.gpu_backend || "default" }))); }
    catch (error) { setError(error.message); }
    finally { setGenerating(false); }
  }
  return <details className="guest-profile-options"><summary>客机名称与初始设备配置</summary>
    <p>建议选择自然、易记、与你以往环境身份不同的名称。用户名、主目录、主机名和尺寸可自行填写；留空时生成一组初始值，后续重启保持一致。</p>
    {error && <p role="alert"><SafetyMessage message={error} /></p>}
    {loaded && !editable && <p>{saved ? "此环境的创建配置已保存，继续安装将复用以下设置。" : "已有环境沿用原配置；这些选项用于创建独立的新环境。"}</p>}
    {loaded && (editable || saved) && <fieldset disabled={locked}>
      {textFields.filter(([key]) => !native || key === "hostname").map(([key, label, placeholder, maxLength]) => <label key={key}>{label}<input value={options[key] || ""} maxLength={maxLength} placeholder={placeholder} onChange={event => set(key, event.target.value)} autoComplete="off" /></label>)}
      {dimensions.filter(([key]) => !native || key === "browser").map(([key, label]) => <label key={key}>{label}<span className="guest-profile-size">{["width", "height"].map((axis, i) => <React.Fragment key={axis}>{i > 0 && " × "}<input type="number" min={i ? 600 : 800} max={i ? 2160 : 3840} placeholder={i ? "高（随机）" : "宽（随机）"} value={options[`${key}_${axis}`] || ""} onChange={event => set(`${key}_${axis}`, event.target.value === "" ? 0 : Number(event.target.value))} /></React.Fragment>)}</span></label>)}
      {!native && <label>音频设备（自动选择真实型号）<output>{options.audio_description || `创建时从 ${capabilities?.audio_models?.length || 12} 个型号中随机选择`}</output><small>型号名称与设备标识保持对应；随机后缀仅用于本环境标识。</small></label>}
      <label>浏览器身份<output>跟随实际安装的浏览器，保持 UA 与 Client Hints 一致</output></label>
      <label>图形渲染后端<select value={options.graphics_backend || "auto"} onChange={event => set("graphics_backend", event.target.value)} disabled={locked || options.webgl === "disabled"}><option value="auto">自动使用客机可用设备</option>{system === "macos" && <option value="metal">ANGLE / Metal</option>}{system === "windows-11" && <option value="d3d11">ANGLE / D3D11</option>}{!native && <option value="vulkan" disabled={options.gpu_backend === "virgl"}>ANGLE / Vulkan（需要驱动支持，VirGL 不适用）</option>}<option value="opengl">ANGLE / OpenGL</option><option value="software">SwiftShader 软件兼容模式</option></select></label>
      {!native && capabilities?.host_os === "linux" && !capabilities.container && <label>虚拟 GPU<select value={options.gpu_backend || "default"} onChange={event => set("gpu_backend", event.target.value)}><option value="default">使用现有虚拟设备</option><option value="virgl">QEMU VirGL + DRI3（实验性，需要宿主 GPU）</option></select></label>}
      <label>WebGL<select value={options.webgl || "native"} onChange={event => set("webgl", event.target.value)}><option value="native">使用客机原生图形能力</option><option value="disabled">关闭网页 3D 图形能力</option></select></label>
      <label>Canvas 图像读取<select value={options.canvas || "native"} onChange={event => set("canvas", event.target.value)}><option value="native">允许网页读取 Canvas 图像</option><option value="blocked">阻止网页读取 Canvas 图像</option></select></label>
      <p>后端名称表示实际渲染路径，是否获得硬件加速需在环境检测中核实。关闭 WebGL 或阻止 Canvas 读取会影响部分网站；这些选项不替换真实驱动报告的显卡型号。</p>
      <button type="button" onClick={randomize}>{generating ? "正在生成…" : "重新随机生成名称和尺寸"}</button>
    </fieldset>}
  </details>;
}
