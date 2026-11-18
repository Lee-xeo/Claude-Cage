// One capture at a time, with a single pending refresh after user input.
// Account for capture time instead of adding a full idle delay to every frame.
export function pollLoginFrames({ capture, onFrame, onError, inputPending = () => false, interval = 550, inputLimit = 300,
  now = () => performance.now(), setTimer = setTimeout, clearTimer = clearTimeout }) {
  let active = true, running = false, requested = false, timer, held;
  async function poll() {
    timer = undefined;
    if (!active || running) return;
    // Do not put another screenshot ahead of already queued typing/scrolling.
    // A scroll or a held key can keep input queued for seconds, so the wait
    // is bounded: the picture still follows what the input is doing.
    if (inputPending()) {
      held ??= now();
      if (now() - held < inputLimit) { timer = setTimer(poll, 25); return; }
    }
    held = undefined;
    running = true;
    requested = false;
    const started = now();
    try {
      const frame = await capture();
      if (active) onFrame(frame);
    } catch (error) {
      if (active) { active = false; onError(error); }
    } finally {
      running = false;
      if (active) timer = setTimer(poll, requested ? 0 : Math.max(25, interval - (now() - started)));
    }
  }
  void poll();
  return {
    refresh() {
      if (!active) return;
      if (running) { requested = true; return; }
      clearTimer(timer);
      timer = setTimer(poll, 0);
    },
    stop() { active = false; clearTimer(timer); },
  };
}
