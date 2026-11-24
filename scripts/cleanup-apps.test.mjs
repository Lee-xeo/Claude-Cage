import assert from "node:assert/strict";
import {
  mkdtemp,
  mkdir,
  writeFile,
  symlink,
  access,
  rm,
  copyFile,
  chmod,
  open,
} from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { execFileSync, spawn } from "node:child_process";
import { cleanupApps } from "./cleanup-apps.mjs";

const cageID = "io.github.lee-xeo.claude-cage";
const exited = (child) => child.exitCode !== null || child.signalCode !== null;
async function stop(child) {
  // An exit that already happened never emits again; waiting for it would hang.
  if (!child || exited(child)) return;
  await new Promise((resolve) => { child.once("exit", resolve); child.kill("SIGTERM"); });
}

async function buildStandInExecutable(directory) {
  const source = path.join(directory, "standin.c");
  const executable = path.join(directory, "standin");
  await writeFile(
    source,
    "#include <stdio.h>\n#include <unistd.h>\nint main(void) {\n  puts(\"ready\");\n  fflush(stdout);\n  for (;;) pause();\n}\n",
  );
  execFileSync("/usr/bin/xcrun", ["clang", source, "-o", executable], {
    stdio: ["ignore", "ignore", "pipe"],
  });
  return executable;
}

test("cleanup preserves a Cage executable still in use", { skip: process.platform !== "darwin", timeout: 180_000 }, async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "cage-cleanup-running-"));
  const buildDir = await mkdtemp(path.join(os.tmpdir(), "cage-cleanup-standin-"));
  let child;
  try {
    const latest = await app(path.join(directory, "new.app"));
    // A re-signed copy of a system binary such as /bin/sleep is arm64e and may
    // be killed at exec on Apple silicon; copying process.execPath breaks when
    // Node links @rpath/libnode (such as Homebrew Node). Compile a tiny
    // self-contained host binary instead.
    const standIn = await buildStandInExecutable(buildDir);
    const running = await app(path.join(directory, "running.app"), cageID, standIn);
    const executable = path.join(running, "Contents/MacOS/Cage");
    child = spawn(executable, [], { stdio: ["ignore", "pipe", "pipe"] });
    let stderr = "";
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    await new Promise((resolve, reject) => {
      child.once("error", reject);
      child.once("exit", (code, signal) => {
        const detail = stderr.trim() ? `: ${stderr.trim()}` : "";
        reject(new Error(`The stand-in App exited before it started (${signal || code})${detail}.`));
      });
      child.stdout.once("data", resolve);
    });
    const skipped = [];
    assert.deepEqual(await cleanupApps({ keep: [latest], roots: [directory], onInUse: (bundle) => skipped.push(bundle) }), []);
    assert.deepEqual(skipped, [running]);
    await access(executable);
    assert.ok(!exited(child), "the stand-in App must still run while cleanup decides");
    await stop(child);
    child = null;
    assert.deepEqual(await cleanupApps({ keep: [latest], roots: [directory] }), [running]);
  } finally {
    await stop(child);
    await rm(buildDir, { recursive: true, force: true });
    await rm(directory, { recursive: true, force: true });
  }
});
async function app(directory, id = cageID, executable = "/bin/sleep") {
  await mkdir(path.join(directory, "Contents/MacOS"), { recursive: true });
  await writeFile(
    path.join(directory, "Contents/Info.plist"),
    `<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>${id}</string><key>CFBundleExecutable</key><string>Cage</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`,
  );
  await copyFile(executable, path.join(directory, 'Contents/MacOS/Cage'));
  execFileSync('/usr/bin/codesign', ['--force', '--sign', '-', directory], { stdio: 'ignore' });
  return directory;
}

test("cleanup removes an App whose executable is only open for reading", { skip: process.platform !== "darwin" }, async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "cage-cleanup-readonly-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const latest = await app(path.join(directory, "new.app"));
  const old = await app(path.join(directory, "old.app"));
  const handle = await open(path.join(old, "Contents/MacOS/Cage"), "r");
  t.after(() => handle.close());
  const skipped = [];
  assert.deepEqual(await cleanupApps({ keep: [latest], roots: [directory], onInUse: (bundle) => skipped.push(bundle) }), [old]);
  assert.deepEqual(skipped, []);
});

