import assert from "node:assert/strict";
import { readdirSync, readFileSync } from "node:fs";
import test from "node:test";

const read = path => readFileSync(new URL(path, import.meta.url), "utf8");

test("the Russian mobile catalog covers mobile labels and preserves interpolation", () => {
  const mobile = JSON.parse(read("./mobile-cli/ru.json"));
  const desktop = JSON.parse(read("./locales/ru.json"));
  const catalog = { ...desktop, ...mobile };
  const args = message => (message.match(/\{\d+\}/g) || []).sort();
  for (const [source, value] of Object.entries(mobile)) {
    assert.ok(value.trim(), source);
    assert.doesNotMatch(value, /\p{Script=Han}/u, source);
    assert.deepEqual(args(value), args(source), source);
  }
  for (const file of readdirSync(new URL("./mobile-cli/", import.meta.url)).filter(file => /\.(jsx|mjs)$/.test(file))) {
    for (const [, literal] of read("./mobile-cli/" + file).matchAll(/\bmobileText\(("(?:[^"\\]|\\.)*")/g)) {
      const source = JSON.parse(literal);
      assert.ok(catalog[source], `${file}: ${source}`);
    }
  }
});
