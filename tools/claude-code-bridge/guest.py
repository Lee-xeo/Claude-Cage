"""Run inside the configured Cage guest. The one-shot SSH client may exit at any time."""
import base64
import fcntl
import json
import os
from pathlib import Path
import sqlite3
import subprocess
import sys
import tempfile
import time
import uuid


ROOT = Path.home() / ".cache" / "cage-claude-bridge"
WORKSPACE = "/home/ubuntu/.claude-cage-workspace"
CLAUDE = "/home/cage/.local/lib/node_modules/@anthropic-ai/claude-code/bin/claude.exe"
os.umask(0o077)


def connect():
    return sqlite3.connect(ROOT / "jobs.sqlite3", timeout=8)


def finish(db, job, status, body):
    db.execute("UPDATE jobs SET status=?, body=? WHERE id=?", (status, body, job))
    if status == "completed":
        db.execute("UPDATE chats SET session=(SELECT session FROM jobs WHERE id=?) WHERE first_job=?", (job, job))
    db.execute("UPDATE chats SET first_job=NULL WHERE first_job=?", (job,))


def worker(job, session, resume):
    try:
        command = [CLAUDE, "-p", "--output-format", "json",
                   "--resume" if resume else "--session-id", session]
        attachments = ROOT / "files" / session
        if attachments.is_dir():
            command.extend(["--add-dir", str(attachments)])
        proc = subprocess.run(command, cwd=WORKSPACE, stdin=sys.stdin.buffer,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                              env={**os.environ, "CLAUDE_CONFIG_DIR": "/home/cage/.claude"})
        try:
            reply = json.loads(proc.stdout)
        except ValueError:
            raise ValueError(proc.stderr.decode(errors="replace").strip() or "Claude CLI 未返回有效回复")
        if proc.returncode or reply.get("is_error") or not isinstance(reply.get("result"), str):
            raise ValueError(reply.get("result") or "Claude CLI 执行失败")
        if reply.get("session_id") != session:
            raise ValueError("Claude CLI 返回了不同的会话 ID")
        status, body = "completed", reply["result"]
    except Exception as exc:
        status, body = "failed", str(exc)
    with connect() as db:
        finish(db, job, status, body)


def lookup(db, job):
    row = db.execute("SELECT session, status, body FROM jobs WHERE id=?", (job,)).fetchone()
    if row is None:
        raise ValueError("找不到这个云端任务：" + job)
    session, status, body = row
    if status == "running":
        # The detached worker inherits this lock. A guest restart releases it, too.
        with (ROOT / "locks" / job).open("rb") as lock:
            try:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError:
                pass
            else:
                status, body = db.execute("SELECT status, body FROM jobs WHERE id=?", (job,)).fetchone()
                if status == "running":
                    status, body = "failed", "云端任务已中断。"
                    finish(db, job, status, body)
    result = {"jobId": job, "sessionId": session, "status": status}
    if status != "running":
        result["text" if status == "completed" else "message"] = body
    return result


def make_prompt(request, job, session):
    paths = []
    for i, item in enumerate(request["files"]):
        name = item["name"]
        if Path(name).name != name or name in ("", ".", ".."):
            raise ValueError("无效的附件名")
        directory = ROOT / "files" / session / job / str(i + 1)
        directory.mkdir(parents=True)
        path = directory / name
        path.write_bytes(base64.b64decode(item["data"], validate=True))
        paths.append(str(path))
    prompt = request["text"]
    if paths:
        prompt += "\n\n[本轮用户附件，可用 Read 读取]\n" + "\n".join(paths)
    return prompt


def submit(db, request):
    job, chat = request["jobId"], request.get("threadId")
    db.execute("BEGIN IMMEDIATE")
    session = request.get("sessionId")
    if not session and chat and not request.get("newSession"):
        bound = db.execute("SELECT session, first_job FROM chats WHERE id=?", (chat,)).fetchone()
        if bound:
            session, first_job = bound
            if first_job:
                pending = lookup(db, first_job)
                if pending["status"] != "failed":
                    session = pending["sessionId"]
    resume = bool(session)
    session = session or str(uuid.uuid4())
    active = db.execute("SELECT id FROM jobs WHERE session=? AND status='running'", (session,)).fetchone()
    if active and lookup(db, active[0])["status"] == "running":
        return {"status": "busy", "sessionId": session, "jobId": active[0],
                "message": "上一条还在处理，这条尚未发送。请先取回上一条结果。"}

    prompt = make_prompt(request, job, session)
    db.execute("INSERT INTO jobs VALUES (?, ?, 'running', NULL)", (job, session))
    if chat:
        # Bind when this turn succeeds. An invalid explicit resume leaves the last good session usable.
        db.execute("INSERT INTO chats VALUES (?, NULL, ?) ON CONFLICT(id) DO UPDATE SET first_job=excluded.first_job",
                   (chat, job))
    with (ROOT / "locks" / job).open("xb") as lock, tempfile.TemporaryFile() as stdin:
        fcntl.flock(lock, fcntl.LOCK_EX)
        stdin.write(prompt.encode())
        stdin.seek(0)
        # GUEST_SOURCE is supplied by the SSH launcher. The worker owns no SSH streams.
        db.commit()
        try:
            subprocess.Popen([sys.executable, "-c", GUEST_SOURCE, job, session, "1" if resume else "0"],
                             stdin=stdin, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                             start_new_session=True, pass_fds=(lock.fileno(),))
        except OSError as exc:
            finish(db, job, "failed", str(exc))
            return lookup(db, job)
    return {"status": "running", "jobId": job, "sessionId": session}


def dispatch():
    request = json.load(sys.stdin)
    (ROOT / "locks").mkdir(parents=True, exist_ok=True)
    with connect() as db:
        db.execute("CREATE TABLE IF NOT EXISTS jobs(id TEXT PRIMARY KEY, session TEXT, status TEXT, body TEXT)")
        db.execute("CREATE INDEX IF NOT EXISTS active_jobs ON jobs(session) WHERE status='running'")
        db.execute("CREATE TABLE IF NOT EXISTS chats(id TEXT PRIMARY KEY, session TEXT, first_job TEXT)")
        if request["action"] == "ask":
            receipt = submit(db, request)
            print(json.dumps(receipt), flush=True)
            if receipt["status"] != "running" or request["waitSeconds"] == 0:
                return
        until = time.monotonic() + request["waitSeconds"]
        while True:
            result = lookup(db, request["jobId"])
            if result["status"] != "running" or time.monotonic() >= until:
                print(json.dumps(result), flush=True)
                return
            time.sleep(min(0.5, max(0, until - time.monotonic())))


if len(sys.argv) == 4:
    worker(sys.argv[1], sys.argv[2], sys.argv[3] == "1")
else:
    try:
        dispatch()
    except Exception as exc:
        print(json.dumps({"error": str(exc)}), flush=True)
        sys.exit(1)
