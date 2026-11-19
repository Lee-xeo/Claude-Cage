"""Read local Claude Code usage; persist counters, never transcript content."""
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import sqlite3
import stat
import sys
import time

FIELDS = ("input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens")
MAX_LINE = 4 << 20
MAX_BYTES = 32 << 20
MODEL = re.compile(r"[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,127}\Z")
ACCOUNT = re.compile(r"[a-f0-9]{24}\Z")
ENTRYPOINTS = {
    "cli": "terminal",
    "claude-desktop": "desktop_code", "claude-desktop-3p": "desktop_code",
    "local-agent": "cowork", "local_agent": "cowork",
    "claude-vscode": "ide",
    "sdk-cli": "automation", "sdk-ts": "automation", "sdk-py": "automation",
    "claude-code-github-action": "automation",
    "remote": "remote", "remote_desktop": "remote", "remote_mobile": "remote",
    "remote_cowork": "remote", "ssh-remote": "remote",
    "claude_in_slack": "other", "claude-in-slack": "other", "claude-in-teams": "other",
}


def digest(value):
    return hashlib.sha256(value.encode()).hexdigest()


def ordinary(path):
    # Reject links and Windows junctions at every traversed directory boundary.
    return not path.is_symlink() and not (hasattr(path, "is_junction") and path.is_junction())


def private_directory(path):
    if not ordinary(path):
        raise ValueError("unsafe statistics directory")
    mode = 0o777 if os.name == "nt" and os.environ.get("CAGE_REMOTE_APP_CONTAINER") == "1" else 0o700
    path.mkdir(mode=mode, parents=True, exist_ok=True)
    if os.name != "nt":
        info = path.stat()
        if info.st_uid != os.getuid() or info.st_mode & 0o077:
            raise ValueError("statistics directory must be private")


def sources(home, accounts):
    yield "default", home / ".claude"
    if accounts.is_dir() and ordinary(accounts):
        for entry in sorted(accounts.iterdir()):
            if ACCOUNT.fullmatch(entry.name) and ordinary(entry):
                yield entry.name, entry / "config"


def transcripts(config):
    if not ordinary(config):
        return
    projects = config / "projects"
    if not projects.is_dir() or not ordinary(projects):
        return
    # Only documented transcript positions. Never walk tool outputs, memory,
    # credentials, backups, orphaned transcripts or arbitrary symlink targets.
    for project in sorted(projects.iterdir()):
        if not ordinary(project) or not project.is_dir():
            continue
        for item in sorted(project.iterdir()):
            if not ordinary(item):
                continue
            if item.is_file() and item.suffix == ".jsonl" and ".orphaned-" not in item.name:
                yield item
            elif item.is_dir():
                agents = item / "subagents"
                if agents.is_dir() and ordinary(agents):
                    for child in sorted(agents.iterdir()):
                        if ordinary(child) and child.is_file() and child.suffix == ".jsonl":
                            yield child


def usage_record(row, cutoff, now):
    if not isinstance(row, dict) or row.get("type") != "assistant":
        return None
    message = row.get("message")
    if not isinstance(message, dict) or message.get("model") == "<synthetic>":
        return None
    usage = message.get("usage")
    if not isinstance(usage, dict):
        return None
    key = message.get("id") or row.get("uuid")
    if not isinstance(key, str) or not 1 <= len(key) <= 256:
        raise ValueError("missing request identity")
    raw_stamp = row.get("timestamp")
    if not isinstance(raw_stamp, str):
        raise ValueError("invalid timestamp")
    stamp = dt.datetime.fromisoformat(raw_stamp.replace("Z", "+00:00"))
    if stamp.tzinfo is None:
        raise ValueError("timestamp has no timezone")
    timestamp = stamp.timestamp()
    if not 0 < timestamp <= now + 300:
        raise ValueError("invalid timestamp")
    if timestamp < cutoff:
        return None
    counters = tuple(usage.get(field, 0) for field in FIELDS)
    if any(type(n) is not int or not 0 <= n <= 10**12 for n in counters):
        raise ValueError("invalid counters")
    # Cache fields can legitimately be absent, but input/output must be reported.
    if not all(field in usage for field in FIELDS[:2]):
        raise ValueError("incomplete counters")
    model = message.get("model", "unknown")
    if not isinstance(model, str) or not MODEL.fullmatch(model):
        model = "unknown"
    entrypoint = row.get("entrypoint")
    client = ENTRYPOINTS.get(entrypoint, "unknown") if isinstance(entrypoint, str) else "unknown"
    return digest(key), timestamp, model, counters, client


