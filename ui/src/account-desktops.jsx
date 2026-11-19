import React, { useRef, useState, useEffect, useCallback } from "react";
import {
  ChevronDown,
  ChevronRight,
  Monitor,
  Columns2,
  Power,
  Plus,
} from "lucide-react";
import RFB from "../.generated/novnc/core/rfb.js";
import { InlineSelect } from "./inline-select";
import { api } from "./api";
import { AccountIconNumber } from "./agent-icon";
import { runtimeSocketURL } from "./client-context.mjs";

const title = (a) => a.number ? `#${String(a.number).padStart(2, "0")} ${a.label}` : a.label;
const entry = (id) => "/desktop-instances/" + id;

function DesktopAccountLabel({ account }) {
  return <>
    <Monitor size={20} strokeWidth={1.75} aria-hidden="true">
      {account.number > 0 && <AccountIconNumber number={account.number} y={10} />}
    </Monitor>
    <span className="account-desktop-label">{account.label}</span>
  </>;
}

export function useAccountDesktops(onShow, onHide, notify, confirm, visible, revision) {
  const [expanded, setExpanded] = useState(false);
  const [catalog, setCatalog] = useState(null);
  const [panes, setPanes] = useState([]);
  const [pending, setPending] = useState("");
  const lock = useRef(false);
  const generation = useRef(0);
  const notifyRef = useRef(notify);
  notifyRef.current = notify;
  const refresh = useCallback(async (background = false) => {
    const g = ++generation.current;
    try {
      const next = await api("/desktop-instances");
      if (g !== generation.current) return;
      setCatalog(next);
      setPanes(old => {
        if (!next.instances || !old.length) return old;
        if (old.some(p => next.redirects?.[p.id] === "default")) return [];
        const seen = new Set();
        const updated = [];
        for (const pane of old) {
          const id = next.redirects?.[pane.id] || pane.id;
          const item = next.instances.find(a => a.id === id);
          if (!item || seen.has(id) || id === "default") continue;
          seen.add(id);
          updated.push(item);
        }
        return updated;
      });
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
  async function act(account, action, beside = false, create = false) {
    if (lock.current) return;
    lock.current = true;
    try {
      if (
        action === "stop" &&
        !(await confirm({
          title: "关闭 " + title(account) + "？",
          body: "请先保存这个 Desktop 中的工作。",
          confirm: "关闭实例",
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
      if (action === "start" && panes.some(p => p.id === account.id) && account.running) {
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
        setPanes((old) => old.filter((a) => a.id !== account.id));
        if (panes.length === 1 && panes[0].id === account.id) onHide();
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
  return {
    expanded,
    catalog,
    panes,
    pending,
    act,
    refresh,
    clear: () => setPanes([]),
    toggle: () => {
      setExpanded(!expanded);
    },
  };
}

export function DesktopList({ model, disabled, selected, onDefault }) {
  const { catalog, expanded, panes, pending } = model;
  const [creating, setCreating] = useState(false);
  const [accountID, setAccountID] = useState("");
  const instances = catalog?.instances ?? catalog?.accounts ?? [];
  const original = instances.find(a => a.id === "default");
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
          {!catalog?.redirects?.default && <button className="sidebar-link" disabled={disabled} onClick={onDefault}>
            <DesktopAccountLabel account={original || {label:"默认 Desktop"}} />
          </button>}
          {catalog?.supported &&
            instances.filter(a => a.id !== "default").map((a) => (
              <div className="account-desktop-row" key={a.id}>
                <button
                  title={title(a) + (a.running ? " · 运行中" : " · 未启动")}
                  aria-label={title(a) + (pending === a.id ? " · 处理中" : a.running ? " · 运行中" : " · 未启动")}
                  aria-pressed={selected && panes.some((p) => p.id === a.id)}
                  className="sidebar-link"
                  disabled={Boolean(pending) || disabled}
                  onClick={() => model.act(a, "start")}
                >
                  <DesktopAccountLabel account={a} />
                  {pending === a.id || a.running ? <span aria-hidden="true">{pending === a.id ? "…" : "●"}</span> : null}
                </button>
                {panes.length > 0 && !panes.some((p) => p.id === a.id) && (
                  <button
                    className="icon-button"
                    disabled={Boolean(pending) || disabled}
                    aria-label={"并排打开 " + title(a)}
                    onClick={() => model.act(a, "start", true)}
                  >
                    <Columns2 size={14} />
                  </button>
                )}
                {a.running && (
                  <button
                    className="icon-button"
                    disabled={Boolean(pending) || disabled}
                    aria-label={"关闭 " + title(a)}
                    onClick={() => model.act(a, "stop")}
                  >
                    <Power size={14} />
                  </button>
                )}
              </div>
            ))}
          {catalog?.supported && Array.isArray(catalog.instances) && <div className="desktop-create">
            <button type="button" className="sidebar-link" disabled={disabled || Boolean(pending)}
              aria-expanded={creating} onClick={() => setCreating(!creating)}><Plus size={16}/>新建 Desktop</button>
            {creating && <div className="desktop-create-fields">
              <label>所属账号<InlineSelect inline aria-label="Desktop 所属账号" value={accountID} onValueChange={setAccountID}>
                <option value="">在此实例中登录</option>
                {catalog.accounts.map(a => <option key={a.id} value={a.id}>{title(a)}</option>)}
              </InlineSelect></label>
              <button className="primary" disabled={disabled || Boolean(pending)} onClick={choose}>打开 Desktop</button>
            </div>}
          </div>}
        </div>
      )}
    </div>
  );
}

function DesktopPane({ account, visible, close, busy }) {
  const box = useRef(null);
  const [message, setMessage] = useState("");
  const [attempt, retry] = useState(0);
  useEffect(() => {
    if (!visible) return;
    let live = true,
      client;
    setMessage("正在连接…");
    const path = entry(account.id);
    api(path + "/vnc-credentials")
      .then(({ password }) => {
        if (!live) return;
        client = new RFB(box.current, runtimeSocketURL(path + "/vnc"), {
          credentials: { password },
        });
        client.scaleViewport = true;
        client.resizeSession = true;
        client.focusOnClick = true;
        client.addEventListener("connect", () => {
          if (live) setMessage("");
        });
        client.addEventListener("disconnect", () => {
          if (live) setMessage("画面已断开");
        });
      })
      .catch((e) => {
        if (live) setMessage(e.message);
      });
    return () => {
      live = false;
      client?.disconnect();
    };
  }, [account.id, account.generation, visible, attempt]);
  return (
    <section className="account-desktop-pane" id={"desktop-pane-"+account.id} aria-label={title(account)}>
      <header>
        <strong className="account-desktop-title" title={title(account)}>
          <DesktopAccountLabel account={account} />
        </strong>
        <button disabled={busy} onClick={close}>
          关闭实例
        </button>
      </header>
      <div className="account-desktop-screen" ref={box} />
      {message && (
        <div className="account-desktop-message" role="status">
          {message}{" "}
          {message !== "正在连接…" && (
            <button onClick={() => retry(attempt + 1)}>重新连接</button>
          )}
        </div>
      )}
    </section>
  );
}

export function DesktopPanes({ model, visible }) {
  return (
    <div className="account-desktop-panes">
      {model.panes.map((a) => (
        <DesktopPane
          key={a.id}
          account={a}
          visible={visible}
          busy={Boolean(model.pending)}
          close={() => model.act(a, "stop")}
        />
      ))}
    </div>
  );
}
