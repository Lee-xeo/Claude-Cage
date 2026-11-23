import assert from "node:assert/strict";
import test from "node:test";
import {
  buildAddressSuggestions,
  completeDotCom,
  findInlineCompletion,
  looksLikeURLInput,
} from "./browser-omnibox.mjs";

test("common site prefix suggests domain suffix and inline completion", () => {
  const items = buildAddressSuggestions({ query: "you" });
  assert.equal(items[0].kind, "site");
  assert.equal(items[0].url, "https://www.youtube.com/");
  assert.equal(items.at(-1).kind, "search");

  const inline = findInlineCompletion("you", items);
  assert.deepEqual(inline, {
    text: "youtube.com",
    suffix: "tube.com",
    url: "https://www.youtube.com/",
  });
});

test("favorites, history and open tabs merge and outrank generic sites", () => {
  const now = 1_700_000_000_000;
  const items = buildAddressSuggestions({
    query: "git",
    now,
    activeTabId: "tab-current",
    tabs: [
      { id: "tab-current", kind: "web", url: "https://github.com/current", title: "Current" },
      { id: "tab-other", kind: "web", url: "https://github.com/Lee-xeo/Claude-Cage", title: "Claude Cage" },
    ],
    favorites: [
      { id: "f1", url: "https://github.com/", title: "My GitHub" },
    ],
    history: [
      { id: "h1", url: "https://github.com/Lee-xeo/Claude-Cage/issues/774", title: "Issue 774", lastVisitTime: now - 60_000, visitCount: 5 },
      { id: "h2", url: "http://127.0.0.1:18080/?folder=/work", title: "Editor", lastVisitTime: now, visitCount: 99 },
    ],
  });
  assert.equal(items[0].kind, "favorite");
  assert.equal(items[0].url, "https://github.com/");
  assert.ok(items.some(item => item.kind === "tab" && item.tabId === "tab-other"));
  assert.ok(!items.some(item => item.url.includes("127.0.0.1:18080")));
});

test("publicOnly mode filters private addresses and omits search fallback when disabled", () => {
  const items = buildAddressSuggestions({
    query: "local",
    publicOnly: true,
    allowSearch: false,
    favorites: [
      { id: "f1", url: "http://localhost:3000/app", title: "Local App" },
      { id: "f2", url: "https://local.example.com/", title: "Public Local" },
    ],
  });
  assert.equal(items.length, 1);
  assert.equal(items[0].url, "https://local.example.com/");
  assert.equal(looksLikeURLInput("localhost:3000", { publicOnly: true }), null);
  assert.equal(looksLikeURLInput("localhost:3000", { publicOnly: false }), "http://localhost:3000/");
});

test("completeDotCom appends .com for bare host labels only", () => {
  assert.equal(completeDotCom("youtube"), "https://www.youtube.com/");
  assert.equal(completeDotCom("Claude"), "https://www.claude.com/");
  assert.equal(completeDotCom("youtube.com"), null);
  assert.equal(completeDotCom("hello world"), null);
  assert.equal(completeDotCom("https://youtube"), null);
});
