import React, {
  createContext,
  useContext,
  useEffect,
  useId,
  useRef,
  useState,
} from "react";
import {
  Pencil,
  Check,
  ChevronDown,
  Eye,
  EyeOff,
  Fingerprint,
  LockKeyhole,
  LogOut,
  Settings,
} from "lucide-react";
import { api, native, nativeAvailable, writeClipboard } from "./api";
import { getClientContext, isRemoteClient } from "./client-context.mjs";
import logo from "./logo.png";
import "./account.css";
import { Modal } from "./modal";
import { TokenUsagePanel } from "./token-usage.jsx";
import { translate, useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";
import { LanguageSettings } from "./language-settings";

const AccountContext = createContext(null);
export const useAccount = () => useContext(AccountContext);
const localDesktop = () => getClientContext().desktop_gateway && !isRemoteClient();
const trustedLogin = (action, password) => api("/client/trusted-login", {
  action, password, connection_id: getClientContext().connection_id,
});
const credentialPresent = (status) => status === "available" || status === "authentication_required";

export function AccountProvider({ children }) {
  const [account, setAccount] = useState(null);
  const [error, setError] = useState("");
  const [deviceCheck, setDeviceCheck] = useState(null);
  const deviceKey = JSON.stringify([account?.device_binding, account?.devices?.automatic, account?.devices?.biometric]);
  const deviceSupport = deviceCheck?.key === deviceKey ? deviceCheck.value : null;
  const [deviceNotice, setDeviceNotice] = useState("");
  const [recovery, setRecovery] = useState(null);
  const sequence = useRef(0);
  const refresh = async () => {
    const current = ++sequence.current;
    try {
      const value = await api("/account");
      if (sequence.current === current) {
        setAccount(value);
        setError("");
      }
      return value;
    } catch (e) {
      if (sequence.current === current) setError(e.message);
      throw e;
    }
  };
  const act = async (action, input = {}) => {
    const current = ++sequence.current;
    const value = await api(`/account/${action}`, input);
    const { recovery_key: recoveryKey, ...view } = value;
    if (recoveryKey) {
      // A status read started during this write must not replace its result or
      // discard the only response containing the newly generated secret.
      ++sequence.current;
      setRecovery({ key: recoveryKey, binding: view.device_binding, username: view.username, reset: action === "recovery-reset" });
      setAccount(view);
    } else if (action !== "logout" && sequence.current === current) setAccount(view);
    return value;
  };
  useEffect(() => {
    if (recovery && account && (recovery.binding !== account.device_binding || (!recovery.reset && !account.unlocked))) setRecovery(null);
  }, [account?.device_binding, account?.unlocked, recovery]);
  useEffect(() => {
    if (!account || deviceSupport !== null) return;
    if (!nativeAvailable()) { setDeviceCheck({ key: deviceKey, value: { supported: false } }); return; }
    let live = true;
    native("accountDevice", { operation: "status" })
      .then((value) => { if (live) setDeviceCheck({ key: deviceKey, value: value || { supported: false } }); })
      .catch(() => { if (live) setDeviceCheck({ key: deviceKey, value: { supported: false } }); });
    return () => { live = false; };
  }, [Boolean(account), deviceKey, deviceSupport]);
  const deviceAction = async (operation, kind) => {
    if (!deviceSupport?.device_status) throw new Error("当前应用启动器需要更新，请重新安装完整的 Cage 应用后再设置快捷登录。");
    try {
      await native("accountDevice", { operation, kind });
    } finally {
      // Failed enrollment may have revoked its server record. Reconcile both
      // sides after failures too, including a credential removed while open.
      await refresh();
      setDeviceCheck(null);
    }
  };
  useEffect(() => {
    let live = true,
      timer;
    const poll = async () => {
      try {
        await refresh();
      } catch {
        /* Keep the last state while disconnected. */
      }
      if (live) timer = setTimeout(poll, document.hidden ? 10000 : 2500);
    };
    const start = async () => {
      try {
        if (location.hash) {
          await api("/session", { token: location.hash.slice(1) });
          history.replaceState(null, "", location.pathname);
        }
        window.dispatchEvent(new Event("cage-session-ready"));
        if (live) await poll();
      } catch (e) {
        if (live) setError(e.message);
      }
    };
    const locked = () => {
      // A 423 can follow a controller restart or directory change. Do not
      // auto-unlock using the previous account's cached Keychain status.
      setDeviceCheck(null);
      setAccount((previous) =>
        previous ? { ...previous, unlocked: false } : previous,
      );
      refresh().catch(() => {});
    };
    start();
    window.addEventListener("cage-account-locked", locked);
    return () => {
      live = false;
      sequence.current++;
      clearTimeout(timer);
      window.removeEventListener("cage-account-locked", locked);
    };
  }, []);
  return (
    <AccountContext.Provider value={{ account, error, refresh, act, deviceSupport, deviceAction, deviceNotice, setDeviceNotice, recovery: recovery?.binding === account?.device_binding ? recovery : null, dismissRecovery: () => setRecovery(null), recheckDevices: () => setDeviceCheck(null) }}>
      {children}
    </AccountContext.Provider>
  );
}

export function AccountAvatar({ account, large = false }) {
  const initial = Array.from(
    account?.username?.trim() || "C",
  )[0].toLocaleUpperCase();
  return (
    <span
      className={`account-avatar ${large ? "large" : ""}`}
      aria-hidden="true"
    >
      {account?.avatar ? <img src={account.avatar} alt="" /> : initial}
    </span>
  );
}

function PasswordField({
  label,
  value,
  onChange,
  autoComplete = "current-password",
}) {
  const tr = useTranslation();
  const [visible, setVisible] = useState(false);
  const id = useId();
  return (
    <div className="account-field">
      <label htmlFor={id}>{label}</label>
      <span className="password-input">
        <input
          id={id}
          type={visible ? "text" : "password"}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          autoComplete={autoComplete}
          required
          maxLength={1024}
        />
        <button
          type="button"
          className="icon-button"
          aria-label={tr(visible ? "隐藏{0}" : "显示{0}", label)}
          onClick={() => setVisible(!visible)}
        >
          {visible ? <EyeOff size={16} /> : <Eye size={16} />}
        </button>
      </span>
    </div>
  );
}

export function AccountGate({ children }) {
  const tr = useTranslation();
  const { account, error: connectionError, refresh, act, deviceSupport, deviceAction, setDeviceNotice, recovery } = useAccount();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [rememberLogin, setRememberLogin] = useState(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [recovering, setRecovering] = useState(false);
  const automaticAttempt = useRef("");
  const gateAcknowledged = useRef(false);
  const trustedAttempt = useRef(false);
  const enterTrusted = async (action) => {
    setBusy(true); setError("");
    try { await trustedLogin(action); await refresh(); }
    catch (e) { setError(e.message); }
    finally { setBusy(false); }
  };
  useEffect(() => {
    if (account?.unlocked) { trustedAttempt.current = false; setRememberLogin(null); return; }
    if (!account || !localDesktop() || trustedAttempt.current || account.require_authentication) return;
    if (!(account.trusted_local || account.trusted_login)) return;
    trustedAttempt.current = true;
    void enterTrusted("resume");
  }, [account?.unlocked, account?.exists, account?.guest_system, account?.trusted_local, account?.trusted_login, account?.require_authentication]);
  useEffect(() => {
    if (!account || account.unlocked || connectionError || gateAcknowledged.current || !nativeAvailable()) return;
    // A working lock/setup screen is a valid upgraded UI. Waiting for the
    // user's password would incorrectly leave the native update unconfirmed.
    native("workbenchReady").then((accepted) => {
      if (accepted === true) gateAcknowledged.current = true;
    }).catch(() => {});
  }, [Boolean(account), account?.unlocked, connectionError]);
  useEffect(() => {
    const id = account?.devices?.automatic;
    if ((account?.trusted_login && localDesktop()) || !id || account.unlocked || account.require_authentication || !deviceSupport?.supported || deviceSupport.device_status?.automatic !== "available" || automaticAttempt.current === id) return;
    automaticAttempt.current = id;
    setBusy(true);
    deviceAction("unlock", "automatic").catch((e) => setError(e.message)).finally(() => setBusy(false));
  }, [account?.unlocked, account?.devices?.automatic, account?.require_authentication, deviceSupport]);
  if (recovery) return <RecoveryKeyScreen />;
  if (account?.unlocked) return children;
  if (recovering) return <AccountRecovery onBack={() => setRecovering(false)} onReset={() => { setRecovering(false); setPassword(""); setConfirmation(""); setRememberLogin(false); setError(""); }} />;
  const creating = account && !account.exists;
  const rememberThisDevice = rememberLogin ?? Boolean(creating || account?.trusted_login || account?.devices?.automatic);
  const submit = async (event) => {
    event.preventDefault();
    if (busy) return;
    if (creating && password !== confirmation) {
      setError("两次密码不一致");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await act(
        creating ? "create" : "unlock",
        creating
          ? { username, password, confirm_password: confirmation }
          : { password },
      );
      if (localDesktop()) {
        try {
          await trustedLogin(rememberThisDevice ? "enable" : "disable", password);
          await refresh();
        } catch (e) {
          setDeviceNotice(rememberThisDevice ? translate("已登录，记住我未能开启：{0}。可在「设置 → 账户」中重试。", e.message) : translate("已登录，记住我未能关闭：{0}。可在「设置 → 账户」中重试。", e.message));
        }
      } else if (creating && rememberThisDevice && deviceSupport?.supported && deviceSupport.device_status) {
        try { await deviceAction("enable", "automatic"); }
        catch (e) { setDeviceNotice(translate("账户已创建，记住登录未能开启：{0}。可在「设置 → 账户」中重试。", e.message)); }
      }
      setPassword("");
      setConfirmation("");
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };
  return (
    <main className="account-gate">
      <header className="account-gate-header">
        <div className="account-brand">
          <img src={logo} alt="" />
          Cage
        </div>
        <div className="startup-language-slot"><LanguageSettings compact /></div>
      </header>
      <section
        className="account-entry"
        aria-label={creating ? isRemoteClient() ? tr("创建云端账户") : tr("创建本地账户") : tr("解锁 Cage")}
      >
        {creating ? (
          <img className="account-welcome-logo" src={logo} alt="" />
        ) : (
          account && <AccountAvatar account={account} large />
        )}
        <h1>
          {creating
            ? tr("注册 Cage 账户")
            : account
              ? tr("欢迎回来，{0}", (account.username))
              : tr("正在连接 Cage")}
        </h1>
        <p className="account-entry-intro">
          {creating
            ? isRemoteClient() ? tr("在云端创建账户，保护这台主机上的工作与登录状态。") : tr("先注册用户名和密码，再选择客机系统并安装环境。")
            : account
              ? account.trusted_local ? tr("本机工作区") : tr("输入密码，继续你的工作。")
              : isRemoteClient() ? tr("正在读取云端账户…") : tr("正在读取本机账户…")}
        </p>
        {account?.background_running && (
          <div className="account-background">
            <i />
            {tr("后台环境仍在运行，任务没有停止")}
          </div>
        )}
        {account?.trusted_local && <div className="account-trust-choice">
          <p>{localDesktop() ? tr("使用当前系统用户的本机信任凭据进入工作区。") : tr("请回到受信任的桌面设置账户密码，再从此处登录。")}</p>
          {localDesktop() && <button className="quiet-button" disabled={busy} onClick={() => enterTrusted("resume")}>{busy ? tr("正在进入…") : tr("重新进入本机工作区")}</button>}
        </div>}
        {account?.trusted_local && error && <p className="account-error" role="alert">{tr(error)}</p>}
        {account && !account.trusted_local && (
          <form onSubmit={submit}>
            <fieldset disabled={busy}>
              {creating && (
                <label className="account-field">
                  {tr("用户名")}
                  <input
                    autoFocus
                    autoComplete="username"
                    maxLength={40}
                    required
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                    placeholder={tr("你希望怎样被称呼")}
                  />
                </label>
              )}
              <PasswordField
                label={creating ? tr("设置密码") : tr("账户密码")}
                value={password}
                onChange={setPassword}
                autoComplete={creating ? "new-password" : "current-password"}
              />
              {creating && (
                <PasswordField
                  label={tr("确认密码")}
                  value={confirmation}
                  onChange={setConfirmation}
                  autoComplete="new-password"
                />
              )}
              {(error || connectionError) && (
                <p className="account-error" role="alert">
                  {localizeBackendMessage(error || connectionError, tr)}
                </p>
              )}
              {!creating && Object.values(deviceSupport?.device_status || {}).includes("missing") && (
                <p className="field-help" role="status">{tr("当前目录的快捷登录凭据缺失或已失效。请用密码解锁，再到「设置 → 账户 → 登录与解锁」重新开启。")}</p>
              )}
              {!creating && Object.values(deviceSupport?.device_status || {}).includes("unavailable") && (
                <p className="field-help" role="status">{tr("暂时无法检查系统钥匙串，请先用密码解锁。快捷登录授权记录已保留。")}</p>
              )}
              {deviceSupport?.supported && !deviceSupport.device_status && (
                <p className="field-help" role="status">{tr("当前应用启动器需要更新，暂时无法检查快捷登录状态。请先用密码进入，并重新安装完整的 Cage 应用。")}</p>
              )}
              {(localDesktop() || (creating && deviceSupport?.supported && deviceSupport.device_status)) && (
                <label className="account-device-option check-line">
                  <input type="checkbox" checked={rememberThisDevice} onChange={(e) => setRememberLogin(e.target.checked)} />
                  <span><strong>{localDesktop() ? tr("记住我") : tr("记住登录")}</strong><small>{localDesktop() ? tr("信任此电脑，下次用当前系统用户打开时直接进入。手动锁定仍需验证。") : tr("下次打开直接进入。手动锁定后仍需验证。")}</small></span>
                </label>
              )}
              {!creating && deviceSupport?.supported && credentialPresent(deviceSupport.device_status?.biometric) && account.devices?.biometric && (
                <button type="button" className="quiet-button account-submit" disabled={busy}
                  onClick={async () => {
                    setBusy(true); setError("");
                    try { await deviceAction("unlock", "biometric"); }
                    catch (e) { setError(e.message); }
                    finally { setBusy(false); }
                  }}>
                  <Fingerprint size={18} />{tr("使用 Touch ID 解锁")}
                </button>
              )}
              <button
                className="primary account-submit"
                disabled={busy || !password || (creating && (!username.trim() || (!localDesktop() && nativeAvailable() && deviceSupport === null)))}
              >
                {busy ? tr("正在解锁…") : creating ? isRemoteClient() ? tr("创建云端账户") : tr("创建本地账户") : tr("解锁 Cage")}
              </button>
              {!creating && <button type="button" className="text-button" onClick={() => { setPassword(""); setError(""); setRecovering(true); }}>{tr("忘记密码？")}</button>}
            </fieldset>
          </form>
        )}
        {!account && connectionError && (
          <>
            <p role="alert" className="account-error">
              {tr(connectionError)}
            </p>
            <button onClick={() => refresh().catch(() => {})}>{tr("重新连接")}</button>
          </>
        )}
        <p className="account-local-note">
          <LockKeyhole size={14} />
          {isRemoteClient() ? tr("账户资料保存在当前云端主机") : tr("账户资料仅保存在这台设备上")}
        </p>
        {account && !account.trusted_local && <p className="account-recovery-note">
          {creating
            ? tr("注册后会生成恢复密钥。请保存到密码管理器，忘记密码时可用它恢复。")
            : account?.recovery_ready ? tr("忘记密码时，可使用已保存的恢复密钥重置。") : tr("此账户尚未设置恢复密钥。能登录时，请到「设置 → 账户」补设。")}
        </p>}
      </section>
      {nativeAvailable() && (
        <button
          className="text-button account-exit-app"
          onClick={() => native("quitNow").catch((e) => setError(e.message))}
        >
          {account?.background_running ? tr("退出应用，后台继续运行") : tr("退出 Cage")}
        </button>
      )}
    </main>
  );
}

function RecoveryKeyScreen() {
  const tr = useTranslation();
  const { recovery, dismissRecovery } = useAccount();
  const [saved, setSaved] = useState(false);
  const [notice, setNotice] = useState("");
  return <main className="account-gate">
    <section className="account-entry account-recovery" aria-label={tr("保存恢复密钥")}>
      <h1>{recovery.reset ? tr("密码已重置") : tr("保存你的恢复密钥")}</h1>
      <p className="account-entry-intro">{recovery.reset && tr("请保存新的恢复密钥。")}{tr("账户：")}{recovery.username}{tr("。这份密钥可以重置密码并访问原有资料，请存入密码管理器或安全的离线位置，不要分享给他人。")}</p>
      <label className="account-field">{tr("恢复密钥")}<textarea aria-label={tr("恢复密钥")} readOnly value={recovery.key} rows={4} spellCheck={false} autoComplete="off" onFocus={(event) => event.target.select()} /></label>
      <div className="account-recovery-actions">
        <button className="quiet-button" onClick={async () => {
          try { await writeClipboard(recovery.key); setNotice("恢复密钥已复制，请粘贴到密码管理器。"); }
          catch { setNotice("未能复制，请选中上方完整密钥，手动复制并保存。"); }
        }}>{tr("复制恢复密钥")}</button>
        <button className="quiet-button" onClick={async () => {
          try {
            if (nativeAvailable()) {
              if (await native("saveRecoveryKey", { key: recovery.key })) setNotice("恢复密钥文件已保存，请妥善保管。");
              return;
            }
            const url = URL.createObjectURL(new Blob([recovery.key + "\n"], { type: "text/plain;charset=utf-8" }));
            const link = document.createElement("a"); link.href = url; link.download = "账户恢复密钥.txt"; link.click();
            setTimeout(() => URL.revokeObjectURL(url), 1000);
            setNotice("请确认恢复密钥文件已保存到安全位置。");
          } catch { setNotice("未能保存文件，请重试或复制完整密钥到密码管理器。"); }
        }}>{tr("下载密钥文件")}</button>
      </div>
      {notice && <p className="field-help" role="status">{tr(notice)}</p>}
      <p className="field-help">{tr("完整密钥仅在此处显示。关闭或刷新后，可验证账户密码重新生成；重新生成后旧密钥失效。")}{recovery.reset && tr("原来的资料和 Claude 登录状态已保留，Cage 的记住我和快捷解锁已关闭，请用新密码登录后重新设置。")}</p>
      <label className="account-device-option check-line"><input type="checkbox" checked={saved} onChange={(event) => setSaved(event.target.checked)} /><span>{tr("我已安全保存完整恢复密钥")}</span></label>
      <button className="primary account-submit" disabled={!saved} onClick={dismissRecovery}>{recovery.reset ? tr("返回登录") : tr("继续使用 Cage")}</button>
    </section>
  </main>;
}

function AccountRecovery({ onBack, onReset }) {
  const tr = useTranslation();
  const { account, act } = useAccount();
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");
  const [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  return <main className="account-gate">
    <section className="account-entry account-recovery" aria-label={tr("恢复账户")}>
      <h1>{tr("使用恢复密钥重置密码")}</h1>
      <p className="account-entry-intro">{tr("为") + " "}{account.username}{" " + tr("设置新密码，原来的资料和 Claude 登录状态会保留。")}</p>
      {account.recovery_ready ? <form onSubmit={async (event) => {
        event.preventDefault();
        if (busy) return;
        if (password !== confirmation) { setError("两次密码不一致"); return; }
        setBusy(true); setError("");
        try { await act("recovery-reset", { recovery_key: code, password, confirm_password: confirmation }); onReset(); }
        catch (e) { setError(e.message); }
        finally { setBusy(false); }
      }}><fieldset disabled={busy}>
        <label className="account-field">{tr("已保存的恢复密钥")}<textarea aria-label={tr("已保存的恢复密钥")} autoFocus required value={code} onChange={(event) => setCode(event.target.value)} rows={4} maxLength={256} autoComplete="off" spellCheck={false} /></label>
        <PasswordField label={tr("新密码")} value={password} onChange={setPassword} autoComplete="new-password" />
        <PasswordField label={tr("确认新密码")} value={confirmation} onChange={setConfirmation} autoComplete="new-password" />
        <p className="field-help">{tr("重置后会换发恢复密钥，旧密钥失效。Cage 的记住我和快捷解锁会关闭。")}</p>
        {error && <p className="account-error" role="alert">{tr(error)}</p>}
        <button className="primary account-submit" disabled={!code.trim() || !password || !confirmation}>{busy ? tr("正在重置…") : tr("重置密码")}</button>
      </fieldset></form> : <p className="field-help" role="status">{tr("此账户尚未设置恢复密钥。请使用原密码或已开启的快捷登录进入，再到「设置 → 账户」补设。若所有解锁方式均不可用，当前无法恢复加密资料；现有数据会保留。")}</p>}
      <button className="text-button account-submit" disabled={busy} onClick={onBack}>{tr("返回密码登录")}</button>
    </section>
  </main>;
}

function AccountRecoveryRow({ disabled, onDirty }) {
  const tr = useTranslation();
  const { account, act } = useAccount();
  const [password, setPassword] = useState("");
  const [open, setOpen] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => { onDirty(Boolean(password)); }, [password, onDirty]);
  useEffect(() => () => onDirty(false), []);
  const showForm = !account.recovery_ready || open;
  const submit = async (event) => {
    event.preventDefault();
    if (busy || !password) return;
    setBusy(true); setError("");
    try {
      await act("recovery-generate", { password });
      setPassword("");
      setOpen(false);
    } catch (e) { setError(e.message); }
    finally { setBusy(false); }
  };
  return (
    <section className="settings-row account-security-row" aria-label={tr("账户恢复")}>
      <div className="settings-row-copy">
        <strong>{tr("恢复密钥")}</strong>
        {error && <small className="account-inline-error" role="alert">{tr(error)}</small>}
      </div>
      {showForm ? (
        <form className="account-inline-form" onSubmit={submit}>
          <input
            type="password"
            aria-label={tr("生成密钥前验证账户密码")}
            placeholder={tr("账户密码")}
            value={password}
            disabled={disabled || busy}
            autoComplete="current-password"
            onChange={(e) => setPassword(e.target.value)}
          />
          <button className="quiet-button" disabled={disabled || busy || !password}>
            {busy ? tr("正在生成…") : account.recovery_ready ? tr("重新生成恢复密钥") : tr("生成恢复密钥")}
          </button>
          {account.recovery_ready && (
            <button type="button" className="text-button" disabled={busy} onClick={() => { setOpen(false); setPassword(""); setError(""); }}>
              {tr("取消")}
            </button>
          )}
        </form>
      ) : (
        <div className="settings-row-control">
          <button type="button" className="quiet-button" disabled={disabled} onClick={() => setOpen(true)}>
            {tr("重新生成恢复密钥")}
          </button>
        </div>
      )}
    </section>
  );
}

export function AccountMenu({ onSettings, onConfirm, onStatus, busy }) {
  const tr = useTranslation();
  const { account, act, deviceNotice, setDeviceNotice } = useAccount();
  useEffect(() => {
    if (deviceNotice) { onStatus(deviceNotice); setDeviceNotice(""); }
  }, [deviceNotice]);
  const [open, setOpen] = useState(false);
  const root = useRef();
  useEffect(() => {
    if (!open) return;
    root.current.querySelector('[role="menuitem"]').focus();
    const dismiss = (event) => {
      if (
        event.key === "Escape" ||
        (event.type === "click" && !root.current?.contains(event.target))
      ) {
        if (event.key === "Escape") root.current?.querySelector(".account-trigger")?.focus();
        setOpen(false);
      }
    };
    window.addEventListener("click", dismiss);
    window.addEventListener("keydown", dismiss);
    return () => {
      window.removeEventListener("click", dismiss);
      window.removeEventListener("keydown", dismiss);
    };
  }, [open]);
  const perform = async (action) => {
    setOpen(false);
    if (
      action === "logout" &&
      !(await onConfirm({
        title: isRemoteClient() ? "退出云端账户？" : "退出本地账户？",
        body: "这会停止 Cage 环境与后台任务，并锁定登录凭据。请先保存文件。",
        confirm: "停止环境并退出账户",
      }))
    )
      return;
    try {
      await act(action);
    } catch (e) {
      onStatus(e.message);
    }
  };
  return (
    <div className="account-menu-root" ref={root}>
      <button
        className="account-trigger"
        aria-label={tr("账户菜单，{0}", (account.username))}
        aria-expanded={open}
        aria-haspopup="menu"
        onClick={() => setOpen(!open)}
      >
        <AccountAvatar account={account} />
        <span className="account-trigger-name">
          <strong>{account.username}</strong>
          <small>{account.trusted_local ? tr("受信任的本机") : isRemoteClient() ? tr("云端账户") : tr("本地账户")}</small>
        </span>
        <ChevronDown size={15} />
      </button>
      {open && (
        <div className="account-menu" role="menu" aria-label={isRemoteClient() ? tr("云端账户菜单") : tr("本地账户菜单")}
          onKeyDown={(event) => {
            if (event.key !== "ArrowDown" && event.key !== "ArrowUp") return;
            event.preventDefault();
            const items = [...event.currentTarget.querySelectorAll("button:enabled")];
            const step = event.key === "ArrowDown" ? 1 : -1;
            items[(items.indexOf(event.target) + step + items.length) % items.length].focus();
          }}
        >
          <div className="account-menu-label">{tr("你的 Cage")}</div>
          <button
            role="menuitem"
            onClick={() => {
              setOpen(false);
              onSettings("account");
            }}
          >
            <Settings size={16} />
            {tr("设置")}
          </button>
          {account.trusted_local ? <button role="menuitem" onClick={() => { setOpen(false); onSettings("account"); }}>
            <LockKeyhole size={16} />{" " + tr("设置账户密码")}
          </button> : <>
          <div className="account-menu-divider" />
          <button role="menuitem" onClick={() => perform("lock")}>
            <LockKeyhole size={16} />
            {tr("锁定 Cage")}<small>{tr("任务继续运行")}</small>
          </button>
          <button
            role="menuitem"
            disabled={busy}
            onClick={() => perform("logout")}
          >
            <LogOut size={16} />
            {tr("退出账户")}
          </button></>}
        </div>
      )}
    </div>
  );
}

export function AccountSettings({ busy: environmentBusy, onDirty, onConfirm }) {
  const tr = useTranslation();
  const { account, act, refresh } = useAccount();
  const [name, setName] = useState(account.username),
    [avatar, setAvatar] = useState(account.avatar || "");
  const [oldPassword, setOldPassword] = useState(""),
    [password, setPassword] = useState(""),
    [confirmation, setConfirmation] = useState("");
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [message, setMessage] = useState("");
  const [feedbackTarget, setFeedbackTarget] = useState("profile");
  const [recoveryDirty, setRecoveryDirty] = useState(false);
  const [passwordOpen, setPasswordOpen] = useState(false);
  const closePassword = async () => {
    if (busy) return;
    if ((oldPassword || password || confirmation) && !(await onConfirm({ title: "放弃尚未保存的密码修改？", body: "当前密码保持不变。", confirm: "放弃修改" }))) return;
    setPasswordOpen(false);
    setOldPassword(""); setPassword(""); setConfirmation("");
    if (feedbackTarget === "password") { setError(""); setMessage(""); }
  };
  const file = useRef();
  const changed =
    name !== account.username || avatar !== (account.avatar || "");
  useEffect(() => {
    onDirty?.(changed || Boolean(oldPassword || password || confirmation) || recoveryDirty);
  }, [changed, oldPassword, password, confirmation, recoveryDirty]);
  useEffect(() => () => onDirty?.(false), []);
  const upload = async (event) => {
    setFeedbackTarget("profile");
    const selected = event.target.files?.[0];
    event.target.value = "";
    if (!selected) return;
    if (
      selected.size > 2 * 1024 * 1024 ||
      !["image/png", "image/jpeg"].includes(selected.type)
    ) {
      setError("请选择不超过 2 MB 的 PNG 或 JPEG 图片");
      return;
    }
    const reader = new FileReader();
    reader.onload = () => {
      setAvatar(reader.result);
      setError("");
      setMessage("");
    };
    reader.readAsDataURL(selected);
  };
  const save = async (event, changingPassword = false) => {
    event.preventDefault();
    if (busy) return;
    setFeedbackTarget(changingPassword ? "password" : "profile");
    if (changingPassword && password !== confirmation) {
      setError("两次密码不一致");
      return;
    }
    setBusy(true);
    setError("");
    setMessage("");
    try {
      if (changingPassword && account.trusted_local) {
        await trustedLogin("protect", password);
        await refresh();
        setPassword(""); setConfirmation("");
        setMessage("已设置密码，下次打开 Cage 时使用密码登录。");
        setPasswordOpen(false);
        return;
      }
      const value = await act(
        changingPassword ? "password" : "profile",
        changingPassword
          ? {
              old_password: oldPassword,
              password,
              confirm_password: confirmation,
            }
          : { username: name, avatar },
      );
      if (changingPassword) {
        setPasswordOpen(false);
        setOldPassword("");
        setPassword("");
        setConfirmation("");
        setMessage("密码已修改，应用登录状态保持不变");
      } else {
        setName(value.username);
        setAvatar(value.avatar || "");
        setMessage("账户资料已保存");
      }
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };
  const feedback = (
    <>
      {error && (
        <p className="account-error" role="alert">
          {localizeBackendMessage(error, tr)}
        </p>
      )}
      {message && (
        <p className="account-saved" role="status">
          <Check size={15} />
          {tr(message)}
        </p>
      )}
    </>
  );
  return (
    <div className="account-settings">
      <form className="account-profile" onSubmit={(event) => save(event)}>
        <fieldset disabled={busy || environmentBusy}>
          <button type="button" className="account-avatar-edit" aria-label={tr("更换头像")}
            onClick={() => file.current.click()}>
            <AccountAvatar account={{ username: name, avatar }} large />
            <span className="account-avatar-overlay" aria-hidden="true"><Pencil size={24} /></span>
          </button>
          <input ref={file} type="file" accept="image/png,image/jpeg" hidden
            onChange={upload} aria-label={tr("选择头像图片")} />
          <input className="account-profile-name" aria-label={tr("用户名")} value={name} maxLength={40}
            autoComplete="username" onChange={(e) => setName(e.target.value)} required />
          {changed && (
            <div className="account-profile-actions">
              <button className="primary" disabled={!name.trim() || busy}><Check size={16} />{tr("保存资料")}</button>
            </div>
          )}
        </fieldset>
      </form>
      {(feedbackTarget === "profile" || !passwordOpen) && feedback}
      <TokenUsagePanel onConfirm={onConfirm} />
      {passwordOpen && <Modal title={account.trusted_local ? tr("设置账户密码") : tr("修改密码")} onClose={closePassword}>
        <form className="account-password-dialog" onSubmit={(event) => save(event, true)}>
          <fieldset disabled={busy || environmentBusy}>
            <p className="field-help">
              {tr("修改密码不会清除 Claude 的登录状态。忘记密码时，可在登录页使用已保存的恢复密钥重置。")}
            </p>
            {!account.trusted_local && <PasswordField label={tr("当前密码")} value={oldPassword} onChange={setOldPassword} />}
            <PasswordField label={tr("新密码")} value={password} onChange={setPassword} autoComplete="new-password" />
            <PasswordField label={tr("确认新密码")} value={confirmation} onChange={setConfirmation} autoComplete="new-password" />
            {feedbackTarget === "password" && feedback}
            <div className="account-password-actions">
              <button type="button" className="quiet-button" onClick={closePassword}>{tr("取消")}</button>
              <button className="primary" disabled={busy || (!account.trusted_local && !oldPassword) || !password || !confirmation}>
                {busy ? tr("正在保存…") : account.trusted_local ? tr("设置密码并关闭免账号登录") : tr("保存密码")}
              </button>
            </div>
          </fieldset>
        </form>
      </Modal>}
      <AccountDeviceSettings
        onOpenPassword={() => {
          setFeedbackTarget("password");
          setError("");
          setMessage("");
          setPasswordOpen(true);
        }}
        passwordDisabled={busy || environmentBusy}
        recoveryDisabled={busy || environmentBusy}
        onRecoveryDirty={setRecoveryDirty}
      />
    </div>
  );
}

function TrustedDeviceRow() {
  const tr = useTranslation();
  const { account, refresh } = useAccount();
  const enabled = Boolean(account.trusted_login || account.devices?.automatic);
  const [password, setPassword] = useState("");
  const [message, setMessage] = useState("");
  const [busy, setBusy] = useState(false);
  const update = async (event) => {
    event.preventDefault();
    setBusy(true); setMessage("");
    try {
      await trustedLogin(enabled ? "disable" : "enable", password);
      setPassword("");
      await refresh();
    } catch (error) { setMessage(error.message); }
    finally { setBusy(false); }
  };
  return (
    <div className="settings-row account-security-row">
      <div className="settings-row-copy">
        <strong>{tr("记住我")}</strong>
        {message && <small className="account-inline-error" role="alert">{tr(message)}</small>}
      </div>
      <form className="account-inline-form" onSubmit={update}>
        {!enabled && (
          <input
            type="password"
            aria-label={tr("验证账户密码")}
            placeholder={tr("账户密码")}
            value={password}
            disabled={busy}
            autoComplete="current-password"
            onChange={(e) => setPassword(e.target.value)}
          />
        )}
        <button className="quiet-button" disabled={busy || (!enabled && !password)}>
          {enabled ? tr("关闭记住我") : tr("信任此电脑")}
        </button>
      </form>
    </div>
  );
}

function AccountDeviceSettings({ onOpenPassword, passwordDisabled, recoveryDisabled, onRecoveryDirty }) {
  const tr = useTranslation();
  const { account, deviceSupport, deviceAction, recheckDevices } = useAccount();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [message, setMessage] = useState("");
  const showDevice = !account.trusted_local && Boolean(deviceSupport?.supported);
  const legacyLauncher = showDevice && !deviceSupport.device_status;
  const states = deviceSupport?.device_status || {};
  const issueText = (kind) => states[kind] === "missing"
    ? tr("当前目录的凭据缺失或已失效，请重新开启。")
    : states[kind] === "unavailable"
      ? tr("暂时无法检查系统钥匙串，授权记录已保留。请稍后重新检查。")
      : "";
  const biometricNote = issueText("biometric") || (!deviceSupport?.biometric_available && !credentialPresent(states.biometric) ? tr("请先在 macOS 系统设置中设置 Touch ID。") : "");
  const change = async (kind, enabled) => {
    setBusy(true); setError(""); setMessage("");
    try {
      await deviceAction(enabled ? "enable" : "disable", kind);
      setMessage(enabled ? "已开启，解锁凭据保存在这台 Mac 的系统钥匙串中" : "已关闭快捷登录授权");
    } catch (e) { setError(e.message); }
    finally { setBusy(false); }
  };
  return <section className="account-device-settings" aria-label={tr("登录与解锁")}>
    <h4>{tr("登录与解锁")}</h4>
    <div className="settings-card">
      {showDevice && !legacyLauncher && !localDesktop() && (
        <label className="settings-row settings-switch-row">
          <span className="settings-row-copy">
            <strong>{tr("记住登录")}</strong>
            {issueText("automatic") && <small>{issueText("automatic")}</small>}
          </span>
          <input
            type="checkbox"
            checked={Boolean(account.devices?.automatic) && states.automatic === "available"}
            disabled={busy || states.automatic === "unavailable"}
            onChange={(event) => change("automatic", event.target.checked)}
          />
        </label>
      )}
      {showDevice && !legacyLauncher && (
        <label className="settings-row settings-switch-row">
          <span className="settings-row-copy">
            <strong>{tr("使用 Touch ID 解锁")}</strong>
            {biometricNote && <small>{biometricNote}</small>}
          </span>
          <input
            type="checkbox"
            checked={Boolean(account.devices?.biometric) && credentialPresent(states.biometric)}
            disabled={busy || states.biometric === "unavailable" || (!deviceSupport.biometric_available && !credentialPresent(states.biometric))}
            onChange={(event) => change("biometric", event.target.checked)}
          />
        </label>
      )}
      {localDesktop() && !account.trusted_local && <TrustedDeviceRow />}
      <div className="settings-row">
        <div className="settings-row-copy">
          <strong>{tr("账户密码")}</strong>
        </div>
        <div className="settings-row-control">
          <button type="button" className="quiet-button" disabled={passwordDisabled} onClick={onOpenPassword}>
            {account.trusted_local ? tr("设置密码") : tr("修改密码")}
          </button>
        </div>
      </div>
      {!account.trusted_local && <AccountRecoveryRow disabled={recoveryDisabled} onDirty={onRecoveryDirty} />}
    </div>
    {legacyLauncher && (
      <p className="field-help" role="status">{tr("当前应用启动器需要更新，无法检查快捷登录状态。请重新安装完整的 Cage 应用后再设置；现有授权已保留。")}</p>
    )}
    {showDevice && !legacyLauncher && Object.values(states).includes("unavailable") && (
      <button type="button" className="quiet-button" disabled={busy} onClick={recheckDevices}>{tr("重新检查快捷登录")}</button>
    )}
    {error && <p className="account-error" role="alert">{tr(error)}</p>}
    {message && <p className="account-saved" role="status">{tr(message)}</p>}
  </section>;
}
