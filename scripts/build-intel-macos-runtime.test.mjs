import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { chmod, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath, pathToFileURL } from "node:url";

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const runtimeScript = path.join(repoRoot, "scripts", "build-intel-macos-runtime.sh");
const windowsDepsPath = path.join(repoRoot, "packaging", "windows", "dependencies.json");
const bashPath = process.platform === "darwin" ? "/bin/bash" : "bash";

async function readScriptWheels() {
  const res = spawnSync(
    bashPath,
    [
      "-c",
      `set -euo pipefail; source ${JSON.stringify(runtimeScript)}; printf '%s\\n' "\${QEMU_PYTHON_WHEELS[@]}"`,
    ],
    { encoding: "utf8" },
  );
  assert.equal(res.status, 0, res.stderr);
  return res.stdout
    .trim()
    .split("\n")
    .filter(Boolean)
    .map((line) => {
      const [file, url, hash] = line.split("|");
      return { file, url, hash };
    });
}

test(
  "pinned QEMU Python wheels stay in sync with packaging/windows/dependencies.json",
  { skip: process.platform === "win32" },
  async () => {
    const windowsDeps = JSON.parse(await readFile(windowsDepsPath, "utf8"));
    const expected = windowsDeps.qemuPythonWheels.map(({ file, url, hash }) => ({
      file,
      url,
      hash,
    }));
    const actual = await readScriptWheels();
    assert.deepEqual(actual, expected);
  },
);

test(
  "prepare_qemu_python_wheels uses verified cached wheels offline and encodes spaces as a file URI",
  { skip: process.platform === "win32" },
  async (t) => {
    const root = await mkdtemp(path.join(os.tmpdir(), "cage-intel-wheels space-"));
    t.after(() => rm(root, { recursive: true, force: true }));
    const binDir = path.join(root, "bin");
    const wheelDir = path.join(root, "python wheels");
    await mkdir(binDir, { recursive: true });
    await mkdir(wheelDir, { recursive: true });

    const payload = Buffer.from("fake-verified-wheel");
    const digest = createHash("sha256").update(payload).digest("hex");
    await writeFile(path.join(wheelDir, "pip-test.whl"), payload);

    const curlPath = path.join(binDir, "curl");
    await writeFile(curlPath, "#!/bin/sh\necho 'UNEXPECTED_CURL' >&2\nexit 99\n");
    await chmod(curlPath, 0o755);

    const command = [
      "set -euo pipefail",
      `source ${JSON.stringify(runtimeScript)}`,
      `QEMU_PYTHON_WHEELS=(${JSON.stringify(`pip-test.whl|https://files.pythonhosted.org/pip-test.whl|${digest}`)})`,
      `prepare_qemu_python_wheels python3 ${JSON.stringify(wheelDir)}`,
      'printf "PIP_NO_INDEX=%s\\nPIP_DISABLE_PIP_VERSION_CHECK=%s\\nPIP_FIND_LINKS=%s\\n" "$PIP_NO_INDEX" "$PIP_DISABLE_PIP_VERSION_CHECK" "$PIP_FIND_LINKS"',
    ].join("\n");
    const env = { ...process.env, PATH: `${binDir}:${process.env.PATH || "/usr/bin:/bin"}` };
    delete env.PIP_NO_INDEX;
    delete env.PIP_FIND_LINKS;
    const res = spawnSync(bashPath, ["-c", command], { env, encoding: "utf8" });
    assert.equal(res.status, 0, res.stderr);
    assert.match(res.stdout, /^PIP_NO_INDEX=1$/m);
    assert.match(res.stdout, /^PIP_DISABLE_PIP_VERSION_CHECK=1$/m);
    const expectedUri = pathToFileURL(await fsRealpath(wheelDir)).href;
    assert.match(res.stdout, new RegExp(`^PIP_FIND_LINKS=${expectedUri.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}$`, "m"));
    assert.doesNotMatch(res.stdout, / /);
  },
);

