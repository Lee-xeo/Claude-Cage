"""Project-local stdio connector for a local Claude Desktop Code session."""
import contextlib
import json
import os
from pathlib import Path
import runpy
import shutil
import socket
import socketserver
import subprocess
import sys
import threading
import uuid

ROOT = Path.home() / ".local/share/project-collaboration"
NAME = "codex"


def read(path, limit):
    with path.open("rb") as stream:
        data = stream.read(limit + 1)
    if len(data) > limit:
        raise ValueError("File too large")
    return json.loads(data)


def native_sessions(config):
    for path in (config / "sessions").glob("*.json"):
        try:
            row = read(path, 64 << 10)
            pid = int(path.stem)
            if row.get("entrypoint") != "claude-desktop" or row.get("kind") != "interactive" or pid <= 0 or row.get("pid") != pid:
                continue
            os.kill(pid, 0)
            sid = str(uuid.UUID(row["sessionId"]))
            yield pid, sid, str(Path(row["cwd"]).resolve())
        except (OSError, KeyError, ValueError, TypeError):
            continue


def local_entry(config, cwd):
    path = config / ".claude.json"
    if not path.exists():
        if config == (Path.home() / ".claude").resolve():
            path = Path.home() / ".claude.json"
        if not path.exists():
            return None
    data = read(path, 16 << 20)
    if NAME in (data.get("mcpServers") or {}):
        return False
    return (data.get("projects", {}).get(cwd, {}).get("mcpServers") or {}).get(NAME)


def register(config, cwd, grant):
    args = ["-I", str(Path(__file__).resolve()), "serve", grant]
    spec = {"type": "stdio", "command": sys.executable, "args": args}
    entry = local_entry(config, cwd)
    if entry is not None:
        # A name collision belongs to the user. The installed adapter path is
        # stable across upgrades so ordinary updates do not replace settings.
        return isinstance(entry, dict) and entry.get("command") == sys.executable and entry.get("args") == args
    cli = shutil.which("claude") or str(Path.home() / ".local/bin/claude")
    env = {**os.environ, "CLAUDE_CONFIG_DIR": str(config)}
    result = subprocess.run([cli, "mcp", "add-json", "--scope", "local", NAME, json.dumps(spec)],
                            cwd=cwd, env=env, stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL,
                            stderr=subprocess.DEVNULL, timeout=8)
    return result.returncode == 0


def sync(bindings):
    ready = []
    for account, projects in bindings.items():
        if account == "default":
            config = Path.home() / ".claude"
        elif len(account) == 24 and all(c in "0123456789abcdef" for c in account):
            config = Path.home() / ".local/share/cage/claude-accounts" / account / "config"
        else:
            continue
        config = config.resolve()
        for _, sid, cwd in native_sessions(config):
            grant = projects.get(cwd)
            if not grant:
                continue
            try:
                # Each Code subprocess and account gets its own private endpoint.
                session = str(uuid.uuid5(uuid.UUID(sid), str(config)))
                if register(config, cwd, grant) and (ROOT / grant / (session + ".sock")).is_socket():
                    ready.append({"grant": grant, "session": session})
            except (OSError, ValueError, subprocess.TimeoutExpired):
                continue
    return ready


def owner_session(config):
    rows = list(native_sessions(config))
    native_id = os.environ.get("CLAUDE_CODE_SESSION_ID")
    # Older Code engines may omit the ID from a stdio child's environment.
    # Accept only an actual, still-live Desktop Code ancestor in that case.
    pid = os.getppid()
    ancestors = set()
    while pid > 1 and pid not in ancestors:
        ancestors.add(pid)
        try:
            stat = Path(f"/proc/{pid}/stat").read_text()
            pid = int(stat.rsplit(")", 1)[1].split()[1])
        except (OSError, ValueError, IndexError):
            break
    return next((r for r in rows if r[0] in ancestors and (not native_id or r[1] == native_id)), None)


