import test from "node:test";
import assert from "node:assert/strict";
import { stopExitState, stopAndExit } from "./quit.js";
test("automatic exit waits for its own successful stop and confirmed VM shutdown", () => {
  const status = {
    vm_running: true,
    job: { id: "old-stop", kind: "stop", running: false, error: "" },
  };
  assert.equal(stopExitState(status, null), "waiting");
  assert.equal(stopExitState(status, "new-stop"), "waiting");
  status.job.id = "new-stop";
  status.job.running = true;
  assert.equal(stopExitState(status, "new-stop"), "waiting");
  status.job.running = false;
  assert.equal(stopExitState(status, "new-stop"), "waiting");
  status.vm_running = false;
  assert.equal(stopExitState(status, "new-stop"), "exit");
  status.job.error = "VM stop failed";
  assert.equal(stopExitState(status, "new-stop"), "failed");
});

test("exit completes without the page polling status, and ignores an old stop", async () => {
  let exits = 0,
    reads = 0;
  const statuses = [
    { vm_running: false, job: { id: "old", kind: "stop", running: false } },
    { vm_running: true, job: { id: "wanted", kind: "stop", running: true } },
    { vm_running: true, job: { id: "wanted", kind: "stop", running: false } },
    { vm_running: false, job: { id: "wanted", kind: "stop", running: false } },
  ];
  assert.equal(
    await stopAndExit({
      stop: async () => ({ id: "wanted" }),
      readStatus: async () => {
        reads++;
        return statuses.shift();
      },
      exit: async () => {
        exits++;
      },
      isCancelled: () => false,
      onStatus: () => {},
      delay: async () => {},
    }),
    true,
  );
  assert.equal(reads, 4);
  assert.equal(exits, 1);
});
test("a failed stop leaves the app open and surfaces the error", async () => {
  await assert.rejects(
    stopAndExit({
      stop: async () => ({ id: "wanted" }),
      readStatus: async () => ({
        vm_running: true,
        job: {
          id: "wanted",
          kind: "stop",
          running: false,
          error: "stop failed",
        },
      }),
      exit: () => assert.fail("must remain open"),
      isCancelled: () => false,
      onStatus: () => {},
    }),
    /stop failed/,
  );
});
test("cancelling automatic exit during a status request prevents app termination", async () => {
  let cancelled = false;
  const result = await stopAndExit({
    stop: async () => ({ id: "wanted" }),
    readStatus: async () => {
      cancelled = true;
      return {
        vm_running: false,
        job: { id: "wanted", kind: "stop", running: false },
      };
    },
    exit: () => assert.fail("cancelled exit"),
    isCancelled: () => cancelled,
    onStatus: () => assert.fail("cancelled update"),
  });
  assert.equal(result, false);
});
test("a stop that finishes while the VM still reports running ends the wait with an error", async () => {
  let clock = 0,
    reads = 0;
  await assert.rejects(
    stopAndExit({
      stop: async () => ({ id: "wanted" }),
      readStatus: async () => {
        reads++;
        return reads < 3
          ? { vm_running: true, job: { id: "wanted", kind: "stop", running: true } }
          : { vm_running: true, job: { id: "wanted", kind: "stop", running: false, error: "" } };
      },
      exit: () => assert.fail("must remain open"),
      isCancelled: () => false,
      onStatus: () => {},
      delay: async () => {
        clock += 1000;
      },
      now: () => clock,
      confirmationGraceMs: 5000,
    }),
    /仍显示在运行/,
  );
  // Two running polls, then the idle grace period counted from the first idle poll.
  assert.equal(reads, 8);
});
test("a status that never names this stop ends the wait with an error", async () => {
  let clock = 0;
  await assert.rejects(
    stopAndExit({
      stop: async () => ({ id: "wanted" }),
      readStatus: async () => ({ vm_running: false, job: { id: "other", kind: "start", running: false } }),
      exit: () => assert.fail("must remain open"),
      isCancelled: () => false,
      onStatus: () => {},
      delay: async () => {
        clock += 1000;
      },
      now: () => clock,
      confirmationGraceMs: 5000,
    }),
    /未报告本次停止任务/,
  );
});
test("a VM that stops shortly after its stop job ends still exits", async () => {
  let clock = 0,
    exits = 0;
  const statuses = [
    { vm_running: true, job: { id: "wanted", kind: "stop", running: false } },
    { vm_running: true, job: { id: "wanted", kind: "stop", running: false } },
    { vm_running: false, job: { id: "wanted", kind: "stop", running: false } },
  ];
  assert.equal(
    await stopAndExit({
      stop: async () => ({ id: "wanted" }),
      readStatus: async () => statuses.shift(),
      exit: async () => {
        exits++;
      },
      isCancelled: () => false,
      onStatus: () => {},
      delay: async () => {
        clock += 1000;
      },
      now: () => clock,
      confirmationGraceMs: 5000,
    }),
    true,
  );
  assert.equal(exits, 1);
});
