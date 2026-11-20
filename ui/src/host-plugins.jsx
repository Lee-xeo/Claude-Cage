import React, { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
  ArrowLeft, Blocks, Check, ChevronDown, ChevronRight, CircleAlert, Clock, Copy, Download, Folder, FolderOpen, Globe2,
  Keyboard, LoaderCircle, Monitor, PanelLeft, RefreshCw, Search, Settings, ShieldCheck, SquareTerminal, Store, Trash2, TriangleAlert, Wrench,
} from "lucide-react";
import { api, writeClipboard } from "./api";
import { runtimeURL } from "./client-context.mjs";
import "./host-plugins.css";
import { translate, useTranslation } from "./interface-language.mjs";

const permissions = {
  files: { title: "读写文件", text: "读取或修改这台电脑上的文件。", icon: FolderOpen },
  commands: { title: "运行命令", text: "以你的系统账户在这台电脑上运行命令。", icon: SquareTerminal },
  screen: { title: "查看屏幕", text: "截取或读取屏幕上显示的内容。", icon: Monitor },
  input: { title: "控制键盘和鼠标", text: "模拟键盘输入和鼠标操作。", icon: Keyboard },
  network: { title: "访问网络", text: "从这台电脑连接网络上的服务。", icon: Globe2 },
};
const builtIn = { commands: { title: "命令", description: "在已打开的项目文件夹中运行命令，较长的命令可在后台运行并稍后读取结果。" } };
const toolNotes = { run_command: "在项目文件夹中运行一条命令", command_job: "读取或停止后台运行的命令" };
const knownIcons = { commands: SquareTerminal, clock: Clock };
const sources = { builtin: "内置", local: "手动安装", catalog: "随应用附带", market: "插件市场" };

const words = name => name.split(/[-_\s]+/).filter(Boolean);
export const pluginTitle = plugin => plugin.built_in && builtIn[plugin.name] && translate(builtIn[plugin.name].title) || plugin.display_name
  || words(plugin.name).map(word => word[0].toUpperCase() + word.slice(1)).join(" ") || plugin.name;
const pluginText = plugin => plugin.built_in && builtIn[plugin.name] && translate(builtIn[plugin.name].description) || plugin.description || translate("插件没有提供说明");
const permissionText = list => list?.length ? list.map(name => permissions[name]?.title ? translate(permissions[name].title) : name).join(translate("、")) : translate("未声明任何权限");
// The app explains in problem when a plugin needs a newer app: a guest plugin,
// an unmet engines range or more than one MCP server. Otherwise an unsupported
// plugin was not built for this system.
const unsupportedLabel = item => item.problem ? translate("需要更新 Cage") : translate("不支持此系统");