async function fsRealpath(target) {
  const { realpath } = await import("node:fs/promises");
  return realpath(target);
}

test(
  "prepare_qemu_python_wheels respects explicit PIP_FIND_LINKS without contacting PyPI",
  { skip: process.platform === "win32" },
  async (t) => {
    const root = await mkdtemp(path.join(os.tmpdir(), "cage-intel-findlinks-"));
    t.after(() => rm(root, { recursive: true, force: true }));
    const binDir = path.join(root, "bin");
    const wheelDir = path.join(root, "python-wheels");
    await mkdir(binDir, { recursive: true });
    const curlPath = path.join(binDir, "curl");
    await writeFile(curlPath, "#!/bin/sh\necho 'UNEXPECTED_CURL' >&2\nexit 99\n");
    await chmod(curlPath, 0o755);

    const command = [
      "set -euo pipefail",
      `source ${JSON.stringify(runtimeScript)}`,
      `prepare_qemu_python_wheels python3 ${JSON.stringify(wheelDir)}`,
      'printf "PIP_NO_INDEX=%s\\nPIP_FIND_LINKS=%s\\n" "$PIP_NO_INDEX" "$PIP_FIND_LINKS"',
    ].join("\n");
    const env = {
      ...process.env,
      PATH: `${binDir}:${process.env.PATH || "/usr/bin:/bin"}`,
      PIP_FIND_LINKS: "/custom/offline/wheels",
    };
    delete env.PIP_NO_INDEX;
    const res = spawnSync(bashPath, ["-c", command], { env, encoding: "utf8" });
    assert.equal(res.status, 0, res.stderr);
    assert.match(res.stdout, /^PIP_NO_INDEX=1$/m);
    assert.match(res.stdout, /^PIP_FIND_LINKS=\/custom\/offline\/wheels$/m);
  },
);

test(
  "prepare_qemu_python_wheels skips downloads when PIP_NO_INDEX=1 and python already provides pip, wheel, and qemu.qmp (#779)",
  { skip: process.platform === "win32" },
  async (t) => {
    const root = await mkdtemp(path.join(os.tmpdir(), "cage-intel-preinstalled-"));
    t.after(() => rm(root, { recursive: true, force: true }));
    const binDir = path.join(root, "bin");
    const siteDir = path.join(root, "site");
    const wheelDir = path.join(root, "python-wheels");
    await mkdir(binDir, { recursive: true });
    await mkdir(path.join(siteDir, "pip"), { recursive: true });
    await mkdir(path.join(siteDir, "wheel"), { recursive: true });
    await mkdir(path.join(siteDir, "qemu", "qmp"), { recursive: true });
    await writeFile(path.join(siteDir, "pip", "__init__.py"), "");
    await writeFile(path.join(siteDir, "wheel", "__init__.py"), "");
    await writeFile(path.join(siteDir, "qemu", "__init__.py"), "");
    await writeFile(path.join(siteDir, "qemu", "qmp", "__init__.py"), "");

    const curlPath = path.join(binDir, "curl");
    await writeFile(curlPath, "#!/bin/sh\necho 'UNEXPECTED_CURL' >&2\nexit 99\n");
    await chmod(curlPath, 0o755);

    const command = [
      "set -euo pipefail",
      `source ${JSON.stringify(runtimeScript)}`,
      `prepare_qemu_python_wheels python3 ${JSON.stringify(wheelDir)}`,
      'printf "PIP_NO_INDEX=%s\\nPIP_FIND_LINKS=%s\\n" "$PIP_NO_INDEX" "${PIP_FIND_LINKS:-}"',
    ].join("\n");
    const env = {
      ...process.env,
      PATH: `${binDir}:${process.env.PATH || "/usr/bin:/bin"}`,
      PYTHONPATH: siteDir,
      PIP_NO_INDEX: "1",
    };
    delete env.PIP_FIND_LINKS;
    const res = spawnSync(bashPath, ["-c", command], { env, encoding: "utf8" });
    assert.equal(res.status, 0, res.stderr);
    assert.match(res.stdout, /^PIP_NO_INDEX=1$/m);
    assert.match(res.stdout, /^PIP_FIND_LINKS=$/m);
  },
);

