// Only the stop requested by this window can authorize its automatic exit.
export function stopExitState(status, jobId) {
  const job = status.job;
  if (!jobId || job?.id !== jobId || job.kind !== "stop" || job.running)
    return "waiting";
  if (job.error) return "failed";
  return status.vm_running ? "waiting" : "exit";
}

// Once no job is running, vm_running may lag only briefly. A VM that keeps
// reporting running after its stop finished, or a status that no longer names
// this stop, cannot be confirmed; report it instead of waiting forever.
export const stopConfirmationGraceMs = 20000;

// tr is the caller's translator; the literals stay in tr() for the catalogs.
export function stopUnconfirmedMessage(status, jobId, tr = (text) => text) {
  if (status.job?.id === jobId)
    return tr("停止任务已结束，但环境仍显示在运行，无法确认已安全停止。可以重试停止，或仅退出桌面应用。");
  return tr("后台未报告本次停止任务的结果，无法确认环境已停止。可以重试停止，或仅退出桌面应用。");
}

// Exit owns its wait loop: boot failures and a stopped UI poll must not strand it.
export async function stopAndExit({
  stop,
  readStatus,
  exit,
  isCancelled,
  onStatus,
  delay = () => new Promise((resolve) => setTimeout(resolve, 350)),
  now = () => Date.now(),
  confirmationGraceMs = stopConfirmationGraceMs,
  tr = (text) => text,
}) {
  const job = await stop();
  if (!job.id) throw new Error(tr("停止请求未返回任务编号，请重试。"));
  let idleSince = null;
  while (!isCancelled()) {
    const status = await readStatus();
    if (isCancelled()) return false;
    onStatus(status);
    const state = stopExitState(status, job.id);
    if (state === "failed") throw new Error(status.job.error);
    if (state === "exit") {
      await exit();
      return true;
    }
    if (status.job?.running) idleSince = null;
    else if (idleSince === null) idleSince = now();
    else if (now() - idleSince >= confirmationGraceMs)
      throw new Error(stopUnconfirmedMessage(status, job.id, tr));
    await delay();
  }
  return false;
}
