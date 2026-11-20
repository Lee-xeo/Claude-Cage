import assert from "node:assert/strict";
import { execFileSync } from "node:child_process";
import { mkdtemp, rm, symlink } from "node:fs/promises";
import { tmpdir } from "node:os";
import { dirname, join } from "node:path";
import { test } from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";
import { isMainModule } from "./is-main-module.mjs";

const here = fileURLToPath(import.meta.url);
const repository = dirname(dirname(here));

test("the entry check accepts the module through a symlinked directory", { skip: process.platform === "win32" }, async t => {
  const root = await mkdtemp(join(tmpdir(), "entry-check-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const linked = join(root, "checkout");
  await symlink(repository, linked, "dir");
  assert.equal(isMainModule(import.meta.url, join(linked, "scripts", "is-main-module.test.mjs")), true);
  assert.equal(isMainModule(import.meta.url, here), true);
  assert.equal(isMainModule(import.meta.url, join(linked, "scripts", "is-main-module.mjs")), false);
  assert.equal(isMainModule(import.meta.url, join(root, "missing.mjs")), false);
  assert.equal(isMainModule(import.meta.url, ""), false);
  assert.equal(isMainModule(pathToFileURL(join(root, "missing.mjs")).href, here), false);

  // A real CLI run from the symlinked path must not silently skip its work.
  assert.throws(
    () => execFileSync(process.execPath, [join(linked, "scripts", "fetch-build-artifact.mjs")], { stdio: "pipe" }),
    error => String(error.stderr).includes("Usage: fetch-build-artifact.mjs"),
  );
  assert.throws(
    () => execFileSync(process.execPath, [join(linked, "www", "public", "agent-bootstrap.mjs"), "--unknown-option"], { stdio: "pipe" }),
    error => String(error.stdout).includes('"status":"error"'),
  );
});