test(
  "prepare_qemu_python_wheels reports actionable offline guidance when wheel download fails",
  { skip: process.platform === "win32" },
  async (t) => {
    const root = await mkdtemp(path.join(os.tmpdir(), "cage-intel-fail-"));
    t.after(() => rm(root, { recursive: true, force: true }));
    const binDir = path.join(root, "bin");
    const wheelDir = path.join(root, "python-wheels");
    await mkdir(binDir, { recursive: true });

    const curlPath = path.join(binDir, "curl");
    await writeFile(curlPath, "#!/bin/sh\necho 'curl: TLS handshake failed' >&2\nexit 35\n");
    await chmod(curlPath, 0o755);

    const command = [
      "set -euo pipefail",
      `source ${JSON.stringify(runtimeScript)}`,
      `prepare_qemu_python_wheels python3 ${JSON.stringify(wheelDir)}`,
    ].join("\n");
    const env = { ...process.env, PATH: `${binDir}:${process.env.PATH || "/usr/bin:/bin"}` };
    delete env.PIP_NO_INDEX;
    delete env.PIP_FIND_LINKS;
    const res = spawnSync(bashPath, ["-c", command], { env, encoding: "utf8" });
    assert.equal(res.status, 1);
    assert.match(res.stderr, /Failed to fetch QEMU Python build wheel/);
    assert.match(res.stderr, /docs\/intel-macos-development\.md/);
  },
);

test(
  "download_to rejects corrupted wheels and cleans up .partial files",
  { skip: process.platform === "win32" },
  async (t) => {
    const root = await mkdtemp(path.join(os.tmpdir(), "cage-intel-badhash-"));
    t.after(() => rm(root, { recursive: true, force: true }));
    const binDir = path.join(root, "bin");
    const wheelDir = path.join(root, "python-wheels");
    await mkdir(binDir, { recursive: true });
    await mkdir(wheelDir, { recursive: true });

    const expectedHash = createHash("sha256").update("expected-bytes").digest("hex");
    await writeFile(path.join(wheelDir, "pip-test.whl"), "stale-bad-bytes");

    const curlPath = path.join(binDir, "curl");
    await writeFile(
      curlPath,
      '#!/bin/sh\nout=""\nwhile [ $# -gt 0 ]; do\n  if [ "$1" = "-o" ]; then out="$2"; shift 2; else shift; fi\ndone\nprintf "wrong-downloaded-bytes" > "$out"\n',
    );
    await chmod(curlPath, 0o755);

    const command = [
      "set -euo pipefail",
      `source ${JSON.stringify(runtimeScript)}`,
      `QEMU_PYTHON_WHEELS=(${JSON.stringify(`pip-test.whl|https://files.pythonhosted.org/pip-test.whl|${expectedHash}`)})`,
      `prepare_qemu_python_wheels python3 ${JSON.stringify(wheelDir)}`,
    ].join("\n");
    const env = { ...process.env, PATH: `${binDir}:${process.env.PATH || "/usr/bin:/bin"}` };
    delete env.PIP_NO_INDEX;
    delete env.PIP_FIND_LINKS;
    const res = spawnSync(bashPath, ["-c", command], { env, encoding: "utf8" });
    assert.equal(res.status, 1);
    assert.match(res.stderr, /Failed to fetch QEMU Python build wheel pip-test\.whl/);
    const { readdir } = await import("node:fs/promises");
    const files = await readdir(wheelDir);
    assert.ok(!files.includes("pip-test.whl.partial"), "partial file must be removed on checksum mismatch");
    assert.equal(await readFile(path.join(wheelDir, "pip-test.whl"), "utf8"), "stale-bad-bytes");
  },
);
