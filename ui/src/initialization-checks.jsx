import React from "react";
import { useTranslation } from "./interface-language.mjs";
import { localizeBackendMessage } from "./backend-message.mjs";

export function InitializationDetails({ status }) {
  const tr = useTranslation();
  const job = status?.job || {};
  const reasons = [...new Set([job.error, status?.safety?.error, status?.network_error].filter(Boolean))];
  const checks = status?.safety?.report?.checks || [];
  const labels = { passed: "已通过", failed: "未通过", unknown: "尚未完成" };
  return <div className="modal-body">
    <p>{job.running ? tr("当前操作") : tr("最近一次操作")}：{job.message ? localizeBackendMessage(job.message, tr) : tr("尚未开始")}</p>
    {reasons.map(reason => <p key={reason} role="alert">{localizeBackendMessage(reason, tr)}</p>)}
    {checks.length > 0 && <><h3>{tr("最近一次检查结果")}</h3><ol className="initialization-checks">{checks.map(check => <li key={check.id}>
      <strong>{check.title} · {labels[check.state] ? tr(labels[check.state]) : tr("尚未完成")}</strong>
      <p>{check.evidence}</p>{check.state !== "passed" && check.remedy && <p>{check.remedy}</p>}
    </li>)}</ol></>}
    <h3>{tr("操作记录")}</h3><pre className="job-log">{job.log || tr("当前操作没有额外日志。失败原因显示在上方，完整记录可在「日志与诊断」中导出。")}</pre>
  </div>;
}
