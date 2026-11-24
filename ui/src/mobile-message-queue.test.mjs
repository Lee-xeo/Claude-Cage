import test from "node:test";
import assert from "node:assert/strict";
import {
  activeTurnIdForQueue,
  canDrainQueue,
  canSendImmediately,
  dequeueMessage,
  enqueueMessage,
  reconcileDispatchError,
  reconcileQueueWithSnapshot,
  removeQueuedMessage,
  requeueAtFront,
  shouldPauseQueueAfterTurn,
} from "./mobile-cli/message-queue.mjs";

const idleState = () => ({
  visible: true,
  ready: true,
  session: { can_send: true, single_turn: false },
  running: false,
  busy: false,
  confirming: false,
  uncertain: false,
  readProgress: "",
  readError: "",
  waitingSync: false,
  queueLength: 0,
  queuePaused: false,
});

test("canSendImmediately requires an idle, synced, unpaused empty queue", () => {
  assert.equal(canSendImmediately(idleState()), true);
  assert.equal(canSendImmediately({ ...idleState(), running: true }), false);
  assert.equal(canSendImmediately({ ...idleState(), busy: true }), false);
  assert.equal(canSendImmediately({ ...idleState(), uncertain: true }), false);
  assert.equal(canSendImmediately({ ...idleState(), waitingSync: true }), false);
  assert.equal(canSendImmediately({ ...idleState(), queueLength: 1 }), false);
  assert.equal(canSendImmediately({ ...idleState(), queuePaused: true }), false);
});

test("canDrainQueue drains FIFO only when visible, ready, idle, and unpaused", () => {
  const readyToDrain = { ...idleState(), queueLength: 2 };
  assert.equal(canDrainQueue(readyToDrain), true);
  assert.equal(canDrainQueue({ ...readyToDrain, visible: false }), false);
  assert.equal(canDrainQueue({ ...readyToDrain, ready: false }), false);
  assert.equal(canDrainQueue({ ...readyToDrain, running: true }), false);
  assert.equal(canDrainQueue({ ...readyToDrain, queuePaused: true }), false);
  assert.equal(canDrainQueue({ ...readyToDrain, waitingSync: true }), false);
  assert.equal(canDrainQueue({ ...readyToDrain, session: { can_send: false } }), false);
  assert.equal(canDrainQueue({ ...readyToDrain, session: { single_turn: true } }), false);
});

test("queue operations preserve FIFO ordering and avoid duplicate request IDs", () => {
  const m1 = { request_id: "r1", text: "first" };
  const m2 = { request_id: "r2", text: "second" };
  let q = [];
  q = enqueueMessage(q, m1);
  q = enqueueMessage(q, m2);
  q = enqueueMessage(q, m1);
  assert.deepEqual(q, [m1, m2]);

  const { head, rest } = dequeueMessage(q);
  assert.deepEqual(head, m1);
  assert.deepEqual(rest, [m2]);

  const requeued = requeueAtFront(rest, m1);
  assert.deepEqual(requeued, [m1, m2]);

  const afterRemove = removeQueuedMessage(requeued, "r1");
  assert.deepEqual(afterRemove, [m2]);
});

test("shouldPauseQueueAfterTurn pauses queued messages when an active turn stops or fails", () => {
  assert.equal(shouldPauseQueueAfterTurn({ id: "t1", status: "running" }, { id: "t1", status: "completed" }, 1), false);
  assert.equal(shouldPauseQueueAfterTurn({ id: "t1", status: "running" }, { id: "t1", status: "stopped" }, 1), true);
  assert.equal(shouldPauseQueueAfterTurn({ id: "t1", status: "running" }, { id: "t1", status: "failed" }, 1), true);
  assert.equal(shouldPauseQueueAfterTurn({ id: "t1", status: "stopping" }, { id: "t1", status: "interrupted" }, 2), true);
  assert.equal(shouldPauseQueueAfterTurn({ id: "t1", status: "stopped" }, { id: "t1", status: "stopped" }, 1), false);
  assert.equal(shouldPauseQueueAfterTurn({ id: "t1", status: "running" }, { id: "t1", status: "stopped" }, 0), false);
});

test("reconcileDispatchError re-queues on first 409 and waits for sync, pausing on repeated 409", () => {
  const m1 = { request_id: "r1", text: "first" };
  const m2 = { request_id: "r2", text: "second" };

  const first = reconcileDispatchError({
    error: Object.assign(new Error("busy"), { status: 409 }),
    item: m1,
    draft: "",
    queue: [m2],
    previousConflictId: null,
  });
  assert.deepEqual(first.queue, [m1, m2]);
  assert.equal(first.waitingSync, true);
  assert.equal(first.paused, false);
  assert.equal(first.conflictId, "r1");
  assert.equal(first.reload, true);

  const repeated = reconcileDispatchError({
    error: Object.assign(new Error("busy"), { status: 409 }),
    item: m1,
    draft: "",
    queue: [m2],
    previousConflictId: "r1",
  });
  assert.deepEqual(repeated.queue, [m1, m2]);
  assert.equal(repeated.waitingSync, false);
  assert.equal(repeated.paused, true);
  assert.equal(repeated.actionError, "busy");
});