test("cleanup reports a retained App once when roots overlap", { skip: process.platform !== "darwin" }, async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "cage-cleanup-overlap-"));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const latest = await app(path.join(directory, "new.app"));
  const modified = await app(path.join(directory, "old.app"));
  await writeFile(path.join(modified, "Contents/notes.txt"), "user work");
  const retained = [];
  assert.deepEqual(await cleanupApps({ keep: [latest], roots: [modified, directory], onModified: (value) => retained.push(value) }), []);
  assert.deepEqual(retained, [modified]);
});

test('cleanup retains project files added to a previously signed app', { skip: process.platform !== 'darwin' }, async (t) => {
  const directory = await mkdtemp(path.join(os.tmpdir(), 'cage-cleanup-modified-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const latest = await app(path.join(directory, 'new.app'));
  const modified = await app(path.join(directory, 'old.app'));
  const project = path.join(modified, 'Contents/my-project');
  await mkdir(project);
  await writeFile(path.join(project, 'work.txt'), 'user work');
  const retained = [];
  assert.deepEqual(await cleanupApps({ keep: [latest], roots: [directory], onModified: value => retained.push(value) }), []);
  assert.deepEqual(retained, [modified]);
  await access(path.join(project, 'work.txt'));
});

test(
  "cleanup removes old Cage copies while retaining replacement, foreign apps and linked directories",
  { skip: process.platform !== "darwin" },
  async (t) => {
    const directory = await mkdtemp(path.join(os.tmpdir(), "cage-cleanup-"));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const root = path.join(directory, "output"),
      outside = path.join(directory, "outside");
    const latest = await app(path.join(root, "Cage.app"));
    const old = await app(path.join(root, "qa backup", "previous-Cage.app"));
    const foreign = await app(path.join(root, "Other.app"), "example.other");
    const external = await app(path.join(outside, "Cage.app"));
    await symlink(outside, path.join(root, "linked-backup"));
    assert.deepEqual(
      await cleanupApps({
        keep: [latest],
        roots: [root, path.join(directory, "missing")],
      }),
      [old],
    );
    await assert.rejects(access(old), { code: "ENOENT" });
    for (const preserved of [latest, foreign, external])
      await access(preserved);
  },
);

test(
  "cleanup refuses to delete anything without a valid Cage replacement",
  { skip: process.platform !== "darwin" },
  async (t) => {
    const directory = await mkdtemp(path.join(os.tmpdir(), "cage-cleanup-"));
    t.after(() => rm(directory, { recursive: true, force: true }));
    const old = await app(path.join(directory, "old.app"));
    const foreign = await app(
      path.join(directory, "Other.app"),
      "example.other",
    );
    await assert.rejects(cleanupApps({ keep: [], roots: [directory] }));
    await assert.rejects(cleanupApps({ keep: [foreign], roots: [directory] }));
    await access(old);
  },
);

test(
  "cleanup skips directories the current user cannot read",
  { skip: process.platform !== "darwin" || process.getuid?.() === 0 },
  async (t) => {
    const directory = await mkdtemp(path.join(os.tmpdir(), "cage-cleanup-"));
    const root = path.join(directory, "output");
    const latest = await app(path.join(root, "Cage.app"));
    const old = await app(path.join(root, "previous-Cage.app"));
    const unreadable = path.join(root, "private installer data");
    await mkdir(unreadable);
    await chmod(unreadable, 0o000);
    t.after(async () => {
      await chmod(unreadable, 0o700);
      await rm(directory, { recursive: true, force: true });
    });
    const skipped = [];
    assert.deepEqual(
      await cleanupApps({ keep: [latest], roots: [root], onUnreadable: (entry) => skipped.push(entry) }),
      [old],
    );
    assert.deepEqual(skipped, [unreadable]);
    await access(latest);
  },
);
