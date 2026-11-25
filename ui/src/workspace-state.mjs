export function readLocal(key, fallback) {
  try {
    return JSON.parse(localStorage.getItem(key)) ?? fallback;
  } catch {
    return fallback;
  }
}

export function orderedTabs(tabs, saved) {
  const order = saved.order || [];
  return [...tabs].sort((a, b) => {
    const pinned =
      Number(Boolean(saved[b.id]?.pinned)) -
      Number(Boolean(saved[a.id]?.pinned));
    if (pinned) return pinned;
    const index = (id) =>
      order.includes(id) ? order.indexOf(id) : order.length;
    return index(a.id) - index(b.id);
  });
}

export function moveItem(items, from, to, after) {
  const source = items.indexOf(from);
  const target = items.indexOf(to);
  if (source < 0 || target < 0 || source === target) return items;
  const position = target - Number(source < target) + Number(after);
  if (position === source) return items;
  const order = [...items];
  order.splice(source, 1);
  order.splice(position, 0, from);
  return order;
}

export function dropAfter(row, clientY) {
  const { top, height } = row.getBoundingClientRect();
  return clientY > top + height / 2;
}

const clientMount = /^\/run\/cage-client-projects\/[0-9a-f]{32}$/;
const folderName = (path) => path?.split(/[\\/]/).filter(Boolean).at(-1);

export function isClientProjectPath(path) {
  return clientMount.test(path || "");
}

export function orderedProjects(projects = [], projectOrder = [], pinnedProjects = []) {
  const pinned = new Set(pinnedProjects || []);
  const ranks = new Map((projectOrder || []).map((path, index) => [path, index]));
  const rank = (path) => ranks.get(path) ?? ranks.size;
  return [...projects].sort((a, b) => {
    const pinDiff = Number(pinned.has(b)) - Number(pinned.has(a));
    if (pinDiff) return pinDiff;
    return rank(a) - rank(b);
  });
}

export function togglePinnedProject(pinnedProjects = [], path) {
  const current = Array.isArray(pinnedProjects) ? pinnedProjects : [];
  if (!path) return current;
  return current.includes(path)
    ? current.filter((item) => item !== path)
    : [...current, path];
}

export function rememberClientProjectName(share) {
  if (!clientMount.test(share.guest_path)) return;
  try { sessionStorage.setItem(share.guest_path, folderName(share.path)); }
  catch { /* The mount is usable without browser storage. */ }
}

export function projectGroups(status) {
  return status?.project_groups || (status?.projects || []).map(path => ({ workspace: path, name: projectName(path), folders: [path] }));
}

export function projectName(path, groups = [], fallback = "新项目") {
  if (clientMount.test(path)) {
    try { return sessionStorage.getItem(path) || "本机共享项目"; }
    catch { return "本机共享项目"; }
  }
  const group = groups.find(item => item.workspace === path || item.folders.includes(path));
  if (group?.name) return group.name;
  return folderName(path) || fallback;
}

export function tabName(tab, saved, project, localize = value => value) {
  const name = saved[tab.id]?.title || (tab.kind === "editor"
    ? `${localize("文件")} · ${tab.workspace ? localize(projectName(tab.workspace)) : project}`
    : tab.title) || localize("新标签页");
  return tab.incognito ? `${localize("无痕")} · ${name}` : name;
}

// The focused tab and its neighbour retain their screen positions on focus changes.
export function focusTab(layout, id) {
  if (layout.active === id) return layout;
  if (!id) return { active: "" };
  if (layout.other === id)
    return { active: id, other: layout.active, side: layout.side === "left" ? "right" : "left" };
  if (layout.other && id.startsWith("terminal:") !== layout.active.startsWith("terminal:") &&
      id.startsWith("terminal:") === layout.other.startsWith("terminal:"))
    return { ...focusTab(layout, layout.other), active: id };
  return { ...layout, active: id };
}

export function splitTab(layout, tabs, id, side) {
  if (!tabs.some(tab => tab.id === id)) return layout;
  const opposite = layout.side === side ? layout.other : layout.active;
  const other = [opposite, layout.other, layout.active, ...tabs.map(tab => tab.id)]
    .find(candidate => candidate !== id && tabs.some(tab => tab.id === candidate));
  return other ? { active: id, other, side } : layout;
}

export function sameState(a, b) {
  if (Object.is(a, b)) return true;
  if (!a || !b || typeof a !== "object" || typeof b !== "object") return false;
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false;
    for (let i = 0; i < a.length; i++) if (!sameState(a[i], b[i])) return false;
    return true;
  }
  const keysA = Object.keys(a);
  const keysB = Object.keys(b);
  if (keysA.length !== keysB.length) return false;
  for (const key of keysA) {
    if (!Object.prototype.hasOwnProperty.call(b, key) || !sameState(a[key], b[key])) return false;
  }
  return true;
}

export function mergeTabs(previous, incoming) {
  const map = new Map(incoming.map((item) => [item.id, item]));
  const seen = new Set();
  const next = [];
  let changed = false;
  for (const item of previous) {
    const updated = map.get(item.id);
    if (!updated) {
      changed = true;
      continue;
    }
    seen.add(item.id);
    if (sameState(item, updated)) next.push(item);
    else {
      changed = true;
      next.push(updated);
    }
  }
  for (const item of incoming) {
    if (!seen.has(item.id)) {
      changed = true;
      next.push(item);
    }
  }
  return changed ? next : previous;
}

export function mergeBrowserTabs(previous, browserTabs) {
  const map = new Map(browserTabs.map((item) => [item.id, item]));
  const seen = new Set();
  const next = [];
  let changed = false;
  for (const item of previous) {
    if (item.kind === "terminal") {
      seen.add(item.id);
      next.push(item);
      continue;
    }
    const updated = map.get(item.id);
    if (!updated) {
      changed = true;
      continue;
    }
    seen.add(item.id);
    if (sameState(item, updated)) next.push(item);
    else {
      changed = true;
      next.push(updated);
    }
  }
  for (const item of browserTabs) {
    if (!seen.has(item.id)) {
      changed = true;
      next.push(item);
    }
  }
  return changed ? next : previous;
}

export function shouldShowBrowserError({
  browserError,
  browserVisible,
  managementPage,
  running,
  connectionError,
}) {
  return Boolean(
    browserError && browserVisible && !managementPage && running && !connectionError,
  );
}