// contributions lists what a plugin adds to the app beyond its tools: pages in
// the sidebar. Every MCP plugin has a server, so servers alone are not shown
// here; they appear as tools. Link handlers are not in effect yet, so they are
// not promised either.
const urlHost = pattern => pattern.replace(/^[a-z]+:\/\//, "").replace(/\/.*$/, "").replace(/:\*$/, "");
function contributions(item) {
  const contributes = item?.contributes || {};
  const list = [];
  for (const view of contributes.views || []) {
    const hosts = [...new Set((view.open_urls || []).map(urlHost))];
    list.push({ key: `view:${view.id}`, icon: PanelLeft, title: translate("页面「{0}」", view.title || view.id),
      text: hosts.length ? translate("在侧栏添加一个页面。页面在隔离的框架中运行，只能使用这个插件自己的工具。它可以在内置浏览器中打开：{0}。", hosts.join(", ")) : translate("在侧栏添加一个页面。页面在隔离的框架中运行，只能使用这个插件自己的工具。它不能打开网页。") });
  }
  return list;
}
const contributionText = item => contributions(item).map(entry => entry.title).join(translate("；"));

// purposes are what a plugin is for: abilities for Claude, from its MCP
// servers, and changes to the app, from its pages. A plugin without
// contributions is a first-version one: one MCP server.
export function purposes(item) {
  const contributes = item?.contributes;
  if (!contributes) return ["Claude 工具"];
  const list = [];
  if (contributes.mcp_servers?.length) list.push("Claude 工具");
  if (contributes.views?.length) list.push("界面扩展");
  return list;
}
// categoryNames are the market's sections. The app lists only those it
// knows, in the order the market gives, so new ones can come later.
const categoryNames = { desktop: "桌面控制", developer: "开发", system: "系统与状态", network: "网络", files: "文件", productivity: "效率", example: "示例" };
const categoriesOf = item => (item?.categories || []).filter(name => categoryNames[name]);
function PurposeTags({ item }) {
  const tr = useTranslation();
  const list = purposes(item), categories = categoriesOf(item);
  return list.length || categories.length ? <span className="plugin-purposes">
    {categories.map(name => <span key={`category:${name}`} className="plugin-purpose category">{tr(categoryNames[name])}</span>)}
    {list.map(label => <span key={label} className="plugin-purpose">{tr(label)}</span>)}
  </span> : null;
}
const size = bytes => !bytes ? "" : bytes < 1024 * 1024 ? `${Math.max(1, Math.round(bytes / 1024))} KB` : `${(bytes / 1024 / 1024).toFixed(1)} MB`;

// useHostPlugins keeps the plugin list current while something shows it,
// and reads the plugin market once when first shown.
export function useHostPlugins(active) {
  const [view, setView] = useState(null), [error, setError] = useState(""), [busy, setBusy] = useState("");
  const [market, setMarket] = useState({ loading: false, loaded: false });
  const mounted = useRef(true), revision = useRef(0), marketRevision = useRef(0);
  const refresh = useCallback(async () => {
    const current = ++revision.current;
    try {
      const value = await api("/host-tools");
      if (mounted.current && current === revision.current) { setView(value); setError(""); }
    } catch (e) { if (mounted.current && current === revision.current) setError(e.message); }
  }, []);
  const loadMarket = useCallback(async (fresh = false) => {
    const current = ++marketRevision.current;
    setMarket(previous => ({ ...previous, loading: true }));
    try {
      const value = await api(`/host-tools/market${fresh ? "?refresh=1" : ""}`);
      if (mounted.current && current === marketRevision.current) setMarket({ ...value, loading: false, loaded: true });
    } catch (e) {
      if (mounted.current && current === marketRevision.current)
        setMarket(previous => ({ ...previous, loading: false, loaded: true, missing: e.status === 404, error: e.status === 404 ? "" : e.message }));
    }
  }, []);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  // "light" keeps only the plugin list current, slowly, for the plugin pages
  // in the main sidebar; the market is read once the plugin views are shown.
  const browsing = active === true;
  useEffect(() => {
    if (!active) return;
    void refresh();
    const timer = setInterval(() => { if (!busy) void refresh(); }, browsing ? 5000 : 30000);
    return () => { revision.current++; clearInterval(timer); };
  }, [active, browsing, busy, refresh]);
  useEffect(() => { if (browsing && !market.loaded && !market.loading) void loadMarket(); }, [browsing, market.loaded, market.loading, loadMarket]);
  // change sends one request; key names what is in progress, for the buttons.
  const change = useCallback(async (body, key = "change") => {
    setBusy(key); setError(""); revision.current++;
    try {
      const value = await api("/host-tools", body);
      if (mounted.current) setView(value);
      if (body.install || body.remove) void loadMarket();
      return true;
    } catch (e) { if (mounted.current) setError(e.message); return false; }
    finally { if (mounted.current) setBusy(""); }
  }, [loadMarket]);
  return { view, error, busy, change, market, loadMarket };
}

// pluginEntries merges what is installed with what can be installed, so the
// sidebar and the detail page describe each plugin the same way.
export function pluginEntries(model) {
  const view = model.view;
  if (!view) return { installed: [], available: [] };
  const offers = new Map();
  for (const item of view.catalog || []) offers.set(item.name, { ...item, source: "catalog" });
  for (const item of model.market?.entries || []) {
    const bundled = offers.get(item.name);
    if (!bundled || item.update || !bundled.supported) offers.set(item.name, { ...item, source: "market" });
  }
  const installed = view.plugins.map(plugin => {
    const offer = offers.get(plugin.name);
    // listing is the market's card for an installed plugin; offer only when it is newer.
    return { name: plugin.name, plugin, offer: offer?.source === "market" && offer.update ? offer : null,
      listing: offer?.source === "market" ? offer : null,
      source: plugin.built_in ? "builtin" : offers.get(plugin.name)?.source || "local",
      icon: plugin.icon ? runtimeURL(`/host-tools/icon?name=${encodeURIComponent(plugin.name)}`) : "" };
  });
  const names = new Set(view.plugins.map(plugin => plugin.name));
  const available = [...offers.values()].filter(item => !names.has(item.name) && !item.installed && !item.installed_version)
    .map(item => ({ name: item.name, offer: item, source: item.source,
      icon: item.icon && item.source === "market" ? runtimeURL(`/host-tools/icon?name=${encodeURIComponent(item.name)}&market=1`) : "" }));
  return { installed, available };
}

function entryInfo(entry, view) {
  const item = entry.plugin || entry.offer;
  const plugin = entry.plugin;
  let state = { tone: "", label: "可安装" };
  if (plugin) {
    state = plugin.problem ? { tone: "problem", label: "无法使用" }
      : plugin.enabled === false ? { tone: "off", label: "已停用" }
      : !plugin.supported ? { tone: "problem", label: unsupportedLabel(plugin) }
      : !view.enabled ? { tone: "", label: "未开启" }
      : plugin.loaded ? { tone: "online", label: "运行中" }
      : view.running ? { tone: "", label: "未加载" } : { tone: "waiting", label: "等待启动" };
  } else if (!item.supported) state = { tone: "problem", label: unsupportedLabel(item) };
  return { item, title: pluginTitle(item), text: plugin?.problem || pluginText(item), version: item.version, state };
}

export function pluginPageTitle(model, page) {
  if (page.kind === "market") return translate("插件市场");
  if (page.kind !== "plugin") return translate("插件设置");
  const { installed, available } = pluginEntries(model);
  const entry = [...installed, ...available].find(item => item.name === page.name);
  return entry ? pluginTitle(entry.plugin || entry.offer) : pluginTitle({ name: page.name });
}

export function PluginIcon({ name, src, builtIn: isBuiltIn, size: pixels = 36 }) {
  const [failed, setFailed] = useState("");
  if (src && failed !== src) return (
    <span className="plugin-icon picture" aria-hidden="true" style={{ "--plugin-icon-size": `${pixels}px` }}>
      <img src={src} alt="" draggable="false" onError={() => setFailed(src)} />
    </span>
  );
  const Glyph = knownIcons[name];
  const hue = [...name].reduce((value, letter) => (value * 31 + letter.codePointAt(0)) % 360, 17);
  const letters = words(name).slice(0, 2).map(word => word[0].toUpperCase()).join("") || "?";
  return (
    <span className={`plugin-icon ${isBuiltIn ? "built-in" : ""}`} aria-hidden="true"
      style={{ "--plugin-hue": hue, "--plugin-icon-size": `${pixels}px` }}>
      {Glyph ? <Glyph size={Math.round(pixels * 0.5)} strokeWidth={1.9} /> : letters}
    </span>
  );
}

function StateChip({ state }) {
  return <span className={`plugin-state ${state.tone}`}><i />{translate(state.label)}</span>;
}

// installPlugin asks before third-party code from the market is installed.
async function installPlugin(model, entry, onConfirm) {
  const offer = entry.offer;
  const replace = Boolean(entry.plugin);
  const adds = contributionText(offer);
  const runs = offer.kind === "guest"
    ? translate("插件在 Cage 环境中以普通用户身份运行，它声明需要：{0}。", permissionText(offer.permissions))
    : translate("插件以你的系统账户权限在这台电脑上运行，它声明需要：{0}。调用前会弹窗征求你的同意，你可以选择始终允许。", permissionText(offer.permissions));
  // A plugin with tools can be allowed for good at install: the user's nod is remembered.
  const offerAlways = !replace && offer.source === "market" && purposes(offer).includes("Claude 工具");
  const answer = offer.source === "market" ? await onConfirm({
    title: replace ? translate("更新「{0}」到 {1}？", pluginTitle(offer), offer.version) : translate("安装「{0}」？", pluginTitle(offer)),
    body: [runs, adds && translate("它还会添加：{0}。", adds), translate("只安装你信任的插件。"), replace && translate("旧版本会移到数据目录的 plugins-removed 中保留。")].filter(Boolean).join(" "),
    confirm: replace ? "更新" : "安装",
    ...(offerAlways ? { check: "安装后始终允许它的工具，不再逐次询问" } : {}),
  }) : true;
  if (!answer) return false;
  const body = offer.source === "market" ? { install: offer.name, source: "market", ...(replace ? { replace: true } : {}) } : { install: offer.name };
  const installed = await model.change(body, `install:${offer.name}`);
  if (installed && answer.checked) await model.change({ plugin: offer.name, always: true }, `always:${offer.name}`);
  return installed;
}

// HostPluginsNav is the main sidebar's way into the plugin sidebar.
// pluginPages are the pages of plugins that are installed, turned on and can
// run; each opens in the workspace from its own entry in the main sidebar.
export function pluginPages(model) {
  const pages = [];
  for (const plugin of model.view?.plugins || []) {
    if (plugin.enabled === false || !plugin.supported || plugin.problem) continue;
    for (const view of plugin.contributes?.views || []) {
      if (!view.url) continue;
      pages.push({ key: `${plugin.name}/${view.id}`, plugin: plugin.name, view, title: view.title || pluginTitle(plugin),
        icon: plugin.icon ? runtimeURL(`/host-tools/icon?name=${encodeURIComponent(plugin.name)}`) : "" });
    }
  }
  return pages;
}

function PluginPageIcon({ src }) {
  const [failed, setFailed] = useState(false);
  if (!src || failed) return <PanelLeft size={17} aria-hidden="true" />;
  return <img className="plugin-page-icon" src={src} alt="" draggable="false" onError={() => setFailed(true)} />;
}

export function PluginPagesNav({ pages, selected, onOpen }) {
  const tr = useTranslation();
  return pages.map(page => (
    <button key={page.key} className={`sidebar-link ${selected === page.key ? "selected" : ""}`}
      aria-current={selected === page.key ? "page" : undefined} onClick={() => onOpen(page)} aria-label={tr("打开 {0}", (page.title))}>
      <PluginPageIcon key={page.icon} src={page.icon} />{page.title}
    </button>
  ));
}

export function HostPluginsNav({ open, onOpen }) {
  const tr = useTranslation();
  return (
    <button className="sidebar-link host-plugins-entry" onClick={onOpen} aria-expanded={open} aria-controls="plugin-sidebar">
      <Blocks size={17} />{tr("插件")}<ChevronRight size={15} className="trailing-icon" />
    </button>
  );
}

function SidebarSection({ title, count, open, onToggle, action, children }) {
  return (
    <section className="plugin-sidebar-section">
      <div className="plugin-sidebar-heading">
        <button type="button" aria-expanded={open} onClick={onToggle}>
          {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}<span>{title}</span>
          {count !== undefined && <em>{count}</em>}
        </button>
        {action}
      </div>
      {open && <div className="plugin-sidebar-rows">{children}</div>}
    </section>
  );
}

function PluginRow({ entry, view, selected, busy, onSelect, onInstall }) {
  const tr = useTranslation();
  const info = entryInfo(entry, view);
  const action = !entry.plugin ? info.item.supported && "安装" : entry.offer && "更新";
  const installing = busy === `install:${entry.name}`;
  return (
    <div className={`plugin-row ${selected ? "selected" : ""} ${entry.plugin?.enabled === false ? "off" : ""}`}>
      <button type="button" className="plugin-row-main" aria-current={selected ? "page" : undefined} onClick={onSelect}
        title={`${info.title}${info.version ? ` ${info.version}` : ""}\n${info.text}`}>
        <PluginIcon name={entry.name} src={entry.icon} builtIn={entry.source === "builtin"} />
        <span className="plugin-row-text">
          <span className="plugin-row-title"><strong>{info.title}</strong>{info.version && <small>{info.version}</small>}</span>
          <span className="plugin-row-description">{info.text}</span>
          <span className="plugin-row-meta">{entry.plugin || !info.item.supported ? <StateChip state={info.state} /> : <span>{translate(sources[entry.source])}</span>}</span>
        </span>
      </button>
      {action && <button type="button" className="plugin-row-action" disabled={Boolean(busy)} onClick={onInstall}>
        {installing ? <LoaderCircle size={12} className="spin" /> : null}{installing ? tr("正在安装") : action}
      </button>}
    </div>
  );
}

// marketQuery matches a plugin against what the user typed.
const matches = (entry, query) => {
  const needle = query.trim().toLowerCase();
  if (!needle) return true;
  const item = entry.plugin || entry.offer;
  const categories = categoriesOf(entry.offer || entry.listing || item).map(name => categoryNames[name]);
  return [entry.name, pluginTitle(item), pluginText(item), ...categories].some(text => text.toLowerCase().includes(needle));
};

export function HostPluginsSidebar({ visible, model, page, onPage, onBack, onConfirm }) {
  const tr = useTranslation();
  const { view, error, busy, market } = model;
  const sidebar = useRef(null);
  const list = useRef(null);
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(true);
  const { installed, available } = useMemo(() => pluginEntries(model), [model]);
  const shown = installed.filter(entry => matches(entry, query));
  const updates = installed.filter(entry => entry.offer).length;
  const installable = available.filter(entry => entry.offer.supported).length;
  const inMarket = page?.kind === "market" || page?.kind === "plugin" && !installed.some(entry => entry.name === page.name);
  useLayoutEffect(() => {
    if (!visible) return;
    setQuery("");
    setOpen(true);
    list.current.scrollTop = 0;
    sidebar.current?.focus({ preventScroll: true });
  }, [visible]);
  useEffect(() => {
    if (!visible) return;
    const leave = event => {
      if (event.key !== "Escape" || event.defaultPrevented || document.querySelector('[role="dialog"], .settings-page')) return;
      // Only from the plugin views: a terminal or page beside them keeps its Escape.
      // The page's own title counts, since opening a plugin page focuses it.
      const target = event.target;
      const inPlugins = target.closest?.(".plugin-sidebar, .plugin-panel") || target.closest?.(".management-page")?.querySelector(".plugin-panel");
      if (!inPlugins || target.matches?.('input[type="search"]') && target.value) return;
      onBack();
    };
    window.addEventListener("keydown", leave);
    return () => window.removeEventListener("keydown", leave);
  }, [visible, onBack]);
  return (
    <aside ref={sidebar} id="plugin-sidebar" className="workspace-sidebar plugin-sidebar" aria-label={tr("插件")}
      data-open={visible} inert={!visible} tabIndex={-1}>
      <div className="sidebar-top">
        <span className="plugin-sidebar-title">
          <button className="icon-button" onClick={onBack} aria-label={tr("返回工作台侧栏")} title={tr("返回工作台侧栏（Esc）")}><ArrowLeft size={16} /></button>
          {tr("插件")}
        </span>
        <span className="plugin-sidebar-tools">
          <button className="icon-button" disabled={!view?.enabled || Boolean(busy)} onClick={() => model.change({ reload: true }, "reload")}
            aria-label={tr("重新加载插件")} title={tr("重新加载插件")}><RefreshCw size={15} className={busy === "reload" ? "spin" : undefined} /></button>
          <button className={`icon-button ${page?.kind === "settings" ? "active" : ""}`} aria-current={page?.kind === "settings" ? "page" : undefined}
            onClick={() => onPage({ kind: "settings" })} aria-label={tr("插件设置")} title={tr("插件设置")}><Settings size={16} /></button>
        </span>
      </div>
      <label className="plugin-search">
        <Search size={15} aria-hidden="true" />
        <input type="search" value={query} onChange={event => setQuery(event.target.value)} placeholder={tr("搜索已安装的插件")} aria-label={tr("搜索已安装的插件")} />
      </label>
      <div className="plugin-sidebar-body" ref={list}>
        <button type="button" className={`plugin-market-entry ${inMarket ? "selected" : ""}`} aria-current={inMarket ? "page" : undefined}
          onClick={() => onPage({ kind: "market" })}>
          <span className="plugin-market-entry-icon"><Store size={18} /></span>
          <span><strong>{tr("插件市场")}</strong><small>{market.loading && !market.loaded ? tr("正在读取…") : market.error ? tr("暂时无法读取") : updates ? tr("{0} 个可更新 · {1} 个可安装", (updates), (installable)) : installable ? tr("{0} 个可安装", (installable)) : tr("浏览和安装插件")}</small></span>
          {updates > 0 && <em aria-hidden="true">{updates}</em>}
          <ChevronRight size={15} className="trailing-icon" />
        </button>
        {view && !view.enabled && view.available && <button type="button" className="plugin-sidebar-notice" onClick={() => onPage({ kind: "settings" })}>
          <ShieldCheck size={17} /><span><strong>{tr("主机工具未开启")}</strong><small>{tr("插件不会运行。点此前往设置开启。")}</small></span>
        </button>}
        {!view && <p className="plugin-sidebar-hint" role={error ? "alert" : "status"}>{error || tr("正在读取插件…")}</p>}
        {view && <SidebarSection title={tr("已安装")} count={installed.length} open={open} onToggle={() => setOpen(value => !value)}>
          {shown.map(entry => <PluginRow key={entry.name} entry={entry} view={view} busy={busy}
            selected={page?.kind === "plugin" && page.name === entry.name}
            onSelect={() => onPage({ kind: "plugin", name: entry.name })} onInstall={() => installPlugin(model, entry, onConfirm)} />)}
          {!shown.length && <p className="plugin-sidebar-hint">{query ? tr("没有匹配的插件") : tr("还没有安装插件")}</p>}
        </SidebarSection>}
      </div>
      {error && view && <p className="plugin-sidebar-error" role="alert">{error}</p>}
    </aside>
  );
}

function MarketCard({ entry, view, busy, onOpen, onInstall }) {
  const tr = useTranslation();
  const info = entryInfo(entry, view);
  const offer = entry.offer || entry.listing;
  const update = Boolean(entry.plugin && entry.offer);
  const installing = busy === `install:${entry.name}`;
  return (
    <article className="plugin-card-item">
      <button type="button" className="plugin-card-main" onClick={onOpen} aria-label={tr("查看 {0}", (info.title))}>
        <PluginIcon name={entry.name} src={entry.icon} size={44} />
        <span className="plugin-card-text">
          <span className="plugin-row-title"><strong>{info.title}</strong><small>{update ? `${entry.plugin.version || "?"} → ${offer.version}` : offer.version}</small></span>
          <span className="plugin-card-description">{pluginText(offer)}</span>
          <PurposeTags item={offer} />
        </span>
      </button>
      <footer>
        <span className="plugin-card-permissions" title={permissionText(offer.permissions)}>
          <ShieldCheck size={13} />{offer.permissions?.length ? permissionText(offer.permissions) : tr("无需额外权限")}
        </span>
        {entry.plugin && !update ? <span className="plugin-state online"><i />{tr("已安装")}</span>
          : !offer.supported ? <span className="plugin-state problem" title={offer.problem || undefined}><i />{unsupportedLabel(offer)}</span>
          : <button type="button" className="primary" disabled={Boolean(busy)} onClick={onInstall}>
            {installing ? <LoaderCircle size={14} className="spin" /> : <Download size={14} />}{installing ? update ? tr("正在更新") : tr("正在安装") : update ? tr("更新") : tr("安装")}
          </button>}
      </footer>
    </article>
  );
}

function Market({ model, onConfirm, onPage }) {
  const tr = useTranslation();
  const { view, busy, market } = model;
  const [query, setQuery] = useState("");
  const [category, setCategory] = useState("");
  const { installed, available } = pluginEntries(model);
  const listed = [...installed.filter(entry => entry.listing), ...available];
  // Only sections that have something in them are offered.
  const sections = (market.categories || Object.keys(categoryNames)).filter(name => categoryNames[name]
    && listed.some(entry => categoriesOf(entry.offer || entry.listing).includes(name)));
  const chosen = sections.includes(category) ? category : "";
  const shown = entry => matches(entry, query) && (!chosen || categoriesOf(entry.offer || entry.listing).includes(chosen));
  const updates = installed.filter(entry => entry.offer && shown(entry));
  const offers = available.filter(shown);
  // Installed plugins stay in the market, marked as such, as in VS Code.
  const current = installed.filter(entry => entry.listing && !entry.offer && shown(entry));
  const card = entry => <MarketCard key={entry.name} entry={entry} view={view} busy={busy}
    onOpen={() => onPage({ kind: "plugin", name: entry.name })} onInstall={() => installPlugin(model, entry, onConfirm)} />;
  const fetched = market.fetched ? new Date(market.fetched) : null;
  return (
    <div className="plugin-market">
      <header className="plugin-market-head">
        <div>
          <h2>{tr("插件市场")}</h2>
          <p>{tr("来自社区插件仓库。安装前请查看插件声明的权限，只安装你信任的插件。")}</p>
        </div>
        <div className="plugin-market-tools">
          <label className="plugin-search large">
            <Search size={16} aria-hidden="true" />
            <input type="search" value={query} onChange={event => setQuery(event.target.value)} placeholder={tr("搜索插件市场")} aria-label={tr("搜索插件市场")} />
          </label>
          {!market.missing && <button type="button" disabled={market.loading} onClick={() => model.loadMarket(true)}>
            <RefreshCw size={15} className={market.loading ? "spin" : undefined} />{tr("刷新")}
          </button>}
        </div>
        {fetched && !Number.isNaN(fetched.getTime()) && <small className="plugin-market-fetched">{tr("更新于") + " "}{fetched.toLocaleString()}</small>}
      </header>
      {sections.length > 0 && <div className="plugin-categories" role="group" aria-label={tr("按分类查看")}>
        <button type="button" aria-pressed={!chosen} onClick={() => setCategory("")}>{tr("全部")}</button>
        {sections.map(name => <button key={name} type="button" aria-pressed={chosen === name} onClick={() => setCategory(name)}>{categoryNames[name]}</button>)}
      </div>}
      {market.error && <div className="plugin-banner problem" role="alert"><TriangleAlert size={17} /><div><strong>{tr("暂时无法读取社区插件")}</strong><p>{market.error}</p></div>
        <button type="button" onClick={() => model.loadMarket(true)}>{tr("重试")}</button></div>}
      {updates.length > 0 && <section className="plugin-market-section">
        <h3>{tr("可更新")}<em>{updates.length}</em></h3>
        <div className="plugin-card-grid">{updates.map(card)}</div>
      </section>}
      <section className="plugin-market-section">
        <h3>{tr("可安装")}<em>{offers.length}</em></h3>
        {offers.length ? <div className="plugin-card-grid">{offers.map(card)}</div>
          : market.loading ? <p className="plugin-loading" role="status"><LoaderCircle size={16} className="spin" />{tr("正在读取插件市场…")}</p>
          : market.error && !query ? null
          : <div className="plugin-empty compact"><Store size={22} /><p>{query ? tr("没有匹配的插件。") : chosen ? tr("这个分类下没有可安装的新插件。") : current.length ? tr("市场里的插件都已安装。") : tr("没有可安装的新插件。")}</p></div>}
      </section>
      {current.length > 0 && <section className="plugin-market-section">
        <h3>{tr("已安装")}<em>{current.length}</em></h3>
        <div className="plugin-card-grid">{current.map(card)}</div>
      </section>}
    </div>
  );
}

function CopyField({ value, label }) {
  const tr = useTranslation();
  const [copied, setCopied] = useState("");
  const copy = async () => {
    try { await writeClipboard(value); setCopied("已复制"); }
    catch { setCopied("复制失败，请手动选中复制"); }
  };
  return (
    <div className="plugin-copy-field">
      <code>{value}</code>
      <button type="button" onClick={copy} aria-label={label}>{copied === "已复制" ? <Check size={15} /> : <Copy size={15} />}{copied || tr("复制")}</button>
    </div>
  );
}

// Grants shows whether the plugin's tools are asked about on each call or
// allowed for good. The same grant covers calls from Claude and from the
// plugin's own pages. Commands are allowed per open project, in the
// confirmation window or here; Windows's window offers only allow and deny.
function Grants({ model, plugin, onConfirm }) {
  const tr = useTranslation();
  const { busy, change } = model;
  const [choice, setChoice] = useState("");
  const key = `always:${plugin.name}`;
  if (plugin.built_in && plugin.name === "commands") {
    const projects = plugin.always_projects || [];
    const open = (model.view?.folders || []).filter(folder => !projects.includes(folder));
    const project = open.includes(choice) ? choice : open[0] || "";
    const revoke = async project => {
      if (!project && !await onConfirm({ title: "撤销全部项目的授权？", body: "之后在任何项目中运行命令都会重新询问。", confirm: "全部撤销" })) return;
      await change({ plugin: plugin.name, always: false, ...(project ? { project } : {}) }, key);
    };
    const allow = async () => {
      if (!project || !await onConfirm({ title: "在这个项目中始终允许运行命令？", confirm: "始终允许",
        body: translate("之后 Claude 在 {0} 中运行命令时不再询问。命令以你的系统账户权限在这台电脑上运行，可以随时在这里撤销。", project) })) return;
      await change({ plugin: plugin.name, always: true, project }, key);
    };
    return (
      <section className="plugin-grants" aria-label={tr("授权")}>
        <div className="plugin-grants-head">
          <div><strong>{tr("授权")}</strong><p>{tr("在确认窗口中选择“始终允许”，或在下方选择已打开的项目，在这些项目中运行命令就不再询问。")}</p></div>
          {projects.length > 1 && <button type="button" disabled={Boolean(busy)} onClick={() => revoke("")}>{tr("全部撤销")}</button>}
        </div>
        {open.length > 0 && <div className="plugin-grant-add">
          <select value={project} disabled={Boolean(busy)} onChange={event => setChoice(event.target.value)} aria-label={tr("选择项目")}>
            {open.map(folder => <option key={folder} value={folder}>{folder}</option>)}
          </select>
          <button type="button" disabled={Boolean(busy)} onClick={allow}>{tr("始终允许")}</button>
        </div>}
        {projects.length ? <ul className="plugin-grant-projects">
          {projects.map(project => <li key={project}>
            <Folder size={15} aria-hidden="true" /><code title={project}>{project}</code>
            <button type="button" disabled={Boolean(busy)} onClick={() => revoke(project)} aria-label={tr("撤销 {0} 的授权", (project))}>{tr("撤销")}</button>
          </li>)}
        </ul> : <p className="plugin-grants-empty">{tr("还没有始终允许的项目，每次运行命令都会询问。")}</p>}
      </section>
    );
  }
  // Inside the environment and declaring nothing: its tools are never asked
  // about, so there is nothing to choose.
  if (plugin.kind === "guest" && !(plugin.permissions || []).length) {
    return (
      <section className="plugin-grants" aria-label={tr("授权")}>
        <div className="plugin-grants-head">
          <div>
            <strong>{tr("授权")}</strong>
            <p>{tr("这个插件在环境内运行，没有声明权限，调用它的工具不需要确认。")}</p>
          </div>
        </div>
      </section>
    );
  }
  const always = Boolean(plugin.always);
  return (
    <section className="plugin-grants" aria-label={tr("授权")}>
      <div className="plugin-grants-head">
        <div>
          <strong>{tr("授权")}</strong>
          <p>{always ? tr("Claude 和插件页面调用它的工具时不再询问。插件更新后如果需要新的权限，会重新询问。") : tr("Claude 或插件页面每次调用它的工具前，都会弹窗询问。")}</p>
        </div>
        <div className="plugin-segmented" role="radiogroup" aria-label={tr("调用工具时")}>
          <button type="button" role="radio" aria-checked={!always} disabled={Boolean(busy)} onClick={() => always && change({ plugin: plugin.name, always: false }, key)}>{tr("每次询问")}</button>
          <button type="button" role="radio" aria-checked={always} disabled={Boolean(busy)} onClick={() => !always && change({ plugin: plugin.name, always: true }, key)}>{tr("始终允许")}</button>
        </div>
      </div>
    </section>
  );
}

function Detail({ model, name, onConfirm, onPage }) {
  const tr = useTranslation();
  const { view, busy, change } = model;
  const [tab, setTab] = useState("tools");
  const { installed, available } = pluginEntries(model);
  const entry = [...installed, ...available].find(item => item.name === name);
  if (!entry) return (
    <div className="plugin-empty">
      <Blocks size={28} />
      <strong>{tr("这个插件已不在清单中")}</strong>
      <p>{tr("它可能已被卸载，或插件文件夹已被移走。")}</p>
      <button type="button" onClick={() => onPage({ kind: "settings" })}>{tr("前往插件设置")}</button>
    </div>
  );
  const info = entryInfo(entry, view);
  const plugin = entry.plugin, offer = entry.offer;
  const tools = plugin?.tools || [];
  const declared = (plugin || offer).permissions || [];
  const added = contributions(plugin || offer);
  // The open-source project the market says the plugin is built on.
  const upstream = (offer || entry.listing)?.source;
  const separator = view.directory.includes("\\") ? "\\" : "/";
  const installing = busy === `install:${name}`, removing = busy === `remove:${name}`, switching = busy === `switch:${name}`;
  const on = plugin?.enabled !== false;
  const remove = async () => {
    if (!await onConfirm({ title: translate("卸载「{0}」？", info.title), body: "插件会停止运行。它的文件夹不会被删除，而是移到数据目录的 plugins-removed 中保留。", confirm: "卸载" })) return;
    // A plugin the market offers stays on its page, ready to install again.
    if (await change({ remove: name }, `remove:${name}`) && entry.source === "local") onPage(null);
  };
  return (
    <article className={`plugin-page ${plugin && !on ? "off" : ""}`}>
      {!plugin && <button type="button" className="plugin-breadcrumb" onClick={() => onPage({ kind: "market" })}><ArrowLeft size={14} />{tr("插件市场")}</button>}
      <header className="plugin-hero">
        <PluginIcon name={name} src={entry.icon} builtIn={entry.source === "builtin"} size={96} />
        <div className="plugin-hero-text">
          <div className="plugin-hero-name"><h2>{info.title}</h2><code>{name}</code></div>
          <div className="plugin-hero-meta">
            <span>{translate(sources[entry.source])}</span>
            {info.version && <span>{tr("版本") + " "}{info.version}</span>}
            {!plugin && offer?.bytes > 0 && <span>{size(offer.bytes)}</span>}
            {plugin && <StateChip state={info.state} />}
            <PurposeTags item={plugin || offer} />
          </div>
          <p>{pluginText(plugin || offer)}</p>
          <div className="plugin-hero-actions">
            {plugin && <label className={`plugin-enable ${on ? "on" : ""}`}>
              <span className="plugin-switch small">
                <input role="switch" type="checkbox" checked={on} disabled={Boolean(busy)} aria-label={tr("启用 {0}", (info.title))}
                  onChange={() => change({ plugin: name, on: !on }, `switch:${name}`)} />
                <span aria-hidden="true" />
              </span>
              {switching ? on ? tr("正在停用…") : tr("正在启用…") : on ? tr("已启用") : tr("已停用")}
            </label>}
            {!plugin && <button type="button" className="primary" disabled={Boolean(busy) || !offer.supported} onClick={() => installPlugin(model, entry, onConfirm)}>
              {installing ? <LoaderCircle size={15} className="spin" /> : <Download size={15} />}{!offer.supported ? unsupportedLabel(offer) : installing ? tr("正在安装…") : tr("安装")}
            </button>}
            {plugin && offer && <button type="button" className="primary" disabled={Boolean(busy)} onClick={() => installPlugin(model, entry, onConfirm)}>
              {installing ? <LoaderCircle size={15} className="spin" /> : <Download size={15} />}{installing ? tr("正在更新…") : tr("更新到 {0}", (offer.version))}
            </button>}
            {plugin && !plugin.built_in && <button type="button" disabled={Boolean(busy)} onClick={remove}>
              {removing ? <LoaderCircle size={15} className="spin" /> : <Trash2 size={15} />}{removing ? tr("正在卸载…") : tr("卸载")}
            </button>}
            {plugin && !view.enabled && view.available && <button type="button" onClick={() => onPage({ kind: "settings" })}><Settings size={15} />{tr("开启主机工具")}</button>}
          </div>
        </div>
      </header>
      {plugin && !on && <div className="plugin-banner" role="status"><CircleAlert size={17} /><div>
        <strong>{plugin.built_in && name === "commands" ? tr("命令已停用") : tr("插件已停用")}</strong>
        <p>{plugin.built_in && name === "commands" ? tr("Claude 将不能在这台电脑上运行命令。打开上方开关即可恢复。") : tr("插件不会运行，Claude 也看不到它提供的工具。打开上方开关即可恢复。")}</p>
      </div></div>}
      {plugin?.problem && <div className="plugin-banner problem" role="alert"><TriangleAlert size={17} /><div><strong>{tr("插件无法加载")}</strong><p>{plugin.problem}</p></div></div>}
      {!plugin && offer?.problem && <div className="plugin-banner problem" role="status"><TriangleAlert size={17} /><div><strong>{tr("暂时无法安装")}</strong><p>{offer.problem}</p></div></div>}
      <nav className="plugin-tabs" role="tablist" aria-label={tr("{0} 的内容", (info.title))}>
        <button type="button" role="tab" aria-selected={tab === "tools"} onClick={() => setTab("tools")}>{tr("工具")}<em>{tools.length}</em></button>
        <button type="button" role="tab" aria-selected={tab === "permissions"} onClick={() => setTab("permissions")}>{plugin ? tr("授权与权限") : tr("权限")}<em>{declared.length}</em></button>
        {added.length > 0 && <button type="button" role="tab" aria-selected={tab === "contributions"} onClick={() => setTab("contributions")}>{tr("功能")}<em>{added.length}</em></button>}
      </nav>
      <div className="plugin-page-body">
        <div className="plugin-page-main" role="tabpanel">
          {tab === "contributions" && added.length ? <>
            <p className="plugin-page-lead">{tr("除了提供给 Claude 的工具，插件还会在应用中添加这些内容。")}</p>
            <ul className="plugin-permission-list">
              {added.map(({ key, icon: Icon, title, text }) => <li key={key}><span><Icon size={17} /></span><div><strong>{title}</strong><p>{text}</p></div></li>)}
            </ul>
          </> : tab !== "permissions" ? tools.length ? <ul className="plugin-tool-list">
            {tools.map(tool => <li key={tool.name}>
              <span className="plugin-tool-icon"><Wrench size={15} /></span>
              <div><code>{tool.name}</code><p>{toolNotes[tool.name] ? tr(toolNotes[tool.name]) : tool.description || tr("没有说明")}</p></div>
            </li>)}
          </ul> : <div className="plugin-empty compact">
            <Wrench size={22} />
            <p>{!plugin ? tr("安装并运行后，这里会列出插件提供给 Claude 的工具。") : plugin.problem ? tr("插件无法加载，没有可用的工具。") : !on ? tr("启用插件后，这里会列出它提供的工具。") : !view.enabled ? tr("开启主机工具后，这里会列出插件提供的工具。") : tr("插件运行后，这里会列出它提供的工具。")}</p>
          </div> : <>
            {plugin && <Grants model={model} plugin={plugin} onConfirm={onConfirm} />}
            <p className="plugin-page-lead">{tr("权限由插件自行声明，供你在安装前判断。")}</p>
            {declared.length ? <ul className="plugin-permission-list">
              {declared.map(key => {
                const known = permissions[key] || { title: key, text: "插件声明的其他权限。", icon: ShieldCheck };
                const Icon = known.icon;
                return <li key={key}><span><Icon size={17} /></span><div><strong>{tr(known.title)}</strong><p>{tr(known.text)}</p></div></li>;
              })}
            </ul> : <div className="plugin-empty compact"><ShieldCheck size={22} /><p>{tr("这个插件没有声明需要任何权限。")}</p></div>}
          </>}
        </div>
        <aside className="plugin-page-aside" aria-label={tr("插件信息")}>
          <h3>{tr("信息")}</h3>
          <dl>
            <div><dt>{tr("标识符")}</dt><dd><code>{name}</code></dd></div>
            {info.version && <div><dt>{tr("版本")}</dt><dd>{info.version}</dd></div>}
            <div><dt>{tr("来源")}</dt><dd>{translate(sources[entry.source])}</dd></div>
            {(plugin || offer).kind && <div><dt>{tr("运行位置")}</dt><dd>{(plugin || offer).kind === "guest" ? tr("Cage 环境") : tr("这台电脑")}</dd></div>}
            <div><dt>{tr("状态")}</dt><dd>{plugin ? tr(info.state.label) : offer.supported ? tr("未安装") : unsupportedLabel(offer)}</dd></div>
            {!plugin && offer?.bytes > 0 && <div><dt>{tr("大小")}</dt><dd>{size(offer.bytes)}</dd></div>}
          </dl>
          {upstream && <>
            <h3>{tr("上游项目")}</h3>
            <dl>
              <div><dt>{tr("项目")}</dt><dd>{upstream.project}</dd></div>
              {upstream.version && <div><dt>{tr("版本")}</dt><dd>{upstream.version}</dd></div>}
              {upstream.license && <div><dt>{tr("许可证")}</dt><dd>{upstream.license}</dd></div>}
            </dl>
            {/* Shown as text to copy: the app opens no address a list names. */}
            {upstream.url && <CopyField value={upstream.url} label={tr("复制项目地址")} />}
          </>}
          {plugin && !plugin.built_in && <>
            <h3>{tr("位置")}</h3>
            <CopyField value={`${view.directory}${separator}${name}`} label={tr("复制插件位置")} />
          </>}
        </aside>
      </div>
    </article>
  );
}

function SettingsPage({ model, status, onConfirm }) {
  const tr = useTranslation();
  const { view, busy, change } = model;
  const waiting = view.enabled && !view.running;
  const toggle = async () => {
    if (!view.enabled && !await onConfirm({ title: "允许 Claude 使用这台电脑？",
      body: "开启后，环境内的 Claude 可以通过插件在这台电脑上操作，例如在已打开的项目文件夹中运行命令。命令以你的系统账户权限运行，不受环境的隔离保护。每次操作前，系统会弹出确认窗口，由你决定是否允许。",
      confirm: "开启" })) return;
    await change({ enabled: !view.enabled }, "toggle");
  };
  const state = !view.available ? { tone: "problem", label: "此环境不支持" } : view.running ? { tone: "online", label: "已开启，插件正在运行" }
    : waiting ? { tone: "waiting", label: status?.ready ? "已开启，正在启动插件" : "已开启，环境启动后生效" } : { tone: "", label: "未开启" };
  return (
    <div className="plugin-settings">
      <section className={`plugin-card plugin-master ${view.enabled ? "on" : ""}`}>
        <div className="plugin-master-row">
          <span className="plugin-master-icon"><Blocks size={24} /></span>
          <div>
            <h2>{tr("允许 Claude 使用这台电脑")}</h2>
            <p>{tr("插件通过 MCP 把这台电脑的能力提供给环境内的 Claude Code 和 Claude 桌面应用。")}</p>
          </div>
          {view.available && <label className="plugin-switch">
            <input role="switch" type="checkbox" aria-label={tr("允许 Claude 使用这台电脑")} checked={view.enabled} disabled={Boolean(busy)} onChange={toggle} />
            <span aria-hidden="true" />
          </label>}
        </div>
        <div className="plugin-master-footer">
          <StateChip state={state} />
          {view.enabled && <button type="button" disabled={Boolean(busy) || !status?.ready} onClick={() => change({ reload: true }, "reload")}>
            <RefreshCw size={15} className={busy === "reload" ? "spin" : undefined} />{tr("重新加载插件")}
          </button>}
        </div>
      </section>
      <ul className="plugin-facts">
        <li><ShieldCheck size={18} /><div><strong>{tr("每次操作都先征求同意")}</strong><p>{tr("系统会弹窗显示插件、工具和参数，你可以只允许一次，或在本次运行期间都允许。")}</p></div></li>
        <li><TriangleAlert size={18} /><div><strong>{tr("不受环境隔离保护")}</strong><p>{tr("插件以你的系统账户权限运行。只安装你信任的插件。")}</p></div></li>
        <li><CircleAlert size={18} /><div><strong>{tr("试验功能")}</strong><p>{tr("此功能仍在试验阶段，默认关闭。")}</p></div></li>
      </ul>
      {view.problems?.length > 0 && <div className="plugin-banner problem" role="alert"><TriangleAlert size={17} /><div><strong>{tr("部分插件未能加载")}</strong>{view.problems.map(problem => <p key={problem}>{problem}</p>)}</div></div>}
      <section className="plugin-card">
        <h3>{tr("可操作的文件夹")}</h3>
        <p className="plugin-card-lead">{tr("命令插件只能在这些已打开的项目文件夹中运行。")}</p>
        {view.folders.length ? <ul className="plugin-folder-list">
          {view.folders.map(folder => <li key={folder}><Folder size={16} /><code>{folder}</code></li>)}
        </ul> : <div className="plugin-empty compact"><FolderOpen size={22} /><p>{tr("还没有打开项目。打开项目后，它的文件夹会出现在这里。")}</p></div>}
      </section>
      <section className="plugin-card">
        <h3>{tr("手动安装插件")}</h3>
        <p className="plugin-card-lead">{tr("把插件文件夹放进下面的位置，然后点击“重新加载插件”。每个插件是一个带有 plugin.json 的文件夹。")}</p>
        <CopyField value={view.directory} label={tr("复制插件文件夹路径")} />
      </section>
    </div>
  );
}

// HostPluginsPanel shows, in the workspace, what the plugin sidebar chose.
export function HostPluginsPanel({ model, page, status, onConfirm, onPage }) {
  const tr = useTranslation();
  const { view, error } = model;
  if (!view) return <div className="plugin-panel">{error ? <p role="alert" className="error-message">{error}</p> : <p role="status" className="plugin-loading"><LoaderCircle size={16} className="spin" />{tr("正在读取插件…")}</p>}</div>;
  return (
    <div className="plugin-panel">
      {error && <p role="alert" className="plugin-banner problem"><TriangleAlert size={17} /><span>{error}</span></p>}
      {page.kind === "plugin" ? <Detail key={page.name} model={model} name={page.name} onConfirm={onConfirm} onPage={onPage} />
        : page.kind === "market" ? <Market model={model} onConfirm={onConfirm} onPage={onPage} />
        : <SettingsPage model={model} status={status} onConfirm={onConfirm} />}
    </div>
  );
}
