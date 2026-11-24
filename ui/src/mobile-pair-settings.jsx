import React, { useEffect, useRef, useState } from "react";
import { api, writeClipboard } from "./api";
import { QrCode } from "lucide-react";
import { useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";

function normalizeDevices(state) {
  if (Array.isArray(state?.devices)) return state.devices;
  if (state?.paired) return [{ id: "", device: state.device ?? null }];
  return [];
}

function deviceSignatureMap(list) {
  return new Map(list.map((item) => [item.id, item.paired_at || ""]));
}

function hasNewPairing(baseline, list) {
  if (!baseline) return false;
  for (const item of list) {
    if (!baseline.has(item.id) || baseline.get(item.id) !== (item.paired_at || "")) {
      return true;
    }
  }
  return false;
}

export function MobilePairSettings({ relay, savedRelay, onChange, onConfirm }) {
  const tr = useTranslation();
  const [custom, setCustom] = useState(Boolean(relay));
  const [token, setToken] = useState("");
  const [code, setCode] = useState(null);
  const [devices, setDevices] = useState([]);
  const [loaded, setLoaded] = useState(false);
  const [maxDevices, setMaxDevices] = useState(8);
  const [power, setPower] = useState(null);
  const [mobileAccessBlocked, setMobileAccessBlocked] = useState(false);
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState("");
  const statusRevision = useRef(0);
  const pairBaseline = useRef(null);
  const paired = devices.length > 0;
  useEffect(() => {
    let active = true;
    const check = async () => {
      const revision = statusRevision.current;
      try {
        const state = await api("/mobile-pair");
        if (!active || revision !== statusRevision.current) return;
        const nextDevices = normalizeDevices(state);
        setDevices(nextDevices);
        setLoaded(true);
        if (typeof state.max_devices === "number" && state.max_devices > 0) {
          setMaxDevices(state.max_devices);
        }
        setPower(nextDevices.length > 0 ? state.keep_awake ?? null : null);
        setMobileAccessBlocked(Boolean(state.mobile_access_blocked));
        if (code && hasNewPairing(pairBaseline.current, nextDevices)) {
          pairBaseline.current = null;
          setCode(null);
          setNotice("新设备已配对。");
        } else if (code && Date.now() >= Date.parse(code.expires)) {
          pairBaseline.current = null;
          setCode(null);
          setNotice("二维码已过期，请重新生成。");
        } else if (code && state.status.includes("未完成")) {
          pairBaseline.current = null;
          setCode(null);
          setNotice(state.status);
        }
      } catch (e) {
        if (active && revision === statusRevision.current) {
          setLoaded(true);
          setNotice(e.message);
        }
      }
    };
    check();
    const timer = setInterval(check, code ? 1500 : 3000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [code]);
  async function generate() {
    setBusy(true);
    setNotice("");
    setCode(null);
    pairBaseline.current = null;
    try {
      const invitation = await api("/mobile-pair", {
        relay: savedRelay,
        admin: custom ? token : "",
      });
      const authoritative = Array.isArray(invitation?.devices)
        ? invitation.devices
        : normalizeDevices(await api("/mobile-pair"));
      statusRevision.current++;
      setDevices(authoritative);
      setLoaded(true);
      pairBaseline.current = deviceSignatureMap(authoritative);
      setCode(invitation);
    } catch (e) {
      pairBaseline.current = null;
      setNotice(e.message);
    } finally {
      setBusy(false);
    }
  }
  async function remove(item) {
    if (
      !(await onConfirm({
        title: "移除配对手机？",
        body: item?.device?.model
          ? tr("移除 {0} 后，这部手机需要重新扫码才能访问工作台。", item.device.model)
          : "这部手机需要重新扫码才能访问工作台。",
        confirm: "移除手机",
      }))
    )
      return;
    setBusy(true);
    try {
      const res = await api("/mobile-pair", item?.id ? { id: item.id } : {}, "DELETE");
      statusRevision.current++;
      const remaining = Array.isArray(res?.devices)
        ? res.devices
        : devices.filter((entry) => entry.id !== item?.id);
      setDevices(remaining);
      if (remaining.length === 0) setPower(null);
      if (pairBaseline.current && item?.id) pairBaseline.current.delete(item.id);
      setNotice("");
    } catch (e) {
      setNotice(e.message);
    } finally {
      setBusy(false);
    }
  }
  // Center the QR and its actions. Relay controls use the same theme below.
  return (
    <section className="remote-link-settings" aria-label={tr("远程连接")}>
      <div className="section-title">
        <h3>{tr("远程连接")}</h3>
        <p>
          {tr("手机配对后可以打开远程浏览器和 Claude Desktop，也可以继续 Claude Code 会话、处理工具审批。")}
        </p>
      </div>
      <section className="remote-pair-card" aria-label={tr("手机配对")}>
        <h4>
          {paired ? tr("已配对设备") : tr("用手机连接 Cage")}
          {paired && (
            <span className="remote-pair-count">
              {devices.length}/{maxDevices}
            </span>
          )}
        </h4>
        {paired && (
          <ul className="remote-pair-list" aria-label={tr("已配对设备")}>
            {devices.map((item, index) => (
              <li key={item.id || index} className="remote-pair-item">
                <div className="remote-pair-device" aria-label={tr("已配对手机")}>
                  {item.device?.model ? (
                    <>
                      <strong>{item.device.model}</strong>
                      <span>{item.device.platform === "ios" ? "iOS" : "Android"}</span>
                    </>
                  ) : mobileAccessBlocked ? (
                    <span>{tr("当前账户模式暂不支持手机访问，请在「设置 → 账户」设置密码或开启「记住登录」后重新连接。")}</span>
                  ) : (
                    <span>{tr("型号待获取，请更新手机端 Cage 后重新连接。")}</span>
                  )}
                </div>
                <button type="button" disabled={busy} onClick={() => remove(item)}>
                  {tr("移除")}
                </button>
              </li>
            ))}
          </ul>
        )}
        {mobileAccessBlocked && (
          <p role="alert">
            {tr("当前电脑处于免密码本机工作区或未开启「记住登录」，手机连接后会无法访问。请在「设置 → 账户」设置账户密码或开启「记住登录」。")}
          </p>
        )}
        {paired ? (
          <p role="status">{tr("在手机上点「重新连接」即可打开工作台。")}</p>
        ) : (
          <p className="remote-pair-instruction">
            {tr("打开手机 Cage，选择「连接电脑」，用「扫码连接」扫描二维码，或选择「使用配对码」粘贴电脑上复制的配对码。")}
          </p>
        )}
        {paired && power?.active && <p className="note">{tr("远程待机已请求主机保持运行，屏幕可正常熄灭。移除配对或结束后台服务后恢复原系统休眠策略。")}</p>}
        {paired && power?.error && <p role="alert">{tr("主机防休眠未生效：")}{tr(power.error)}</p>}
        {code && (
          <div className="remote-pair-code">
            <img
              src={code.image}
              alt={tr("手机配对二维码")}
              width="320"
              height="320"
            />
            <p className="field-help">{tr("二维码约两分钟有效，过期后可刷新。")}</p>
          </div>
        )}
        {!code && !paired && (
          <div className="remote-pair-placeholder" aria-hidden="true">
            <QrCode size={64} strokeWidth={1.25} />
          </div>
        )}
        {devices.length >= maxDevices ? (
          <p className="remote-pair-privacy">
            {tr("已达配对上限（{0} 台），请先移除不需要的设备。", maxDevices)}
          </p>
        ) : (
          <div className="remote-pair-actions">
            <button
              type="button"
              className="primary"
              disabled={!loaded || busy || relay !== savedRelay || (custom && !relay)}
              onClick={generate}
            >
              <QrCode size={18} />
              {busy
                ? tr("正在生成…")
                : code
                  ? tr("刷新二维码")
                  : paired
                    ? tr("配对新设备")
                    : tr("生成配对二维码")}
            </button>
            {code && (
              <button
                type="button"
                onClick={async () => {
                  try {
                    await writeClipboard(code.manual);
                    setNotice("一次性配对码已复制，可在手机上粘贴。");
                  } catch {
                    setNotice("复制失败，请使用二维码。");
                  }
                }}
              >
                {tr("复制配对码")}
              </button>
            )}
          </div>
        )}
        {(!paired || code) && (
          <p className="remote-pair-privacy">
            {tr("只将二维码或配对码交给你希望授权的手机。")}
          </p>
        )}
        {notice && <p role="status">{localizeBackendMessage(notice, tr)}</p>}
      </section>
      <section className="remote-relay-settings" aria-label={tr("中继设置")}>
        <div className="remote-relay-row">
          <div>
            <h4>{tr("中继服务")}</h4>
            <p className="field-help">
              {tr("通过中继连接手机与电脑，可使用官方或自建服务。")}
            </p>
          </div>
          <div
            className="remote-relay-options"
            role="group"
            aria-label={tr("中继服务")}
          >
            <button
              type="button"
              aria-pressed={!custom}
              onClick={() => {
                setCustom(false);
                setToken("");
                onChange("");
                pairBaseline.current = null;
                setCode(null);
              }}
            >
              {tr("Cage 官方中继")}
            </button>
            <button
              type="button"
              aria-pressed={custom}
              onClick={() => setCustom(true)}
            >
              {tr("自建中继")}
            </button>
          </div>
        </div>
        {custom && (
          <>
            <label>
              {tr("自建中继地址")}
              <input
                value={relay}
                placeholder="https://relay.example.com"
                onChange={(e) => {
                  onChange(e.target.value);
                  setToken("");
                  pairBaseline.current = null;
                  setCode(null);
                }}
              />
            </label>
            <label>
              {tr("管理令牌（可选）")}
              <input
                type="password"
                autoComplete="off"
                value={token}
                onChange={(e) => setToken(e.target.value)}
              />
            </label>
          </>
        )}
        {relay !== savedRelay && (
          <small>{tr("保存设置后，可使用新中继生成配对码。")}</small>
        )}
      </section>
    </section>
  );
}
