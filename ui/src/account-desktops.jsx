import React, { useRef, useState, useEffect, useCallback, forwardRef, useImperativeHandle } from "react";
import {
  ChevronDown,
  ChevronRight,
  Monitor,
  Columns2,
  PanelLeftOpen,
  Pencil,
  Power,
  Plus,
  Trash2,
} from "lucide-react";
import RFB from "../.generated/novnc/core/rfb.js";
import { InlineSelect } from "./inline-select";
import { api, native, nativeAvailable } from "./api";
import { AccountIconNumber } from "./agent-icon";
import { BrowserUploads } from "./browser-uploads";
import { ProjectDialog } from "./project-dialog";
import { sendFiles } from "./file-transfer";
import { shareClipboard } from "./shared-clipboard.mjs";
import { desktopInputChannel } from "./desktop-input-channel.mjs";
import { installRemoteInput } from "./remote-input";
import { guestKeys } from "./guest-keys.mjs";
import { followDisplaySize } from "./remote-display.mjs";
import { runtimeSocketURL } from "./client-context.mjs";
import { translate, useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";
import {
  desktopClipboardState,
  desktopMergeNoticeTarget,
  desktopTitle as title,
  reconcileAccountDesktopPanes,
} from "./account-desktop-state.mjs";

const entry = (id) => "/desktop-instances/" + id;

function DesktopAccountLabel({ account }) {
  return <>
    <Monitor size={20} strokeWidth={1.75} aria-hidden="true">
      {account.number > 0 && <AccountIconNumber number={account.number} y={10} />}
    </Monitor>
    <span className="account-desktop-label">{account.label}</span>
  </>;
}

function DesktopRowAction({ label, icon: Icon, disabled, className = "", onClick }) {
  return (
    <button
      type="button"
      className={`icon-button ${className}`.trim()}
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
    >
      <Icon size={14} />
    </button>
  );
}

export function useAccountDesktops(onShow, onHide, notify, confirm, visible, revision) {
  const [expanded, setExpanded] = useState(false);
  const [catalog, setCatalog] = useState(null);
  const [panes, setPanes] = useState([]);
  const [pending, setPending] = useState("");
  const lock = useRef(false);
  const generation = useRef(0);
  const panesRef = useRef(panes);
  panesRef.current = panes;
  const visibleRef = useRef(visible);
  visibleRef.current = visible;
  const previousDefaultRedirect = useRef(null);
  const notifyRef = useRef(notify);
  notifyRef.current = notify;
  const refresh = useCallback(async (background = false) => {
    const g = ++generation.current;
    try {
      const next = await api("/desktop-instances");
      if (g !== generation.current) return;
      const nextDefaultRedirect = next?.redirects?.default || "";
      if (previousDefaultRedirect.current !== null) {
        const merged = desktopMergeNoticeTarget(previousDefaultRedirect.current, next);
        if (merged) {
          notifyRef.current(translate("默认 Desktop 与 {0} 为同一账号，已合并到该实例。", title(merged)));
        }
      }
      previousDefaultRedirect.current = nextDefaultRedirect;
      setCatalog(next);
      setPanes((old) => reconcileAccountDesktopPanes(old, next, Boolean(visibleRef.current)));
      return next;
    } catch (e) {
      if (g === generation.current && !(background && e.status === 409)) notifyRef.current(e.message);
    }
  }, []);
  // Guest login and window lifecycle events advance the revision in the
  // normal workbench status response. Idle panes need no extra SSH polling.
  useEffect(() => {
    if (expanded || visible) void refresh(true);
  }, [expanded, visible, revision, refresh]);
  const focus = id => requestAnimationFrame(() =>
    document.getElementById("desktop-pane-" + id)?.querySelector("canvas")?.focus());
  async function act(account, action, beside = false, create = false, anchor = null) {
    if (lock.current) return;
    lock.current = true;
    try {
      if (
        action === "stop" &&
        !(await confirm({
          title: translate("关闭 {0}？", title(account)),
          body: translate("请先保存这个 Desktop 中的工作。"),
          confirm: translate("关闭实例"),
          anchor,
        }))
      )
        return;
      setPending(account.id || "create");
      if (create) {
        account = await api("/desktop-instances", {account_id: account.id});
        setPending(account.id);
      }
      if (action === "start" && account.id === "default") {
        setPanes([]); onShow(); return true;
      }
      if (action === "start" && panesRef.current.some(p => p.id === account.id) && account.running) {
        onShow(); focus(account.id); return true;
      }
      let job = await api(entry(account.id) + "/" + action, {});
      while (job.running) {
        await new Promise((resolve) => setTimeout(resolve, 500));
        job = await api("/agent/jobs/" + job.id);
      }
      if (job.error || job.canceled) throw new Error(job.error || "操作已取消");
      if (action === "start") {
        setPanes((old) => {
          if (old.some((pane) => pane.id === account.id)) return old;
          if (!beside || !old.length) return [account];
          return [old[0], account];
        });
        onShow();
      } else {
        const closingOnly = panesRef.current.length === 1 && panesRef.current[0].id === account.id;
        setPanes((old) => old.filter((a) => a.id !== account.id));
        if (closingOnly) onHide();
      }
      await refresh();
      if (action === "start") focus(account.id);
      return true;
    } catch (e) {
      notify(e.message);
    } finally {
      lock.current = false;
      setPending("");
    }
  }
  async function rename(account, label) {
    const updated = await api(entry(account.id) + "/metadata", { label });
    setPanes((old) => old.map((pane) => pane.id === account.id ? { ...pane, label: updated.label } : pane));
    await refresh();
    return updated;
  }
  async function remove(account, anchor = null) {
    if (lock.current) return false;
    const confirmed = await confirm({
      title: translate("删除 {0}？", title(account)),
      body: account.running
        ? translate("删除会先关闭此 Desktop 实例，并清除该实例在环境内的本地应用数据与登录状态。不会删除 Claude 账号或 Claude Code 会话历史。")
        : translate("删除会移除此 Desktop 实例，并清除该实例在环境内的本地应用数据与登录状态。不会删除 Claude 账号或 Claude Code 会话历史。"),
      confirm: translate("删除实例"),
      pendingLabel: translate("正在删除…"),
      danger: true,
      anchor,
      submit: async () => {
        if (lock.current) throw new Error(translate("另一个操作仍在进行，请稍候"));
        lock.current = true;
        setPending(account.id);
        try {
          await api(entry(account.id), undefined, "DELETE");
          const closingOnly = panesRef.current.length === 1 && panesRef.current[0].id === account.id;
          setPanes((old) => old.filter((a) => a.id !== account.id));
          if (closingOnly) onHide();
          await refresh();
        } finally {
          lock.current = false;
          setPending("");
        }
      },
    });
    return Boolean(confirmed);
  }
  return {
    expanded,
    catalog,
    panes,
    pending,
    act,
    rename,
    remove,
    refresh,
    clear: () => setPanes([]),
    toggle: () => {
      setExpanded(!expanded);
    },
  };
}

export function DesktopList({ model, disabled, selected, defaultRunning = false, onDefault, onDefaultStop, onRename }) {
  const tr = useTranslation();
  const { catalog, expanded, panes, pending } = model;
  const [creating, setCreating] = useState(false);
  const [accountID, setAccountID] = useState("");
  const instances = catalog?.instances ?? catalog?.accounts ?? [];
  const original = instances.find(a => a.id === "default");
  const defaultEntry = original || { id: "default", label: tr("默认 Desktop") };
  const isDefaultRunning = Boolean(defaultEntry.running || defaultRunning);
  const defaultSelected = selected && panes.length === 0;
  const focusRowActions = (event) => {
    event.preventDefault();
    event.currentTarget.focus();
    event.currentTarget.parentElement?.querySelector(".terminal-row-actions button:not(:disabled)")?.focus();
  };
  const choose = async () => {
    if (await model.act({id:accountID}, "start", false, true)) setCreating(false);
  };
  return (
    <div className="account-desktop-nav">
      <button
        className={"sidebar-link " + (selected ? "selected" : "")}
        aria-expanded={expanded}
        onClick={model.toggle}
      >
        <Monitor size={17} />
        Claude Desktop{" "}
        {expanded ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
      </button>
      {expanded && (
        <div className="account-desktop-list">
          {!catalog?.redirects?.default && (
            <div className={`account-desktop-row ${defaultSelected ? "active" : ""}`}>
              <button
                className="sidebar-link"
                title={title(defaultEntry)}
                aria-pressed={defaultSelected}
                disabled={disabled}
                onClick={onDefault}
                onContextMenu={focusRowActions}
                onKeyDown={(event) => {
                  if (event.key === "F10" && event.shiftKey) focusRowActions(event);
                }}
              >
                <DesktopAccountLabel account={defaultEntry} />
                {isDefaultRunning ? <span className="account-desktop-status" aria-hidden="true">●</span> : null}
              </button>
              {((isDefaultRunning && onDefaultStop) || (catalog?.supported && onRename)) && (
                <div className="terminal-row-actions" role="group" aria-label={tr("{0} 的操作", title(defaultEntry))}>
                  {isDefaultRunning && onDefaultStop && (
                    <DesktopRowAction
                      label={tr("关闭")}
                      icon={Power}
                      disabled={Boolean(pending) || disabled}
                      onClick={(event) => onDefaultStop(defaultEntry, event.currentTarget)}
                    />
                  )}
                  {catalog?.supported && onRename && (
                    <DesktopRowAction
                      label={tr("重命名")}
                      icon={Pencil}
                      disabled={Boolean(pending) || disabled}
                      onClick={() => onRename(defaultEntry)}
                    />
                  )}
                </div>
              )}
            </div>
          )}
          {catalog?.supported &&
            instances.filter(a => a.id !== "default").map((a) => {
              const active = selected && panes.some((p) => p.id === a.id);
              return (
                <div className={`account-desktop-row ${active ? "active" : ""}`} key={a.id}>
                  <button
                    title={title(a) + (a.running ? " " + tr("· 运行中") : " " + tr("· 未启动"))}
                    aria-label={title(a) + (pending === a.id ? " " + tr("· 处理中") : a.running ? " " + tr("· 运行中") : " " + tr("· 未启动"))}
                    aria-pressed={active}
                    className="sidebar-link"
                    disabled={Boolean(pending) || disabled}
                    onClick={() => model.act(a, "start")}
                    onContextMenu={focusRowActions}
                    onKeyDown={(event) => {
                      if (event.key === "F10" && event.shiftKey) focusRowActions(event);
                    }}
                  >
                    <DesktopAccountLabel account={a} />
                    {pending === a.id || a.running ? <span className="account-desktop-status" aria-hidden="true">{pending === a.id ? "…" : "●"}</span> : null}
                  </button>
                  <div className="terminal-row-actions" role="group" aria-label={tr("{0} 的操作", title(a))}>
                    {panes.length > 0 && !panes.some((p) => p.id === a.id) && (
                      <DesktopRowAction
                        label={tr("并排打开")}
                        icon={Columns2}
                        disabled={Boolean(pending) || disabled}
                        onClick={() => model.act(a, "start", true)}
                      />
                    )}
                    {a.running && (
                      <DesktopRowAction
                        label={tr("关闭")}
                        icon={Power}
                        disabled={Boolean(pending) || disabled}
                        onClick={(event) => model.act(a, "stop", false, false, event.currentTarget)}
                      />
                    )}
                    {onRename && (
                      <DesktopRowAction
                        label={tr("重命名")}
                        icon={Pencil}
                        disabled={Boolean(pending) || disabled}
                        onClick={() => onRename(a)}
                      />
                    )}
                    <DesktopRowAction
                      label={tr("删除")}
                      icon={Trash2}
                      className="desktop-delete-action"
                      disabled={Boolean(pending) || disabled}
                      onClick={(event) => model.remove(a, event.currentTarget)}
                    />
                  </div>
                </div>
              );
            })}
          {catalog?.supported && Array.isArray(catalog.instances) && <div className="desktop-create">
            <button type="button" className="sidebar-link" disabled={disabled || Boolean(pending)}
              aria-expanded={creating} onClick={() => setCreating(!creating)}><Plus size={16}/>{tr("新建 Desktop")}</button>
            {creating && <div className="desktop-create-fields">
              <label>{tr("所属账号")}<InlineSelect inline aria-label={tr("Desktop 所属账号")} value={accountID} onValueChange={setAccountID}>
                <option value="">{tr("在此实例中登录")}</option>
                {catalog.accounts.map(a => <option key={a.id} value={a.id}>{title(a)}</option>)}
              </InlineSelect></label>
              <button className="primary" disabled={disabled || Boolean(pending)} onClick={choose}>{tr("打开 Desktop")}</button>
            </div>}
          </div>}
        </div>
      )}
    </div>
  );
}

// DesktopPane shows one Desktop instance's display. A plugin's desktop
// application uses it too, with its own address and labels.
export function DesktopPane({
  account,
  path: instancePath = entry(account.id),
  heading,
  inputLabel = "Claude Desktop 输入",
  closeLabel = "关闭实例",
  ready = true,
  visible,
  windowVisible = true,
  focused,
  onFocus,
  keyboard,
  systemClipboard = true,
  onStatus,
  onFallback,
  onControl,
  close,
  busy,
}) {
  const tr = useTranslation();
  const box = useRef(null);
  const rfb = useRef(null);
  const input = useRef(null);
  const clipboardRef = useRef(null);
  const clipboardReady = useRef(false);
  const screenVisible = visible && windowVisible;
  const clipboardState = desktopClipboardState({
    ready,
    focused,
    visible,
    windowVisible,
    native: nativeAvailable(),
  });
  const clipboardVisibleRef = useRef(clipboardState.pollingVisible);
  clipboardVisibleRef.current = clipboardState.pollingVisible;
  const current = useRef({ visible: screenVisible && focused, keyboard, systemClipboard, onStatus, onFallback });
  current.current = { visible: screenVisible && focused, keyboard, systemClipboard, onStatus, onFallback };
  const [message, setMessage] = useState("");
  const [sizeError, setSizeError] = useState(false);
  const [attempt, retry] = useState(0);
  const sizingRef = useRef(null);
  useEffect(() => {
    if (!focused) input.current?.deactivate();
  }, [focused]);
  useEffect(() => {
    if (!clipboardState.connected) return;
    clipboardReady.current = false;
    const clipboard = shareClipboard({
      connect: () => new WebSocket(runtimeSocketURL(instancePath + "/clipboard")),
      native,
      onStatus: (text) => current.current.onStatus?.(text),
      setActive: (active) => { clipboardReady.current = Boolean(active); },
      isVisible: () => clipboardVisibleRef.current,
    });
    clipboardRef.current = clipboard;
    return () => {
      clipboardReady.current = false;
      clipboardRef.current = null;
      clipboard.dispose();
    };
  }, [account.id, account.generation, clipboardState.connected, instancePath]);
  useEffect(() => {
    clipboardRef.current?.setVisible(clipboardState.pollingVisible);
  }, [clipboardState.pollingVisible]);
  useEffect(() => {
    onControl(account.id, {
      focus: () => rfb.current?.focus(),
      edit: (action) => (input.current?.focused() ? input.current.edit(action) : false),
      pasteText: (text) => input.current?.paste(text),
    });
    return () => onControl(account.id, null);
  }, [account.id, onControl]);
  useEffect(() => {
    if (!screenVisible) return;
    const container = box.current;
    if (!container) return;
    let live = true,
      client,
      keys,
      channel,
      sizing;
    const valid = () => live && current.current.visible;
    setMessage("正在连接…");
    setSizeError(false);
    const path = instancePath;
    const stopSizing = () => {
      sizing?.dispose();
      sizing = undefined;
      sizingRef.current = null;
    };
    api(path + "/vnc-credentials")
      .then(({ password, native_input, display_scale, dynamic_scale }) => {
        if (!live) return;
        client = new RFB(container, runtimeSocketURL(path + "/vnc"), {
          credentials: { password },
        });
        rfb.current = client;
        keys = guestKeys(client.sendKey.bind(client));
        client.sendKey = keys.sendKey;
        client.scaleViewport = true;
        // The workbench sizes the display in screen pixels and sets its density;
        // noVNC's own request would use CSS pixels at 96 DPI.
        client.resizeSession = false;
        client.focusOnClick = true;
        client.addEventListener("connect", () => {
          if (!live) return;
          setMessage("");
          stopSizing();
          // A session started by an earlier version keeps 96 DPI and reports
          // display_scale 1: its pane is sized in CSS pixels as before.
          sizing = followDisplaySize({
            client, container, desktop: true, density: display_scale ?? 1, follow: dynamic_scale === true,
            resize: (size) => api(path + "/resize", size),
            onError: () => { if (live) setSizeError(true); },
            onSuccess: () => { if (live) setSizeError(false); },
          });
          sizingRef.current = sizing;
          input.current?.dispose();
          channel?.dispose();
          channel = desktopInputChannel(() => new WebSocket(runtimeSocketURL(path + "/input")));
          const request = async (value) => {
            if (value.mode !== "cancel" && !valid()) throw new Error("输入实例已切换，请重新点击输入框");
            const result = await channel.request(value);
            if (value.mode !== "cancel" && !valid()) throw new Error("输入实例已切换，请重新点击输入框");
            return result;
          };
          input.current = installRemoteInput(
            {
              events: client,
              setKeyboardTarget: (target) => client.cageSetKeyboardTarget(target),
              sendKey: (...key) => client.sendKey(...key),
              focus: () => client.focus(),
              pasteText: (text) => client.cagePasteText(text),
              runKeyAction: (action) => client.cageRunInput(action),
            },
            container,
            {
              systemClipboard: () => current.current.systemClipboard,
              clipboardActive: () => clipboardReady.current,
              syncClipboard: async () => {
                const clipboard = clipboardRef.current;
                if (!clipboard || !clipboardReady.current) throw new Error("剪贴板连接尚未就绪");
                await clipboard.sync();
                if (!valid() || clipboard !== clipboardRef.current) throw new Error("粘贴实例已切换");
              },
              caretRect: native_input ? (session) => request({ mode: "status", session }) : undefined,
              insertComposition: native_input
                ? (_id, value) => client.cageRunInput(async () => {
                    const result = await request(value);
                    if (value.mode !== "cancel" && !result.available) {
                      throw new Error("输入位置已失去焦点，请重新点击输入框");
                    }
                    return result;
                  })
                : undefined,
              insertText: (text) => !native_input
                ? client.cagePasteContent(Promise.resolve({ text }), undefined, valid)
                : client.cageRunInput(async () => {
                    const state = await request({ mode: "status" });
                    if (!state.available) throw new Error("输入位置已失去焦点，请重新点击输入框");
                    const session = crypto.randomUUID().replaceAll("-", "");
                    const begun = await request({ mode: "begin", context: state.context, session, sequence: 0 });
                    if (!begun.available) throw new Error("输入位置已失去焦点，请重新点击输入框");
                    const result = await request({ mode: "commit", session, sequence: 1, text });
                    if (!result.available) throw new Error("文字输入未完成，请重新点击输入框");
                  }),
              pasteContent: (content) => client.cagePasteContent(
                content,
                (payload) => {
                  if (!valid()) throw new Error("输入目标已切换，请重新传递文件");
                  if (payload.files || payload.fileSelection) {
                    return sendFiles(payload, "desktop", undefined, {
                      valid,
                      desktop: account.id,
                    });
                  }
                  throw new Error("请从本机复制图片后，在此实例按粘贴快捷键，或以文件方式拖入图片");
                },
                valid,
              ),
              inputLabel,
              tabId: () => `desktop:${account.id}`,
              mode: () => current.current.keyboard,
              isActive: valid,
              onStatus: (text) => current.current.onStatus?.(text),
              onFallback: (...args) => current.current.onFallback?.(...args),
            },
          );
          if (valid() && !document.activeElement?.closest(".chrome")) {
            client.focus();
          }
        });
        client.addEventListener("disconnect", () => {
          keys?.stop();
          stopSizing();
          input.current?.dispose();
          input.current = null;
          channel?.dispose();
          channel = undefined;
          if (live) setMessage("画面已断开");
        });
      })
      .catch((e) => {
        if (live) setMessage(e.message);
      });
    return () => {
      live = false;
      stopSizing();
      input.current?.dispose();
      input.current = null;
      channel?.dispose();
      keys?.stop();
      rfb.current = null;
      client?.disconnect();
    };
  }, [account.id, account.generation, screenVisible, attempt, instancePath]);
  return (
    <section
      className="account-desktop-pane"
      id={"desktop-pane-" + account.id}
      aria-label={title(account)}
      data-focused={focused}
      onPointerDownCapture={onFocus}
      onFocusCapture={onFocus}
    >
      <header>
        <strong className="account-desktop-title" title={title(account)}>
          {heading ?? <DesktopAccountLabel account={account} />}
        </strong>
        <button disabled={busy} onClick={close}>
          {tr(closeLabel)}
        </button>
      </header>
      <div className="account-desktop-screen" ref={box} />
      {sizeError && !message && (
        <div className="account-desktop-message" role="alert">
          {tr("画面尺寸未能同步，显示比例可能不准确。")}{" "}
          <button onClick={() => sizingRef.current?.retry()}>{tr("重试调整")}</button>
        </div>
      )}
      {message && (
        <div className="account-desktop-message" role="status">
          {localizeBackendMessage(message, tr)}{" "}
          {message !== "正在连接…" && (
            <button onClick={() => retry(attempt + 1)}>{tr("重新连接")}</button>
          )}
        </div>
      )}
    </section>
  );
}

export const DesktopPanes = forwardRef(function DesktopPanes(
  {
    model,
    ready = true,
    visible,
    windowVisible = true,
    keyboard,
    systemClipboard = true,
    uploads = [],
    status,
    onProjectOpened,
    onStatus,
    onFallback,
    sidebarCollapsed,
    onExpand,
  },
  ref,
) {
  const tr = useTranslation();
  const [focusedID, setFocusedID] = useState("");
  const [closedChooser, setClosedChooser] = useState("");
  const controls = useRef(new Map());
  const setControl = useCallback((id, control) => {
    if (control) controls.current.set(id, control);
    else controls.current.delete(id);
  }, []);
  const activePaneID = model.panes.some((pane) => pane.id === focusedID)
    ? focusedID
    : (model.panes[model.panes.length - 1]?.id || "");
  useImperativeHandle(
    ref,
    () => ({
      edit: (action) => {
        const target = controls.current.get(activePaneID);
        if (target?.edit(action)) return true;
        for (const control of controls.current.values()) {
          if (control.edit(action)) return true;
        }
        return false;
      },
      pasteText: (text) => {
        const target = controls.current.get(activePaneID) || controls.current.values().next().value;
        target?.pasteText(text);
      },
    }),
    [activePaneID],
  );
  const paneIDs = new Set(model.panes.map((pane) => pane.id));
  const paneUploads = uploads.filter((item) => item.desktop && paneIDs.has(item.desktop));
  const directoryRequest = paneUploads.find((item) => item.directory && item.id !== closedChooser);
  return (
    <div className="account-desktop-panes">
      {sidebarCollapsed && (
        <div className="desktop-sidebar-reveal">
          <button
            className="icon-button"
            aria-label={tr("展开侧栏")}
            title={tr("展开侧栏")}
            onClick={onExpand}
          >
            <PanelLeftOpen size={15} />
          </button>
        </div>
      )}
      {!nativeAvailable() && <BrowserUploads requests={paneUploads} onStatus={onStatus} />}
      {directoryRequest && status && (
        <ProjectDialog
          key={directoryRequest.id}
          status={status}
          activate={false}
          onClose={() => {
            setClosedChooser(directoryRequest.id);
            void api("/input/files/cancel", {
              target: "desktop",
              id: directoryRequest.id,
              desktop: directoryRequest.desktop,
            }).catch((e) => onStatus?.(e.message));
          }}
          onOpened={async (workspace) => {
            await api("/input/files/directory", {
              target: "desktop",
              id: directoryRequest.id,
              desktop: directoryRequest.desktop,
              workspace,
            });
            setClosedChooser(directoryRequest.id);
            await onProjectOpened?.(workspace);
          }}
        />
      )}
      {model.panes.map((a) => (
        <DesktopPane
          key={a.id}
          account={a}
          ready={ready}
          visible={visible}
          windowVisible={windowVisible}
          focused={activePaneID === a.id}
          onFocus={() => setFocusedID(a.id)}
          keyboard={keyboard}
          systemClipboard={systemClipboard}
          onStatus={onStatus}
          onFallback={onFallback}
          onControl={setControl}
          busy={Boolean(model.pending)}
          close={() => model.act(a, "stop")}
        />
      ))}
    </div>
  );
});
