import React, { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { RefreshCw } from "lucide-react";
import { api } from "./api";
import { InlineSelect } from "./inline-select";
import { compactTokens, heatLevel, localToday, metricValue, summarizeUsage, tokenFields, tokenMetrics, usageBreakdown, usageClient, usageClients, usageSeries, yearCalendar } from "./token-usage.mjs";
import "./token-usage.css";

// Primary: annual activity and its selected date. Secondary: recorded totals and
// trend. Tertiary: source/retention controls. Reuse paper/ink/muted/line/success
// theme tokens, 8/16/24/32 spacing, 14/16/24 type; refresh is the primary action.
function UsageCurve({ series, mode, metric }) {
  const [hover, setHover] = useState(null);
  const width = 960, height = 220, left = 64, top = 16, bottom = 32;
  const max = Math.max(1,...series.map(point => point.value || 0));
  const plotHeight = height - top - bottom;
  const x = index => left + index / Math.max(1,series.length-1) * (width-left-16);
  const y = value => top + plotHeight * (1 - value/max);
  let connected = false;
  const path = series.map((point,index) => {
    if (point.value === null) { connected = false; return ""; }
    const command = `${connected ? "L" : "M"}${x(index)},${y(point.value)}`;
    connected = true;
    return command;
  }).join(" ");
  const point = hover === null ? null : series[hover];
  const label = tokenMetrics.find(([key])=>key===metric)?.[1];
  return <>
    <div className="token-curve-scroll"><svg className="token-curve" viewBox={`0 0 ${width} ${height}`} role="group" aria-label={`${mode === "weekly" ? "每周" : mode === "cumulative" ? "累计" : "每日"}${label}曲线`}>
      {[0,.5,1].map(fraction => <g key={fraction}>
        <line x1={left} x2={width-16} y1={y(max*fraction)} y2={y(max*fraction)} className="token-grid-line" />
        <text x={left-12} y={y(max*fraction)+4} textAnchor="end">{compactTokens(max*fraction)}</text>
      </g>)}
      <path d={path} className="token-curve-line" />
      {series.map((point,index) => point.value === null ? null : <circle key={point.date}
        cx={x(index)} cy={y(point.value)} r={hover===index ? 5 : 2.5} tabIndex={0}
        className="token-curve-dot" role="img" aria-label={`${point.date}，${point.value.toLocaleString()} Token`}
        onMouseEnter={()=>setHover(index)} onFocus={()=>setHover(index)} onBlur={()=>setHover(null)} />)}
      {[0,Math.floor((series.length-1)/2),series.length-1].filter((n,index,items)=>n>=0&&items.indexOf(n)===index)
        .map(index=><text key={index} x={x(index)} y={height-8} textAnchor={index===0 ? "start" : index===series.length-1 ? "end" : "middle"}>{series[index]?.date.slice(5).replace("-","/")}</text>)}
    </svg></div>
    <p className="token-chart-caption" aria-live="polite">{point ? `${point.date}${mode==="weekly" ? " 所在周" : ""} · ${point.value.toLocaleString()} Token` : mode==="cumulative" ? "累计曲线汇总所选年份内已记录的 Token。" : "无记录的日期留空；周用量汇总该周已有记录。"}</p>
  </>;
}

export function TokenUsagePanel({ onConfirm }) {
  const [data,setData] = useState(null), [error,setError] = useState("");
  const [busy,setBusy] = useState(false);
  const [year,setYear] = useState(""), [metric,setMetric] = useState("total");
  const [model,setModel] = useState("all"), [source,setSource] = useState("all");
  const [client,setClient] = useState("all");
  const [mode,setMode] = useState("daily"), [selected,setSelected] = useState("");
  const generation = useRef(0), pending = useRef(false), mounted = useRef(false);
  const mutationError = useRef(false);
  const heatScroll = useRef(null);
  const load = useCallback(async (body) => {
    if (pending.current) return;
    pending.current = true;
    const request = ++generation.current;
    setBusy(true);
    try {
      const value = await api("/token-usage",body);
      if (mounted.current && generation.current === request) {
        setData(value);
        if (body || !mutationError.current) { mutationError.current=false; setError(""); }
      }
      return value;
    } catch (e) { if (mounted.current && generation.current===request) { mutationError.current=Boolean(body); setError(e.message); } }
    finally { pending.current=false; if (mounted.current && generation.current===request) setBusy(false); }
  },[]);
  useEffect(() => {
    mounted.current=true;
    load().then(value=>{ if (mounted.current && value?.enabled && value.available) load({action:"refresh"}); });
    const timer=setInterval(()=>{if(!document.hidden)load();},15000);
    return ()=>{mounted.current=false;generation.current++;clearInterval(timer);};
  },[load]);
  const snapshot=data?.snapshot;
  const rows=snapshot?.days || [];
  const today=snapshot?.today || localToday();
  const activeYear=year || today.slice(0,4);
  const loaded=Boolean(data);
  useEffect(()=>{
    const element=heatScroll.current;
    if(element)element.scrollLeft=element.scrollWidth-element.clientWidth;
  },[activeYear,loaded]);
  const years=[...new Set([today.slice(0,4),...rows.map(row=>row.date.slice(0,4))])].sort().reverse();
  const models=[...new Set(rows.map(row=>row.model))].sort();
  const sources=[...new Set(rows.map(row=>row.source))].sort();
  const clients=usageClients.filter(([key])=>rows.some(row=>usageClient(row)===key));
  const days=useMemo(()=>summarizeUsage(snapshot?.days,{model,source,client}),[snapshot,model,source,client]);
  const breakdown=usageBreakdown(rows,{year:activeYear,model,source,client},metric);
  const classifiedTotal=breakdown.reduce((sum,item)=>sum+item.value,0);
  const calendar=useMemo(()=>yearCalendar(activeYear),[activeYear]);
  const yearDays=calendar.map(item=>days.get(item.date)).filter(Boolean);
  const sums=Object.fromEntries([...tokenFields,"requests"].map(field=>[field,yearDays.reduce((sum,day)=>sum+day[field],0)]));
  const total=metricValue(sums,"total");
  const activeDays=yearDays.filter(day=>metricValue(day,"total")>0).length;
  const maximum=Math.max(1,...yearDays.map(day=>metricValue(day,metric)));
  const series=usageSeries(calendar,days,metric,mode,today);
  const chosen=selected && selected.startsWith(activeYear) ? selected : (yearDays.at(-1)?.date || (activeYear===today.slice(0,4)?today:activeYear+"-01-01"));
  const chosenDay=days.get(chosen);
  const chartKey=[activeYear,model,source,client,metric,mode].join(":");
  const moveCell=(event,index)=>{
    const delta={ArrowLeft:-7,ArrowRight:7,ArrowUp:-1,ArrowDown:1}[event.key];
    if (!delta) return;
    event.preventDefault();
    const next=calendar[index+delta];
    if (next && next.date<=today) document.getElementById("token-day-"+next.date)?.focus();
  };
  const clear=async()=>{
    if (await onConfirm({title:"清除本地用量统计？",body:"清除已保存的 Token 计数，并从现在起重新统计。Claude Code 原始会话、登录和项目保留。",confirm:"清除统计",danger:true})) await load({action:"clear"});
  };
  return <section className="token-usage" aria-label="Claude 本地用量">
    <div className="token-page-heading"><div><h2>Token 活动</h2><p>Claude Code 与 Desktop Code · 本地记录汇总</p></div>
      <button className="token-refresh" disabled={busy || !data?.enabled || !data?.available} onClick={()=>load({action:"refresh"})}><RefreshCw size={15} className={busy?"token-spinning":""}/> {busy?"正在读取…":"刷新统计"}</button></div>
    {error && <p className="token-error" role="alert">{error}<button disabled={busy} onClick={()=>load(data?.enabled && data?.available?{action:"refresh"}:undefined)}>重试</button></p>}
    {!data && <p role="status">正在读取本地统计…</p>}
    {data && <>
      {!data.available && <p className="token-notice" role="status">环境尚未就绪，显示上次保存的统计。启动环境并完成组件更新后可刷新。</p>}
      {!data.enabled && <p className="token-notice" role="status">统计已暂停，已保存的计数继续保留。</p>}
      {snapshot?.pending && <p className="token-notice" role="status">部分记录尚未读完，将在后续采集中继续处理。</p>}
      {snapshot?.invalid_records>0 && <p className="token-notice" role="status">有 {snapshot.invalid_records.toLocaleString()} 条记录无法解析，当前统计可能不完整。</p>}
      <div className="token-filters">
        <label>年份<InlineSelect inline aria-label="统计年份" value={activeYear} onValueChange={setYear}>{years.map(value=><option key={value} value={value}>{value} 年</option>)}</InlineSelect></label>
        <label>统计口径<InlineSelect inline aria-label="统计口径" value={metric} onValueChange={setMetric}>{tokenMetrics.map(([key,label])=><option key={key} value={key}>{label}</option>)}</InlineSelect></label>
        {models.length>0 && <label>模型<InlineSelect inline aria-label="筛选模型" value={model} onValueChange={setModel}><option value="all">全部模型</option>{models.map(value=><option key={value} value={value}>{value}</option>)}</InlineSelect></label>}
        {sources.length>0 && <label>日志来源<InlineSelect inline aria-label="筛选日志来源" value={source} onValueChange={setSource}><option value="all">全部来源</option>{sources.map(value=><option key={value} value={value}>{data.sources?.[value] || "未知配置"}</option>)}</InlineSelect></label>}
        {clients.length>0 && <label>运行入口<InlineSelect inline aria-label="筛选运行入口" value={client} onValueChange={setClient}><option value="all">全部入口</option>{clients.map(([key,label])=><option key={key} value={key}>{label}</option>)}</InlineSelect></label>}
      </div>
      <dl className="token-totals">
        <div><dt>全年已记录 Token</dt><dd>{compactTokens(total)}</dd></div>
        <div><dt>输出 Token</dt><dd>{compactTokens(sums.output_tokens)}</dd></div>
        <div><dt>已记录请求</dt><dd>{sums.requests.toLocaleString()}</dd></div>
        <div><dt>活跃天数</dt><dd>{activeDays}<small> 天</small></dd></div>
      </dl>
      <section className="token-activity" aria-labelledby="token-heatmap-title">
        <div className="token-section-heading"><h3 id="token-heatmap-title">{activeYear} 年活动</h3><span>{tokenMetrics.find(([key])=>key===metric)?.[1]}</span></div>
        <div className="token-heat-scroll" ref={heatScroll}><div className="token-heatmap" style={{"--token-weeks":calendar.at(-1).column+1}}>
          {calendar.filter(item=>item.date.endsWith("-01")).map(item=><span className="token-month" key={item.date} style={{gridColumn:item.column+1}}>{Number(item.date.slice(5,7))}月</span>)}
          {calendar.map((item,index)=>{const value=metricValue(days.get(item.date),metric);return <button key={item.date} id={"token-day-"+item.date}
            className={`token-cell level-${heatLevel(value,maximum)}${item.date>today?" token-future":""}`} style={{gridColumn:item.column+1,gridRow:item.row+2}}
            aria-label={`${item.date}，${item.date>today?"尚未到来":value===null?"无记录":value.toLocaleString()+" Token"}`}
            aria-pressed={chosen===item.date} disabled={item.date>today} tabIndex={chosen===item.date || !calendar.some(day=>day.date===chosen)&&index===0?0:-1}
            onClick={()=>setSelected(item.date)} onFocus={()=>setSelected(item.date)} onKeyDown={event=>moveCell(event,index)}/>;})}
        </div></div>
        <div className="token-heat-legend"><span><i className="token-cell level--1"/>无记录</span><span>少{[0,1,2,3,4].map(level=><i key={level} className={`token-cell level-${level}`}/>)}多</span></div>
        <div className="token-day-detail" aria-live="polite"><strong>{chosen}</strong>{chosenDay ? <><span>{metricValue(chosenDay,metric).toLocaleString()} Token</span><span>{chosenDay.requests.toLocaleString()} 次请求</span></> : <span>无记录</span>}</div>
      </section>
      {rows.length===0 && <p className="token-empty">尚无可统计的 Token 记录。运行 Claude Code 或 Desktop Code 后刷新；已有历史会从仍保留的日志中读取。</p>}
      {breakdown.length>0 && <section className="token-breakdown" aria-labelledby="token-breakdown-title">
        <div className="token-section-heading"><h3 id="token-breakdown-title">按运行入口分布</h3><span>{activeYear} 年 · {tokenMetrics.find(([key])=>key===metric)?.[1]}</span></div>
        <table aria-label="运行入口用量"><thead><tr><th scope="col">入口</th><th scope="col">Token / 占比</th><th scope="col">请求</th></tr></thead>
          <tbody>{breakdown.map(item=>{const share=classifiedTotal>0?item.value/classifiedTotal*100:0;return <tr key={item.client}>
            <th scope="row"><button onClick={()=>setClient(item.client)} aria-label={`只看${usageClients.find(([key])=>key===item.client)?.[1]}`}>{usageClients.find(([key])=>key===item.client)?.[1]}</button><div className="token-client-track" aria-hidden="true"><span style={{width:`${share}%`}}/></div></th>
            <td aria-label={`${item.value.toLocaleString()} Token，占比 ${share.toFixed(1)}%`}>{compactTokens(item.value)}<small>{share.toFixed(1)}%</small></td><td>{item.requests.toLocaleString()}</td>
          </tr>;})}</tbody></table>
      </section>}
      <p className="token-coverage">仅统计已保存的本地用量，不主动获取普通聊天与云端数据；Cowork 仅纳入标准日志中明确标识的记录。</p>
      <section className="token-trend" aria-labelledby="token-trend-title">
        <div className="token-section-heading"><h3 id="token-trend-title">用量趋势</h3><div className="token-segments" role="group" aria-label="曲线周期">{[["daily","每天"],["weekly","每周"],["cumulative","累计总量"]].map(([key,label])=><button key={key} aria-pressed={mode===key} onClick={()=>setMode(key)}>{label}</button>)}</div></div>
        <UsageCurve key={chartKey} series={series} mode={mode} metric={metric}/>
      </section>
      <details className="token-settings"><summary>统计范围与隐私</summary>
        <p>仅提取时间、模型、入口分类及四类 Token 计数，不保存对话、代码或密钥。总量包含输入、输出、缓存读取和缓存写入；它不能直接换算成订阅剩余额度或实际费用。</p>
        <p>采集默认与托管账号配置中的标准会话记录，按运行环境的本地日期统计{snapshot?.timezone?`（${snapshot.timezone}）`:""}。日志来源表示配置目录，不代表实际扣费账号。自定义配置、CCS、其他设备及已删除或未保存的日志暂不覆盖。</p>
        <p>运行入口依据每条用量记录中的客户端标识，表示请求的发起入口，不代表实际执行机器。缺少标识、无法识别或同一请求的标识冲突时归入“未知入口”。升级前已采集但原日志已删除的计数继续保留，无法补分入口。</p>
        <p>工作台运行、环境就绪且已解锁时约每分钟采集一次。暂停后停止读取，重新启用会补读仍保留的日志。</p>
        <div className="token-settings-actions"><label><input type="checkbox" checked={data.enabled} disabled={busy} onChange={event=>load({action:"configure",enabled:event.target.checked})}/>启用本地统计</label><button disabled={busy || !data.available} onClick={clear}>清除统计</button></div>
      </details>
      <p className="token-updated">{snapshot?.updated_at?`上次采集：${new Date(snapshot.updated_at*1000).toLocaleString()}`:"尚未完成采集"}{data.cutoff>0?` · 仅统计 ${new Date(data.cutoff*1000).toLocaleString()} 之后的请求`:""}</p>
    </>}
  </section>;
}
