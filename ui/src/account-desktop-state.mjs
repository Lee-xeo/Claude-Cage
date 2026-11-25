export const desktopTitle = (a) =>
  a.number ? `#${String(a.number).padStart(2, "0")} ${a.label}` : a.label;

export function hasDefaultDesktop(catalog) {
  if (!catalog) return true;
  return !catalog.default_removed && !catalog.redirects?.default;
}

export function visibleDesktopCount(catalog) {
  const instances = catalog?.instances ?? catalog?.accounts ?? [];
  const others = instances.filter((item) => item.id !== "default");
  return (hasDefaultDesktop(catalog) ? 1 : 0) + others.length;
}

export function canDeleteDesktop(catalog) {
  return Boolean(catalog?.supported) && visibleDesktopCount(catalog) > 1;
}

export function mergedDefaultInstance(catalog) {
  const target = catalog?.redirects?.default;
  if (!target || !Array.isArray(catalog?.instances)) return null;
  return catalog.instances.find((item) => item.id === target) || null;
}

export function fallbackDesktopInstance(catalog) {
  const merged = mergedDefaultInstance(catalog);
  if (merged) return merged;
  if (!catalog?.default_removed || !Array.isArray(catalog?.instances)) return null;
  const others = catalog.instances.filter((item) => item.id !== "default");
  return others.find((item) => item.running) || others[0] || null;
}

export function reconcileAccountDesktopPanes(oldPanes, catalog, viewingDefault = false) {
  if (!catalog?.instances) return oldPanes;
  if (!oldPanes.length) {
    if (!viewingDefault) return oldPanes;
    const merged = mergedDefaultInstance(catalog);
    return merged && merged.id !== "default" ? [merged] : oldPanes;
  }
  if (oldPanes.some((p) => catalog.redirects?.[p.id] === "default")) return [];
  const seen = new Set();
  const updated = [];
  for (const pane of oldPanes) {
    const id = catalog.redirects?.[pane.id] || pane.id;
    const item = catalog.instances.find((a) => a.id === id);
    if (!item || seen.has(id) || id === "default") continue;
    seen.add(id);
    updated.push(item);
  }
  return updated;
}

export function desktopClipboardState({ ready, focused, visible, windowVisible, native }) {
  const connected = Boolean(ready && focused && native);
  return {
    connected,
    pollingVisible: Boolean(connected && visible && windowVisible),
  };
}

export function desktopMergeNoticeTarget(previousRedirect, catalog) {
  const nextRedirect = catalog?.redirects?.default || "";
  if (!nextRedirect || nextRedirect === previousRedirect) return null;
  return mergedDefaultInstance(catalog);
}
