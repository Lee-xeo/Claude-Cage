import React from "react";
import "./initialization.css";

const labels = { passed: "已通过", failed: "未通过", unknown: "尚未完成" };

export function ScreenSharingChecks({ checks = [], summary, status }) {
  const items = checks.length ? checks : status?.checks || [];
  const text = summary || status?.summary;
  if (!text && items.length === 0) return null;
  return <>
    {text && <p role="status">{text}</p>}
    {items.length > 0 && <ol className="initialization-checks" aria-label="屏幕共享检查结果">
      {items.map(check => <li key={check.id}>
        <strong>{check.title} · {labels[check.state] || "尚未完成"}</strong>
        <p>{check.evidence}</p>
        {check.state !== "passed" && check.remedy && <p>{check.remedy}</p>}
      </li>)}
    </ol>}
  </>;
}
