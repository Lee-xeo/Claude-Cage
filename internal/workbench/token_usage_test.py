import datetime as dt
import concurrent.futures
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import threading
import time
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("usage", Path(__file__).with_name("token_usage.py"))
usage = importlib.util.module_from_spec(spec)
spec.loader.exec_module(usage)


class UsageTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.home = Path(self.temp.name).resolve()
        self.root = self.home / "stats"
        self.accounts = self.home / "accounts"
        self.path = self.home / ".claude/projects/project/session.jsonl"
        self.path.parent.mkdir(parents=True)

    def row(self, key="msg_1", **extra):
        return {"type": "assistant", "uuid": "block-" + key,
                "timestamp": dt.datetime.now(dt.timezone.utc).isoformat(),
                "message": {"id": key, "model": "claude-sonnet-4-6", "content": "PRIVATE prompt credential",
                            "usage": dict(zip(usage.FIELDS, [10, 20, 30, 40]))}, **extra}

    def write(self, rows, path=None):
        path = path or self.path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text("".join(json.dumps(row) + "\n" for row in rows))
        return path

    def collect(self, **kwargs):
        return usage.collect(self.root, self.home, self.accounts, **kwargs)

    def test_streaming_copies_subagents_and_restart_preserve_counters_without_content(self):
        row = self.row()
        partial = self.row()
        partial["message"]["usage"]["output_tokens"] = 5
        self.write([partial, row, row])
        self.write([row], self.accounts / ("a" * 24) / "config/projects/other/copy.jsonl")
        self.write([self.row("msg_child")], self.path.parent / "session/subagents/agent-child.jsonl")
        first = self.collect()
        self.assertEqual(sum(d["output_tokens"] for d in first["days"]), 40)
        self.assertEqual(sum(d["requests"] for d in first["days"]), 2)
        self.assertIn("unknown", [d["source"] for d in first["days"]])
        self.assertEqual(first["days"], self.collect()["days"])
        self.path.unlink()
        self.assertEqual(first["days"], self.collect()["days"])
        self.assertNotIn("PRIVATE", json.dumps(first))
        for path in self.root.iterdir():
            self.assertNotIn(b"PRIVATE", path.read_bytes())
            self.assertNotIn(b"msg_1", path.read_bytes())
            self.assertNotIn(b"project/session", path.read_bytes())
            if os.name != "nt": self.assertEqual(path.stat().st_mode & 0o077, 0)

    def test_append_partial_line_replacement_and_malformed_records(self):
        row = self.row()
        self.path.write_text(json.dumps(row)[:-6])
        self.assertTrue(self.collect()["pending"])
        self.assertEqual(self.collect()["days"], [])
        self.write([row])
        first = self.collect()
        self.assertEqual(first["days"][0]["requests"], 1)
        with self.path.open("a") as stream:
            stream.write("not-json\n" + json.dumps(self.row("bad",timestamp=None)) + "\n" + json.dumps(self.row("msg_2")) + "\n")
        second = self.collect()
        self.assertEqual(second["days"][0]["requests"], 2)
        self.assertEqual(second["invalid_records"], 2)
        self.write([self.row("msg_3")])
        self.assertEqual(self.collect()["days"][0]["requests"], 3)

    def test_clear_does_not_reimport_history_or_modify_transcripts(self):
        old = self.row()
        old["timestamp"] = "2025-01-01T00:00:00Z"
        self.write([old])
        self.collect()
        cutoff = time.time()
        cleared = self.collect(cutoff=cutoff, scan=False)
        self.assertEqual(cleared["days"], [])
        self.assertTrue(self.path.exists())
        self.assertEqual(self.collect(cutoff=cutoff)["days"], [])
        self.assertEqual(self.collect(cutoff=0)["days"], [])
        self.write([old, self.row("msg_new")])
        self.assertEqual(self.collect(cutoff=cutoff)["days"][0]["requests"], 1)

    def test_symlinks_and_oversized_lines_cannot_escape_log_roots(self):
        outside = self.home / "private.jsonl"
        self.write([self.row("outside")], outside)
        try:
            (self.path.parent / "linked.jsonl").symlink_to(outside)
            (self.path.parent / "linked").symlink_to(self.home, target_is_directory=True)
        except OSError:
            self.skipTest("symlink privilege unavailable")
        with patch.object(usage, "MAX_LINE", 1024):
            self.path.write_text("x" * 1500 + "\n" + json.dumps(self.row()) + "\n")
            result = self.collect()
        self.assertEqual(result["days"][0]["requests"], 1)
        self.assertEqual(result["invalid_records"], 1)
        self.assertTrue(outside.exists())
        database = self.root / "counters.sqlite3"
        database.unlink()
        database.symlink_to(outside)
        with self.assertRaises(ValueError): self.collect()

    def test_read_only_snapshot_and_local_calendar_boundary(self):
        if not hasattr(time,"tzset"):
            self.skipTest("runtime timezone override unavailable")
        row = self.row()
        row["timestamp"] = "2026-01-01T23:30:00Z"
        self.write([row])
        with patch.dict(os.environ, {"TZ": "UTC-8"}):
            time.tzset()
            try:
                result = self.collect()
                self.assertEqual(result["days"][0]["date"], "2026-01-02")
                self.write([row, self.row("later")])
                self.assertEqual(self.collect(scan=False)["days"], result["days"])
            finally:
                os.environ.pop("TZ", None)
                time.tzset()

    def test_oversized_record_skipping_resumes_within_scan_budget(self):
        self.path.write_text("x" * 5000 + "\n" + json.dumps(self.row()) + "\n")
        with patch.object(usage,"MAX_LINE",1024), patch.object(usage,"MAX_BYTES",1500):
            results = [self.collect() for _ in range(5)]
        self.assertTrue(results[0]["pending"])
        self.assertEqual(results[0]["days"],[])
        self.assertEqual(results[-1]["days"][0]["requests"],1)
        self.assertEqual(results[-1]["invalid_records"],1)

    def test_clear_serializes_with_an_old_collector_process(self):
        old=self.row(timestamp="2025-01-01T00:00:00Z")
        self.write([old])
        self.collect()
        entered, release=threading.Event(),threading.Event()
        original=usage.sources
        def blocked_sources(home,accounts):
            entered.set()
            if not release.wait(5): raise RuntimeError("test barrier timeout")
            yield from original(home,accounts)
        with concurrent.futures.ThreadPoolExecutor() as pool:
            with patch.object(usage,"sources",blocked_sources):
                future=pool.submit(self.collect)
                self.assertTrue(entered.wait(3))
                cutoff=time.time()
                try:
                    with self.assertRaises(sqlite3.OperationalError):
                        self.collect(cutoff=cutoff,scan=False)
                finally:
                    release.set()
                future.result(timeout=3)
        self.assertEqual(self.collect(cutoff=cutoff,scan=False)["days"],[])
        self.assertEqual(self.collect(cutoff=0)["days"],[])

    def test_entrypoints_are_per_request_and_copies_never_claim_a_new_origin(self):
        original=self.row("copied",entrypoint="cli")
        copied={**original,"entrypoint":"claude-desktop"}
        records=[original,copied,original]
        expected={"unknown":2,"terminal":1,"desktop_code":1,"cowork":1,"ide":1,"automation":1,"remote":1}
        for index,entrypoint in enumerate(["cli","claude-desktop-3p","local-agent","claude-vscode","sdk-ts","remote_mobile","future-client"]):
            records.append(self.row("request-"+str(index),entrypoint=entrypoint))
        self.write(records)
        result=self.collect()
        self.assertEqual({day["client"]:day["requests"] for day in result["days"]},expected)
        self.assertEqual(sum(day["input_tokens"] for day in result["days"]),80)
        # Repeated scanning or a later original copy cannot erase a conflict.
        self.write(records+[original])
        self.assertEqual(result["days"],self.collect()["days"])

    def test_old_database_migration_keeps_deleted_history_and_backfills_retained_origins(self):
        row=self.row(entrypoint="claude-desktop")
        self.write([row])
        self.root.mkdir(mode=0o700)
        db=sqlite3.connect(self.root/"counters.sqlite3")
        db.executescript("""
            CREATE TABLE requests (id TEXT PRIMARY KEY,stamp REAL,model TEXT,source TEXT,input INTEGER,output INTEGER,read INTEGER,write INTEGER);
            CREATE TABLE files (id TEXT PRIMARY KEY,signature TEXT,offset INTEGER,modified INTEGER,size INTEGER,invalid INTEGER,skipping INTEGER);
            CREATE TABLE meta (key TEXT PRIMARY KEY,value REAL);
            INSERT INTO meta VALUES ('cutoff',0);
        """)
        timestamp=dt.datetime.fromisoformat(row["timestamp"]).timestamp()
        db.execute("INSERT INTO requests VALUES (?,?,?,?,?,?,?,?)",(usage.digest("msg_1"),timestamp,"claude-sonnet-4-6","default",10,20,30,40))
        db.execute("INSERT INTO requests VALUES (?,?,?,?,?,?,?,?)",(usage.digest("deleted"),timestamp,"claude-sonnet-4-6","default",50,0,0,0))
        info=self.path.stat()
        signature=usage.digest(f"{info.st_dev}:{info.st_ino}:"+self.path.read_bytes()[:64].hex())
        file_key=usage.digest("default:"+str(self.path.relative_to(self.home/".claude")))
        db.execute("INSERT INTO files VALUES (?,?,?,?,?,?,?)",(file_key,signature,info.st_size,info.st_mtime_ns,info.st_size,0,0))
        db.commit();db.close()
        result=self.collect()
        self.assertEqual({day["client"]:day["input_tokens"] for day in result["days"]},{"desktop_code":10,"unknown":50})
        self.assertEqual(sum(day["requests"] for day in result["days"]),2)
        self.assertEqual(sum(sum(day[field] for field in usage.FIELDS) for day in result["days"]),150)
        self.assertEqual(self.collect()["days"],result["days"])


if __name__ == "__main__":
    unittest.main()
