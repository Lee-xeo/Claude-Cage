import { spawn } from "node:child_process";
import { createHash } from "node:crypto";
import { createReadStream } from "node:fs";
import { mkdir, mkdtemp, rename, rm, stat } from "node:fs/promises";
import { basename, dirname, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { isMainModule } from "./is-main-module.mjs";

export async function sha256(file) {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(file)) hash.update(chunk);
  return hash.digest("hex");
}

async function size(file) {
  try { return (await stat(file)).size; }
  catch (error) { if (error.code !== "ENOENT") throw error; return null; }
}

function curl(url, file, resume) {
  return new Promise((done, reject) => {
    const child = spawn(process.platform === "win32" ? "curl.exe" : "curl", [
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

// Restarting curl ourselves keeps the partial bytes between attempts.
// Each caller owns its partial file; no unfinished download is a cache hit.
export async function fetchBuildArtifact(url, expected, destination, transfer = curl) {
  if (new URL(url).protocol !== "https:" || !/^[a-f0-9]{64}$/i.test(expected)) {
    throw new Error("Build downloads require HTTPS and a pinned SHA-256.");
  }
  const name = basename(destination);
  const checksum = expected.toLowerCase();
  if (await size(destination) !== null) {
    if (await sha256(destination) !== checksum) {
      throw new Error(`Build artifact checksum mismatch: ${name}. Remove the invalid cached file and retry.`);
    }
    return destination;
  }
  await mkdir(dirname(destination), { recursive: true });
  const stage = await mkdtemp(join(dirname(destination), ".cage-download-"));
  const part = join(stage, name + ".part");
  try {
    for (let attempt = 1; attempt <= 4; attempt++) {
      const resume = (await size(part) ?? 0) > 0;
      const { code, http } = await transfer(url, part, resume);
      if (code === 0 || (resume && http === "416")) {
        if (await size(part) !== null && await sha256(part) === checksum) {
          await rename(part, destination);
          return destination;
        }
        if (!resume || attempt === 4) throw new Error(`Build artifact checksum mismatch: ${name}.`);
        // A changed object or invalid partial must be downloaded from byte 0.
        await rm(part, { force: true });
      } else if (attempt < 4 && resume && (code === 33 || code === 36)) {
        // Some proxies or origins do not support Range.
        await rm(part, { force: true });
      } else if (attempt === 4) {
        throw new Error(`Could not download ${name} (curl exit ${code}, HTTP ${http}).`);
      }
      console.warn(`Retrying download of ${name} (${attempt + 1}/4).`);
      await delay(attempt * 500);
    }
    throw new Error(`Could not download ${name}.`);
  } finally { await rm(stage, { recursive: true, force: true }); }
}

if (isMainModule(import.meta.url)) {
  if (process.argv.length !== 5) throw new Error("Usage: fetch-build-artifact.mjs URL SHA256 FILE");
  await fetchBuildArtifact(...process.argv.slice(2));
}
