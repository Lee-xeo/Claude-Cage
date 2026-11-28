import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { chmod, copyFile, mkdir, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const envScript = path.join(repoRoot, "scripts", "macos-build-env.sh");
const bashPath = process.platform === "darwin" ? "/bin/bash" : "bash";

async function createFixture(t, { sdks = [], xcrunScript }) {
  const root = await mkdtemp(path.join(os.tmpdir(), "cage-macos-build-env-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const binDir = path.join(root, "bin");
  const sdkDir = path.join(root, "SDKs");
  const workDir = path.join(root, "work");
  await mkdir(binDir, { recursive: true });
  await mkdir(sdkDir, { recursive: true });
  await mkdir(workDir, { recursive: true });
  for (const name of sdks) {
    await mkdir(path.join(sdkDir, name), { recursive: true });
  }
  const xcrunPath = path.join(binDir, "xcrun");
  await writeFile(xcrunPath, `#!/bin/sh\n${xcrunScript}\n`);
  await chmod(xcrunPath, 0o755);
  return { root, binDir, sdkDir, workDir };
}

function runCheck({ binDir, sdkDir, workDir }, { env = {}, extraRoots = [] } = {}) {
  const roots = [sdkDir, ...extraRoots].map((r) => JSON.stringify(r)).join(" ");
  const command = [
    "set -euo pipefail",
    `source ${JSON.stringify(envScript)}`,
    `macos_sdk_roots() { printf '%s\\n' ${roots}; }`,
    "macos_check_toolchain",
    'printf "SELECTED_SDKROOT=%s\\n" "${SDKROOT:-}"',
  ].join("\n");
  const childEnv = {
    ...process.env,
    PATH: `${binDir}:${process.env.PATH || "/usr/bin:/bin"}`,
    ...env,
  };
  if (!("SDKROOT" in env)) delete childEnv.SDKROOT;
  return spawnSync(bashPath, ["-c", command], {
    cwd: workDir,
    env: childEnv,
    encoding: "utf8",
  });
}

test(
  "falls back to newest installed SDK when default SDK is newer than swiftc (#783)",
  { skip: process.platform === "win32" },
  async (t) => {
    const fixture = await createFixture(t, {
      sdks: ["MacOSX14.5.sdk", "MacOSX15.5.sdk", "MacOSX26.2.sdk"],
      xcrunScript: `
tool="$1"; shift
if [ "$tool" = "clang" ]; then
  exit 0
fi
if [ "$tool" = "swiftc" ]; then
  case "\${SDKROOT:-}" in
    */MacOSX15.5.sdk|*/MacOSX14.5.sdk) exit 0 ;;
    *)
      echo "/SDK/Swift.swiftinterface:1:1: error: failed to build module 'Swift'; this SDK is not supported by the compiler (the SDK is built with 'Apple Swift version 6.2', while this compiler is 'Apple Swift version 6.1.2')." >&2
      exit 1
      ;;
  esac
fi
exit 1
`,
    });
    const res = runCheck(fixture);
    assert.equal(res.status, 0, res.stderr);
    const expected = path.join(fixture.sdkDir, "MacOSX15.5.sdk");
    assert.match(res.stdout, new RegExp(`SELECTED_SDKROOT=${expected.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`));
    assert.match(res.stderr, /The default macOS SDK is newer than this swiftc; using .*MacOSX15\.5\.sdk/);
    const leftovers = await readdir(path.join(fixture.workDir, "_output"));
    assert.deepEqual(leftovers, []);
  },
);

test(
  "clang fallback skips an SDK that links C but fails swiftc and picks one passing both",
  { skip: process.platform === "win32" },
  async (t) => {
    const fixture = await createFixture(t, {
      sdks: ["MacOSX15.4.sdk", "MacOSX26.2.sdk", "MacOSX26.5.sdk"],
      xcrunScript: `
tool="$1"; shift
if [ "$tool" = "clang" ]; then
  case "\${SDKROOT:-}" in
    ""|*/MacOSX26.5.sdk)
      echo "ld: tapi: unknown architecture arm64e.x1" >&2
      exit 1
      ;;
    *) exit 0 ;;
  esac
fi
if [ "$tool" = "swiftc" ]; then
  case "\${SDKROOT:-}" in
    */MacOSX15.4.sdk) exit 0 ;;
    *)
      echo "error: failed to build module 'Swift'; this SDK is not supported by the compiler" >&2
      exit 1
      ;;
  esac
fi
exit 1
`,
    });
    const res = runCheck(fixture);
    assert.equal(res.status, 0, res.stderr);
    const expected = path.join(fixture.sdkDir, "MacOSX15.4.sdk");
    assert.match(res.stdout, new RegExp(`SELECTED_SDKROOT=${expected.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}`));
    assert.match(res.stderr, /The default macOS SDK is newer than this clang; using .*MacOSX15\.4\.sdk/);
  },
);

test(
  "preserves original default SDK Swift error and prints actionable SDKROOT hint when no installed SDK matches",
  { skip: process.platform === "win32" },
  async (t) => {
    const fixture = await createFixture(t, {
      sdks: ["MacOSX26.2.sdk"],
      xcrunScript: `
tool="$1"; shift
if [ "$tool" = "clang" ]; then
  exit 0
fi
if [ "$tool" = "swiftc" ]; then
  if [ -z "\${SDKROOT:-}" ]; then
    echo "DEFAULT_SDK_MARKER: this SDK is not supported by the compiler" >&2
  else
    echo "CANDIDATE_SDK_MARKER: this SDK is not supported by the compiler" >&2
  fi
  exit 1
fi
exit 1
`,
    });
    const res = runCheck(fixture);
    assert.equal(res.status, 1);
    assert.match(res.stderr, /DEFAULT_SDK_MARKER/);
    assert.doesNotMatch(res.stderr, /CANDIDATE_SDK_MARKER/);
    assert.match(res.stderr, /set SDKROOT to an older installed SDK/);
    const leftovers = await readdir(path.join(fixture.workDir, "_output"));
    assert.deepEqual(leftovers, []);
  },
);

test(
  "does not silently override an explicitly set SDKROOT when swiftc rejects it",
  { skip: process.platform === "win32" },
  async (t) => {
    const fixture = await createFixture(t, {
      sdks: ["MacOSX15.5.sdk", "MacOSX26.2.sdk"],
      xcrunScript: `
tool="$1"; shift
if [ "$tool" = "clang" ]; then
  exit 0
fi
if [ "$tool" = "swiftc" ]; then
  case "\${SDKROOT:-}" in
    */MacOSX15.5.sdk) exit 0 ;;
    *)
      echo "error: failed to build module 'Swift'; this SDK is not supported by the compiler" >&2
      exit 1
      ;;
  esac
fi
exit 1
`,
    });
    const explicit = path.join(fixture.sdkDir, "MacOSX26.2.sdk");
    const res = runCheck(fixture, { env: { SDKROOT: explicit } });
    assert.equal(res.status, 1);
    assert.match(res.stderr, /set SDKROOT to an older installed SDK/);
  },
);

test(
  "keeps SwiftBridging diagnostic without triggering SDK fallback",
  { skip: process.platform === "win32" },
  async (t) => {
    const fixture = await createFixture(t, {
      sdks: ["MacOSX15.5.sdk"],
      xcrunScript: `
tool="$1"; shift
if [ "$tool" = "clang" ]; then
  exit 0
fi
if [ "$tool" = "swiftc" ]; then
  if [ -n "\${SDKROOT:-}" ]; then
    echo "UNEXPECTED_FALLBACK" >&2
    exit 0
  fi
  echo "error: redefinition of module 'SwiftBridging'" >&2
  exit 1
fi
exit 1
`,
    });
    const res = runCheck(fixture);
    assert.equal(res.status, 1);
    assert.match(res.stderr, /leftover module maps/);
    assert.doesNotMatch(res.stderr, /UNEXPECTED_FALLBACK/);
  },
);

test(
  "macos_list_installed_sdks sorts versions descending and deduplicates overlapping roots",
  { skip: process.platform === "win32" },
  async (t) => {
    const fixture = await createFixture(t, {
      sdks: ["MacOSX14.5.sdk", "MacOSX15.2.sdk", "MacOSX15.10.sdk", "MacOSX26.2.sdk"],
      xcrunScript: "exit 0",
    });
    const command = [
      "set -euo pipefail",
      `source ${JSON.stringify(envScript)}`,
      `macos_sdk_roots() { printf '%s\\n' ${JSON.stringify(fixture.sdkDir)} ${JSON.stringify(fixture.sdkDir)}; }`,
      "macos_list_installed_sdks",
    ].join("\n");
    const res = spawnSync(bashPath, ["-c", command], {
      cwd: fixture.workDir,
      encoding: "utf8",
    });
    assert.equal(res.status, 0, res.stderr);
    assert.deepEqual(
      res.stdout.trim().split("\n"),
      ["MacOSX26.2.sdk", "MacOSX15.10.sdk", "MacOSX15.2.sdk", "MacOSX14.5.sdk"].map((name) =>
        path.join(fixture.sdkDir, name),
      ),
    );
  },
);

test(
  "healthy default SDK leaves SDKROOT unset without running fallback probes",
  { skip: process.platform === "win32" },
  async (t) => {
    const fixture = await createFixture(t, {
      sdks: ["MacOSX15.5.sdk"],
      xcrunScript: `
if [ -n "\${SDKROOT:-}" ]; then
  echo "UNEXPECTED_SDKROOT=$SDKROOT" >&2
  exit 1
fi
exit 0
`,
    });
    const res = runCheck(fixture);
    assert.equal(res.status, 0, res.stderr);
    assert.equal(res.stdout.trim(), "SELECTED_SDKROOT=");
    assert.equal(res.stderr.trim(), "");
  },
);

async function writeStub(binDir, name, body) {
  const file = path.join(binDir, name);
  await writeFile(file, `#!/bin/sh\n${body}\n`);
  await chmod(file, 0o755);
}

test("Node.js range matches engines.node in ui/package.json (#1103)", async () => {
  const pkg = JSON.parse(await readFile(path.join(repoRoot, "ui", "package.json"), "utf8"));
  const lock = JSON.parse(await readFile(path.join(repoRoot, "ui", "package-lock.json"), "utf8"));
  const script = await readFile(envScript, "utf8");
  const declared = script.match(/^macos_node_range='([^']+)'$/m)?.[1];
  assert.equal(declared, pkg.engines?.node);
  assert.equal(lock.packages[""].engines?.node, pkg.engines.node);
  assert.equal(lock.packages["node_modules/vite"].engines?.node, pkg.engines.node);
});

test(
  "macos_check_node accepts Vite 8 Node.js versions and rejects older ones (#1103)",
  { skip: process.platform === "win32" },
  async (t) => {
    const root = await mkdtemp(path.join(os.tmpdir(), "cage-macos-node-"));
    t.after(() => rm(root, { recursive: true, force: true }));
    const binDir = path.join(root, "bin");
    await mkdir(binDir, { recursive: true });
    const cases = {
      "v20.17.0": false,
      "v20.18.3": false,
      "v20.19.0": true,
      "v21.7.3": false,
      "v22.11.0": false,
      "v22.12.0": true,
      "v22.23.5": true,
      "v24.1.0": true,
      garbage: false,
    };
    const command = `set -euo pipefail\nsource ${JSON.stringify(envScript)}\nmacos_check_node`;
    for (const [version, ok] of Object.entries(cases)) {
      await writeStub(binDir, "node", `echo ${version}`);
      const res = spawnSync(bashPath, ["-c", command], {
        cwd: root,
        env: { ...process.env, PATH: `${binDir}:/usr/bin:/bin` },
        encoding: "utf8",
      });
      assert.equal(res.status === 0, ok, `${version}: ${res.stderr}`);
      if (!ok) {
        assert.match(res.stderr, /requires Node\.js \^20\.19\.0 \|\| >=22\.12\.0; found /);
        assert.ok(res.stderr.includes(version), res.stderr);
      }
    }
  },
);

// Runs a copy of build-app.sh with stubbed host tools; make and the mihomo
// download only record their calls.
async function runBuildApp(t, { nodeVersion, failTarget = "" }) {
  const root = await mkdtemp(path.join(os.tmpdir(), "cage-build-app-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const binDir = path.join(root, "bin");
  const scriptsDir = path.join(root, "scripts");
  await mkdir(binDir, { recursive: true });
  await mkdir(scriptsDir, { recursive: true });
  await copyFile(path.join(repoRoot, "scripts", "build-app.sh"), path.join(scriptsDir, "build-app.sh"));
  await copyFile(envScript, path.join(scriptsDir, "macos-build-env.sh"));
  await writeFile(path.join(root, "VERSION"), "0.2.2\n");
  const log = path.join(root, "calls.log");
  await writeFile(path.join(scriptsDir, "fetch-proxy-core.sh"), `echo fetch-proxy-core >> ${JSON.stringify(log)}\n`);
  await writeStub(binDir, "uname", "echo Darwin");
  await writeStub(binDir, "uv", "exit 0");
  await writeStub(binDir, "xcrun", "exit 0");
  await writeStub(binDir, "node", `echo ${nodeVersion}`);
  await writeStub(
    binDir,
    "make",
    [
      `echo "make $*" >> ${JSON.stringify(log)}`,
      `[ "${failTarget}" = cage-ui ] && [ "$1" = cage-ui ] && exit 1`,
      `[ "${failTarget}" = cage ] && [ "$1" = -o ] && exit 1`,
      "exit 0",
    ].join("\n"),
  );
  const res = spawnSync(bashPath, [path.join(scriptsDir, "build-app.sh")], {
    cwd: root,
    env: { ...process.env, PATH: `${binDir}:/usr/bin:/bin`, HOME: root },
    encoding: "utf8",
  });
  const calls = await readFile(log, "utf8").catch(() => "");
  return { res, calls };
}

test(
  "build-app.sh stops on unsupported Node.js before downloading mihomo (#1103)",
  { skip: process.platform === "win32" },
  async (t) => {
    const { res, calls } = await runBuildApp(t, { nodeVersion: "v20.17.0" });
    assert.equal(res.status, 1, res.stderr);
    assert.match(res.stderr, /requires Node\.js \^20\.19\.0 \|\| >=22\.12\.0; found v20\.17\.0/);
    assert.equal(calls, "");
  },
);

test(
  "build-app.sh reports a UI build failure without the GOPROXY hint (#1103)",
  { skip: process.platform === "win32" },
  async (t) => {
    const { res, calls } = await runBuildApp(t, { nodeVersion: "v22.12.0", failTarget: "cage-ui" });
    assert.equal(res.status, 1, res.stderr);
    assert.match(res.stderr, /Building the workbench UI failed/);
    assert.match(res.stderr, /Node\.js on PATH: v22\.12\.0/);
    assert.doesNotMatch(res.stderr, /GOPROXY/);
    assert.equal(calls, "fetch-proxy-core\nmake cage-ui\n");
  },
);

test(
  "build-app.sh keeps the GOPROXY hint for failures after the UI step (#1103)",
  { skip: process.platform === "win32" },
  async (t) => {
    const { res, calls } = await runBuildApp(t, { nodeVersion: "v22.12.0", failTarget: "cage" });
    assert.equal(res.status, 1, res.stderr);
    assert.match(res.stderr, /GOPROXY=https:\/\/goproxy\.cn/);
    assert.doesNotMatch(res.stderr, /workbench UI failed/);
    assert.equal(calls, "fetch-proxy-core\nmake cage-ui\nmake -o cage-ui cage VERSION=0.2.2\n");
  },
);
