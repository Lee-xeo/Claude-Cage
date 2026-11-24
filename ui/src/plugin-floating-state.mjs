// Pure geometry helpers for draggable floating plugin views.
import { FLOATING_BOUNDS } from "./plugin-view-host.mjs";

export const FLOATING_MARGIN = 12;
export const FLOATING_HEADER_HEIGHT = 36;
export const DEFAULT_FLOATING_WIDTH = 320;
export const DEFAULT_FLOATING_HEIGHT = 260;
export const COLLAPSED_FLOATING_WIDTH = 228;

const finite = (value, fallback) => Number.isFinite(value) ? value : fallback;

export function clampFloatingSize(requested, viewport) {
  const vw = Math.max(FLOATING_BOUNDS.minWidth + FLOATING_MARGIN * 2, finite(viewport?.width, 1280));
  const vh = Math.max(FLOATING_BOUNDS.minHeight + FLOATING_HEADER_HEIGHT + FLOATING_MARGIN * 2, finite(viewport?.height, 800));
  const maxWidth = Math.min(FLOATING_BOUNDS.maxWidth, Math.max(FLOATING_BOUNDS.minWidth, vw - FLOATING_MARGIN * 2));
  const maxHeight = Math.min(FLOATING_BOUNDS.maxHeight, Math.max(FLOATING_BOUNDS.minHeight, vh - FLOATING_HEADER_HEIGHT - FLOATING_MARGIN * 2));
  const width = Math.round(Math.min(maxWidth, Math.max(FLOATING_BOUNDS.minWidth, finite(requested?.width, DEFAULT_FLOATING_WIDTH))));
  const height = Math.round(Math.min(maxHeight, Math.max(FLOATING_BOUNDS.minHeight, finite(requested?.height, DEFAULT_FLOATING_HEIGHT))));
  return { width, height };
}

export function clampFloatingPosition(position, box, viewport) {
  const vw = Math.max(0, finite(viewport?.width, 1280));
  const vh = Math.max(0, finite(viewport?.height, 800));
  const width = Math.max(0, finite(box?.width, DEFAULT_FLOATING_WIDTH));
  const height = Math.max(0, finite(box?.height, DEFAULT_FLOATING_HEIGHT + FLOATING_HEADER_HEIGHT));
  const maxX = Math.max(FLOATING_MARGIN, vw - width - FLOATING_MARGIN);
  const maxY = Math.max(FLOATING_MARGIN, vh - height - FLOATING_MARGIN);
  const x = Math.round(Math.min(maxX, Math.max(FLOATING_MARGIN, finite(position?.x, maxX))));
  const y = Math.round(Math.min(maxY, Math.max(FLOATING_MARGIN, finite(position?.y, FLOATING_MARGIN))));
  return { x, y };
}

export function defaultFloatingPosition(box, viewport, stackIndex = 0) {
  const vw = finite(viewport?.width, 1280);
  const width = finite(box?.width, DEFAULT_FLOATING_WIDTH);
  const offset = Math.max(0, Math.floor(finite(stackIndex, 0)));
  return clampFloatingPosition(
    { x: vw - width - 20 - offset * 20, y: 20 + offset * 28 },
    box,
    viewport,
  );
}
