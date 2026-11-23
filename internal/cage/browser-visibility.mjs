export function rememberOpener(target, previous) {
  // Navigation can omit openerId. Keep its UI return target without restoring
  // page-to-page access or changing Chromium's cross-origin opener policy.
  if (target.openerId || !previous?.openerId || target.targetId !== previous.targetId ||
      target.browserContextId !== previous.browserContextId) return target;
  return { ...target, openerId: previous.openerId };
}

export function targetAfterClose(targets, closed) {
  const opener = targets.get(closed?.openerId);
  if (opener && opener.targetId !== closed.targetId &&
      opener.browserContextId === closed.browserContextId) return opener.targetId;
  return targets.keys().next().value || "";
}

// Chromium must observe a minimized window to throttle its hidden pages.
// Withdrawing an X window alone leaves document.visibilityState visible.
// Visible guest browser windows always stay fullscreen so Chromium's own
// tab strip and address bar never appear inside the workbench browser frame.
export class BrowserVisibility {
  constructor(cdp, targets, canHide = async () => true, activeTarget = () => "") {
    this.cdp = cdp;
    this.targets = targets;
    this.canHide = canHide;
    this.activeTarget = activeTarget;
    this.visible = true;
    this.queue = Promise.resolve();
  }

  set(visible) {
    if (typeof visible !== "boolean") throw new Error("Invalid browser visibility");
    this.visible = visible;
    return this.sync();
  }

  sync() {
    const result = this.queue.catch(() => {}).then(() => this.apply());
    this.queue = result;
    return result;
  }

  activate(targetId) {
    const result = this.queue.catch(() => {}).then(async () => {
      await this.cdp("Target.activateTarget", { targetId });
      if (this.visible || !await this.canHide()) {
        const { windowId, bounds } = await this.cdp("Browser.getWindowForTarget", { targetId });
        await this.ensureFullscreen(windowId, bounds);
      } else {
        await this.apply();
      }
    });
    this.queue = result;
    return result;
  }

  async ensureFullscreen(windowId, bounds) {
    if (bounds.windowState === "fullscreen") return false;
    if (bounds.windowState === "minimized") {
      await this.setBounds(windowId, { windowState: "normal" }, { allowMaximized: true });
    }
    await this.setBounds(windowId, { windowState: "fullscreen" });
    return true;
  }

  async setBounds(windowId, bounds, { allowMaximized = false } = {}) {
    await this.cdp("Browser.setWindowBounds", { windowId, bounds });
    // The CDP reply can precede the window manager's state transition.
    // Fullscreen/minimized changes must observe the intermediate state, which
    // Openbox may report as maximized instead of normal.
    for (let attempt = 0; attempt < 80; attempt++) {
      const current = await this.cdp("Browser.getWindowBounds", { windowId });
      const state = current.bounds.windowState;
      if (state === bounds.windowState || (allowMaximized && state === "maximized")) return;
      await new Promise(resolve => setTimeout(resolve, 25));
    }
    throw new Error("Browser window did not change state");
  }

  async apply() {
    const visible = this.visible || !await this.canHide();
    const windows = new Map();
    for (const targetId of this.targets()) {
      try {
        const window = await this.cdp("Browser.getWindowForTarget", { targetId });
        windows.set(window.windowId, window.bounds);
      } catch (error) {
        if (this.targets().includes(targetId)) throw error;
      }
    }
    let unminimized = false;
    for (const [windowId, bounds] of windows) {
      if (visible) {
        if (bounds.windowState === "minimized") unminimized = true;
        await this.ensureFullscreen(windowId, bounds);
      } else if (bounds.windowState !== "minimized") {
        if (bounds.windowState !== "normal") {
          await this.setBounds(windowId, { windowState: "normal" }, { allowMaximized: true });
        }
        await this.setBounds(windowId, { windowState: "minimized" });
      }
    }
    if (visible && unminimized && windows.size > 1) {
      const active = this.activeTarget();
      if (active && this.targets().includes(active)) {
        await this.cdp("Target.activateTarget", { targetId: active }).catch(() => {});
      }
    }
  }
}
