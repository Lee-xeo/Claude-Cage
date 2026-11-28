export const sessionKey = tab => tab.cli_session_id ? `session:${tab.cli_session_id}` : tab.id;

// Mirrors the host limit, which counts only terminals whose process is still running.
export const TERMINAL_LIMIT = 12;
export const liveTerminalCount = tabs => tabs.filter(tab => tab.kind === "terminal" && !tab.closed).length;

export function unassignedTerminalTabs(tabs, projects, projectGroups = []) {
  const folders = new Set(projects.flatMap(path => projectGroups.find(group => group.workspace === path)?.folders || [path]));
  return tabs.filter(tab => tab.kind === "terminal" && !folders.has(tab.workspace));
}

export const SESSION_PREVIEW_LIMIT = 5;

// A collapsed project shows its first rows, plus the open one wherever it sits:
// a finished turn drops its row back to its first-seen place, and the terminal
// a notification opened must still be findable in its project.
export function previewSessionRows(rows, active, showAll, limit = SESSION_PREVIEW_LIMIT) {
  return showAll ? rows : rows.filter((row, index) => index < limit || (active && row.id === active));
}

export function projectSessionRows(tabs, sessions, saved, manualOrder, baseline) {
  const live = tabs.filter(tab => !tab.closed || !sessions.some(session => session.id === tab.cli_session_id));
  const rows = [
    ...sessions.map(session => live.find(tab => tab.cli_session_id === session.id) || ({
      id: `session:${session.id}`, kind: "terminal", workspace: session.workspace,
      title: session.title, cli_session_id: session.id, historySession: session,
      claudeAccountNumber: session.claudeAccountNumber,
    })),
    ...live.filter(tab => !sessions.some(session => session.id === tab.cli_session_id)),
  ].map(tab => {
    const session = sessions.find(session => session.id === tab.cli_session_id);
    return { ...tab, claudeAccountNumber: session?.account_id ? session.claudeAccountNumber : tab.claudeAccountNumber,
      sessionActive: Boolean(session?.active),
      sessionPaused: Boolean(session?.paused),
      sessionRunningElsewhere: Boolean(tab.historySession && session?.running && !session.paused),
      sessionClaudeDesktop: Boolean(tab.historySession && session?.running && session?.claude_desktop),
      sessionPinned: Boolean(session ? session.pinned : saved[tab.id]?.pinned) };
  });
  for (const row of rows) if (!baseline.has(sessionKey(row))) baseline.set(sessionKey(row), baseline.size);
  return rows.sort((a, b) => {
    const rank = row => manualOrder?.includes(sessionKey(row)) ? manualOrder.indexOf(sessionKey(row)) : manualOrder?.length;
    if (manualOrder) return rank(a) - rank(b) || baseline.get(sessionKey(a)) - baseline.get(sessionKey(b));
    return Number(b.sessionActive) - Number(a.sessionActive) || Number(b.sessionPinned) - Number(a.sessionPinned) ||
      baseline.get(sessionKey(a)) - baseline.get(sessionKey(b));
  });
}
