import { InlineSelect } from "./inline-select";
import React, { useEffect, useRef, useState } from "react";
import {
  ChevronDown,
  Download,
  Plus,
  Search,
  RefreshCw,
  Check,
  Activity,
  Server,
  Trash2,
  Pencil,
  LoaderCircle,
  Square,
  CircleHelp,
  ArrowRight,
  X,
  Route,
} from "lucide-react";
import { api } from "./api";
import { SafetyMessage } from "./safety-message";
import { isLocalListener } from "./proxy-server.mjs";
import { groupNodesByRegion } from "./proxy-region.mjs";
import { ProxyYAMLInput } from "./proxy-yaml-input";
import { LocalConfigImport } from "./local-config-import";
import { getClientContext } from "./client-context.mjs";
import { useWindowVisibility } from "./window-visibility";
import "./proxy.css";
import { activeInterfaceLanguage, translate, useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";

const bytes = (v) => {
  const n = Math.max(0, Number(v) || 0);
  if (n < 1024) return `${n.toFixed(0)} B`;
  if (n < 1024 ** 2) return `${(n / 1024).toFixed(1)} KiB`;
  if (n < 1024 ** 3) return `${(n / 1024 ** 2).toFixed(1)} MiB`;
  return `${(n / 1024 ** 3).toFixed(2)} GiB`;
};
function usageLabel(raw) {
  const values = Object.fromEntries(
    (raw || "").split(";").map((v) => v.trim().split("=")),
  );
  const used = Number(values.upload || 0) + Number(values.download || 0);
  return [
    values.total ? `${bytes(used)} / ${bytes(values.total)}` : "",
    Number(values.expire) > 0
      ? translate("到期 {0}", new Date(Number(values.expire) * 1000).toLocaleDateString(activeInterfaceLanguage()))
      : "",
  ]
    .filter(Boolean)
    .join(" · ");
}
const emptyForm = {
  name: "",
  url: "",
  content: "",
  interval_hours: 24,
  host_download: false,
};
const emptyManual = {
  name: "",
  type: "socks5",
  server: "",
  port: "1080",
  username: "",
  password: "",
  sshAuth: "password",
  privateKey: "",
  passphrase: "",
  hostKey: "",
};
export function ProxySettings({ status, onApplied, onConfirm, onDirty, initializing = false }) {
  const tr = useTranslation();
  const windowVisible = useWindowVisibility();
  const [proxy, setProxy] = useState(null),
    [tab, setTab] = useState("nodes"),
    [search, setSearch] = useState(""),
    [source, setSource] = useState(""),
    [region, setRegion] = useState(""),
    [regionsExpanded, setRegionsExpanded] = useState(false),
    [regionsOverflow, setRegionsOverflow] = useState(false),
    [collapsedRegions, setCollapsedRegions] = useState({}),
    [form, setForm] = useState(null),
    [mode, setMode] = useState("url"),
    [error, setError] = useState(""),
    [pending, setPending] = useState(""),
    [notice, setNotice] = useState(""),
    [delays, setDelays] = useState({}),
    [fastest, setFastest] = useState(""),
    [nodeEdit, setNodeEdit] = useState({ id: "", server: "", port: "" }),
    [connections, setConnections] = useState({ connections: [] }),
    [logs, setLogs] = useState([]),
    [port, setPort] = useState("17896"),
    [rates, setRates] = useState({ up: 0, down: 0 });
  const [manual, setManual] = useState(emptyManual);
  const [routeDraft, setRouteDraft] = useState(null);
  const [hostRefresh, setHostRefresh] = useState(false);
  const [helpOpen, setHelpOpen] = useState(false);
  const [readingFile, setReadingFile] = useState(false);
  const alive = useRef(true),
    regionBarRef = useRef(null),
    mutating = useRef(false),
    delayReasons = useRef({}),
    loading = useRef(false),
    latest = useRef(null),
    appliedFailure = useRef(null),
    measuring = useRef(false),
    portDirty = useRef(false);
  const reload = async () => {
    const failure = appliedFailure.current;
    const value = await api("/proxy");
    if (alive.current) {
      setProxy(value);
      if (!portDirty.current) setPort(String(value.port));
      if (failure && appliedFailure.current === failure && value.network_open) {
        setError((current) => current === failure.message ? "" : current);
        appliedFailure.current = null;
      }
    }
    return value;
  };
  useEffect(() => {
    alive.current = true;
    return () => {
      alive.current = false;
    };
  }, []);
  useEffect(() => {
    if (!windowVisible) return;
    const tick = async () => {
      if (loading.current) return;
      loading.current = true;
      try {
        await reload();
      } catch (e) {
        if (alive.current) setError(e.message);
      } finally {
        loading.current = false;
      }
    };
    tick();
    const timer = setInterval(tick, 4000);
    return () => {
      clearInterval(timer);
    };
  }, [windowVisible]);
  useEffect(() => {
    if (!windowVisible || (tab !== "connections" && tab !== "diagnostics")) return;
    let cancelled = false;
    const tick = async () => {
      try {
        if (tab === "diagnostics") {
          const result = await api("/proxy/logs");
          if (!cancelled) setLogs(result.lines);
        } else {
          const result = await api("/proxy/connections"),
            now = Date.now();
          if (cancelled) return;
          if (latest.current) {
            const seconds = (now - latest.current.time) / 1000;
            if (seconds > 0)
              setRates({
                up: Math.max(
                  0,
                  (result.uploadTotal - latest.current.uploadTotal) / seconds,
                ),
                down: Math.max(
                  0,
                  (result.downloadTotal - latest.current.downloadTotal) /
                    seconds,
                ),
              });
          }
          latest.current = { ...result, time: now };
          setConnections(result);
        }
      } catch (e) {
        if (!cancelled) setError(e.message);
      }
    };
    tick();
    const timer = setInterval(tick, 3000);
    return () => {
      cancelled = true;
      clearInterval(timer);
      latest.current = null;
    };
  }, [tab, windowVisible]);
  const busy = Boolean(pending) || Boolean(status.job?.running);
  const mutate = async (action, input = {}) => {
    if (mutating.current) return false;
    mutating.current = true;
    appliedFailure.current = null;
    setError("");
    setNotice("");
    setPending(action);
    try {
      const job = await api(`/proxy/${action}`, input);
      let current;
      do {
        await new Promise((resolve) => setTimeout(resolve, 500));
        current = (await api("/status")).job;
        if (current.id !== job.id)
          throw new Error(tr("操作已被其他任务替代，请查看当前状态"));
      } while (current.running);
      portDirty.current = false;
      const updated = await reload();
      await onApplied();
      if (current.error) {
        if (current.settings_saved) {
          appliedFailure.current = { message: current.error };
        }
        throw new Error(current.error);
      }
      if (alive.current) {
        setForm(null);
        setManual(emptyManual);
        if (action === "route" || action === "select") setRouteDraft(null);
        const importedSource =
          action === "import"
            ? updated.sources.at(-1)
            : action === "refresh"
              ? updated.sources.find((source) => source.id === input.id)
              : null;
        setNotice(
          importedSource
            ? importedSource.skipped?.length
              ? tr("已导入 {0} 个节点，跳过 {1} 个；可在来源下查看跳过原因", importedSource.nodes.length, importedSource.skipped.length)
              : tr("已导入 {0} 个节点，跳过 {1} 个", importedSource.nodes.length, 0)
            : current.message || tr("已完成"),
        );
        setDelays({});
        if (action === "import" && needsMigration) setTab("nodes");
        if (action === "import-local") { setTab("nodes"); setSearch(""); setSource(""); setRegion(""); setNotice(tr("配置已导入。请在下方选择出口，再点击“应用代理”。")); }
      }
      return true;
    } catch (e) {
      if (alive.current) setError(e.message);
      return false;
    } finally {
      mutating.current = false;
      if (alive.current) setPending("");
    }
  };
  const applyRoute = async (nodeIDs) => {
    if (
      (!proxy.active && !needsMigration || Number(port) !== proxy.port) &&
      status.vm_running &&
      !(await onConfirm({
        title: "启用内置代理并重启环境？",
        body: "首次启用或修改代理端口需要重启，当前终端会结束。后续只切换节点会保留终端进程，但网络连接会重新建立。",
        confirm: "启用并重启",
      }))
    )
      return;
    mutate("route", { node_ids: nodeIDs, port: Number(port) });
  };
  const measure = async (ids) => {
    if (measuring.current) return;
    measuring.current = true;
    setPending("delay");
    setError("");
    try {
      // A small worker pool bounds probes without changing the selected group.
      let index = 0;
      const timings = {};
      if (ids.length > 1) setFastest("");
      await Promise.all(
        Array.from({ length: Math.min(4, ids.length) }, async () => {
          while (index < ids.length && alive.current) {
            const id = ids[index++];
            try {
              const result = await api("/proxy/delay", { node_id: id });
              delete delayReasons.current[id];
              if (result.delay > 0) timings[id] = result.delay;
              if (alive.current)
                setDelays((old) => ({ ...old, [id]: `${result.delay} ms` }));
            } catch (e) {
              // The server separates unloaded nodes, timeouts and upstream
              // failures; keep its reason instead of a fixed guess.
              delayReasons.current[id] = e.message;
              if (alive.current)
                setDelays((old) => ({ ...old, [id]: "不可用 / 未载入" }));
            }
          }
        }),
      );
      // Offer the fastest node as a draft only; applying it stays explicit and
      // runs the usual exit and network checks.
      if (ids.length > 1 && alive.current) {
        const best = Object.entries(timings).sort((a, b) => a[1] - b[1])[0];
        setFastest(best ? best[0] : "");
      }
    } finally {
      measuring.current = false;
      if (alive.current) setPending("");
    }
  };
  const submit = async () => {
    let value = {
      id: form.id || "",
      // Pasted node data has no file name to borrow; an empty name used to
      // leave "导入并验证" disabled with no visible reason (#410).
      name: form.name.trim() || (mode === "file" && !form.id ? tr("本地导入") : form.name),
      url: form.url,
      content: form.content,
      interval_hours: form.interval_hours,
      host_download: form.host_download,
    };
    if (mode === "manual" && !form.id) {
      if (!manualValid) return;
      const node = {
        name: manual.name.trim(),
        type: manual.type,
        server: manual.server.trim(),
        port: Number(manual.port),
        username: manual.username.trim(),
      };
      if (manual.type === "ssh" && manual.sshAuth === "private-key") {
        node["private-key"] = manual.privateKey;
        if (manual.passphrase) node["private-key-passphrase"] = manual.passphrase;
      } else {
        node.password = manual.password;
      }
      if (manual.type === "ssh" && manual.hostKey.trim())
        node["host-key"] = manual.hostKey.split(/\r?\n/).map((key) => key.trim()).filter(Boolean);
      value = {
        ...form,
        name: form.name || node.name,
        url: "",
        content: JSON.stringify({ proxies: [node] }),
        interval_hours: 0,
      };
    }
    if (form.id && !form.remote && form.content.trim()) {
      // A local source is updated in place from new YAML; node IDs, the
      // selection and exit rules follow unchanged node names (#507).
      const id = form.id, content = form.content;
      if (await mutate("edit", value)) await mutate("refresh", { id, content });
      return;
    }
    mutate(form.id ? "edit" : "import", value);
  };
  const editNodeAddress = () => {
    const port = Number(nodeEdit.port);
    if (!nodeEdit.id || !nodeEdit.server.trim() || !Number.isInteger(port) || port < 1 || port > 65535) return;
    mutate("edit-node", { id: form.id, node_id: nodeEdit.id, server: nodeEdit.server.trim(), server_port: port });
  };
  const savedRoute = proxy?.route?.length
    ? proxy.route
    : proxy?.selected
      ? [proxy.selected]
      : [""];
  const route = routeDraft ?? savedRoute;
  const routeChanged = JSON.stringify(route) !== JSON.stringify(savedRoute);
  const manualSSH = manual.type === "ssh";
  const localListener = isLocalListener(manual.server);
  const manualValid = Boolean(
    manual.name.trim() && manual.server.trim() && !localListener &&
    Number.isInteger(Number(manual.port)) && Number(manual.port) > 0 && Number(manual.port) <= 65535 &&
    (!manualSSH || manual.username.trim() &&
      (manual.sshAuth === "password" ? manual.password : manual.privateKey.includes("PRIVATE KEY"))),
  );
  useEffect(() => {
    onDirty?.(routeChanged || initializing && Boolean(form));
    return () => onDirty?.(false);
  }, [onDirty, routeChanged, initializing, Boolean(form)]);
  useEffect(() => {
    const el = regionBarRef.current;
    if (!el) { setRegionsOverflow(false); return; }
    const measureOverflow = () => setRegionsOverflow(el.scrollHeight > 68);
    measureOverflow();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measureOverflow);
    observer.observe(el);
    return () => observer.disconnect();
  }, [tab, proxy, search, source]);
  if (!proxy)
    return <div className="proxy-loading" role={error ? "alert" : "status"}>{error || tr("正在读取代理配置…")}</div>;
  const allNodes = proxy.sources.flatMap((s) =>
    s.nodes.map((n) => ({ ...n, source: s.name, sourceID: s.id })),
  );
  const selected = allNodes.find((n) => n.id === proxy.selected);
  const usableNodes = allNodes.filter((node) => !node.blocked_reason);
  const needsMigration = allNodes.some((node) => node.blocked_reason && savedRoute.includes(node.id));
  const active = proxy.active && !needsMigration;
  const routeValid =
    route.every((id) => usableNodes.some((n) => n.id === id)) &&
    new Set(route).size === route.length;
  const routeName = (id) => allNodes.find((n) => n.id === id)?.name || tr("待选择");
  const updateHop = (index, id) =>
    setRouteDraft(route.map((current, i) => (i === index ? id : current)));
  const baseNodes = allNodes.filter(
    (n) =>
      (!source || n.sourceID === source) &&
      `${n.name} ${n.source} ${n.type}`
        .toLowerCase()
        .includes(search.toLowerCase()),
  );
  const regionGroups = groupNodesByRegion(baseNodes);
  const activeRegion = region && regionGroups.some((g) => g.code === region) ? region : "";
  const nodes = activeRegion
    ? regionGroups.find((g) => g.code === activeRegion)?.nodes || []
    : baseNodes;
  const showRegionBar = regionGroups.length > 1;
  const showGroupedList = !activeRegion && regionGroups.length > 1;
  const measurable = nodes.filter((node) => !node.blocked_reason);
  const renderNodeRow = (n) => (
    <div
      className={`proxy-node ${!n.blocked_reason && n.id === route.at(-1) ? "selected" : ""}`}
      key={n.id}
    >
      <button
        className="proxy-node-choice"
        disabled={
          busy || Boolean(n.blocked_reason) ||
          !proxy.core_available ||
          route.slice(0, -1).includes(n.id)
        }
        title={
          n.blocked_reason
            ? localizeBackendMessage(n.blocked_reason, tr)
            : (route.slice(0, -1).includes(n.id)
              ? tr("此节点已用作中转")
              : tr("选为最终出口，点击应用代理后生效"))
        }
        onClick={() => updateHop(route.length - 1, n.id)}
        aria-pressed={!n.blocked_reason && n.id === route.at(-1)}
        aria-label={tr("选择节点 {0}", (n.name))}
      >
        <span className="proxy-radio">
          {!n.blocked_reason && n.id === route.at(-1) && <Check size={12} />}
        </span>
        <span>
          <strong>{n.blocked_reason ? tr("{0}（需迁移）", n.name) : n.name}</strong>
          <small>
            {n.source} · {n.type.toUpperCase()}
          </small>
        </span>
      </button>
      {active && savedRoute.includes(n.id) && (
        <span className="proxy-node-role">
          {n.id === proxy.selected ? tr("当前出口") : tr("当前中转")}
        </span>
      )}
      <span className="proxy-delay" title={n.blocked_reason ? undefined : (delayReasons.current[n.id] ? localizeBackendMessage(delayReasons.current[n.id], tr) : undefined)}>
        {!n.blocked_reason && (delays[n.id] ? tr(delays[n.id]) : tr("未测速"))}
      </span>
      <button
        aria-label={tr("测速 {0}", (n.name))}
        title={tr("检测此节点到 www.gstatic.com 的 HTTP 往返延迟，通常不含建连开销，不代表网页首开耗时")}
        disabled={busy || !proxy.running || Boolean(n.blocked_reason)}
        onClick={() => measure([n.id])}
      >
        <Activity size={14} />
      </button>
    </div>
  );
  const openImport = (importMode) => {
    setTab("sources");
    setForm({ ...emptyForm });
    setMode(importMode);
    setManual(emptyManual);
  };
  return (
    <div className="proxy-manager">
      <div className="section-title">
        <div className="proxy-heading">
          <h3>{tr("内置代理")}</h3>
          <button
            type="button"
            className="icon-button proxy-help-toggle"
            aria-label={tr("代理使用说明")}
            title={helpOpen ? tr("收起代理使用说明") : tr("查看代理使用说明")}
            aria-expanded={helpOpen}
            aria-controls="proxy-help"
            onClick={() => setHelpOpen((open) => !open)}
          >
            <CircleHelp size={17} aria-hidden="true" />
          </button>
        </div>
        {!initializing && <p>{tr("导入具体节点。使用代理链时，在 Cage 中依次选择中转和最终出口。")}</p>}
      </div>
      {getClientContext().mode === "local" && <LocalConfigImport busy={busy} onImport={id => mutate("import-local", { id })} />}
      {helpOpen && (
        <section
          id="proxy-help"
          className="proxy-help"
          aria-labelledby="proxy-help-title"
        >
          <h4 id="proxy-help-title">{tr("代理使用说明")}</h4>
          <p className="proxy-help-intro">
            {tr("Cage 自带代理核心，无需另外安装 Clash。请先准备你已有的代理订阅或节点信息，Cage 不提供代理节点。")}
          </p>
          <ol className="proxy-help-steps">
            <li>
              <strong>{tr("导入你的代理")}</strong>
              <p>{tr("打开「订阅」→「添加来源」，填写来源名称，再选择一种方式：")}</p>
              <ul>
                <li>
                  <strong>{tr("订阅链接：")}</strong>{tr("从代理服务商处复制 Clash/Mihomo 格式的 HTTPS 订阅地址，粘贴后点击「导入并验证」。首次还没有可用节点时，勾选「通过主机网络下载这份订阅」；主机需要能访问该地址。")}
                </li>
                <li>
                  <strong>{tr("节点配置")}</strong> · {tr("粘贴 v2rayN 导出的节点分享链接（每行一个），或导入 Clash/Mihomo YAML。")}
                </li>
                <li>
                  <strong>{tr("手动节点：")}</strong>{tr("支持 HTTP CONNECT、SOCKS5 和 SSH。填写节点名称、服务器地址、端口及认证信息；SSH 可选择密码或私钥认证，再点击「导入并验证」。")}
                </li>
              </ul>
            </li>
            <li>
              <strong>{tr("选择节点并启用")}</strong>
              <p>
                {tr("导入完成后切到「节点」，选择最终出口，再点击「应用代理」。住宅代理无法从国内连接时，选择「链式代理」，在它前面添加可连接的中转节点；节点可以来自不同订阅。首次启用时若环境正在运行，需要重启；后续切换只重建网络连接。")}
              </p>
            </li>
            <li>
              <strong>{tr("确认可以联网")}</strong>
              <p>
                {initializing ? tr("点击“开始安装”，等待客机安装和网络检查完成。以初始化界面的检查结果为准；") : tr("回到工作台启动环境，等待网络检查通过。以工作台的联网状态为准；")}{tr("「测速」只检测节点延迟。Cage 内的浏览器、终端和开发工具会共用所选节点，无需分别配置代理。")}
              </p>
            </li>
          </ol>
          <div className="proxy-help-notes">
            <strong>{tr("导入失败或无法联网？")}</strong>
            <p>
              {tr("确认复制的是订阅地址，而非服务商登录页或管理页。可导入 Clash/Mihomo YAML、分享链接列表或其 Base64 编码；暂不支持只引用远程节点列表的配置。无法识别的节点会列在跳过原因中。")}
            </p>
            <p>
              {tr("订阅下载失败时，可检查主机是否能访问订阅地址，或改用本地 YAML。节点不可用时，换一个节点并查看「诊断」；更换节点后仍需通过出口 IP 和地区检查。")}
            </p>
          </div>
        </section>
      )}
      <div className="proxy-summary">
        <Server size={22} />
        <div>
          <strong>
            {needsMigration ? tr("代理链需要迁移") : selected ? tr("最终出口 · {0}", selected.name) : tr("尚未配置代理节点")}
          </strong>
          {needsMigration ? (
            <span className="proxy-current-route">{savedRoute.map(routeName).join(" → ")}</span>
          ) : savedRoute.length > 1 && (
            <span className="proxy-current-route">
              {tr("当前链路：{0}", savedRoute.map(routeName).join(" → "))}
            </span>
          )}
          <span>
            {needsMigration
              ? tr("旧配置使用本机代理端口。请导入原代理链中的节点，再按原顺序应用。")
              : active
              ? proxy.running
                ? tr("Cage 自带核心运行中")
                : tr("核心未就绪")
              : tr("请选择节点后应用，未配置时无法联网")}{" "}
            {!needsMigration && "· " + tr("独立端口 {0}", proxy.port)}
          </span>
        </div>
        {needsMigration ? (
          (tab !== "sources" || !form) && <button type="button" disabled={busy} onClick={() => openImport("file")}>{tr("导入节点")}</button>
        ) : <span className={`proxy-badge ${proxy.network_open ? "online" : ""}`}>
          {tr(proxy.state_label)}
        </span>}
      </div>
      {active && status.location && (
        <p className="field-help">
          {tr("最近检测出口：{0}。联网是否放行以工作台检查状态为准。", `${status.location.ip} · ${status.location.country} ${status.location.city}`.trim())}
        </p>
      )}
      {!proxy.core_available && (
        <div role="alert" className="error-message">
          {tr("安装包缺少随附代理核心，请重新构建或安装完整 Cage 应用。")}
        </div>
      )}
      <p className="field-help">
        {tr("仅允许经节点转发的 IPv4 TCP 和 DNS；公网 UDP、IPv6、ICMP 持续阻断。切换会关闭旧网络连接并重新检查出口。")}
      </p>
      <div role="tablist" aria-label={tr("代理管理")} className="proxy-tabs">
        {[
          ["nodes", "节点"],
          ["sources", "订阅"],
          ["connections", "连接"],
          ["diagnostics", "诊断"],
        ].map(([id, label]) => (
          <button
            key={id}
            role="tab"
            aria-selected={tab === id}
            onClick={() => setTab(id)}
          >
            {tr(label)}
          </button>
        ))}
      </div>
      {tab === "nodes" && (
        <>
          {allNodes.length > 0 && (
            <section
              className="proxy-route"
              aria-labelledby="proxy-route-title"
            >
              <div className="proxy-route-heading">
                <div>
                  <h4 id="proxy-route-title">{tr("配置连接方式")}</h4>
                  <p>{tr("直接使用一个出口，或经中转连接住宅代理。")}</p>
                </div>
                <div
                  className="proxy-route-mode"
                  role="group"
                  aria-label={tr("连接方式")}
                >
                  <button
                    type="button"
                    aria-pressed={route.length === 1}
                    disabled={busy}
                    onClick={() => setRouteDraft([route.at(-1)])}
                  >
                    {tr("单节点")}
                  </button>
                  <button
                    type="button"
                    aria-pressed={route.length > 1}
                    disabled={busy}
                    onClick={() => {
                      if (route.length === 1) setRouteDraft(["", route[0]]);
                    }}
                  >
                    {tr("链式代理")}
                  </button>
                </div>
              </div>
              <ol className="proxy-route-hops">
                {route.map((id, index) => {
                  const final = index === route.length - 1;
                  const label = final
                    ? tr("最终出口")
                    : route.length > 2 ? tr("中转节点 {0}", index + 1) : tr("中转节点");
                  return (
                    <li
                      key={index}
                      className={`proxy-route-hop ${final ? "is-exit" : ""}`}
                    >
                      <span className="proxy-hop-number" aria-hidden="true">
                        {index + 1}
                      </span>
                      <div className="proxy-hop-control">
                        <label htmlFor={`proxy-hop-${index}`}>{label}</label>
                        <InlineSelect
                          inline
                          id={`proxy-hop-${index}`}
                          aria-label={label}
                          value={id}
                          disabled={busy}
                          onValueChange={(next) => updateHop(index, next)}
                        >
                          <option value="" disabled>
                            {tr("请选择{0}", label)}
                          </option>
                          {Boolean(id) &&
                            !allNodes.some((n) => n.id === id) && (
                              <option value={id} disabled>
                                {tr("节点已被移除，请重新选择")}
                              </option>
                            )}
                          {proxy.sources.map((src) => (
                            <optgroup key={src.id} label={src.name}>
                              {src.nodes.map((n) => (
                                <option
                                  key={n.id}
                                  value={n.id}
                                  disabled={Boolean(n.blocked_reason) || route.some(
                                    (hop, i) => i !== index && hop === n.id,
                                  )}
                                >
                                  {n.blocked_reason ? tr("{0}（需迁移）", n.name) : n.name}
                                </option>
                              ))}
                            </optgroup>
                          ))}
                        </InlineSelect>
                        {final && usableNodes.some((node) => node.id === id) && <small>{tr("目标网站看到此节点的出口 IP")}</small>}
                      </div>
                      {!final && (
                        <button
                          className="icon-button proxy-hop-remove"
                          type="button"
                          disabled={busy}
                          aria-label={tr("移除{0}", (label))}
                          title={tr("移除{0}", (label))}
                          onClick={() =>
                            setRouteDraft(route.filter((_, i) => i !== index))
                          }
                        >
                          <X size={15} />
                        </button>
                      )}
                    </li>
                  );
                })}
              </ol>
              {route.length > 1 && route.length < usableNodes.length && (
                <button
                  type="button"
                  className="proxy-route-add"
                  disabled={busy || route.length >= usableNodes.length}
                  onClick={() =>
                    setRouteDraft([...route.slice(0, -1), "", route.at(-1)])
                  }
                >
                  <Plus size={14} />
                  {tr("添加中转")}
                </button>
              )}
              {routeValid && <div className="proxy-route-preview" aria-label={tr("待应用链路")}>
                <Route size={16} aria-hidden="true" />
                <span>Cage</span>
                {route.map((id, index) => (
                  <React.Fragment key={index}>
                    <ArrowRight size={13} aria-hidden="true" />
                    <strong>{routeName(id)}</strong>
                  </React.Fragment>
                ))}
                <ArrowRight size={13} aria-hidden="true" />
                <span>{tr("目标网站")}</span>
              </div>}
              <div className="proxy-route-actions">
                <span aria-live="polite">
                  {routeChanged
                    ? tr("有未应用的代理修改")
                    : active
                      ? tr("当前配置已应用")
                      : tr("选择完成后应用代理")}
                </span>
                <div>
                  {routeDraft !== null && (
                    <button
                      type="button"
                      disabled={busy}
                      onClick={() => setRouteDraft(null)}
                    >
                      {tr("撤销修改")}
                    </button>
                  )}
                  <button
                    type="button"
                    className={routeChanged || !active ? "primary" : ""}
                    disabled={busy || !proxy.core_available || !routeValid}
                    onClick={() => applyRoute(route)}
                  >
                    {pending === "route" ? (
                      <LoaderCircle size={14} className="spin" />
                    ) : !routeChanged && active ? (
                      <RefreshCw size={14} />
                    ) : (
                      <Check size={14} />
                    )}
                    {routeChanged || !active ? tr("应用代理") : tr("重新应用")}
                  </button>
                </div>
              </div>
              {proxy.route_error && !needsMigration && (
                <p role="alert" className="proxy-source-error">
                  <SafetyMessage message={proxy.route_error} />
                </p>
              )}
              {error && (
                <div role="alert" className="error-message">
                  <SafetyMessage message={error} />
                </div>
              )}
            </section>
          )}
          {allNodes.length > 0 && (
            <div className="proxy-node-heading">
              <h4>{tr("节点")}</h4>
              <button
                type="button"
                disabled={busy}
                onClick={() => openImport("url")}
              >
                <Plus size={14} />
                {tr("添加来源")}
              </button>
            </div>
          )}
          <div className="proxy-toolbar">
            <div className="proxy-search">
              <Search size={15} aria-hidden="true" />
              <input
                className="proxy-search-input"
                aria-label={tr("搜索节点")}
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder={tr("搜索节点、来源或协议")}
              />
            </div>
            <InlineSelect
              inline
              aria-label={tr("节点来源")}
              value={source}
              onValueChange={(next) => setSource(next)}
            >
              <option value="">{tr("全部来源")}</option>
              {proxy.sources.map((s) => (
                <option key={s.id} value={s.id}>
                  {s.name}
                </option>
              ))}
            </InlineSelect>
            <button
              disabled={busy || !proxy.running || !measurable.length}
              title={tr("检测 HTTP 往返延迟，通常不含建连开销，不代表网页首开耗时")}
              onClick={() => measure(measurable.map((n) => n.id))}
            >
              <Activity size={14} />
              {tr("测速")}
            </button>
            {fastest && fastest !== route.at(-1) && allNodes.some((n) => n.id === fastest) && (
              <button
                disabled={busy}
                title={tr("把测速最快的节点设为待应用的出口；点击「应用代理」后才会切换并重新检查网络")}
                onClick={() => setRouteDraft([fastest])}
              >
                <Check size={14} />
                {tr("选用最快：{0}", routeName(fastest))}
              </button>
            )}
          </div>
          {showRegionBar && (
            <div className="proxy-region-panel">
              <div
                ref={regionBarRef}
                className={`proxy-region-bar ${regionsExpanded ? "expanded" : "clamped"}`}
                role="group"
                aria-label={tr("节点")}
              >
                <button
                  type="button"
                  className="proxy-region-chip"
                  aria-pressed={!activeRegion}
                  onClick={() => setRegion("")}
                >
                  <span>{tr("全部")}</span>
                  <small>{baseNodes.length}</small>
                </button>
                {regionGroups.map((group) => (
                  <button
                    type="button"
                    key={group.code}
                    className="proxy-region-chip"
                    aria-pressed={activeRegion === group.code}
                    onClick={() => setRegion((current) => current === group.code ? "" : group.code)}
                  >
                    {group.flag && <span className="proxy-region-flag" aria-hidden="true">{group.flag}</span>}
                    <span>{group.label}</span>
                    <small>{group.nodes.length}</small>
                  </button>
                ))}
              </div>
              {regionsOverflow && (
                <button
                  type="button"
                  className="text-button proxy-region-toggle"
                  aria-expanded={regionsExpanded}
                  onClick={() => setRegionsExpanded((expanded) => !expanded)}
                >
                  <span>{regionsExpanded ? tr("收起显示") : tr("展开显示")}</span>
                  <ChevronDown size={14} className={regionsExpanded ? "rotated" : ""} />
                </button>
              )}
            </div>
          )}
          {!allNodes.length ? (
            <div className="proxy-empty">
              <Download size={26} />
              <strong>{tr("添加第一份节点来源")}</strong>
              <p>{tr("粘贴 v2rayN 导出的节点分享链接（每行一个），或导入 Clash/Mihomo YAML。")}</p>
              <button
                onClick={() => openImport("file")}
              >
                <Plus size={15} />
                {tr("导入节点")}
              </button>
            </div>
          ) : (
            <div className="proxy-node-list">
              {showGroupedList ? (
                regionGroups.map((group) => {
                  const collapsed = Boolean(collapsedRegions[group.code]);
                  return (
                    <div className="proxy-region-group" key={group.code}>
                      <button
                        type="button"
                        className="proxy-region-group-header"
                        aria-expanded={!collapsed}
                        onClick={() =>
                          setCollapsedRegions((prev) => ({
                            ...prev,
                            [group.code]: !prev[group.code],
                          }))
                        }
                      >
                        {group.flag && <span className="proxy-region-flag" aria-hidden="true">{group.flag}</span>}
                        <strong>{group.label}</strong>
                        <span className="proxy-region-count">{group.nodes.length}</span>
                        <ChevronDown size={14} className={`proxy-region-chevron ${collapsed ? "collapsed" : ""}`} />
                      </button>
                      {!collapsed && group.nodes.map(renderNodeRow)}
                    </div>
                  );
                })
              ) : (
                nodes.map(renderNodeRow)
              )}
              {!nodes.length && (
                <p className="field-help">{tr("没有符合搜索条件的节点。")}</p>
              )}
            </div>
          )}
        </>
      )}
      {tab === "sources" && (
        <>
          <label className="check-line">
            <input
              type="checkbox"
              checked={hostRefresh}
              onChange={(e) => setHostRefresh(e.target.checked)}
            />
            {tr("手动更新订阅时通过主机网络下载")}
          </label>
          <p className="field-help">
            {tr("自动更新始终使用当前配置的代理，不会切换到主机下载。")}
          </p>
          <div className="proxy-toolbar">
            <span>
              {tr("{0} 个来源 · {1} 个节点", proxy.sources.length, allNodes.length)}
            </span>
            <button
              disabled={busy}
              onClick={() => {
                setForm({ ...emptyForm });
                setMode("url");
              }}
            >
              <Plus size={14} />
              {tr("添加来源")}
            </button>
          </div>
          {proxy.sources.map((s) => (
            <div className="proxy-source" key={s.id}>
              <div>
                <strong>{s.name}</strong>
                <small>
                  {s.remote ? s.host : tr("本地导入")} · {tr("{0} 个节点", s.nodes.length)} ·{" "}
                  {s.interval_hours && s.remote
                    ? tr("每 {0} 小时更新", (s.interval_hours))
                    : tr("手动更新")}
                </small>
                <small>
                  {tr("更新于 {0}", new Date(s.updated_at).toLocaleString(activeInterfaceLanguage()))}
                  {usageLabel(s.usage) && ` · ${usageLabel(s.usage)}`}
                </small>
                {s.error && (
                  <span className="proxy-source-error">{localizeBackendMessage(s.error, tr)}</span>
                )}
                {s.skipped?.length > 0 && (
                  <details className="proxy-source-skipped">
                    <summary>
                      {tr("已导入 {0} 个节点，跳过 {1} 个 · 查看原因", s.nodes.length, s.skipped.length)}
                    </summary>
                    <ul>
                      {s.skipped.map((issue) => (
                        <li key={issue.index}>
                          {tr("第 {0} 个节点", issue.index)}
                          {issue.name && `「${issue.name}」`}
                          {issue.type && `（${issue.type}）`}：{localizeBackendMessage(issue.reason, tr)}
                        </li>
                      ))}
                    </ul>
                  </details>
                )}
              </div>
              <div className="proxy-source-actions">
                {s.remote && (
                  <button
                    disabled={busy}
                    aria-label={tr("更新 {0}", (s.name))}
                    onClick={() =>
                      mutate("refresh", {
                        id: s.id,
                        host_download: hostRefresh,
                      })
                    }
                  >
                    <RefreshCw size={14} />
                  </button>
                )}
                <button
                  disabled={busy}
                  aria-label={tr("编辑 {0}", (s.name))}
                  onClick={() => {
                    setForm({
                      ...emptyForm,
                      id: s.id,
                      name: s.name,
                      interval_hours: s.interval_hours,
                      remote: s.remote,
                    });
                    const first = !s.remote && s.nodes[0];
                    setNodeEdit(first ? { id: first.id, server: String(first.server ?? ""), port: String(first.port ?? "") } : { id: "", server: "", port: "" });
                    setMode("url");
                  }}
                >
                  <Pencil size={14} />
                </button>
                <button
                  disabled={busy}
                  aria-label={tr("删除 {0}", (s.name))}
                  onClick={async () => {
                    if (
                      await onConfirm({
                        title: tr("删除来源“{0}”？", s.name),
                        body: s.nodes.some((n) => n.id === proxy.selected)
                          ? tr("当前节点来自此来源。删除后 Cage 会保持离线，直到你选择其他节点。")
                          : tr("将移除此来源及其节点，本地项目文件不受影响。"),
                        confirm: "删除来源",
                      })
                    )
                      mutate("delete", { id: s.id });
                  }}
                >
                  <Trash2 size={14} />
                </button>
              </div>
            </div>
          ))}
          {form && (
            <div className="proxy-import">
              <h4>{form.id ? tr("编辑来源") : tr("添加节点来源")}</h4>
              {!form.id && (
                <div className="proxy-tabs">
                  {[
                    ["url", "订阅链接"],
                    ["file", "节点配置"],
                    ["manual", "手动节点"],
                  ].map(([id, label]) => (
                    <button
                      key={id}
                      className={mode === id ? "selected" : ""}
                      onClick={() => {
                        setMode(id);
                        setForm((old) => ({ ...old, content: "", url: "" }));
                      }}
                    >
                      {tr(label)}
                    </button>
                  ))}
                </div>
              )}
              <label>
                {tr("来源名称")}
                <input
                  autoFocus
                  value={form.name}
                  maxLength={100}
                  onChange={(e) => setForm({ ...form, name: e.target.value })}
                  placeholder={tr("例如：日常代理")}
                />
              </label>
              {mode === "url" && (!form.id || form.remote) && (
                <>
                  <label>
                    {form.id ? tr("新订阅链接（留空保留）") : tr("订阅链接")}
                    <input
                      value={form.url}
                      type="url"
                      autoComplete="off"
                      onChange={(e) =>
                        setForm({ ...form, url: e.target.value })
                      }
                      placeholder="https://…"
                    />
                  </label>
                  <label>
                    {tr("自动更新间隔")}
                    <InlineSelect
                      inline
                      aria-label={tr("自动更新间隔")}
                      value={form.interval_hours}
                      onValueChange={(next) =>
                        setForm({
                          ...form,
                          interval_hours: Number(next),
                        })
                      }
                    >
                      {[
                        [0, "仅手动更新"],
                        [1, "每小时"],
                        [6, "每 6 小时"],
                        [24, "每天"],
                        [168, "每周"],
                      ].map(([v, label]) => (
                        <option value={v} key={v}>
                          {tr(label)}
                        </option>
                      ))}
                    </InlineSelect>
                  </label>
                  {!form.id && (
                    <label className="check-line">
                      <input
                        type="checkbox"
                        checked={form.host_download}
                        onChange={(e) =>
                          setForm({ ...form, host_download: e.target.checked })
                        }
                      />
                      {tr("通过主机网络下载这份订阅")}
                    </label>
                  )}
                  <p className="field-help">
                    {tr("默认经当前配置的代理下载。首次没有可用节点时，可勾选主机下载或导入本地文件；失败不会自动改用其他路径。")}
                  </p>
                </>
              )}
              {form.id && !form.remote && (() => {
                const source = proxy.sources.find((src) => src.id === form.id);
                const nodesOfSource = source?.nodes ?? [];
                return (
                  <>
                    <div className="proxy-node-edit">
                      <strong>{tr("修改节点地址")}</strong>
                      <div className="field-row">
                        <label>
                          {tr("节点")}
                          <select
                            value={nodeEdit.id}
                            disabled={busy}
                            onChange={(e) => {
                              const n = nodesOfSource.find((x) => x.id === e.target.value);
                              setNodeEdit({ id: e.target.value, server: String(n?.server ?? ""), port: String(n?.port ?? "") });
                            }}
                          >
                            {nodesOfSource.map((n) => (
                              <option key={n.id} value={n.id}>{n.name}</option>
                            ))}
                          </select>
                        </label>
                        <label>
                          {tr("服务器")}
                          <input value={nodeEdit.server} disabled={busy} onChange={(e) => setNodeEdit({ ...nodeEdit, server: e.target.value })} />
                        </label>
                        <label>
                          {tr("端口")}
                          <input inputMode="numeric" value={nodeEdit.port} disabled={busy} onChange={(e) => setNodeEdit({ ...nodeEdit, port: e.target.value })} />
                        </label>
                      </div>
                      <p className="field-help">{tr("只修改服务器和端口，认证信息与协议参数保持不变；修改后需重新通过出口检查。")}</p>
                      <button type="button" disabled={busy || !nodeEdit.id || !nodeEdit.server.trim() || !nodeEdit.port} onClick={editNodeAddress}>
                        {tr("保存地址")}
                      </button>
                    </div>
                    <p className="field-help">{tr("可粘贴或选择新的 YAML 更新此来源；名称不变的节点保留选择与出口规则。留空则只修改名称。")}</p>
                    <ProxyYAMLInput
                      content={form.content}
                      disabled={busy}
                      onReadingChange={setReadingFile}
                      onContentChange={(content) => setForm((old) => ({ ...old, content }))}
                      onFileRead={(_, content) => setForm((old) => ({ ...old, content }))}
                    />
                  </>
                );
              })()}
              {mode === "file" && !form.id && (
                <ProxyYAMLInput
                  content={form.content}
                  disabled={busy}
                  onReadingChange={setReadingFile}
                  onContentChange={(content) =>
                    setForm((old) => ({ ...old, content }))
                  }
                  onFileRead={(value, content) =>
                    setForm((old) => ({
                      ...old,
                      content,
                      name:
                        old.name ||
                        value.name.replace(/\.[^.]+$/, "").slice(0, 100),
                      interval_hours: 0,
                    }))
                  }
                />
              )}
              {mode === "manual" && !form.id && (
                <>
                  <div className="field-row">
                    <label>
                      {tr("节点名称")}
                      <input
                        value={manual.name}
                        onChange={(e) =>
                          setManual({ ...manual, name: e.target.value })
                        }
                      />
                    </label>
                    <label>
                      {tr("协议")}
                      <InlineSelect
                        inline
                        aria-label={tr("协议")}
                        value={manual.type}
                        onValueChange={(next) =>
                          setManual({
                            ...manual,
                            type: next,
                            port: manual.port === (manualSSH ? "22" : "1080")
                              ? next === "ssh" ? "22" : "1080"
                              : manual.port,
                          })
                        }
                      >
                        <option value="socks5">SOCKS5</option>
                        <option value="http">HTTP CONNECT</option>
                        <option value="ssh">SSH</option>
                      </InlineSelect>
                    </label>
                  </div>
                  <div className="field-row">
                    <label>
                      {tr("服务器")}
                      <input
                        value={manual.server}
                        onChange={(e) =>
                          setManual({ ...manual, server: e.target.value })
                        }
                        placeholder={tr("域名或 IP")}
                      />
                    </label>
                    <label>
                      {tr("端口")}
                      <input
                        type="number"
                        min="1"
                        max="65535"
                        value={manual.port}
                        onChange={(e) =>
                          setManual({ ...manual, port: e.target.value })
                        }
                      />
                    </label>
                  </div>
                  {localListener && <p role="alert" className="proxy-source-error">{tr("这里需要节点服务器地址。本机代理端口无法用作节点。")}</p>}
                  {manualSSH && (
                    <label>
                      {tr("认证方式")}
                      <InlineSelect
                        inline
                        aria-label={tr("认证方式")}
                        value={manual.sshAuth}
                        onValueChange={(next) => setManual({
                          ...manual, sshAuth: next,
                          ...(next !== manual.sshAuth ? { password: "", privateKey: "", passphrase: "" } : {}),
                        })}
                      >
                        <option value="password">{tr("密码")}</option>
                        <option value="private-key">{tr("私钥")}</option>
                      </InlineSelect>
                    </label>
                  )}
                  <div className="field-row">
                    <label>
                      {manualSSH ? tr("用户名（必填）") : tr("用户名")}
                      <input
                        value={manual.username}
                        autoComplete="off"
                        onChange={(e) =>
                          setManual({ ...manual, username: e.target.value })
                        }
                      />
                    </label>
                    {(!manualSSH || manual.sshAuth === "password") && <label>
                      {manualSSH ? tr("密码（必填）") : tr("密码")}
                      <input
                        type="password"
                        value={manual.password}
                        autoComplete="new-password"
                        onChange={(e) =>
                          setManual({ ...manual, password: e.target.value })
                        }
                      />
                    </label>}
                  </div>
                  {manualSSH && <>
                    {manual.sshAuth === "private-key" && <>
                      <label>
                        {tr("私钥内容（必填）")}
                        <textarea
                          aria-label={tr("私钥内容（必填）")}
                          value={manual.privateKey}
                          rows={7}
                          spellCheck={false}
                          autoComplete="off"
                          autoCapitalize="none"
                          onChange={(e) => setManual({ ...manual, privateKey: e.target.value })}
                          placeholder="-----BEGIN OPENSSH PRIVATE KEY-----"
                        />
                      </label>
                      <p className="field-help">{tr("粘贴完整 PEM / OpenSSH 私钥内容，不支持填写本机文件路径。")}</p>
                      <label>
                        {tr("私钥口令（可选）")}
                        <input
                          type="password"
                          value={manual.passphrase}
                          autoComplete="new-password"
                          onChange={(e) => setManual({ ...manual, passphrase: e.target.value })}
                        />
                      </label>
                    </>}
                    <label>
                      {tr("服务器公钥（可选，建议填写）")}
                      <textarea
                        aria-label={tr("服务器公钥（可选，建议填写）")}
                        value={manual.hostKey}
                        rows={2}
                        spellCheck={false}
                        autoCapitalize="none"
                        onChange={(e) => setManual({ ...manual, hostKey: e.target.value })}
                        placeholder="ssh-ed25519 AAAA…"
                      />
                    </label>
                    <p className="field-help">{tr("填写可信的服务器公钥以核对服务器身份，每行一条；留空时不校验服务器身份。SSH 仅转发 TCP。")}</p>
                  </>}
                </>
              )}
              <div className="proxy-form-actions">
                <button disabled={busy} onClick={() => { setForm(null); setManual(emptyManual); }}>
                  {tr("取消")}
                </button>
                <button
                  className="primary"
                  disabled={
                    busy ||
                    readingFile ||
                    (!form.id &&
                      (mode === "url"
                        ? !form.url || !form.name
                        : mode === "file"
                          ? !form.content.trim()
                          : !manualValid))
                  }
                  onClick={submit}
                >
                  {form.id ? tr("保存来源") : tr("导入并验证")}
                </button>
              </div>
            </div>
          )}
        </>
      )}
      {tab === "connections" && (
        <>
          <div className="proxy-metrics">
            <span>
              {tr("上传") + " "}<strong>{bytes(rates.up)}/s</strong>
              <small>{tr("累计") + " "}{bytes(connections.uploadTotal)}</small>
            </span>
            <span>
              {tr("下载") + " "}<strong>{bytes(rates.down)}/s</strong>
              <small>{tr("累计") + " "}{bytes(connections.downloadTotal)}</small>
            </span>
          </div>
          <p className="field-help">
            {tr("显示当前核心的连接与流量，包含 Cage 自身检测请求；重启核心后累计值重置。")}
          </p>
          <div className="proxy-node-list">
            {connections.connections.map((c) => (
              <div className="proxy-connection" key={c.id}>
                <span>
                  {c.host}:{c.port}
                  <small>{c.network}</small>
                </span>
                <small>
                  ↑ {bytes(c.upload)} · ↓ {bytes(c.download)}
                </small>
              </div>
            ))}
            {!connections.connections.length && (
              <p className="proxy-empty">{tr("暂无活动连接")}</p>
            )}
          </div>
        </>
      )}
      {tab === "diagnostics" && (
        <>
          <p className="field-help">
            {tr("随附核心 {0}。以下为 Cage 状态摘要；含凭据风险的核心原始日志不向页面公开。", proxy.version || tr("未运行"))}
          </p>
          <div className="proxy-diagnostics" role="log">
            {logs.map((line, i) => (
              <div key={i}>{line}</div>
            ))}
          </div>
          <label>
            {tr("独立代理端口")}
            <input
              type="number"
              min="1024"
              max="65535"
              value={port}
              onChange={(e) => {
                portDirty.current = true;
                setPort(e.target.value);
              }}
            />
          </label>
          <div className="proxy-toolbar">
            <button
              disabled={busy || !selected || needsMigration}
              onClick={async () => {
                if (
                  Number(port) !== proxy.port &&
                  status.vm_running &&
                  !(await onConfirm({
                    title: "更换代理端口并重启环境？",
                    body: "端口变化需要重启环境，当前终端会结束。",
                    confirm: "更换并重启",
                  }))
                )
                  return;
                mutate("restart", { port: Number(port) });
              }}
            >
              <RefreshCw size={14} />
              {tr("应用端口并重启核心")}
            </button>
            <button
              disabled={busy || !proxy.running}
              onClick={() => mutate("stop")}
            >
              <Square size={14} />
              {tr("停止核心")}
            </button>
          </div>
          <p className="field-help">
            {tr("Cage 不设置系统代理或主机 TUN。本机其他代理的 TUN 仍可能影响核心出站，需要结合实际路由验证。")}
          </p>
        </>
      )}
      {pending && (
        <p className="proxy-progress" role="status">
          <LoaderCircle size={14} className="spin" />
          {pending === "delay"
            ? tr("正在检测节点延迟…")
            : status.job?.message
              ? localizeBackendMessage(status.job.message, tr)
              : tr("正在处理…")}
        </p>
      )}
      {error && !(tab === "nodes" && allNodes.length > 0) && (
        <div role="alert" className="error-message">
          <SafetyMessage message={error} />
        </div>
      )}
      {notice && (
        <p role="status" className="proxy-notice">
          <Check size={14} />
          {localizeBackendMessage(notice, tr)}
        </p>
      )}
    </div>
  );
}
