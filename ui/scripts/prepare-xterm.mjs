// Backport xtermjs/xterm.js#6200 to the ESM bundle shipped in 6.0.0.
// A modifier alone must preserve the gate used to deduplicate IME input.
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";

const packageRoot = new URL("../node_modules/@xterm/xterm/", import.meta.url);
const manifest = JSON.parse(readFileSync(new URL("package.json", packageRoot), "utf8"));
const source = readFileSync(new URL(manifest.module, packageRoot), "utf8");
const original = "_keyDown(e){if(this._keyDownHandled=!1,this._keyDownSeen=!0,";
if (manifest.version !== "6.0.0" || source.split(original).length !== 2) {
  throw new Error("Review the xterm IME backport when updating @xterm/xterm (upstream #6200)");
}

const replacement = `_keyDown(e){
  this._keyDownHandled = false;
  if (![16, 17, 18, 91, 92, 93, 224].includes(e.keyCode) && e.key !== "Meta") {
    this._keyDownSeen = true;
  }
  if (`;
const generated = new URL("../.generated/", import.meta.url);
mkdirSync(generated, { recursive: true });
writeFileSync(new URL("xterm.mjs", generated),
  source.replace(original, replacement).replace("//# sourceMappingURL=xterm.mjs.map", ""));
