export const activeTurn = (status) => status === "running" || status === "stopping" || status === "queued";
export const abnormalTurn = (status) => status === "stopped" || status === "failed" || status === "interrupted";

export function activeTurnIdForQueue(snapshot, pendingSubmission) {
  const last = snapshot?.turns?.at(-1);
  if (activeTurn(last?.status) && last?.id) return last.id;
  if (pendingSubmission?.request_id) return pendingSubmission.request_id;
  return null;
}

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

export function shouldRetryOnQueueAction({
  readError,
  connectionError,
  uncertain,
  confirming,
  queuePaused = false,
}) {
  if (queuePaused || confirming) return false;
  return Boolean(readError || connectionError || uncertain);
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
  return abnormalTurn(nextTurn.status);
}

export function reconcileQueueWithSnapshot({
  prevTurn,
  snapshot,
  queueLength,
  paused,
  waitingTurnId,
  pendingRequestId,
}) {
  if (!queueLength) {
    return { paused: false, waitingTurnId: null };
  }
  if (paused) {
    return { paused: true, waitingTurnId: null };
  }
  const turns = snapshot?.turns || [];
  const last = turns.at(-1) || null;
  if (shouldPauseQueueAfterTurn(prevTurn, last, queueLength)) {
    return { paused: true, waitingTurnId: null };
  }
  if (waitingTurnId) {
    const awaited = turns.find((turn) => turn.id === waitingTurnId);
    if (awaited) {
      if (abnormalTurn(awaited.status)) {
        return { paused: true, waitingTurnId: null };
      }
      if (activeTurn(awaited.status)) {
        return { paused: false, waitingTurnId };
      }
      return {
        paused: false,
        waitingTurnId: activeTurn(last?.status) && last?.id ? last.id : (pendingRequestId || null),
      };
    }
    if (pendingRequestId && waitingTurnId === pendingRequestId) {
      return { paused: false, waitingTurnId };
    }
    if (abnormalTurn(last?.status)) {
      return { paused: true, waitingTurnId: null };
    }
    if (activeTurn(last?.status) && last?.id) {
      return { paused: false, waitingTurnId: last.id };
    }
    return { paused: false, waitingTurnId: null };
  }
  if (activeTurn(last?.status) && last?.id) {
    return { paused: false, waitingTurnId: last.id };
  }
  if (pendingRequestId) {
    return { paused: false, waitingTurnId: pendingRequestId };
  }
  return { paused: false, waitingTurnId: null };
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
