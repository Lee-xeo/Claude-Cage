// Pure state helpers for the guided proxy setup and multi-hop chain picker.

export const emptyFormDefaults = {
  name: "",
  url: "",
  content: "",
  interval_hours: 24,
  host_download: false,
};

export const emptyManualDefaults = {
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

/**
 * Determine whether the source add/edit form has actual user modifications.
 * Opening an untouched "Add source" or "Edit source" form must not block setup.
 */
export function isSourceFormDirty(form, mode, manual, sources = []) {
  if (!form) return false;
  if (form.id) {
    const existing = sources.find((src) => src.id === form.id);
    if (!existing) return true;
    if ((form.name ?? "") !== (existing.name ?? "")) return true;
    if (Number(form.interval_hours ?? 0) !== Number(existing.interval_hours ?? 0)) return true;
    if (String(form.url ?? "").trim() !== "") return true;
    if (String(form.content ?? "").trim() !== "") return true;
    return false;
  }
  if (String(form.name ?? "").trim() !== "") return true;
  if (mode === "url") {
    return (
      String(form.url ?? "").trim() !== "" ||
      Number(form.interval_hours ?? 24) !== emptyFormDefaults.interval_hours ||
      Boolean(form.host_download)
    );
  }
  if (mode === "file") {
    return String(form.content ?? "").trim() !== "";
  }
  if (mode === "manual" && manual) {
    const defaultPort = manual.type === "ssh" ? "22" : "1080";
    return Boolean(
      String(manual.name ?? "").trim() ||
      manual.type !== emptyManualDefaults.type ||
      String(manual.server ?? "").trim() ||
      String(manual.port ?? "") !== defaultPort ||
      String(manual.username ?? "").trim() ||
      String(manual.password ?? "") ||
      manual.sshAuth !== emptyManualDefaults.sshAuth ||
      String(manual.privateKey ?? "").trim() ||
      String(manual.passphrase ?? "") ||
      String(manual.hostKey ?? "").trim(),
    );
  }
  return false;
}

/**
 * Resolve which hop index in `route` clicking a node row in the list will fill.
 * - Single-node mode always targets index 0.
 * - Chain mode honors an explicit valid hop selection; otherwise targets the
 *   first empty hop so users fill transit -> exit smoothly, settling back on
 *   the final exit once all hops are filled.
 */
export function resolvePickerHop(route, requestedHop, usableNodeIDs = null) {
  if (!Array.isArray(route) || route.length <= 1) return 0;
  if (
    Number.isInteger(requestedHop) &&
    requestedHop >= 0 &&
    requestedHop < route.length
  ) {
    return requestedHop;
  }
  const isFilled = (id) =>
    Boolean(id) && (usableNodeIDs ? usableNodeIDs.has(id) : true);
  const firstEmpty = route.findIndex((id) => !isFilled(id));
  return firstEmpty >= 0 ? firstEmpty : route.length - 1;
}

/**
 * After picking a node for `pickedIndex`, advance automatically to the next
 * empty hop if one remains, or return null so focus settles cleanly on exit.
 */
export function nextPickerHopAfterPick(nextRoute, pickedIndex, usableNodeIDs = null) {
  if (!Array.isArray(nextRoute) || nextRoute.length <= 1) return null;
  const isFilled = (id) =>
    Boolean(id) && (usableNodeIDs ? usableNodeIDs.has(id) : true);
  for (let i = pickedIndex + 1; i < nextRoute.length; i++) {
    if (!isFilled(nextRoute[i])) return i;
  }
  for (let i = 0; i < pickedIndex; i++) {
    if (!isFilled(nextRoute[i])) return i;
  }
  return null;
}

/**
 * Swap relay and exit in a 2-hop chain when users accidentally reverse the order.
 */
export function swapTwoHopRoute(route) {
  if (!Array.isArray(route) || route.length !== 2) return route;
  return [route[1], route[0]];
}

/**
 * Collapse a multi-hop route back to single-node mode without discarding the
 * only selected node if the exit slot happened to be empty.
 */
export function singleNodeFromRoute(route) {
  if (!Array.isArray(route) || !route.length) return [""];
  const exit = route.at(-1);
  if (exit) return [exit];
  const fallback = route.find(Boolean) || "";
  return [fallback];
}

/**
 * Summarize the guided setup progress and what blocks installation next.
 */
export function setupGuideProgress({
  usableNodeCount = 0,
  route = [""],
  routeValid = false,
  routeChanged = false,
  active = false,
  needsMigration = false,
  sourceDirty = false,
  chainMode = false,
}) {
  const effectiveChain = Boolean(chainMode || route.length > 1);
  const hasNodes = usableNodeCount > 0;
  const needsMoreNodesForChain = effectiveChain && usableNodeCount < 2;
  const importDone = hasNodes && !needsMoreNodesForChain && !needsMigration;
  const routeDone = importDone && Boolean(routeValid);
  const appliedDone = Boolean(active && !routeChanged && !needsMigration);
  let blockReason = "ready";
  if (sourceDirty) blockReason = "source-dirty";
  else if (needsMigration) blockReason = "migration";
  else if (!hasNodes) blockReason = "empty";
  else if (needsMoreNodesForChain) blockReason = "need-more-nodes";
  else if (!routeValid) blockReason = "incomplete-route";
  else if (routeChanged || !active) blockReason = "unapplied";
  const currentStep = !importDone ? 1 : !routeDone ? 2 : !appliedDone ? 3 : 4;
  return {
    effectiveChain,
    hasNodes,
    needsMoreNodesForChain,
    importDone,
    routeDone,
    appliedDone,
    currentStep,
    blockReason,
  };
}
