import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { createWorkbench } from "./workbench-fixture.mjs";

const first = "1111111111111111";
const second = "2222222222222222";
const event = { id: randomUUID(), at: Date.now() / 1000 };
let nextTarget;
const messages = [];
const { browser, page, errors } = await createWorkbench({
  beforeNavigate: async (page, state) => {
    await page.addInitScript(() => { window.cageClaudeCompletionAvailable = true; });
    Object.assign(state.status, { ready: true, vm_running: true });
    state.terminals = [
      { id: first, title: "Claude 1", kind: "claude", workspace: state.settings.workspace },
      { id: second, title: "Claude 2", kind: "claude", workspace: state.settings.workspace, claude_completions: [event] },
    ];
  },
  nativeRequest: body => {
    if (body.action !== "claudeCompletions") return null;
    messages.push(body);
    if (!nextTarget || !body.terminals.includes(nextTarget)) return {};
    const terminal = nextTarget;
    nextTarget = undefined;
    return { terminal };
  },
});

const lane = page.locator(".terminal-lane[data-focused=true]");
const openNotification = () => page.evaluate(() =>
  window.dispatchEvent(new Event("cage-claude-completion-open")));

try {
  await page.getByText("Claude 1", { exact: true }).first().click();
  await lane.getByText("Claude 1", { exact: true }).first().waitFor();
  nextTarget = second;
  await openNotification();
  await lane.getByText("Claude 2", { exact: true }).first().waitFor();
  assert.ok(messages.some(msg => msg.visible.includes(first) && !msg.visible.includes(second)
    && msg.events.some(row => row.id === event.id && row.terminal === second)));

  nextTarget = first;
  await openNotification();
  await lane.getByText("Claude 1", { exact: true }).first().waitFor();
  assert.ok(messages.some(msg => msg.visible.includes(second)));
  assert.deepEqual(errors, []);
  console.log("Completion notifications carry their terminal and click through to the selected pane.");
} finally {
  await browser.close();
}
