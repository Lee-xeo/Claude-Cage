import React, { useLayoutEffect, useRef, useState } from "react";
import { TerminalSquare } from "lucide-react";
import { AgentIcon } from "./agent-icon";
import { InlineSelect } from "./inline-select";

export function TerminalMenu({ choice, busy, profiles, usesOfficialCLI, onClose, onSelect }) {
  const menu = useRef(null);
  const [configuring, setConfiguring] = useState(false);
  const [account, setAccount] = useState("auto");
  const [handoff, setHandoff] = useState(false);
  const close = useRef(onClose);
  close.current = onClose;
  useLayoutEffect(() => {
    const node = menu.current;
    const anchor = choice.anchor;
    if (!anchor?.isConnected) {
      close.current();
      return;
    }
    anchor.setAttribute("aria-expanded", "true");
    anchor.setAttribute("aria-controls", "terminal-create-menu");
    node.querySelector("button:not(:disabled)")?.focus();
    const dismiss = (restoreFocus = false) => {
      if (restoreFocus) anchor.focus();
      close.current();
    };
    const outside = (event) => {
      if (!node.contains(event.target) && !anchor.contains(event.target)) dismiss();
    };
    const keydown = (event) => {
      if (event.defaultPrevented) return;
      if (event.key === "Escape" || !configuring && event.key === "Tab") {
        if (event.key === "Escape") event.preventDefault();
        event.stopPropagation();
        dismiss(true);
      } else if (!configuring && ["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
        event.preventDefault();
        const items = [...node.querySelectorAll("button:not(:disabled)")];
        const index = items.indexOf(document.activeElement);
        const next = event.key === "Home" ? 0 : event.key === "End" ? items.length - 1
          : (index + (event.key === "ArrowDown" ? 1 : -1) + items.length) % items.length;
        items[next]?.focus();
      }
    };
    const closeCommand = () => dismiss(true);
    document.addEventListener("click", outside, true);
    document.addEventListener("keydown", keydown);
    document.addEventListener("cage-close-modal", closeCommand);
    return () => {
      anchor.setAttribute("aria-expanded", "false");
      anchor.removeAttribute("aria-controls");
      document.removeEventListener("click", outside, true);
      document.removeEventListener("keydown", keydown);
      document.removeEventListener("cage-close-modal", closeCommand);
    };
  }, [choice, configuring]);

  const selectAccount = (id) => {
    setAccount(id);
    setHandoff(id === "auto" && profiles.scheduling.switch_policy === "allow");
  };
  const configureCode = () => {
    if (!usesOfficialCLI) { onSelect("claude"); return; }
    const known = profiles.accounts || [];
    const initial = known.some(a => a.enabled && a.status === "ready") ? "auto"
      : known.find(a => a.status === "ready")?.id || (profiles.accounts ? "login" : "auto");
    selectAccount(initial);
    setConfiguring(true);
    void profiles.load();
  };
  if (configuring) {
    const accounts = profiles.accounts || [];
    const eligible = accounts.filter(a => a.enabled && a.status === "ready");
    const sources = account === "auto" ? eligible : accounts.filter(a => a.id === account);
    const hasBackup = sources.some(a => a.handoff_group && eligible.some(other =>
      other.id !== a.id && other.handoff_group === a.handoff_group));
    const handoffNote = account === "login" ? "登录后按该账号的接续分组查找备用账号。"
      : hasBackup ? "使用同一接续分组内的可用账号。"
      : "目前没有同组备用账号。在账号管理中设置接续分组并启用备用账号后生效。";
    return <div ref={menu} id="terminal-create-menu" className="terminal-create-menu terminal-create-form"
      role="group" aria-label="新建 Claude Code 终端">
      <label>启动账号
        <InlineSelect inline aria-label="启动账号" value={account} disabled={busy} onValueChange={selectAccount}>
          <option value="auto">自动选择账号</option>
          {(profiles.accounts || []).map(a => <option key={a.id} value={a.id}>
            {`#${String(a.number).padStart(2, "0")} ${a.label}${a.status === "ready" ? "" : " · 需要登录"}`}
          </option>)}
          <option value="login">在此终端登录</option>
        </InlineSelect>
      </label>
      <label className="check-line"><input type="checkbox" checked={handoff} disabled={busy}
        onChange={e => setHandoff(e.target.checked)} />额度用尽后自动接续</label>
      {handoff && <small>{handoffNote}</small>}
      <button className="primary" disabled={busy} onClick={() => onSelect("claude", account, handoff)}>创建 Claude Code 终端</button>
    </div>;
  }
  return (
    <div ref={menu} id="terminal-create-menu" className="menu terminal-create-menu" role="menu" aria-label="新建终端">
      <button role="menuitem" disabled={busy} onClick={() => onSelect("shell")}>
        <TerminalSquare size={16} />普通终端
      </button>
      <button role="menuitem" disabled={busy} onClick={configureCode}>
        <AgentIcon />Claude 终端
      </button>
    </div>
  );
}
