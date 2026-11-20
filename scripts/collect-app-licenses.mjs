#!/usr/bin/env node
import { cpSync, mkdirSync, readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { resolve, join } from "node:path";

// Collect the licenses of the packages actually shipped in the workbench.
const target = process.argv[2];
if (!target) throw new Error("Usage: node scripts/collect-app-licenses.mjs <output-directory>");
const root = resolve(import.meta.dirname, "..");
mkdirSync(target, { recursive: true });
for (const file of ["LICENSE", "NOTICE", "THIRD_PARTY_LICENSES"]) {
  cpSync(join(root, file), join(target, file));
}
cpSync(join(root, "internal/exitpolicy/data"), join(target, "exit-country-data"), { recursive: true });
const lock = JSON.parse(readFileSync(join(root, "ui/package-lock.json"), "utf8"));
const notices = [];
for (const [path, entry] of Object.entries(lock.packages)) {
  if (!path || entry.dev) continue;
  const source = join(root, "ui", path);
  const name = JSON.parse(readFileSync(join(source, "package.json"), "utf8")).name;
  const dest = join(target, "npm", name);
  const files = readdirSync(source).filter((file) => /^(licen[sc]e|copying|notice)(\.|$)/i.test(file) && statSync(join(source, file)).isFile());
  if (!files.length) throw new Error(`Missing license for ${name}@${entry.version}`);
  mkdirSync(dest, { recursive: true });
  for (const file of files) cpSync(join(source, file), join(dest, file));
  notices.push(`${name}@${entry.version}: ${entry.license || "see license files"}`);
}
writeFileSync(join(target, "npm", "NOTICE.txt"), `${notices.join("\n")}\n`);
const novnc = join(root, "ui/vendor/novnc");
for (const file of ["LICENSE.txt", "AUTHORS", "NOTICE.cage", "docs", "vendor/pako/LICENSE"]) {
  mkdirSync(join(target, "novnc", file.includes("/") ? file.slice(0, file.lastIndexOf("/")) : ""), { recursive: true });
  cpSync(join(novnc, file), join(target, "novnc", file), { recursive: true });
}
// MPL-covered source and local modifications travel with the executable.
cpSync(join(root, "ui/.generated/novnc"), join(target, "novnc-source"), { recursive: true });

// The embedded Tailscale node is a separate guest executable and module.
// Name each module from Go's metadata rather than its cache path, which
// differs with a custom GOMODCACHE. -buildvcs=false keeps source archives
// without .git working while GOFLAGS stays cleared.
const tailnetModules = new Map();
const moduleFormat = '{{with .Module}}{{with .Replace}}{{.Dir}}{{else}}{{.Dir}}{{end}}{{"\\t"}}{{.Path}}{{"\\t"}}{{.Version}}{{end}}';
for (const [os, arch] of [["linux", "amd64"], ["darwin", "arm64"], ["windows", "amd64"]]) {
  const lines = execFileSync("go", ["list", "-buildvcs=false", "-deps", "-f", moduleFormat, "."], {
    cwd: join(root, "tools/tailnet"), env: { ...process.env, GOOS: os, GOARCH: arch, CGO_ENABLED: "0", GOFLAGS: "" }, encoding: "utf8",
  });
  for (const line of lines.split(/\r?\n/).filter(Boolean)) {
    const [dir, path, version] = line.split("\t");
    if (!dir || !path) throw Error(`Missing guest dependency module metadata: ${line}`);
    if (resolve(dir) !== join(root, "tools/tailnet")) tailnetModules.set(dir, version ? `${path}@${version}` : path);
  }
}
const tailnetNotices = [];
for (const [dir, name] of tailnetModules) {
  const dest = join(target, "tailnet-go", name);
  const licenses = readdirSync(dir).filter(file => /^(licen[sc]e|copying|notice|patents|authors)(\.|$)/i.test(file) && statSync(join(dir, file)).isFile());
  if (!licenses.length) throw Error(`Missing guest dependency license: ${name}`);
  mkdirSync(dest, { recursive: true });
  for (const file of licenses) cpSync(join(dir, file), join(dest, file));
  tailnetNotices.push(name);
}
writeFileSync(join(target, "tailnet-go", "NOTICE.txt"), tailnetNotices.sort().join("\n") + "\n");
