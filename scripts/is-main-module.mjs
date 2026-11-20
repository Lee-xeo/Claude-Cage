import { realpathSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

// Node resolves symlinks for the entry module's import.meta.url, but
// process.argv[1] keeps the path the caller typed. Comparing real paths keeps
// CLI entry points working from checkouts under symlinked directories such as
// macOS /tmp -> /private/tmp.
export function isMainModule(moduleURL, entry = process.argv[1]) {
  if (!entry) return false;
  try {
    return realpathSync(fileURLToPath(moduleURL)) === realpathSync(resolve(entry));
  } catch {
    return false;
  }
}
