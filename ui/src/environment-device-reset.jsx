import React, { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api";
import { InlineSelect } from "./inline-select";
import { SafetyMessage } from "./safety-message";
import { activeInterfaceLanguage, translate, useTranslation } from "./interface-language.mjs";
import "./environment-device-reset.css";

const customTextFields = [
  ["hostname", "主机名", "留空使用随机 PC 名称", 15],
  ["username", "Linux 工作账户", "留空随机生成", 24],
  ["work_home", "工作账户主目录", "留空使用 /home/<工作账户>", 69],
  ["browser_username", "Linux 浏览器账户", "留空随机生成，须与工作账户不同", 24],
  ["browser_home", "浏览器账户主目录", "留空使用 /home/<浏览器账户>", 69],
  ["extension_name", "Linux 控制扩展名称", "留空随机选择", 64],
];

const customDimensions = [
  ["display", "Linux 虚拟显示"],
  ["browser", "浏览器初始窗口"],
];

function formatBytes(bytes) {
  if (!bytes || bytes < 1024) return `${bytes || 0} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GiB`;
}

export function EnvironmentDeviceReset({ status, onConfirm, onOpenProxy }) {
  const tr = useTranslation();
  const [info, setInfo] = useState(null);
  const [loading, setLoading] = useState(false);
  const [working, setWorking] = useState(false);
  const [generating, setGenerating] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [mode, setMode] = useState("rebuild");
  const [selectedItems, setSelectedItems] = useState([]);
  const [itemsInitialized, setItemsInitialized] = useState(false);
  const [irreversible, setIrreversible] = useState(false);
  const [customProfile, setCustomProfile] = useState({});
  const wasJobRunning = useRef(false);

  const load = useCallback(async () => {
    setLoading(true);
    setError("");
    try {
      const next = await api("/environment/device-reset");
      setInfo(next);
      setItemsInitialized((initialized) => {
        if (!initialized && Array.isArray(next.targets)) {
          setSelectedItems(next.targets.filter((t) => t.default_selected).map((t) => t.id));
          return true;
        }
        return initialized;
      });
    } catch (failure) {
      setError(failure.message);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    const running = Boolean(status.job?.running);
    if (wasJobRunning.current && !running) {
      load();
    }
    wasJobRunning.current = running;
  }, [status.job?.running, load]);

  const storageRecovery = Boolean(status.storage_recovery?.required);
  const busy = loading || working || generating || Boolean(status.job?.running);
  const quickDisabled = busy || storageRecovery || !info?.ready;
  const targets = info?.targets || [];
  const uncheckedIdentity = mode === "quick" && targets.some((t) => t.affects_identity && !selectedItems.includes(t.id));

  useEffect(() => {
    if (storageRecovery && mode !== "rebuild") {
      setMode("rebuild");
    }
  }, [storageRecovery, mode]);

  useEffect(() => {
    load();
  }, [storageRecovery, load]);

  function updateCustomField(key, next) {
    setCustomProfile((prev) => {
      const updated = { ...prev, [key]: next };
      if (key === "webgl" && next === "disabled") updated.graphics_backend = "auto";
      if (key === "gpu_backend" && next === "virgl" && updated.graphics_backend === "vulkan") {
        updated.graphics_backend = "opengl";
      }
      return updated;
    });
  }

  async function randomizeCustomProfile() {
    setGenerating(true);
    setError("");
    try {
      const preview = await api("/environment/device-reset/preview", {
        webgl: customProfile.webgl || "native",
        canvas: customProfile.canvas || "native",
        graphics_backend: customProfile.graphics_backend || "auto",
        gpu_backend: customProfile.gpu_backend || "default",
      });
      setCustomProfile(preview);
    } catch (failure) {
      setError(failure.message);
    } finally {
      setGenerating(false);
    }
  }

  function hasCustomFields() {
    return Object.values(customProfile).some((v) => v !== "" && v !== 0 && v !== undefined && v !== null);
  }

  async function executeReset() {
    setError("");
    setNotice("");
    const isRebuild = mode === "rebuild";
    const title = isRebuild
      ? irreversible
        ? "彻底换新机并销毁旧环境？"
        : "重建全新客机并归档旧环境？"
      : irreversible
        ? "快速换机并彻底删除所选数据？"
        : "执行原地快速换机？";
    const lines = [];
    if (isRebuild) {
      lines.push(translate("将停止当前环境，从本地缓存的基础镜像重建全新客机（全新系统盘、Linux 账户、主机名、硬件标识与全新浏览器资料）。"));
    } else {
      lines.push(translate("将停止客户端与当前环境，轮换虚拟硬件与系统指纹，并清理已勾选的 {0} 项环境资料。", selectedItems.length));
    }
    lines.push(translate("宿主机项目文件夹、代理订阅、节点配置与密码保险箱不会受到影响。"));
    if (irreversible) {
      lines.push(translate("你已勾选「彻底清理旧数据（不可恢复）」：新环境验证就绪后将立即永久删除旧数据以释放磁盘空间，无法回滚。"));
    } else {
      lines.push(translate("旧环境或已清理资料会移入受控归档，确认新环境正常前可随时回滚。"));
    }
    const confirmed = await onConfirm({
      title,
      body: lines.join("\n"),
      confirm: irreversible
        ? isRebuild
          ? "确认彻底换新机（不可恢复）"
          : "确认快速换机（不可恢复）"
        : isRebuild
          ? "开始一键换新机"
          : "开始快速换机",
    });
    if (!confirmed) return;

    setWorking(true);
    try {
      const payload = {
        mode,
        items: mode === "quick" ? selectedItems : targets.map((t) => t.id),
        irreversible,
        confirm_irreversible: irreversible,
      };
      if (isRebuild && hasCustomFields()) {
        payload.custom_profile = customProfile;
      }
      const result = await api("/environment/device-reset/execute", payload);
      if (result && !result.running) {
        await load();
      }
      setNotice(tr("换机任务已提交，请在下方查看实时进度。"));
    } catch (failure) {
      setError(failure.message);
    } finally {
      setWorking(false);
    }
  }

  async function restoreRecord(kind, id, label) {
    const confirmed = await onConfirm({
      title: kind === "archive" ? "回滚到这个旧环境？" : "恢复这批重置前的数据？",
      body: kind === "archive"
        ? translate("将停止当前环境并切回归档旧机（{0}）。宿主机项目文件与代理配置保持不变。", label)
        : "将把重置前移入恢复批次的资料还原到原位置；原位置已新生成的文件不会被覆盖。",
      confirm: kind === "archive" ? "确认回滚旧机" : "确认恢复数据",
    });
    if (!confirmed) return;
    setWorking(true);
    setError("");
    setNotice("");
    try {
      const res = await api("/environment/device-reset/restore", { kind, id });
      if (kind === "archive") {
        setNotice(tr("旧环境回滚任务已启动。"));
        if (res && !res.running) {
          await load();
        }
      } else {
        setNotice(res?.status === "partial" ? tr("部分资料已恢复，原位置已有新数据的项目已保留。") : tr("已恢复这批重置前的数据。"));
        await load();
      }
    } catch (failure) {
      setError(failure.message);
    } finally {
      setWorking(false);
    }
  }

  async function deleteRecord(kind, id) {
    const confirmed = await onConfirm({
      title: kind === "archive" ? "永久删除这份旧环境归档？" : "永久删除这批恢复副本？",
      body: "删除后将立即释放磁盘空间，且无法再从该归档回滚。当前正在使用的环境和宿主机项目文件不受影响。",
      confirm: "确认永久删除",
    });
    if (!confirmed) return;
    setWorking(true);
    setError("");
    setNotice("");
    try {
      await api("/environment/device-reset/delete-archive", { kind, id });
      setNotice(tr("已删除旧归档并释放空间。"));
      await load();
    } catch (failure) {
      setError(failure.message);
    } finally {
      setWorking(false);
    }
  }

  const runtimeLabel = info?.runtime_kind === "vz"
    ? tr("Apple Virtualization（可轮换 Machine Identifier、MAC、主机名、machine-id 与设备配置；框架不开放自定义 SMBIOS）")
    : info?.runtime_kind === "qemu"
      ? tr("QEMU 虚拟机（可轮换 UUID、SMBIOS Type 1/2/3 序列号、MAC、主机名、machine-id 与设备配置）")
      : info?.runtime_kind === "container"
        ? tr("Linux 容器（可轮换主机名、machine-id、MAC 与资料；彻底换新机可重建容器身份，宿主内核不变）")
        : "";

  return (
    <section className="environment-device-reset" aria-label={tr("一键换机与指纹重置")} aria-busy={working || loading}>
      <h4>{tr("一键换机与指纹重置")}</h4>
      <p className="field-help">
        {tr("替换客机硬件标识、系统指纹与应用资料以切断与旧环境的关联。宿主机项目文件夹、代理配置和密码保险箱永久保留。")}
      </p>

      {info && !info.supported && (
        <p className="field-help">{info.unsupported_reason || tr("当前连接方式不支持直接换机。")}</p>
      )}

      {info?.supported && (
        <>
          <div className="device-reset-current" aria-label={tr("当前客机身份摘要")}>
            <div><span>{tr("主机名")}</span><strong>{info.current?.hostname || "—"}</strong></div>
            <div><span>{tr("网卡 MAC")}</span><strong>{info.current?.mac_suffix || "—"}</strong></div>
            <div><span>{tr("机器 UUID")}</span><strong>{info.current?.uuid_prefix ? `${info.current.uuid_prefix}…` : "—"}</strong></div>
            <div><span>{tr("系统 machine-id")}</span><strong>{info.current?.machine_id_prefix ? `${info.current.machine_id_prefix}…` : "—"}</strong></div>
            <div><span>{tr("Linux 账户")}</span><strong>{info.current?.username || "—"} / {info.current?.browser_username || "—"}</strong></div>
            <div><span>{tr("音频与显示")}</span><strong>{[info.current?.audio_description, info.current?.display_size].filter(Boolean).join(" · ") || "—"}</strong></div>
          </div>
          {runtimeLabel && <p className="field-help">{runtimeLabel}</p>}

          <div className="device-reset-modes" role="radiogroup" aria-label={tr("选择换机方式")}>
            <label className={`device-reset-mode${mode === "quick" ? " selected" : ""}`}>
              <span className="device-reset-mode-head">
                <input
                  type="radio"
                  name="device-reset-mode"
                  value="quick"
                  checked={mode === "quick"}
                  disabled={busy || storageRecovery}
                  onChange={() => setMode("quick")}
                />
                <strong>{tr("快速换机（原地重置）")}</strong>
              </span>
              <small>{tr("保留系统盘与已装开发工具链，约 10–30 秒。轮换虚拟硬件与系统指纹，并按下方勾选清理应用资料；Linux 用户名不变。")}</small>
            </label>
            <label className={`device-reset-mode${mode === "rebuild" ? " selected" : ""}`}>
              <span className="device-reset-mode-head">
                <input
                  type="radio"
                  name="device-reset-mode"
                  value="rebuild"
                  checked={mode === "rebuild"}
                  disabled={busy}
                  onChange={() => setMode("rebuild")}
                />
                <strong>{tr("彻底换新机（模板重建）")}</strong>
              </span>
              <small>{tr("从本地缓存的基础镜像重建全新客机，零残留。连同 Linux 账户名、主目录、系统盘与全部指纹一起焕然一新。")}</small>
            </label>
          </div>

          {mode === "quick" && (
            <>
              <div className="cache-items" aria-label={tr("选择需要清理的数据")}>
                {targets.map((item) => (
                  <label className="check-line cache-item" key={item.id}>
                    <input
                      type="checkbox"
                      aria-label={item.title}
                      checked={selectedItems.includes(item.id)}
                      disabled={busy}
                      onChange={(event) =>
                        setSelectedItems((prev) =>
                          event.target.checked ? [...prev, item.id] : prev.filter((id) => id !== item.id)
                        )
                      }
                    />
                    <span>
                      <strong>{tr(item.title)}</strong>
                      <small>{tr(item.description)}</small>
                    </span>
                    <span className="cache-size">
                      {info.ready ? (
                        <>
                          {formatBytes(item.bytes)}
                          <small>{item.files}{" " + tr("个文件")}</small>
                        </>
                      ) : (
                        <small>{tr("待启动")}</small>
                      )}
                    </span>
                  </label>
                ))}
              </div>
              {uncheckedIdentity && (
                <p className="device-reset-warning">
                  {tr("提示：你取消勾选了部分包含登录态或设备标识的资料，换机后该部分可能仍与旧环境存在关联。")}
                </p>
              )}
              {!info.ready && (
                <p className="field-help">{tr("原地快速换机需要当前环境处于运行就绪状态；你可以先启动环境，或直接选择「彻底换新机」。")}</p>
              )}
            </>
          )}

          {mode === "rebuild" && (
            <details className="guest-profile-options">
              <summary>{tr("自定义新机名称与设备配置")}</summary>
              <p>{tr("留空将自动全随机生成一组全新的主机名、账户名、音频型号和显示尺寸；也可以手动指定或先点击下方按钮预览。")}</p>
              <fieldset disabled={busy}>
                {customTextFields.map(([key, label, placeholder, maxLength]) => (
                  <label key={key}>
                    {tr(label)}
                    <input
                      value={customProfile[key] || ""}
                      maxLength={maxLength}
                      placeholder={tr(placeholder)}
                      onChange={(event) => updateCustomField(key, event.target.value)}
                      autoComplete="off"
                    />
                  </label>
                ))}
                {customDimensions.map(([key, label]) => (
                  <label key={key}>
                    {tr(label)}
                    <span className="guest-profile-size">
                      {["width", "height"].map((axis, i) => (
                        <React.Fragment key={axis}>
                          {i > 0 && " × "}
                          <input
                            type="number"
                            min={i ? 600 : 800}
                            max={i ? 2160 : 3840}
                            placeholder={i ? tr("高（随机）") : tr("宽（随机）")}
                            value={customProfile[`${key}_${axis}`] || ""}
                            onChange={(event) =>
                              updateCustomField(
                                `${key}_${axis}`,
                                event.target.value === "" ? 0 : Number(event.target.value)
                              )
                            }
                          />
                        </React.Fragment>
                      ))}
                    </span>
                  </label>
                ))}
                <label>
                  {tr("音频设备（自动选择真实型号）")}
                  <output>
                    {customProfile.audio_description ||
                      tr("创建时从 {0} 个型号中随机选择", info.audio_models?.length || 12)}
                  </output>
                </label>
                <label>
                  {tr("图形渲染后端")}
                  <InlineSelect
                    inline
                    aria-label={tr("图形渲染后端")}
                    value={customProfile.graphics_backend || "auto"}
                    onValueChange={(value) => updateCustomField("graphics_backend", value)}
                    disabled={busy || customProfile.webgl === "disabled"}
                  >
                    <option value="auto">{tr("自动使用客机可用设备")}</option>
                    <option value="vulkan" disabled={customProfile.gpu_backend === "virgl"}>
                      {tr("ANGLE / Vulkan（需要驱动支持，VirGL 不适用）")}
                    </option>
                    <option value="opengl">ANGLE / OpenGL</option>
                    <option value="software">{tr("SwiftShader 软件兼容模式")}</option>
                  </InlineSelect>
                </label>
                {info.host_os === "linux" && !info.container && (
                  <label>
                    {tr("虚拟 GPU")}
                    <InlineSelect
                      inline
                      aria-label={tr("虚拟 GPU")}
                      value={customProfile.gpu_backend || "default"}
                      onValueChange={(value) => updateCustomField("gpu_backend", value)}
                    >
                      <option value="default">{tr("使用现有虚拟设备")}</option>
                      <option value="virgl">{tr("QEMU VirGL + DRI3（实验性，需要宿主 GPU）")}</option>
                    </InlineSelect>
                  </label>
                )}
                <label>
                  WebGL
                  <InlineSelect
                    inline
                    aria-label="WebGL"
                    value={customProfile.webgl || "native"}
                    onValueChange={(value) => updateCustomField("webgl", value)}
                  >
                    <option value="native">{tr("使用客机原生图形能力")}</option>
                    <option value="disabled">{tr("关闭网页 3D 图形能力")}</option>
                  </InlineSelect>
                </label>
                <label>
                  {tr("Canvas 图像读取")}
                  <InlineSelect
                    inline
                    aria-label={tr("Canvas 图像读取")}
                    value={customProfile.canvas || "native"}
                    onValueChange={(value) => updateCustomField("canvas", value)}
                  >
                    <option value="native">{tr("允许网页读取 Canvas 图像")}</option>
                    <option value="blocked">{tr("阻止网页读取 Canvas 图像")}</option>
                  </InlineSelect>
                </label>
                <button type="button" onClick={randomizeCustomProfile}>
                  {generating ? tr("正在生成…") : tr("重新随机生成名称和尺寸")}
                </button>
              </fieldset>
            </details>
          )}

          <div className="cache-protected" aria-label={tr("受保护资料")}>
            <strong>{tr("以下个人数据永久保留，绝不触碰")}</strong>
            {(info.protected || []).map((item) => (
              <p key={item}>{tr(item)}</p>
            ))}
          </div>

          <label className="check-line device-reset-irreversible">
            <input
              type="checkbox"
              checked={irreversible}
              disabled={busy}
              onChange={(event) => setIrreversible(event.target.checked)}
            />
            <span>
              <strong>{tr("彻底清理旧数据（不可恢复）")}</strong>
              <small>
                {tr("默认保留旧环境归档或恢复批次以便随时回滚；勾选后仅在新环境验证就绪后立即删除旧数据释放空间，失败会自动回滚。")}
              </small>
            </span>
          </label>

          <div className="environment-version-actions">
            <button
              className="primary"
              disabled={mode === "quick" ? quickDisabled : busy}
              onClick={executeReset}
            >
              {working
                ? tr("正在处理…")
                : mode === "rebuild"
                  ? tr("一键换新机")
                  : tr("执行快速换机")}
            </button>
            <button disabled={busy} onClick={load}>
              {loading ? tr("正在刷新…") : tr("刷新状态")}
            </button>
          </div>

          {error && (
            <p role="alert" className="error-message">
              <SafetyMessage message={tr(error)} />
            </p>
          )}
          {status.job?.running && (status.job.kind === "device-reset" || status.job.kind === "device-reset-restore") && (
            <p role="status" className="field-help">
              {status.job.message}
            </p>
          )}
          {notice && (
            <p role="status" className="field-help">
              {tr(notice)}
            </p>
          )}

          {info.last_result && (
            <div role="status" className="cache-result" aria-label={tr("最近一次换机摘要")}>
              <strong>
                {info.last_result.mode === "rebuild"
                  ? tr("已完成彻底换新机")
                  : tr("已完成原地快速换机")}
                {" · "}
                {new Date(info.last_result.completed_at * 1000).toLocaleString(activeInterfaceLanguage())}
              </strong>
              <div className="device-reset-diff">
                <div className="device-reset-diff-row">
                  <span>{tr("主机名")}</span>
                  <strong>{info.last_result.before?.hostname || "—"} → {info.last_result.after?.hostname || "—"}</strong>
                </div>
                <div className="device-reset-diff-row">
                  <span>{tr("网卡 MAC")}</span>
                  <strong>{info.last_result.before?.mac_suffix || "—"} → {info.last_result.after?.mac_suffix || "—"}</strong>
                </div>
                <div className="device-reset-diff-row">
                  <span>{tr("机器 UUID")}</span>
                  <strong>{info.last_result.before?.uuid_prefix || "—"} → {info.last_result.after?.uuid_prefix || "—"}</strong>
                </div>
                {info.last_result.after?.audio_description && (
                  <div className="device-reset-diff-row">
                    <span>{tr("音频设备")}</span>
                    <strong>{info.last_result.before?.audio_description || "—"} → {info.last_result.after.audio_description}</strong>
                  </div>
                )}
              </div>
              <div className="device-reset-proxy-tip">
                <span>{tr("建议同时切换代理出口节点，避免新旧环境共用同一出口 IP。")}</span>
                {onOpenProxy && (
                  <button type="button" onClick={onOpenProxy}>
                    {tr("前往切换代理节点")}
                  </button>
                )}
              </div>
            </div>
          )}

          {((info.archives && info.archives.length > 0) ||
            (info.recovery_batches && info.recovery_batches.length > 0)) && (
            <div className="cache-recovery" aria-label={tr("旧环境归档与恢复")}>
              <h4>{tr("旧环境归档与恢复")}</h4>
              <p className="field-help">{tr("归档保留在本地磁盘且不会自动删除；确认新机稳定后可手动删除以释放空间。")}</p>
              {(info.archives || []).map((archive) => (
                <div className="setting-summary-row" key={archive.id}>
                  <span>
                    {new Date(archive.created_at * 1000).toLocaleString(activeInterfaceLanguage())} · {archive.previous_hostname || archive.id} ·{" "}
                    {formatBytes(archive.bytes)}
                  </span>
                  <span className="device-reset-archive-actions">
                    <button
                      disabled={busy || storageRecovery}
                      onClick={() => restoreRecord("archive", archive.id, archive.previous_hostname || archive.id)}
                    >
                      {tr("回滚到此旧机")}
                    </button>
                    <button disabled={busy || storageRecovery} onClick={() => deleteRecord("archive", archive.id)}>
                      {tr("删除归档")}
                    </button>
                  </span>
                </div>
              ))}
              {(info.recovery_batches || []).map((batch) => (
                <div className="setting-summary-row" key={batch.id}>
                  <span>
                    {new Date(batch.created_at * 1000).toLocaleString(activeInterfaceLanguage())} · {batch.count}{" " + tr("项重置前资料")} ·{" "}
                    {formatBytes(batch.bytes)}
                  </span>
                  <span className="device-reset-archive-actions">
                    <button
                      disabled={busy || !info.ready}
                      onClick={() => restoreRecord("batch", batch.id, batch.id)}
                    >
                      {tr("恢复此批资料")}
                    </button>
                    <button disabled={busy || !info.ready} onClick={() => deleteRecord("batch", batch.id)}>
                      {tr("删除副本")}
                    </button>
                  </span>
                </div>
              ))}
            </div>
          )}
        </>
      )}
    </section>
  );
}
