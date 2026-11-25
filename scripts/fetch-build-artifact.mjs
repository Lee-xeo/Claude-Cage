import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { createReadStream, existsSync } from "node:fs";
import { mkdir, mkdtemp, rename, rm, stat } from "node:fs/promises";
import { basename, dirname, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { isMainModule } from "./is-main-module.mjs";

export async function digest(file, algorithm = "sha256") {
  const hash = createHash(algorithm);
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return hash.digest("hex");
}

export const sha256 = file => digest(file, "sha256");

// A pinned digest names its algorithm by length: SHA-256 or SHA-512.
function pinnedDigest(expected) {
  if (/^[a-f0-9]{64}$/i.test(expected)) return { algorithm: "sha256", checksum: expected.toLowerCase() };
  if (/^[a-f0-9]{128}$/i.test(expected)) return { algorithm: "sha512", checksum: expected.toLowerCase() };
  return null;
}

// Windows may put MSYS2's curl first on PATH during QEMU builds; use the
// system curl.exe, which takes native paths.
function curlCommand() {
  if (process.platform !== "win32") return "curl";
  const system = join(process.env.SystemRoot || "C:\\Windows", "System32", "curl.exe");
  return existsSync(system) ? system : "curl.exe";
}

async function size(file) {
  try { return (await stat(file)).size; }
  catch (error) { if (error.code !== "ENOENT") throw error; return null; }
}

// curl --fail exits 22 on HTTP >= 400. Client errors other than timeouts and
// rate limits name a request the server will keep refusing.
function permanentHTTPFailure(code, http) {
  return code === 22 && /^4\d\d$/.test(http) && !["408", "425", "429"].includes(http);
}

function curl(url, file, resume) {
  return new Promise((done, reject) => {
    const child = spawn(curlCommand(), [
      "--disable", "--fail", "--location", "--max-redirs", "5",
      "--proto", "=https", "--proto-redir", "=https", "--connect-timeout", "20",
      "--speed-limit", "1", "--speed-time", "45",
      ...(resume ? ["--continue-at", "-"] : []), "--output", file,
      "--write-out", "%{http_code}", url,
    ], { stdio: ["ignore", "pipe", "inherit"] });
    let http = "";
    child.stdout.setEncoding("utf8").on("data", chunk => { http += chunk; });
    child.once("error", reject);
    child.once("close", code => done({ code, http }));
  });
}

// Optional HTTPS mirror prefix for GitHub release downloads (e.g.
// CAGE_GITHUB_MIRROR=https://mirror.example.com). Downloaded bytes are still
// verified against the pinned SHA-256 / SHA-512 checksum below.
export function resolveArtifactURL(rawURL, mirror = process.env.CAGE_GITHUB_MIRROR) {
  const parsed = new URL(rawURL);
  if (parsed.protocol !== "https:") {
    throw new Error("Build downloads require HTTPS and a pinned SHA-256 or SHA-512.");
  }
  const prefix = (mirror ?? "").trim();
  if (!prefix) return rawURL;
  if (parsed.hostname !== "github.com" && parsed.hostname !== "raw.githubusercontent.com") {
    return rawURL;
  }
  const mirrorURL = new URL(prefix);
  if (mirrorURL.protocol !== "https:" || mirrorURL.username || mirrorURL.password || mirrorURL.search || mirrorURL.hash) {
    throw new Error("CAGE_GITHUB_MIRROR must be an HTTPS URL without credentials, query, or fragment.");
  }
  return `${prefix.replace(/\/+$/, "")}/${rawURL}`;
}

// Restarting curl ourselves keeps the partial bytes between attempts.
// Each caller owns its partial file; no unfinished download is a cache hit.
// An attempt that added bytes does not use up a retry, so a slow link that
// keeps progressing converges; four attempts in a row without progress fail.
export async function fetchBuildArtifact(url, expected, destination, transfer = curl, { maxTransfers = 64, mirror = process.env.CAGE_GITHUB_MIRROR } = {}) {
  const pinned = pinnedDigest(expected);
  if (new URL(url).protocol !== "https:" || !pinned) {
    throw new Error("Build downloads require HTTPS and a pinned SHA-256 or SHA-512.");
  }
  const downloadURL = resolveArtifactURL(url, mirror);
  const { algorithm, checksum } = pinned;
  const name = basename(destination);
  if (await size(destination) !== null) {
    if (await digest(destination, algorithm) !== checksum) {
      throw new Error(`Build artifact checksum mismatch: ${name}. Remove the invalid cached file and retry.`);
    }
    return destination;
  }
  await mkdir(dirname(destination), { recursive: true });
  const stage = await mkdtemp(join(dirname(destination), ".cage-download-"));
  const part = join(stage, name + ".part");
  try {
    for (let attempt = 1, transfers = 1; attempt <= 4 && transfers <= maxTransfers; attempt++, transfers++) {
      const before = await size(part) ?? 0;
      const resume = before > 0;
      const { code, http } = await transfer(downloadURL, part, resume);
      if (permanentHTTPFailure(code, http) && !(resume && http === "416")) {
        // A missing or forbidden object does not appear on retry.
        throw new Error(`Could not download ${name} (curl exit ${code}, HTTP ${http}).`);
      }
      if (code === 0 || (resume && http === "416")) {
        if (await size(part) !== null && await digest(part, algorithm) === checksum) {
          await rename(part, destination);
          return destination;
        }
        if (!resume || attempt === 4) throw new Error(`Build artifact checksum mismatch: ${name}.`);
        // A changed object or invalid partial must be downloaded from byte 0.
        await rm(part, { force: true });
      } else if (attempt < 4 && resume && (code === 33 || code === 36)) {
        // Some proxies or origins do not support Range.
        await rm(part, { force: true });
      } else {
        const kept = await size(part) ?? 0;
        if (transfers === maxTransfers || (attempt === 4 && kept <= before)) {
          throw new Error(`Could not download ${name} (curl exit ${code}, HTTP ${http}).`);
        }
        if (kept > before) attempt = 0; // The next resume starts a fresh retry count.
      }
      console.warn(`Retrying download of ${name} (${(await size(part) ?? 0)} bytes kept).`);
      await delay(Math.max(attempt, 1) * 500);
    }
    throw new Error(`Could not download ${name}.`);
  } finally { await rm(stage, { recursive: true, force: true }); }
}

if (isMainModule(import.meta.url)) {
  if (process.argv.length !== 5) throw new Error("Usage: fetch-build-artifact.mjs URL SHA256|SHA512 FILE");
  await fetchBuildArtifact(...process.argv.slice(2));
}
