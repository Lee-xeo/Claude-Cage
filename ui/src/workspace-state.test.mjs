import test from "node:test";
import assert from "node:assert/strict";
import {
  isClientProjectPath,
  mergeBrowserTabs,
  mergeTabs,
  orderedProjects,
  projectSaveOpens,
  sameState,
  shouldShowBrowserError,
  togglePinnedProject,
} from "./workspace-state.mjs";

test("sameState compares nested JSON values structurally without false negatives", () => {
  assert.equal(sameState(null, null), true);
  assert.equal(sameState(null, {}), false);
  assert.equal(sameState({ a: 1, b: [2, { c: "x" }] }, { b: [2, { c: "x" }], a: 1 }), true);
  assert.equal(sameState({ a: 1, b: [2, { c: "x" }] }, { a: 1, b: [2, { c: "y" }] }), false);
  assert.equal(sameState({ a: 1 }, { a: 1, b: undefined }), false);
  assert.equal(sameState([], {}), false);
});

test("mergeTabs preserves previous array and item references when unchanged", () => {
  const previous = [
    { id: "b1", kind: "web", title: "Home", url: "https://example.com" },
    { id: "terminal:t1", kind: "terminal", title: "Claude", closed: false },
  ];
  const unchanged = mergeTabs(previous, [
    { id: "terminal:t1", kind: "terminal", title: "Claude", closed: false },
    { id: "b1", kind: "web", title: "Home", url: "https://example.com" },
  ]);
  assert.equal(unchanged, previous);

  const updated = mergeTabs(previous, [
    { id: "b1", kind: "web", title: "Home", url: "https://example.com" },
    { id: "terminal:t1", kind: "terminal", title: "Claude 2", closed: false },
    { id: "b2", kind: "web", title: "New", url: "https://example.org" },
  ]);
  assert.notEqual(updated, previous);
  assert.equal(updated[0], previous[0], "unchanged tab keeps previous object identity");
  assert.equal(updated[1].title, "Claude 2");
  assert.equal(updated[2].id, "b2");
});

test("mergeBrowserTabs preserves terminals and avoids replacing unchanged browser tabs", () => {
  const previous = [
    { id: "b1", kind: "web", title: "Docs" },
    { id: "terminal:t1", kind: "terminal", title: "Shell" },
  ];
  const unchanged = mergeBrowserTabs(previous, [{ id: "b1", kind: "web", title: "Docs" }]);
  assert.equal(unchanged, previous);

  const removed = mergeBrowserTabs(previous, []);
  assert.notEqual(removed, previous);
  assert.deepEqual(removed, [previous[1]]);
  assert.equal(removed[0], previous[1]);
});

test("shouldShowBrowserError hides transient browser errors when the browser view is closed", () => {
  const base = {
    browserError: "读取浏览器标签页失败：连接在返回完整结果前中断。",
    browserVisible: true,
    managementPage: "",
    running: true,
    connectionError: "",
  };
  assert.equal(shouldShowBrowserError(base), true);
  assert.equal(shouldShowBrowserError({ ...base, browserVisible: false }), false);
  assert.equal(shouldShowBrowserError({ ...base, managementPage: "plugins" }), false);
  assert.equal(shouldShowBrowserError({ ...base, running: false }), false);
  assert.equal(shouldShowBrowserError({ ...base, connectionError: "连接中断" }), false);
  assert.equal(shouldShowBrowserError({ ...base, browserError: "" }), false);
});

test("orderedProjects places pinned projects first and respects saved order within each group", () => {
  const projects = ["/p/a", "/p/b", "/p/c", "/p/d"];
  assert.deepEqual(orderedProjects(projects, [], ["/p/c"]), ["/p/c", "/p/a", "/p/b", "/p/d"]);
  assert.deepEqual(
    orderedProjects(projects, ["/p/d", "/p/b", "/p/c", "/p/a"], ["/p/a", "/p/c"]),
    ["/p/c", "/p/a", "/p/d", "/p/b"],
  );
  assert.deepEqual(togglePinnedProject(["/p/a"], "/p/b"), ["/p/a", "/p/b"]);
  assert.deepEqual(togglePinnedProject(["/p/a", "/p/b"], "/p/a"), ["/p/b"]);
  assert.equal(isClientProjectPath("/run/cage-client-projects/0123456789abcdef0123456789abcdef"), true);
  assert.equal(isClientProjectPath("/projects/alpha"), false);
});

test("projectSaveOpens skips activation only for a direct sidebar edit", () => {
  assert.equal(projectSaveOpens("edit", "/p/b"), false);
  assert.equal(projectSaveOpens("browse", "/p/b"), true);
  assert.equal(projectSaveOpens("browse", ""), true);
  assert.equal(projectSaveOpens("edit", ""), true);
});