def collect(root, home, accounts, cutoff=0, scan=True):
    private_directory(root)
    database = root / "counters.sqlite3"
    for suffix in ("", "-journal", "-wal", "-shm"):
        if not ordinary(Path(str(database) + suffix)):
            raise ValueError("unsafe statistics file")
    previous_mask = os.umask(0o077)
    try:
        db = sqlite3.connect(database, timeout=1)
    finally:
        os.umask(previous_mask)
    try:
        db.executescript("""
            CREATE TABLE IF NOT EXISTS requests (
                id TEXT PRIMARY KEY, stamp REAL NOT NULL, model TEXT NOT NULL, source TEXT NOT NULL,
                input INTEGER NOT NULL, output INTEGER NOT NULL, read INTEGER NOT NULL, write INTEGER NOT NULL);
            CREATE TABLE IF NOT EXISTS files (
                id TEXT PRIMARY KEY, signature TEXT NOT NULL, offset INTEGER NOT NULL,
                modified INTEGER NOT NULL, size INTEGER NOT NULL, invalid INTEGER NOT NULL,
                skipping INTEGER NOT NULL);
            CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value REAL NOT NULL);
        """)
        # Serialize cutoff reads with every import, including old helper
        # processes which outlive their SSH/controller connection.
        db.execute("BEGIN IMMEDIATE")
        # Additive migration: keep numeric history, and reread retained files
        # once to recover entrypoints. Deleted history stays unclassified.
        columns = {row[1] for row in db.execute("PRAGMA table_info(requests)")}
        if "client" not in columns:
            db.execute("ALTER TABLE requests ADD COLUMN client TEXT NOT NULL DEFAULT 'unknown'")
            db.execute("ALTER TABLE requests ADD COLUMN client_conflict INTEGER NOT NULL DEFAULT 0")
            db.execute("DELETE FROM files")
        old_cutoff = db.execute("SELECT value FROM meta WHERE key='cutoff'").fetchone()
        cutoff = max(cutoff, old_cutoff[0] if old_cutoff else 0)
        if old_cutoff is None or old_cutoff[0] < cutoff:
            # A persistent cutoff prevents a later refresh from importing cleared history.
            db.execute("DELETE FROM requests")
            db.execute("DELETE FROM files")
            db.execute("INSERT OR REPLACE INTO meta VALUES ('cutoff', ?)", (cutoff,))
            db.execute("INSERT OR REPLACE INTO meta VALUES ('updated', ?)", (time.time(),))
        pending = False
        scanned_bytes = 0
        deadline = time.monotonic() + 5
        now = time.time()
        if scan:
            for source, config in sources(home, accounts):
                for path in transcripts(config):
                    if scanned_bytes >= MAX_BYTES or time.monotonic() > deadline:
                        pending = True
                        break
                    file_key = digest(source + ":" + str(path.relative_to(config)))
                    try:
                        fd = os.open(path, os.O_RDONLY | getattr(os, "O_NOFOLLOW", 0) | getattr(os, "O_BINARY", 0))
                        with os.fdopen(fd, "rb") as stream:
                            info = os.fstat(stream.fileno())
                            if not stat.S_ISREG(info.st_mode):
                                continue
                            # Fixed prefix length avoids invalidating the cursor on normal appends.
                            signature = digest(f"{info.st_dev}:{info.st_ino}:" + stream.read(min(64, info.st_size)).hex())
                            saved = db.execute("SELECT signature,offset,modified,size,invalid,skipping FROM files WHERE id=?", (file_key,)).fetchone()
                            offset, invalid, skipping = 0, 0, False
                            if saved and saved[0] == signature and info.st_size >= saved[1]:
                                offset, invalid, skipping = saved[1], saved[4], bool(saved[5])
                                if info.st_size == saved[3] and info.st_mtime_ns != saved[2]:
                                    offset, invalid, skipping = 0, 0, False
                            stream.seek(offset)
                            while True:
                                if scanned_bytes >= MAX_BYTES or time.monotonic() > deadline:
                                    pending = True
                                    break
                                start = stream.tell()
                                line = stream.readline(MAX_LINE + 1)
                                if not line:
                                    break
                                scanned_bytes += len(line)
                                if skipping or len(line) > MAX_LINE:
                                    # Persist a skip cursor, so an oversized record obeys the
                                    # same budget and cannot trap every subsequent refresh.
                                    if not skipping:
                                        invalid += 1
                                    skipping = not line.endswith(b"\n")
                                    offset = stream.tell()
                                    continue
                                if not line.endswith(b"\n"):
                                    pending = True
                                    offset = start
                                    break
                                offset = stream.tell()
                                try:
                                    record = usage_record(json.loads(line), cutoff, now)
                                    if record is None:
                                        continue
                                    key, timestamp, model, counters, client = record
                                    db.execute("""INSERT INTO requests
                                        (id,stamp,model,source,input,output,read,write,client) VALUES (?,?,?,?,?,?,?,?,?)
                                        ON CONFLICT(id) DO UPDATE SET
                                        stamp=min(stamp,excluded.stamp),
                                        source=CASE WHEN source=excluded.source THEN source ELSE 'unknown' END,
                                        client=CASE
                                            WHEN client_conflict=1 THEN 'unknown'
                                            WHEN client='unknown' THEN excluded.client
                                            WHEN excluded.client='unknown' OR client=excluded.client THEN client
                                            ELSE 'unknown' END,
                                        client_conflict=CASE WHEN client_conflict=1 OR
                                            (client!='unknown' AND excluded.client!='unknown' AND client!=excluded.client)
                                            THEN 1 ELSE 0 END,
                                        input=max(input,excluded.input), output=max(output,excluded.output),
                                        read=max(read,excluded.read), write=max(write,excluded.write)""",
                                               (key, timestamp, model, source, *counters, client))
                                except (ValueError, KeyError, TypeError, OverflowError):
                                    invalid += 1
                            db.execute("INSERT OR REPLACE INTO files VALUES (?,?,?,?,?,?,?)",
                                       (file_key, signature, offset, info.st_mtime_ns, info.st_size, invalid, int(skipping)))
                    except OSError:
                        pending = True
            db.execute("INSERT OR REPLACE INTO meta VALUES ('updated', ?)", (now,))
        db.commit()
        # SQLite localtime uses the runtime OS timezone on all three platforms,
        # including Windows without a separate IANA timezone database.
        daily = []
        for row in db.execute("""SELECT date(stamp,'unixepoch','localtime'),model,source,client,
                sum(input),sum(output),sum(read),sum(write),count(*)
                FROM requests GROUP BY 1,2,3,4 ORDER BY 1,2,3,4"""):
            daily.append(dict(zip(("date", "model", "source", "client", *FIELDS, "requests"), row)))
        updated = db.execute("SELECT value FROM meta WHERE key='updated'").fetchone()
        invalid = db.execute("SELECT coalesce(sum(invalid),0) FROM files").fetchone()[0]
        return {"version": 1, "updated_at": int(updated[0]) if updated else 0,
                "timezone": time.tzname[time.localtime().tm_isdst], "today": time.strftime("%Y-%m-%d"),
                "cutoff": cutoff, "pending": pending, "invalid_records": invalid, "days": daily}
    finally:
        db.close()


def main():
    request = json.load(sys.stdin)
    cutoff = request.get("cutoff", 0)
    if type(cutoff) not in (int, float) or not 0 <= cutoff <= time.time() + 300:
        raise ValueError("invalid cutoff")
    home = Path.home()
    if os.name == "nt":
        root = Path(os.environ["CAGE_CLI_STATE_ROOT"]).parent / "token-usage"
        accounts = Path(os.environ["CAGE_CLI_ACCOUNT_ROOT"])
    else:
        root = home / ".local/share/cage/token-usage"
        accounts = home / ".local/share/cage/claude-accounts"
    print(json.dumps(collect(root, home, accounts, cutoff, request.get("scan", True)), separators=(",", ":")))


if __name__ == "__main__":
    try:
        main()
    except Exception:
        # Never include a log line, path, SQL value or exception in the bridge response.
        print('{"error":"statistics_unavailable"}')
        sys.exit(1)
