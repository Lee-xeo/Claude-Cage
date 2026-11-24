export const activeTurn = (status) => status === "running" || status === "stopping" || status === "queued";

export function canSendImmediately({
  running,
  busy,
  confirming,
  uncertain,
  readProgress,
  readError,
  waitingSync,
  queueLength,
  queuePaused,
}) {
  return Boolean(
    !running &&
    !busy &&
    !confirming &&
    !uncertain &&
    !readProgress &&
    !readError &&
    !waitingSync &&
    queueLength === 0 &&
    !queuePaused
  );
}

export function canDrainQueue({
  visible,
  ready,
  session,
  running,
  busy,
  confirming,
  uncertain,
  readProgress,
  readError,
  waitingSync,
  queueLength,
  queuePaused,
}) {
  return Boolean(
    visible &&
    ready &&
    session &&
    !session.single_turn &&
    session.can_send !== false &&
    !running &&
    !busy &&
    !confirming &&
    !uncertain &&
    !readProgress &&
    !readError &&
    !waitingSync &&
    !queuePaused &&
    queueLength > 0
  );
}

export function enqueueMessage(queue, item) {
  if (!item?.request_id || queue.some((entry) => entry.request_id === item.request_id)) return queue;
  return [...queue, item];
}

export function requeueAtFront(queue, item) {
  if (!item?.request_id) return queue;
  return [item, ...queue.filter((entry) => entry.request_id !== item.request_id)];
}

export function dequeueMessage(queue) {
  if (!queue.length) return { head: null, rest: queue };
  const [head, ...rest] = queue;
  return { head, rest };
}

export function removeQueuedMessage(queue, requestId) {
  return queue.filter((entry) => entry.request_id !== requestId);
}

export function shouldPauseQueueAfterTurn(prevTurn, nextTurn, queueLength) {
  if (!queueLength || !prevTurn || !nextTurn) return false;
  if (!activeTurn(prevTurn.status)) return false;
  return ["stopped", "failed", "interrupted"].includes(nextTurn.status);
}

export function reconcileDispatchError({ error, item, draft, queue, previousConflictId }) {
  if (error?.status === 409) {
    const nextQueue = requeueAtFront(queue, item);
    const repeated = previousConflictId === item.request_id;
    return {
      queue: nextQueue,
      draft,
      keepPending: false,
      uncertain: false,
      paused: repeated,
      waitingSync: !repeated,
      conflictId: repeated ? null : item.request_id,
      actionError: repeated ? error.message || "当前会话暂无法发送排队消息，已暂停发送。" : "",
      reload: true,
    };
  }
  const unknown = !error?.status || error.status >= 500;
  if (unknown) {
    return {
      queue,
      draft,
      keepPending: true,
      uncertain: true,
      paused: false,
      waitingSync: false,
      conflictId: null,
      actionError: "",
      reload: true,
    };
  }
  if (queue.length > 0 || draft) {
    return {
      queue: requeueAtFront(queue, item),
      draft,
      keepPending: false,
      uncertain: false,
      paused: true,
      waitingSync: false,
      conflictId: null,
      actionError: error.message || "",
      reload: false,
    };
  }
  return {
    queue,
    draft: item.text,
    keepPending: false,
    uncertain: false,
    paused: false,
    waitingSync: false,
    conflictId: null,
    actionError: error.message || "",
    reload: false,
  };
}
