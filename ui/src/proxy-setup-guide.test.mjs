import assert from "node:assert/strict";
import test from "node:test";
import {
  emptyFormDefaults,
  emptyManualDefaults,
  isSourceFormDirty,
  nextPickerHopAfterPick,
  resolvePickerHop,
  setupGuideProgress,
  singleNodeFromRoute,
  swapTwoHopRoute,
} from "./proxy-setup-guide.mjs";

test("isSourceFormDirty ignores untouched new and edit forms while catching real edits", () => {
  const sources = [{ id: "src-1", name: "测试来源", interval_hours: 24, remote: true }];
  assert.equal(isSourceFormDirty(null, "url", emptyManualDefaults, sources), false);
  assert.equal(isSourceFormDirty({ ...emptyFormDefaults }, "url", emptyManualDefaults, sources), false);
  assert.equal(isSourceFormDirty({ ...emptyFormDefaults }, "file", emptyManualDefaults, sources), false);
  assert.equal(isSourceFormDirty({ ...emptyFormDefaults }, "manual", emptyManualDefaults, sources), false);
  assert.equal(
    isSourceFormDirty({ ...emptyFormDefaults, name: "待导入的来源" }, "url", emptyManualDefaults, sources),
    true,
  );
  assert.equal(
    isSourceFormDirty({ ...emptyFormDefaults, host_download: true }, "url", emptyManualDefaults, sources),
    true,
  );
  assert.equal(
    isSourceFormDirty({ ...emptyFormDefaults, content: "proxies: []" }, "file", emptyManualDefaults, sources),
    true,
  );
  assert.equal(
    isSourceFormDirty({ ...emptyFormDefaults }, "manual", { ...emptyManualDefaults, server: "1.2.3.4" }, sources),
    true,
  );
  assert.equal(
    isSourceFormDirty({ ...emptyFormDefaults, id: "src-1", name: "测试来源", interval_hours: 24, remote: true }, "url", emptyManualDefaults, sources),
    false,
  );
  assert.equal(
    isSourceFormDirty({ ...emptyFormDefaults, id: "src-1", name: "修改后的来源", interval_hours: 24, remote: true }, "url", emptyManualDefaults, sources),
    true,
  );
  assert.equal(
    isSourceFormDirty({ ...emptyFormDefaults, id: "src-1", name: "测试来源", interval_hours: 24, url: "https://example.com/sub" }, "url", emptyManualDefaults, sources),
    true,
  );
});

test("resolvePickerHop and nextPickerHopAfterPick smoothly guide transit -> exit selection", () => {
  assert.equal(resolvePickerHop([""], null), 0);
  assert.equal(resolvePickerHop(["a"], 5), 0);

  // Chain with empty transit targets transit first (0).
  assert.equal(resolvePickerHop(["", ""], null), 0);
  assert.equal(resolvePickerHop(["", "exit"], null), 0);
  // Picking transit advances to empty exit (1).
  assert.equal(nextPickerHopAfterPick(["transit", ""], 0), 1);
  assert.equal(resolvePickerHop(["transit", ""], null), 1);
  // Picking exit when all hops are filled clears override and settles on exit.
  assert.equal(nextPickerHopAfterPick(["transit", "exit"], 1), null);
  assert.equal(resolvePickerHop(["transit", "exit"], null), 1);
  // Explicit user selection on hop 0 is respected even when all hops are filled.
  assert.equal(resolvePickerHop(["transit", "exit"], 0), 0);
  // Picking exit first while transit is still empty loops back to transit (0).
  assert.equal(nextPickerHopAfterPick(["", "exit"], 1), 0);
  // 3-hop chain advances sequentially through empty hops.
  assert.equal(nextPickerHopAfterPick(["hop1", "", "exit"], 0), 1);
  // Invalid/removed node IDs are treated as empty when usableNodeIDs is given.
  const usable = new Set(["a", "b"]);
  assert.equal(resolvePickerHop(["removed", "b"], null, usable), 0);
});

test("swapTwoHopRoute and singleNodeFromRoute preserve user intent cleanly", () => {
  assert.deepEqual(swapTwoHopRoute(["exit", "relay"]), ["relay", "exit"]);
  assert.deepEqual(swapTwoHopRoute(["", "relay"]), ["relay", ""]);
  assert.deepEqual(swapTwoHopRoute(["a", "b", "c"]), ["a", "b", "c"]);

  assert.deepEqual(singleNodeFromRoute(["relay", "exit"]), ["exit"]);
  assert.deepEqual(singleNodeFromRoute(["relay", ""]), ["relay"]);
  assert.deepEqual(singleNodeFromRoute(["", ""]), [""]);
});

test("setupGuideProgress reports accurate checklist progression and block reasons", () => {
  assert.deepEqual(
    setupGuideProgress({ usableNodeCount: 0, route: [""], chainMode: false }),
    {
      effectiveChain: false,
      hasNodes: false,
      needsMoreNodesForChain: false,
      importDone: false,
      routeDone: false,
      appliedDone: false,
      currentStep: 1,
      blockReason: "empty",
    },
  );
  assert.equal(
    setupGuideProgress({ usableNodeCount: 1, route: ["", ""], chainMode: true }).blockReason,
    "need-more-nodes",
  );
  assert.equal(
    setupGuideProgress({ usableNodeCount: 2, route: ["relay", ""], routeValid: false, chainMode: true }).blockReason,
    "incomplete-route",
  );
  assert.equal(
    setupGuideProgress({ usableNodeCount: 2, route: ["relay", "exit"], routeValid: true, routeChanged: true, active: false, chainMode: true }).blockReason,
    "unapplied",
  );
  assert.equal(
    setupGuideProgress({ usableNodeCount: 2, route: ["relay", "exit"], routeValid: true, routeChanged: false, active: true, sourceDirty: true, chainMode: true }).blockReason,
    "source-dirty",
  );
  assert.deepEqual(
    setupGuideProgress({ usableNodeCount: 2, route: ["relay", "exit"], routeValid: true, routeChanged: false, active: true, chainMode: true }),
    {
      effectiveChain: true,
      hasNodes: true,
      needsMoreNodesForChain: false,
      importDone: true,
      routeDone: true,
      appliedDone: true,
      currentStep: 4,
      blockReason: "ready",
    },
  );
});