def serve(grant):
    os.umask(0o077)
    config = Path(os.environ.get("CLAUDE_CONFIG_DIR", Path.home() / ".claude")).resolve()
    owner = owner_session(config)
    mailbox = None
    if owner and register_matches(config, owner[2], grant):
        runtime = runpy.run_path("/usr/local/bin/client_runtime.py")
        factory = runtime.get("CodexMailbox")
        if factory:
            mailbox = factory()
    with contextlib.ExitStack() as stack:
        if mailbox:
            folder = ROOT / str(uuid.UUID(grant))
            folder.mkdir(mode=0o700, parents=True, exist_ok=True)
            os.chdir(folder)
            address = str(uuid.uuid5(uuid.UUID(owner[1]), str(config))) + ".sock"
            claim_lock = threading.Lock()
            claimed = None

            class Handler(socketserver.StreamRequestHandler):
                def handle(self):
                    nonlocal claimed
                    self.connection.settimeout(12)
                    try:
                        req = json.loads(self.rfile.readline(64 << 10))
                        if req.get("action") not in ("codex-read", "codex-done"):
                            return
                        result = mailbox.exchange(req)
                        if "id" in result:
                            with claim_lock:
                                if result["id"] == claimed:
                                    result = {"ok": True}
                                else:
                                    claimed = result["id"]
                        self.wfile.write(json.dumps(result).encode() + b"\n")
                    except (OSError, ValueError, KeyError):
                        return

            if Path(address).is_socket():
                with socket.socket(socket.AF_UNIX) as probe:
                    try:
                        probe.connect(address)
                    except ConnectionRefusedError:
                        Path(address).unlink()
                    else:
                        raise ValueError("Conversation already connected")
            server = stack.enter_context(socketserver.ThreadingUnixStreamServer(address, Handler))
            server.daemon_threads = True
            stack.callback(Path(address).unlink, missing_ok=True)
            threading.Thread(target=server.serve_forever, daemon=True).start()
            stack.callback(server.shutdown)
        for raw in sys.stdin.buffer:
            msg = json.loads(raw)
            if "id" not in msg:
                continue
            response = {"jsonrpc": "2.0", "id": msg["id"]}
            method = msg.get("method")
            if method == "initialize":
                response["result"] = {"protocolVersion": "2024-11-05", "capabilities": {"tools": {}},
                                      "serverInfo": {"name": NAME, "version": "1"}}
            elif method == "ping":
                response["result"] = {}
            elif method == "tools/list":
                response["result"] = {"tools": mailbox.tools if mailbox else []}
            elif method == "tools/call" and mailbox:
                try:
                    value = json.dumps(mailbox.call(msg["params"]), ensure_ascii=False)
                    response["result"] = {"content": [{"type": "text", "text": value}]}
                except (ValueError, KeyError, TypeError):
                    response["result"] = {"isError": True, "content": [{"type": "text", "text": "Invalid request or a previous task is pending."}]}
            else:
                response["error"] = {"code": -32601, "message": "Unavailable"}
            print(json.dumps(response), flush=True)


def register_matches(config, cwd, grant):
    entry = local_entry(config, cwd)
    return (isinstance(entry, dict) and entry.get("command") == sys.executable and
            entry.get("args") == ["-I", str(Path(__file__).resolve()), "serve", grant])


def rpc(request):
    os.chdir(ROOT / str(uuid.UUID(request["endpoint"])))
    with socket.socket(socket.AF_UNIX) as client:
        client.settimeout(12)
        client.connect(str(uuid.UUID(request["session"])) + ".sock")
        client.sendall(json.dumps(request).encode() + b"\n")
        with client.makefile("rb") as stream:
            return json.loads(stream.readline(64 << 10))


if __name__ == "__main__":
    if sys.argv[1] == "serve":
        serve(sys.argv[2])
    else:
        req = json.load(sys.stdin)
        print(json.dumps(sync(req["bindings"]) if sys.argv[1] == "sync" else rpc(req)))
