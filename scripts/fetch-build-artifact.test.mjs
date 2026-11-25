import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { appendFile, mkdtemp, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { test } from "node:test";
import { fetchBuildArtifact, resolveArtifactURL } from "./fetch-build-artifact.mjs";

const content = Buffer.from("verified build input");
const checksum = createHash("sha256").update(content).digest("hex");
const url = "https://build-fixture.invalid/dependency.gz";

async function fixture(t) {
  const root = await mkdtemp(join(tmpdir(), "cage-build-download-test-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  return { root, destination: join(root, "dependency.gz") };
}

test("an interrupted transfer resumes and the verified file becomes a cache hit", async t => {
  const { root, destination } = await fixture(t);
  const chunk = content.subarray(0, 8);
  const offsets = [];
  await fetchBuildArtifact(url, checksum, destination, async (_url, part, resume) => {
    await assert.rejects(readFile(destination), { code: "ENOENT" });
    if (!resume) {
      offsets.push(0);
      await writeFile(part, chunk);
      return { code: 18, http: "200" };
    }
    offsets.push((await readFile(part)).length);
    await appendFile(part, content.subarray(chunk.length));
    return { code: 0, http: "206" };
  });
  assert.deepEqual(offsets, [0, 8]);
  assert.deepEqual(await readFile(destination), content);
  assert.deepEqual(await readdir(root), ["dependency.gz"]);
  await fetchBuildArtifact(url, checksum, destination, () => assert.fail("warm cache must not download"));
});

test("a server that rejects Range causes a safe full retry", async t => {
  const { destination } = await fixture(t);
  const attempts = [];
  await fetchBuildArtifact(url, checksum, destination, async (_url, part, resume) => {
    attempts.push(resume);
    if (attempts.length === 1) {
      await writeFile(part, "partial");
      return { code: 18, http: "200" };
    }
    if (resume) return { code: 33, http: "200" };
    await assert.rejects(readFile(part), { code: "ENOENT" });
    await writeFile(part, content);
    return { code: 0, http: "200" };
  });
  assert.deepEqual(attempts, [false, true, false]);
  assert.deepEqual(await readFile(destination), content);
});

test("a complete file with a wrong checksum is never published", async t => {
  const { root, destination } = await fixture(t);
  await assert.rejects(fetchBuildArtifact(url, checksum, destination, async (_url, part) => {
    await writeFile(part, "not the reviewed artifact");
    return { code: 0, http: "200" };
  }), /checksum mismatch/);
  assert.deepEqual(await readdir(root), []);
});

test("a slow transfer that keeps progressing resumes past the four-attempt limit", async t => {
  const { destination } = await fixture(t);
  const offsets = [];
  await fetchBuildArtifact(url, checksum, destination, async (_url, part, resume) => {
    const kept = resume ? (await readFile(part)).length : 0;
    offsets.push(kept);
    const next = Math.min(content.length, kept + 2);
    await appendFile(part, content.subarray(kept, next));
    return next === content.length ? { code: 0, http: "206" } : { code: 28, http: "206" };
  });
  assert.equal(offsets.length, Math.ceil(content.length / 2));
  assert.deepEqual(offsets, offsets.map((_, index) => index * 2));
  assert.deepEqual(await readFile(destination), content);
});

test("four attempts in a row without new bytes fail and keep nothing", async t => {
  const { root, destination } = await fixture(t);
  let calls = 0;
  await assert.rejects(fetchBuildArtifact(url, checksum, destination, async (_url, part) => {
    calls++;
    if (calls === 1) await writeFile(part, content.subarray(0, 4));
    return { code: 28, http: "206" };
  }), /Could not download/);
  assert.equal(calls, 5);
  assert.deepEqual(await readdir(root), []);
});

test("a SHA-512 pin is verified with SHA-512", async t => {
  const { destination } = await fixture(t);
  const sha512 = createHash("sha512").update(content).digest("hex");
  await fetchBuildArtifact(url, sha512.toUpperCase(), destination, async (_url, part) => {
    await writeFile(part, content);
    return { code: 0, http: "200" };
  });
  assert.deepEqual(await readFile(destination), content);
  await assert.rejects(fetchBuildArtifact(url, "ab".repeat(48), destination, () => assert.fail()), /pinned SHA-256 or SHA-512/);
});

test("a missing object fails at once and keeps nothing", async t => {
  const { root, destination } = await fixture(t);
  let calls = 0;
  await assert.rejects(fetchBuildArtifact(url, checksum, destination, async () => {
    calls++;
    return { code: 22, http: "404" };
  }), /Could not download dependency\.gz \(curl exit 22, HTTP 404\)/);
  assert.equal(calls, 1);
  assert.deepEqual(await readdir(root), []);
});

test("a permanent refusal while resuming discards the partial file", async t => {
  const { root, destination } = await fixture(t);
  let calls = 0;
  await assert.rejects(fetchBuildArtifact(url, checksum, destination, async (_url, part) => {
    calls++;
    if (calls === 1) {
      await writeFile(part, content.subarray(0, 4));
      return { code: 18, http: "200" };
    }
    return { code: 22, http: "403" };
  }), /HTTP 403/);
  assert.equal(calls, 2);
  assert.deepEqual(await readdir(root), []);
});

test("rate limits and server errors are retried", async t => {
  const { destination } = await fixture(t);
  const replies = [{ code: 22, http: "429" }, { code: 22, http: "503" }];
  await fetchBuildArtifact(url, checksum, destination, async (_url, part) => {
    if (replies.length) return replies.shift();
    await writeFile(part, content);
    return { code: 0, http: "200" };
  });
  assert.deepEqual(await readFile(destination), content);
});

test("CAGE_GITHUB_MIRROR prefixes only GitHub HTTPS URLs and keeps checksum verification", async t => {
  const ghURL = "https://github.com/MetaCubeX/mihomo/releases/download/v1.19.33/mihomo.gz";
  assert.equal(
    resolveArtifactURL(ghURL, "https://mirror.example.com/"),
    "https://mirror.example.com/https://github.com/MetaCubeX/mihomo/releases/download/v1.19.33/mihomo.gz",
  );
  assert.equal(resolveArtifactURL(url, "https://mirror.example.com"), url);
  assert.throws(() => resolveArtifactURL(ghURL, "http://insecure.example.com"), /HTTPS URL/);
  assert.throws(() => resolveArtifactURL(ghURL, "https://user:pass@mirror.example.com"), /without credentials/);
  assert.throws(() => resolveArtifactURL(ghURL, "https://mirror.example.com/?q=1"), /query, or fragment/);
  assert.throws(() => resolveArtifactURL(ghURL, "https://mirror.example.com/#frag"), /query, or fragment/);

  const { destination } = await fixture(t);
  let requested = "";
  await fetchBuildArtifact(ghURL, checksum, destination, async (target, part) => {
    requested = target;
    await writeFile(part, content);
    return { code: 0, http: "200" };
  }, { mirror: "https://mirror.example.com/" });
  assert.equal(requested, "https://mirror.example.com/" + ghURL);
  assert.deepEqual(await readFile(destination), content);
});
