import React, { useEffect, useRef, useState } from "react";
import { ChevronDown } from "lucide-react";
import { InlineSelect } from "./inline-select";
import "./session-account.css";
import { useTranslation } from "./interface-language.mjs";

const failures = {rate_limit:"请求受到限流，请核对额度后选择等待或接续",authentication_failed:"账号登录失效，请重新登录",account_on_hold:"账号受限，自动接续已停止",overloaded:"服务暂时过载，保留原账号",server_error:"服务暂时异常，保留原账号"};

// Desktop and mobile use the same session operation and durable request ID.
export function SessionAccount({ id, request, onChanged, compact = false, inline = false, minimal = false, defaultOpen = false, paused = false }) {
  const tr = useTranslation();
  const [open,setOpen]=useState(defaultOpen), [view,setView]=useState(null), [busy,setBusy]=useState(false), [error,setError]=useState("");
  const [target,setTarget]=useState(""), [confirmed,setConfirmed]=useState(false), [continueTask,setContinueTask]=useState(false);
  const pending=useRef(null), current=useRef({request,onChanged}), root=useRef(null); current.current={request,onChanged};
  const terminalID=useRef(null);
  const path="/sessions/"+id+"/account";
  useEffect(() => {
    if (!inline || !open) return;
    const dismiss = (event) => {
      if (!root.current?.contains(event.target)) setOpen(false);
    };
    const onKey = (event) => {
      if (event.key === "Escape") { event.stopPropagation(); setOpen(false); }
    };
    document.addEventListener("pointerdown", dismiss);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("pointerdown", dismiss);
      document.removeEventListener("keydown", onKey);
    };
  }, [inline, open]);
  useEffect(() => {
    let live=true,timer;
    const load=async()=>{
      try {
        const result=await current.current.request(path,{body:{action:"inspect"}});
        if(live) {
          setView(result); setTarget(previous=>result.candidates.some(a=>a.id===previous)?previous:(result.candidates[0]?.id || ""));
          const next=result.session.desktop_terminal_id;
          if(terminalID.current && next && next!==terminalID.current)current.current.onChanged?.(result.session);
          terminalID.current=next;
        }
      } catch(e) { if(live && open) setError(e.message); }
      if(live) timer=setTimeout(load,open?2000:10000);
    };
    void load(); return()=>{live=false;clearTimeout(timer);};
  },[id,path,open]);
  const change=async(body)=>{
    if(busy)return; setBusy(true);setError("");
    try {
      const result=await current.current.request(path,{body}); setView(result);current.current.onChanged?.(result.session);
      if(body.action==="switch") {pending.current=null;setConfirmed(false);}
    } catch(e){setError(e.message);} finally{setBusy(false);}
  };
  if(!view) {
    if(!defaultOpen)return null;
    return <section className="session-account" aria-label={tr("会话账号")}><div className="session-account-options">
      {error?<p role="alert">{tr(error)}</p>:<p>{tr("正在读取会话…")}</p>}
    </div></section>;
  }
  if(!view.supported) {
    if(!defaultOpen)return null;
    return <section className="session-account" aria-label={tr("会话账号")}><div className="session-account-options">
      <p>{tr("此会话不能进行账号接续")}</p>
    </div></section>;
  }
  const session=view.session, sw=session.account_switch;
  const sessionPaused = paused || session.paused;
  const occupied = sessionPaused || session.active;
  const switching=sw && sw.phase!=="completed";
  const accountLabel = session.account_label || tr("Claude 账号");
  const statusHint = session.account_failure ? tr("需要处理") : switching ? tr("正在交接") : view.unopened ? tr("用其他账号继续") : tr("会话绑定");
  return <section ref={root} className={`session-account ${compact?"session-account--compact":""} ${inline?"session-account--inline":""} ${minimal?"session-account--minimal":""}`} aria-label={tr("会话账号")}>
    {!defaultOpen && <button type="button" className={inline ? "session-account-pill" : "quiet-button"} aria-expanded={open}
      title={minimal ? `${accountLabel} · ${statusHint}` : undefined}
      aria-label={minimal ? `${accountLabel} · ${statusHint}` : undefined}
      onClick={()=>setOpen(v=>!v)}>
      {minimal ? <>
        {(session.account_failure || switching) && <i className={`session-account-dot ${session.account_failure ? "bad" : "live"}`} aria-hidden="true" />}
        <span className="session-account-name">{accountLabel}</span>
        <ChevronDown size={13} aria-hidden="true" />
      </> : `${accountLabel} · ${statusHint}`}
    </button>}
    {open && <div className="session-account-options">
      {view.unopened
        ?<p>{tr("原账号：")}{session.account_label || session.account_id}{tr("。可直接使用同组其他账号继续此会话，原账号历史保留不动。")}</p>
        :<p>{tr("当前账号：")}{session.account_label || session.account_id}{tr("。会话重连会保持此账号。")}</p>}
      {session.account_failure && <p role="status">{failures[session.account_failure] || tr("本轮发生错误，原会话已保留")}</p>}
      {sessionPaused && <p role="status">{tr("Claude 已暂停，原会话仍占用此账号，请先继续对话")}</p>}
      {sw && <p role="status">{sw.phase==="completed"?tr("已接续至"):tr("接续目标：")}{sw.target_label}{sw.error && ` · ${sw.error}`}</p>}
      <label>{tr("此会话切换授权")}<InlineSelect aria-label={tr("此会话切换授权")} disabled={busy || switching} value={view.switch_policy} onValueChange={policy=>change({action:"policy",policy})}>
        <option value="ask">{tr("每次询问")}</option><option value="allow">{tr("额度耗尽后自动接续")}</option><option value="wait">{tr("始终等待原账号")}</option>
      </InlineSelect></label>
      {!switching && <>
        <label>{tr("接续账号")}<InlineSelect aria-label={tr("接续账号")} value={target} disabled={busy || !view.candidates.length} onValueChange={value=>{setTarget(value);setConfirmed(false);pending.current=null;}}>
          {view.candidates.length?view.candidates.map(a=><option key={a.id} value={a.id}>{a.label}</option>):<option value="">{tr("无可用账号")}</option>}
        </InlineSelect></label>
        {!view.candidates.length && <p>{tr("在账号设置中，将允许共享会话历史的账号设为同一接续分组。")}</p>}
        <p>{tr("目标账号将基于历史副本继续处理，原账号会话保留不动，缓存可能重建。执行中的工具、后台任务和未确认结果会阻止切换。")}</p>
        <label className="check-line"><input type="checkbox" checked={confirmed} onChange={e=>setConfirmed(e.target.checked)} />{tr("允许将此会话历史交给所选账号")}</label>
        <label className="check-line"><input type="checkbox" checked={continueTask} onChange={e=>{setContinueTask(e.target.checked);pending.current=null;}} />{tr("恢复后继续未完成的任务")}</label>
        <button type="button" disabled={busy || occupied || !target || !confirmed} onClick={()=>{
          pending.current ||= {action:"switch",target,request_id:crypto.randomUUID(),source_id:view.source_id,confirm:true,continue:continueTask};
          void change(pending.current);
        }}>{busy?tr("正在交接…"):view.unopened?tr("用所选账号继续"):tr("切换并恢复历史")}</button>
      </>}
      {switching && sw.phase!=="starting" && <button disabled={busy || occupied} onClick={()=>change({action:"switch",request_id:sw.id,source_id:view.source_id,target,confirm:true})}>{tr("重试账号交接")}</button>}
      {switching && sw.phase==="preparing" && <button disabled={busy} onClick={()=>change({action:"cancel",request_id:sw.id})}>{tr("取消交接，保留原账号")}</button>}
      {switching && sw.phase==="starting" && <p>{tr("正在等待目标账号连接。请保留当前页面，或从会话列表重新打开。")}</p>}
      {error && <p role="alert">{tr(error)}</p>}
    </div>}
  </section>;
}