test("reconcileDispatchError preserves order and never drops messages on 4xx errors", () => {
  const m1 = { request_id: "r1", text: "first" };
  const m2 = { request_id: "r2", text: "second" };

  const withRemainingQueue = reconcileDispatchError({
    error: Object.assign(new Error("invalid"), { status: 400 }),
    item: m1,
    draft: "",
    queue: [m2],
    previousConflictId: null,
  });
  assert.deepEqual(withRemainingQueue.queue, [m1, m2]);
  assert.equal(withRemainingQueue.paused, true);
  assert.equal(withRemainingQueue.draft, "");
  assert.equal(withRemainingQueue.actionError, "invalid");

  const withDraftOnly = reconcileDispatchError({
    error: Object.assign(new Error("invalid"), { status: 400 }),
    item: m1,
    draft: "typing next",
    queue: [],
    previousConflictId: null,
  });
  assert.deepEqual(withDraftOnly.queue, [m1]);
  assert.equal(withDraftOnly.paused, true);
  assert.equal(withDraftOnly.draft, "typing next");

  const standalone = reconcileDispatchError({
    error: Object.assign(new Error("invalid"), { status: 400 }),
    item: m1,
    draft: "",
    queue: [],
    previousConflictId: null,
  });
  assert.deepEqual(standalone.queue, []);
  assert.equal(standalone.paused, false);
  assert.equal(standalone.draft, "first");
});

test("reconcileDispatchError marks network and 5xx failures as uncertain without dropping queue", () => {
  const m1 = { request_id: "r1", text: "first" };
  const m2 = { request_id: "r2", text: "second" };
  const netErr = reconcileDispatchError({
    error: new Error("offline"),
    item: m1,
    draft: "draft",
    queue: [m2],
    previousConflictId: null,
  });
  assert.equal(netErr.keepPending, true);
  assert.equal(netErr.uncertain, true);
  assert.deepEqual(netErr.queue, [m2]);
  assert.equal(netErr.draft, "draft");
  assert.equal(netErr.reload, true);
});

test("activeTurnIdForQueue prefers the active turn and falls back to pending submission", () => {
  assert.equal(activeTurnIdForQueue({ turns: [{ id: "t1", status: "running" }] }, { request_id: "r1" }), "t1");
  assert.equal(activeTurnIdForQueue({ turns: [{ id: "t1", status: "completed" }] }, { request_id: "r1" }), "r1");
  assert.equal(activeTurnIdForQueue({ turns: [{ id: "t1", status: "completed" }] }, null), null);
});

test("reconcileQueueWithSnapshot pauses queued messages after switching back to an abnormally ended turn", () => {
  for (const status of ["failed", "stopped", "interrupted"]) {
    const result = reconcileQueueWithSnapshot({
      prevTurn: null,
      snapshot: { turns: [{ id: "t1", status }] },
      queueLength: 1,
      paused: false,
      waitingTurnId: "t1",
      pendingRequestId: null,
    });
    assert.deepEqual(result, { paused: true, waitingTurnId: null });
  }

  const failedBeforeLaterTurn = reconcileQueueWithSnapshot({
    prevTurn: null,
    snapshot: { turns: [{ id: "t1", status: "failed" }, { id: "t2", status: "completed" }] },
    queueLength: 1,
    paused: false,
    waitingTurnId: "t1",
    pendingRequestId: null,
  });
  assert.deepEqual(failedBeforeLaterTurn, { paused: true, waitingTurnId: null });

  const truncatedAbnormal = reconcileQueueWithSnapshot({
    prevTurn: null,
    snapshot: { turns: [{ id: "t9", status: "interrupted" }] },
    queueLength: 1,
    paused: false,
    waitingTurnId: "t1",
    pendingRequestId: null,
  });
  assert.deepEqual(truncatedAbnormal, { paused: true, waitingTurnId: null });
});

test("reconcileQueueWithSnapshot resumes cleanly after completed turn and tracks active turns", () => {
  const completed = reconcileQueueWithSnapshot({
    prevTurn: null,
    snapshot: { turns: [{ id: "t1", status: "completed" }] },
    queueLength: 2,
    paused: false,
    waitingTurnId: "t1",
    pendingRequestId: null,
  });
  assert.deepEqual(completed, { paused: false, waitingTurnId: null });

  const stillRunning = reconcileQueueWithSnapshot({
    prevTurn: null,
    snapshot: { turns: [{ id: "t1", status: "running" }] },
    queueLength: 1,
    paused: false,
    waitingTurnId: "t1",
    pendingRequestId: null,
  });
  assert.deepEqual(stillRunning, { paused: false, waitingTurnId: "t1" });

  const completedThenDesktopRunning = reconcileQueueWithSnapshot({
    prevTurn: null,
    snapshot: { turns: [{ id: "t1", status: "completed" }, { id: "t2", status: "running" }] },
    queueLength: 1,
    paused: false,
    waitingTurnId: "t1",
    pendingRequestId: null,
  });
  assert.deepEqual(completedThenDesktopRunning, { paused: false, waitingTurnId: "t2" });

  const pendingNotYetAdmitted = reconcileQueueWithSnapshot({
    prevTurn: null,
    snapshot: { turns: [{ id: "t0", status: "completed" }] },
    queueLength: 1,
    paused: false,
    waitingTurnId: "r1",
    pendingRequestId: "r1",
  });
  assert.deepEqual(pendingNotYetAdmitted, { paused: false, waitingTurnId: "r1" });

  const discoveredRunningAfterConflict = reconcileQueueWithSnapshot({
    prevTurn: null,
    snapshot: { turns: [{ id: "t2", status: "running" }] },
    queueLength: 1,
    paused: false,
    waitingTurnId: null,
    pendingRequestId: null,
  });
  assert.deepEqual(discoveredRunningAfterConflict, { paused: false, waitingTurnId: "t2" });
});
