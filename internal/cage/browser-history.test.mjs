import assert from "node:assert/strict";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";
import { BrowserHistory, PhoneHistoryStore, isHistoryURL, normalizeHistoryInput } from "./browser-history.mjs";
import { webAddress } from "./browser-phone-network.mjs";

test("isHistoryURL keeps ordinary HTTP(S) URLs and excludes credentials or editor loopback", () => {
  assert.equal(isHistoryURL("https://www.youtube.com/watch?v=1"), true);
  assert.equal(isHistoryURL("http://localhost:3000/app"), true);
  assert.equal(isHistoryURL("http://127.0.0.1:18080/?folder=/work"), false);
  assert.equal(isHistoryURL("https://user:pass@example.com/"), false);
  assert.equal(isHistoryURL("chrome://settings"), false);
  assert.equal(isHistoryURL("chrome-extension://abcdefghijklmnopabcdefghijklmnop/control.html"), false);
  assert.equal(isHistoryURL("about:blank"), false);
});

test("normalizeHistoryInput validates mode, query, limit, url and range", () => {
  assert.deepEqual(normalizeHistoryInput({}), { mode: "list", query: "", limit: 100, startTime: 0 });
  assert.deepEqual(normalizeHistoryInput({ mode: "list", query: "  yt ", limit: 20 }), { mode: "list", query: "yt", limit: 20, startTime: 0 });
  assert.deepEqual(normalizeHistoryInput({ mode: "delete", url: "https://example.com/" }), { mode: "delete", url: "https://example.com/" });
  assert.deepEqual(normalizeHistoryInput({ mode: "clear", range: "week" }), { mode: "clear", range: "week" });
  assert.throws(() => normalizeHistoryInput({ mode: "drop" }));
  assert.throws(() => normalizeHistoryInput({ mode: "list", limit: 999 }));
  assert.throws(() => normalizeHistoryInput({ mode: "delete", url: "javascript:alert(1)" }));
  assert.throws(() => normalizeHistoryInput({ mode: "clear", range: "year" }));
});

test("BrowserHistory repairs missing controller session and filters returned URLs", async () => {
  let repaired = 0;
  let calls = 0;
  const history = new BrowserHistory({
    control: async repair => {
      if (repair) repaired++;
      return repair ? "session-2" : "session-1";
    },
    cdp: async (method, params, sessionId) => {
      calls++;
      assert.equal(method, "Runtime.evaluate");
      if (sessionId === "session-1") {
        return { exceptionDetails: { text: "ReferenceError: manageCageHistory is not defined" } };
      }
      assert.match(params.expression, /^manageCageHistory\(/);
      return {
        result: {
          value: {
            items: [
              { id: "1", url: "https://www.youtube.com/", title: "YouTube", lastVisitTime: 2000, visitCount: 3 },
              { id: "2", url: "http://127.0.0.1:18080/?folder=/work", title: "Editor", lastVisitTime: 3000, visitCount: 1 },
              { id: "3", url: "chrome://extensions/", title: "Extensions", lastVisitTime: 4000, visitCount: 1 },
            ],
          },
        },
      };
    },
  });
  const result = await history.request({ mode: "list", query: "you", limit: 10 });
  assert.equal(repaired, 1);
  assert.equal(calls, 2);
  assert.deepEqual(result, {
    items: [{ id: "1", url: "https://www.youtube.com/", title: "YouTube", lastVisitTime: 2000, visitCount: 3 }],
  });
});

test("PhoneHistoryStore persists public visits, searches, deletes and clears ranges per profile", async t => {
  const dir = await mkdtemp(join(tmpdir(), "cage-phone-history-"));
  t.after(() => rm(dir, { recursive: true, force: true }));
  let clock = 1_700_000_000_000;
  const store = new PhoneHistoryStore({ directory: dir, validateURL: webAddress, now: () => clock });
  const profile = "a".repeat(64);

  await store.record(profile, "http://127.0.0.1:3000/private", "Private");
  await store.record(profile, "https://github.com/Lee-xeo/Claude-Cage", "Claude Cage");
  clock += 1000;
  await store.record(profile, "https://github.com/Lee-xeo/Claude-Cage", "Claude Cage Repo");
  clock += 2 * 60 * 60 * 1000;
  await store.record(profile, "https://www.youtube.com/watch?v=abc", "Live Stream");

  const listed = await store.request(profile, { mode: "list", query: "", limit: 10 });
  assert.equal(listed.items.length, 2);
  assert.equal(listed.items[0].url, "https://www.youtube.com/watch?v=abc");
  assert.equal(listed.items[1].title, "Claude Cage Repo");
  assert.equal(listed.items[1].visitCount, 1);

  const searched = await store.request(profile, { mode: "list", query: "youtube" });
  assert.equal(searched.items.length, 1);

  await store.request(profile, { mode: "clear", range: "hour" });
  const afterHour = await store.request(profile, { mode: "list" });
  assert.deepEqual(afterHour.items.map(i => i.url), ["https://github.com/Lee-xeo/Claude-Cage"]);

  await store.request(profile, { mode: "delete", url: "https://github.com/Lee-xeo/Claude-Cage" });
  const afterDelete = await store.request(profile, { mode: "list" });
  assert.equal(afterDelete.items.length, 0);
});
