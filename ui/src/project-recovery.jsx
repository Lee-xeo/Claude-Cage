import React from "react";

export function ProjectRecovery({ recovery, disabled, onRetry, onSettings }) {
  return <div className="project-recovery-actions">
    {["environment-update", "environment-restart"].includes(recovery) && onSettings &&
      <button type="button" disabled={disabled} onClick={onSettings}>{recovery === "environment-update" ? "查看环境组件" : "打开环境设置"}</button>}
    {recovery === "project-directory" && onSettings &&
      <button type="button" disabled={disabled} onClick={onSettings}>查看诊断记录</button>}
    <button type="button" disabled={disabled} onClick={onRetry}>{recovery === "project-approval" ? "完成项目授权" : "重新连接项目"}</button>
  </div>;
}
