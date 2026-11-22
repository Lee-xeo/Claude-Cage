import React, { useEffect, useRef, useState } from "react";
import { ArrowLeft, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { api } from "./api";
import { InlineSelect } from "./inline-select";
import { activeInterfaceLanguage, useTranslation } from "./interface-language.mjs";
import { cookieEditable, cookieForm, cookieFromForm, newCookieForm, siteHostname } from "./browser-cookie-form.mjs";

// The value field is masked until shown and is never logged; the form is
// discarded on close so the value does not outlive the editor.
function CookieEditor({ editor, busy, onChange, onSave, onCancel }) {
  const tr = useTranslation();
  const [revealed, setRevealed] = useState(false);
  const { form } = editor;
  const field = (name, label, extra = {}) => <label>{label}<input value={form[name]} disabled={busy} spellCheck={false}
    autoComplete="off" autoCapitalize="off" onChange={event => onChange({ ...form, [name]: event.target.value })} {...extra} /></label>;
  const check = (name, label) => <label className="cookie-check"><input type="checkbox" checked={form[name]} disabled={busy}
    onChange={event => onChange({ ...form, [name]: event.target.checked })} />{label}</label>;
  return <form className="cookie-editor" aria-label={editor.key ? tr("编辑 Cookie") : tr("新增 Cookie")}
    onSubmit={event => { event.preventDefault(); onSave(); }}>
    <strong>{editor.key ? tr("编辑 Cookie") : tr("新增 Cookie")}</strong>
    {field("name", tr("名称"), { required: true, maxLength: 4096, autoFocus: true })}
    <label>{tr("值")}<textarea className={revealed ? "" : "cookie-secret"} value={form.value} disabled={busy} rows={2}
      spellCheck={false} autoComplete="off" autoCapitalize="off" maxLength={4096} data-sensitive="true"
      onChange={event => onChange({ ...form, value: event.target.value })} /></label>
    <label className="cookie-check"><input type="checkbox" checked={revealed} onChange={event => setRevealed(event.target.checked)} />{tr("显示值")}</label>
    {field("domain", tr("域名"), { required: true, maxLength: 254 })}
    <p className="cookie-editor-note">{tr("以“.”开头时包含子域名，否则仅限该主机名。")}</p>
    {field("path", tr("路径"), { required: true, maxLength: 1024 })}
    <div className="cookie-editor-row">{check("secure", "Secure")}{check("httpOnly", "HttpOnly")}
      <span>SameSite<InlineSelect inline aria-label="SameSite" value={form.sameSite} disabled={busy} onValueChange={sameSite => onChange({ ...form, sameSite })}>
        <option value="">{tr("未指定")}</option><option value="Lax">Lax</option><option value="Strict">Strict</option><option value="None">None</option>
      </InlineSelect></span></div>
    <div className="cookie-editor-row">{check("session", tr("会话结束时过期"))}
      {!form.session && field("expires", tr("过期时间"), { type: "datetime-local", required: true })}</div>
    <div className="cookie-editor-row"><button type="submit" disabled={busy}>{tr("保存")}</button>
      <button type="button" disabled={busy} onClick={onCancel}>{tr("取消")}</button></div>
  </form>;
}

export function BrowserCookies({ tab, onClose }) {
  const tr = useTranslation();
  const [scope, setScope] = useState(/^https?:/.test(tab.url) ? "site" : "all");
  const [query, setQuery] = useState("");
  const [page, setPage] = useState(0);
  const [revision, setRevision] = useState(0);
  const [data, setData] = useState(null);
  const [value, setValue] = useState(null);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [confirmation, setConfirmation] = useState(null);
  const [selected, setSelected] = useState(new Set());
  const [editor, setEditor] = useState(null);
  const hostname = siteHostname(tab.url);
  const generation = useRef(0), search = useRef(null), operation = useRef(false), selectAll = useRef(null);
  const selectableKeys = data?.selectableKeys || [];
  const allSelected = selectableKeys.length > 0 && selectableKeys.every(key => selected.has(key));
  useEffect(() => {
    if (selectAll.current) selectAll.current.indeterminate = selected.size > 0 && !allSelected;
  }, [selected, allSelected, data]);
  useEffect(() => { search.current?.focus(); }, []);
  useEffect(() => {
    const version = ++generation.current;
    setLoading(true); setData(null); setValue(null); setError(""); setConfirmation(null); setEditor(null);
    api("/browser/cookies", { id: tab.id, url: tab.url, mode: "list", scope, query, page })
      .then(result => {
        if (version !== generation.current) return;
        setData(result);
        const available = new Set(result.selectableKeys);
        setSelected(previous => new Set([...previous].filter(key => available.has(key))));
      })
      .catch(error => { if (version === generation.current) setError(error.message); })
      .finally(() => { if (version === generation.current) setLoading(false); });
    return () => { generation.current++; };
  }, [tab.id, scope, query, page, revision]);
  const run = async (mode, cookieKey, extra = {}, edit = null) => {
    if (operation.current || loading) return;
    operation.current = true; setBusy(true); setError("");
    const version = generation.current;
    try {
      const result = await api("/browser/cookies", { id: tab.id, url: tab.url, mode, scope, query, page, cookieKey,
        ...(mode === "delete-selected" ? { cookieKeys: [...selected] } : {}), ...extra });
      if (version !== generation.current) return;
      if (mode === "details" && edit) {
        setValue(null); setEditor({ key: cookieKey, form: cookieForm(edit, result.value) });
      } else if (mode === "details") setValue({ key: cookieKey, value: result.value });
      else {
        setData(result); setValue(null); setConfirmation(null); setEditor(null);
        const available = new Set(result.selectableKeys);
        setSelected(previous => new Set([...previous].filter(key => available.has(key))));
      }
    } catch (error) {
      if (version === generation.current) setError(error.message);
    } finally { operation.current = false; setBusy(false); }
  };
  return <section className="browser-cookies" aria-label={tr("Cookie 管理")} onKeyDown={event => {
    if (event.key === "Escape") { event.stopPropagation(); if (editor) setEditor(null); else onClose(); }
  }}>
    <div className="cookie-heading">
      <button className="icon-button" aria-label={tr("关闭 Cookie 管理")} onClick={onClose}><ArrowLeft size={15} /></button>
      <strong>{tr("Cookie 管理")}</strong><span>{tab.incognito ? tr("无痕会话") : tr("普通会话")}</span>
      <button className="icon-button" aria-label={tr("刷新 Cookie")} disabled={busy || loading}
        onClick={() => { setSelected(new Set()); setRevision(revision + 1); }}><RefreshCw size={15} /></button>
    </div>
    <div className="cookie-toolbar">
      <InlineSelect inline aria-label={tr("Cookie 网站范围")} value={scope} disabled={busy} onValueChange={next => { setSelected(new Set()); setScope(next); setPage(0); }}>
        <option value="site" disabled={!/^https?:/.test(tab.url)}>{tr("当前网站")}</option><option value="all">{tr("所有网站")}</option>
      </InlineSelect>
      <input ref={search} aria-label={tr("搜索 Cookie")} placeholder={tr("搜索网站或 Cookie 名称")} maxLength={200}
        value={query} disabled={busy} onChange={event => { setSelected(new Set()); setQuery(event.target.value); setPage(0); }} />
      <button disabled={busy || loading || !data || !hostname} title={hostname ? undefined : tr("请在网站页面中新增 Cookie")}
        onClick={() => { setValue(null); setEditor({ form: newCookieForm(tab.url) }); }}><Plus size={14} />{tr("新增 Cookie")}</button>
      <button disabled={busy || loading || !data} onClick={() => setConfirmation(confirmation === "clear" ? null : "clear")}>
        <Trash2 size={14} />{scope === "site" ? tr("清空当前网站") : tr("清空所有网站")}
      </button>
    </div>
    <div className="cookie-selection">
      <label><input ref={selectAll} type="checkbox" checked={allSelected} disabled={busy || loading || !selectableKeys.length}
        onChange={event => { setSelected(new Set(event.target.checked ? selectableKeys : [])); setConfirmation(null); }} />
        {tr("全选当前搜索结果（{0} 条，含所有分页）", selectableKeys.length)}</label>
      <span role="status">{tr("已选 {0} 条", selected.size)}</span>
      <button disabled={busy || loading || !selected.size} onClick={() => setConfirmation(confirmation === "selected" ? null : "selected")}>
        <Trash2 size={14} />{tr("删除所选")}</button>
    </div>
    {data && !data.selectableKeys && <p className="cookie-selection-note">{tr("批量删除需要更新环境组件。")}</p>}
    {data?.selectableKeys && data.total > selectableKeys.length && <p className="cookie-selection-note">
      {tr("{0} 条特殊分区 Cookie 无法勾选；如需删除，请使用“清空所有网站”。", data.total - selectableKeys.length)}
    </p>}
    {confirmation === "selected" && <div className="cookie-confirm" role="alert">
      <span>{tr("删除所选的 {0} 条 Cookie？仅删除已勾选的条目，相关网站可能需要重新登录。", selected.size)}</span>
      <button disabled={busy || loading || !selected.size} onClick={() => run("delete-selected")}>{tr("确认删除")}</button>
      <button disabled={busy} onClick={() => setConfirmation(null)}>{tr("取消")}</button>
    </div>}
    {confirmation === "clear" && <div className="cookie-confirm" role="alert">
      <span>{scope === "site" ? tr("清空 {0} 的 Cookie？搜索条件不限制清空范围，网站可能需要重新登录。", data?.hostname) : tab.incognito ? tr("清空此无痕会话所有网站的 Cookie？搜索条件不限制清空范围，网站可能需要重新登录。") : tr("清空此普通会话所有网站的 Cookie？搜索条件不限制清空范围，网站可能需要重新登录。")}</span>
      <button disabled={busy} onClick={() => run("clear")}>{tr("确认清空")}</button>
      <button disabled={busy} onClick={() => setConfirmation(null)}>{tr("取消")}</button>
    </div>}
    {editor && <CookieEditor key={editor.key || "new"} editor={editor} busy={busy} onChange={form => setEditor({ ...editor, form })}
      onCancel={() => setEditor(null)} onSave={() => run("set", editor.key || undefined, { cookie: cookieFromForm(editor.form) })} />}
    {error && <p className="cookie-error" role="alert">{tr(error)}</p>}
    <div className="cookie-list" aria-busy={busy || loading}>
      {loading ? <p>{tr("正在读取 Cookie…")}</p> : data?.cookies.length === 0 ? <p>{tr("没有匹配的 Cookie。")}</p> : data?.cookies.map(cookie =>
        <article className="cookie-record" key={cookie.key}>
          <div className="cookie-record-heading">
            <input type="checkbox" aria-label={tr("选择 Cookie {0}", cookie.name || tr("（空名称）"))} checked={selected.has(cookie.key)}
              disabled={busy || !selectableKeys.includes(cookie.key)} onChange={event => {
                const checked = event.target.checked;
                setSelected(previous => {
                  const next = new Set(previous);
                  if (checked) next.add(cookie.key); else next.delete(cookie.key);
                  return next;
                });
                setConfirmation(null);
              }} />
            <strong>{cookie.name || tr("（空名称）")}</strong>
            <button disabled={busy} onClick={() => value?.key === cookie.key ? setValue(null) : run("details", cookie.key)}>
              {value?.key === cookie.key ? tr("隐藏值") : tr("查看值")}</button>
            <button disabled={busy || !cookieEditable(cookie, hostname)} aria-label={tr("编辑 Cookie {0}", cookie.name || tr("（空名称）"))}
              title={cookie.partitionKey || cookie.partitionKeyOpaque ? tr("暂不支持编辑分区 Cookie") : cookieEditable(cookie, hostname) ? tr("编辑 Cookie") : tr("只能编辑当前网站的 Cookie")}
              onClick={() => run("details", cookie.key, {}, cookie)}><Pencil size={14} /></button>
            <button disabled={busy || cookie.partitionKeyOpaque} aria-label={tr("删除 Cookie {0}", (cookie.name))}
              title={cookie.partitionKeyOpaque ? tr("此分区 Cookie 需要清空所有网站") : tr("删除此 Cookie")}
              onClick={() => run("delete", cookie.key)}><Trash2 size={14} /></button></div>
          <div className="cookie-metadata">{cookie.domain} · {tr("路径 {0}", cookie.path)} · {cookie.size} B</div>
          <div className="cookie-metadata">{cookie.session ? tr("会话结束时过期") : tr("过期：{0}", (new Date(cookie.expires * 1000).toLocaleString(activeInterfaceLanguage())))}
            {cookie.secure && " · Secure"}{cookie.httpOnly && " · HttpOnly"}{cookie.sameSite && ` · SameSite=${cookie.sameSite}`}
            {(cookie.partitionKey || cookie.partitionKeyOpaque) && " · " + tr("分区：{0}", cookie.partitionKey?.topLevelSite || tr("不透明"))}</div>
          {value?.key === cookie.key && <pre className="cookie-value">{value.value || tr("（空值）")}</pre>}
        </article>)}
    </div>
    {data && <div className="cookie-pagination"><span>{tr("共 {0} 条 · 第 {1} 页", data.total, data.page + 1)}</span>
      <button disabled={busy || loading || data.page === 0} onClick={() => setPage(data.page - 1)}>{tr("上一页")}</button>
      <button disabled={busy || loading || (data.page + 1) * 50 >= data.total} onClick={() => setPage(data.page + 1)}>{tr("下一页")}</button>
    </div>}
  </section>;
}
