import assert from "node:assert/strict";
import test from "node:test";
import {
  canDeleteDesktop,
  desktopClipboardState,
  desktopMergeNoticeTarget,
  desktopTitle,
  fallbackDesktopInstance,
  hasDefaultDesktop,
  mergedDefaultInstance,
  reconcileAccountDesktopPanes,
  visibleDesktopCount,
} from "./account-desktop-state.mjs";

const instanceA = { id: "a".repeat(24), number: 1, label: "工作账号", running: true };
const instanceB = { id: "b".repeat(24), number: 2, label: "备用账号", running: true };

test("isolated desktop clipboard stays connected across window occlusion and pauses polling only", () => {
  const active = desktopClipboardState({
    ready: true,
    focused: true,
    visible: true,
    windowVisible: true,
    native: true,
  });
  assert.deepEqual(active, { connected: true, pollingVisible: true });

  // Switching to another full-screen/occluding macOS window must not tear down the clipboard socket.
  const occluded = desktopClipboardState({
    ready: true,
    focused: true,
    visible: true,
    windowVisible: false,
    native: true,
  });
  assert.deepEqual(occluded, { connected: true, pollingVisible: false });

  // Opening settings pauses polling while keeping the slot warm.
  const settingsOpen = desktopClipboardState({
    ready: true,
    focused: true,
    visible: false,
    windowVisible: true,
    native: true,
  });
  assert.deepEqual(settingsOpen, { connected: true, pollingVisible: false });

  // Losing pane focus or stopping the VM disconnects cleanly.
  assert.deepEqual(
    desktopClipboardState({ ready: true, focused: false, visible: true, windowVisible: true, native: true }),
    { connected: false, pollingVisible: false },
  );
  assert.deepEqual(
    desktopClipboardState({ ready: false, focused: true, visible: true, windowVisible: true, native: true }),
    { connected: false, pollingVisible: false },
  );
});

test("reconcileAccountDesktopPanes switches off retired default Desktop and emits merge notice once", () => {
  const catalog = {
    instances: [instanceA, instanceB],
    redirects: { default: instanceA.id },
  };
  assert.equal(desktopTitle(instanceA), "#01 工作账号");
  assert.deepEqual(mergedDefaultInstance(catalog), instanceA);
  assert.deepEqual(reconcileAccountDesktopPanes([], catalog, true), [instanceA]);
  assert.deepEqual(reconcileAccountDesktopPanes([], catalog, false), []);

  // Redirecting an account pane back to default clears account panes.
  assert.deepEqual(
    reconcileAccountDesktopPanes([instanceB], { instances: [instanceA], redirects: { [instanceB.id]: "default" } }, false),
    [],
  );

  // Merge notice fires only when default redirect changes to a new target.
  assert.deepEqual(desktopMergeNoticeTarget("", catalog), instanceA);
  assert.equal(desktopMergeNoticeTarget(instanceA.id, catalog), null);
});

test("canDeleteDesktop allows deleting any instance including default only while more than one remains", () => {
  const defaultEntry = { id: "default", number: 0, label: "默认 Desktop", running: false };
  const stoppedA = { ...instanceA, running: false };

  // Unloaded or unsupported catalog cannot delete.
  assert.equal(hasDefaultDesktop(null), true);
  assert.equal(visibleDesktopCount(null), 1);
  assert.equal(canDeleteDesktop(null), false);
  assert.equal(canDeleteDesktop({ supported: false, instances: [defaultEntry, instanceA] }), false);

  // Sole remaining default desktop cannot be deleted.
  const onlyDefault = { supported: true, instances: [defaultEntry], redirects: {}, default_removed: false };
  assert.equal(visibleDesktopCount(onlyDefault), 1);
  assert.equal(canDeleteDesktop(onlyDefault), false);
  assert.equal(fallbackDesktopInstance(onlyDefault), null);

  // Default + another desktop: both can be deleted.
  const defaultAndOne = { supported: true, instances: [defaultEntry, instanceA], redirects: {}, default_removed: false };
  assert.equal(visibleDesktopCount(defaultAndOne), 2);
  assert.equal(canDeleteDesktop(defaultAndOne), true);

  // Even before default was ever launched (omitted from instances), default + another = 2 visible.
  const implicitDefaultAndOne = { supported: true, instances: [instanceA], redirects: {} };
  assert.equal(hasDefaultDesktop(implicitDefaultAndOne), true);
  assert.equal(visibleDesktopCount(implicitDefaultAndOne), 2);
  assert.equal(canDeleteDesktop(implicitDefaultAndOne), true);

  // Once default is removed and only one isolated desktop remains, it cannot be deleted.
  const onlyIsolated = { supported: true, instances: [stoppedA], redirects: {}, default_removed: true };
  assert.equal(hasDefaultDesktop(onlyIsolated), false);
  assert.equal(visibleDesktopCount(onlyIsolated), 1);
  assert.equal(canDeleteDesktop(onlyIsolated), false);
  assert.deepEqual(fallbackDesktopInstance(onlyIsolated), stoppedA);

  // With default removed and two isolated desktops, deletion is allowed and fallback prefers running.
  const twoIsolated = { supported: true, instances: [stoppedA, instanceB], redirects: {}, default_removed: true };
  assert.equal(visibleDesktopCount(twoIsolated), 2);
  assert.equal(canDeleteDesktop(twoIsolated), true);
  assert.deepEqual(fallbackDesktopInstance(twoIsolated), instanceB);

  // When default was merged into the sole remaining desktop, deletion is blocked.
  const mergedSole = { supported: true, instances: [instanceA], redirects: { default: instanceA.id } };
  assert.equal(hasDefaultDesktop(mergedSole), false);
  assert.equal(visibleDesktopCount(mergedSole), 1);
  assert.equal(canDeleteDesktop(mergedSole), false);
  assert.deepEqual(fallbackDesktopInstance(mergedSole), instanceA);
});
