export const DEFAULT_SIDEBAR_WIDTH = 272;
export const MIN_SIDEBAR_WIDTH = 216;
export const MAX_SIDEBAR_WIDTH = 520;
export const SIDEBAR_WIDTH_STEP = 16;

export function maxSidebarWidth(viewportWidth) {
  if (!Number.isFinite(viewportWidth) || viewportWidth <= 0) return MAX_SIDEBAR_WIDTH;
  const half = Math.floor(viewportWidth * 0.48);
  return Math.max(MIN_SIDEBAR_WIDTH, Math.min(MAX_SIDEBAR_WIDTH, half));
}

export function clampSidebarWidth(value, viewportWidth) {
  const num = Number(value);
  if (!Number.isFinite(num) || num <= 0) return DEFAULT_SIDEBAR_WIDTH;
  const max = maxSidebarWidth(viewportWidth);
  return Math.round(Math.max(MIN_SIDEBAR_WIDTH, Math.min(max, num)));
}

export function nextSidebarWidth(startWidth, deltaX, rtl, viewportWidth) {
  const signedDelta = rtl ? -deltaX : deltaX;
  return clampSidebarWidth(startWidth + signedDelta, viewportWidth);
}
