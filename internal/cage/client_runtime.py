"""Keep controller state outside the environment passed to external programs."""
import hashlib
import json
import os
from pathlib import Path
import runpy
import shlex
import stat
import subprocess
import sys
import tempfile
import time
from urllib.parse import urlsplit
import uuid
import threading

_KEYS = ("CAGE_CLI_STATE_ROOT", "CAGE_CLI_ACCOUNT_ROOT", "CAGE_CLI_SHARED_ROOT",
         "CAGE_CLI_ACCOUNT_HELPER", "CAGE_CLI_BINARY", "CAGE_CLAUDE_SUBJECT", "CAGE_REMOTE_APP_CONTAINER",
         "CAGE_LOGIN_OWNER", "CAGE_ACCOUNT_BROWSER")

_context = {**os.environ, **globals().get("_controller_context", {})}
_remote_windows = os.name == "nt" and _context.get("CAGE_REMOTE_APP_CONTAINER") == "1"
# Python 3.13's Windows 0700 ACL omits the package SID. Inherit the existing
# package-private ACL instead; this does not grant access to any other identity.
PRIVATE_MODE = 0o777 if _remote_windows else 0o700


class RuntimePathConflict(ValueError):
    def __init__(self, alias, target):
        super().__init__("Runtime path is already in use")
        self.alias, self.target = alias, target


def login_marker(config):
    name = hashlib.sha256(str(config.resolve()).encode()).hexdigest()
    return root() / ("auth-" + name)


def record_login_open(config, connection, args):
    """Record only which trusted terminal started the official authorization."""
    import fcntl
    if len(args) != 1:
        return
    url = urlsplit(args[0])
    hosts = ("claude.ai", "claude.com", "console.anthropic.com", "platform.claude.com", "platform.anthropic.com")
    if url.scheme != "https" or url.netloc not in hosts or url.path.rstrip("/") not in ("/oauth/authorize", "/cai/oauth/authorize"):
        return
    marker = login_marker(Path(config))
    subject = changed_login(Path(config), "").get("subject", "")
    with marker.open("a+") as f:
        fcntl.flock(f, fcntl.LOCK_EX)
        f.seek(0)
        try:
            previous = json.load(f)
        except ValueError:
            previous = {}
        # Two outstanding browser flows for the same physical profile cannot
        # establish which terminal committed the new token first.
        if previous.get("subject") == subject and previous.get("owner") != connection and time.time() < previous.get("until", 0):
            connection = "ambiguous"
        f.seek(0)
        f.truncate()
        json.dump({"owner": connection, "subject": subject, "until": time.time() + 900}, f)
        f.flush()


def changed_login(config, previous):
    # A login changes the official account metadata. Send only a change hint;
    # the controller checks auth status before archiving or rebinding a session.
    meta = config / ".claude.json"
    if not meta.is_file() and config == (Path.home() / ".claude").resolve():
        meta = Path.home() / ".claude.json"
    try:
        info = meta.stat()
        stamp = f"{info.st_ino}:{info.st_mtime_ns}:{info.st_size}"
        if stamp == previous or info.st_size > 16 << 20:
            return {"stamp": stamp}
        with meta.open("rb") as stream:
            body = stream.read((16 << 20) + 1)
        if len(body) > 16 << 20:
            return {"stamp": stamp}
        account = json.loads(body).get("oauthAccount") or {}
        email = (account.get("emailAddress") or "").strip().lower()
        org = (account.get("organizationUuid") or "").strip()
        subject = hashlib.sha256(f"{email}\n{org}".encode()).hexdigest() if email else ""
        try:
            marker = json.loads(login_marker(config).read_text())
            owner = marker["owner"] if time.time() <= marker["until"] else ""
        except (OSError, ValueError, KeyError, TypeError):
            owner = ""
        return {"stamp": stamp, "subject": subject, "owner": owner}
    except (OSError, ValueError, AttributeError):
        return {}


def changed_prompt_login(state):
    expected = state.get("login_subject")
    if not expected:
        return False
    hint = changed_login(Path(state["config"]), state.get("login_check", ""))
    if hint.get("stamp") and hint["stamp"] != state.get("login_check"):
        state["login_check"] = hint["stamp"]
        state["login_identity"] = hint.get("subject", "")
    return bool(state.get("login_identity")) and state["login_identity"] != expected


def root():
    owner = hashlib.sha256(str(Path.home()).encode()).hexdigest()[:16]
    temporary = Path.home() / "Temp" if _remote_windows else Path(tempfile.gettempdir())
    directory = temporary / ("runtime-" + owner)
    directory.mkdir(mode=PRIVATE_MODE, exist_ok=True)
    info = directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or os.name != "nt" and (info.st_uid != os.getuid() or info.st_mode & 0o077):
        raise ValueError("Private runtime directory unavailable")
    return directory


def path(value):
    """Alias an existing owned integration path without moving its contents."""
    target = Path(value)
    if _remote_windows:
        return str(target)
    if "cage" not in str(target).lower():
        return str(target)
    if not target.exists():
        return str(Path(path(target.parent)) / target.name) if target.parent != target else str(target)
    if os.name == "nt" and target.is_file() and "cage" not in target.name.lower():
        return str(Path(path(target.parent)) / target.name)
    directory = root() / "paths"
    directory.mkdir(mode=PRIVATE_MODE, exist_ok=True)
    name = hashlib.sha256(str(target.absolute()).encode()).hexdigest()[:32]
    if target.is_file() and target.suffix.lower() in (".exe", ".dll", ".json", ".jsonl", ".py"):
        name += target.suffix.lower()
    alias = directory / name
    if not alias.exists():
        try:
            if os.name == "nt":
                platform = runpy.run_path(Path(__file__).with_name("windows_cli_platform.py"))
                platform["resource_link"](alias, target)
            else:
                alias.symlink_to(target.absolute(), target_is_directory=target.is_dir())
        except FileExistsError:
            pass
    if alias.resolve() != target.resolve() and not (os.name == "nt" and alias.is_file() and os.path.samefile(alias, target)):
        raise RuntimePathConflict(alias, target)
    return str(alias)


def python():
    # A directory junction preserves Python's adjacent DLLs and standard library.
    return str(Path(path(Path(sys.executable).parent)) / Path(sys.executable).name)


def launcher(script, *, executable=False, arguments=(), login_config=None):
    context = globals().get("_controller_context", os.environ)
    internal = {key: context[key] for key in _KEYS if key in context}
    target = str(Path(script).absolute())
    body = ("#!" + python() + " -I\nimport os,runpy,sys\n" +
            ("os.execv(" + repr(target) + ", [" + repr(target) + ", *" + repr(list(arguments)) + ", *sys.argv[1:]])\n" if executable else
             "sys.argv[0]=" + repr(target) + "\nrunpy.run_path(" + repr(target) +
             ",run_name='__main__',init_globals={'_controller_context':" + repr(internal) + "})\n"))
    connection = os.environ.get("CAGE_LOGIN_CONNECTION", "")
    if executable and login_config and connection and os.name != "nt":
        invoke = ("runpy.run_path(" + repr(str(Path(__file__).resolve())) + ")['record_login_open']("
                  + repr(str(login_config)) + "," + repr(connection) + ",sys.argv[1:])\n")
        body = body.replace("os.execv(", invoke + "os.execv(", 1)
    destination = root() / (hashlib.sha256(body.encode()).hexdigest() + ".py")
    if not destination.exists():
        with tempfile.NamedTemporaryFile(dir=destination.parent, delete=False) as stream:
            stream.write(body.encode())
            temporary = stream.name
        os.chmod(temporary, 0o700)
        os.replace(temporary, destination)
    elif destination.is_symlink() or destination.read_text() != body:
        raise ValueError("Runtime launcher is already in use")
    return str(destination)


def command(script, args=()):
    return [python(), "-I", launcher(script), *args]


def command_text(script, args=()):
    argv = command(script, args)
    return subprocess.list2cmdline(argv) if os.name == "nt" else shlex.join(argv)


def environment(values):
    clean = {key: value for key, value in values.items() if not key.upper().startswith("CAGE_")}
    if "PATH" in clean:
        clean["PATH"] = os.pathsep.join(path(entry) if entry else entry for entry in clean["PATH"].split(os.pathsep))
    for key in ("CLAUDE_CONFIG_DIR", "CLAUDE_CODE_GIT_BASH_PATH", "XDG_RUNTIME_DIR", "XAUTHORITY"):
        if clean.get(key):
            clean[key] = path(clean[key])
    browser = clean.get("BROWSER", "")
    connection = values.get("CAGE_LOGIN_CONNECTION")
    if connection and not browser and os.name != "nt":
        browser = "/usr/local/libexec/cage-cli/bin/cage-open" if sys.platform == "darwin" else "/usr/local/bin/cage-open"
    if browser and (connection or "cage" in browser.lower()) and Path(browser).is_file():
        clean["BROWSER"] = launcher(browser, executable=True, login_config=clean.get("CLAUDE_CONFIG_DIR") or Path.home() / ".claude")
    for key, prefix in (("PULSE_SERVER", "unix:"), ("IBUS_ADDRESS", "unix:path=")):
        value = clean.get(key, "")
        if value.startswith(prefix):
            clean[key] = prefix + path(value[len(prefix):])
    return clean


class CodexMailbox:
    """One Claude terminal's outbound task and its last answer."""

    tools = [
        {
            "name": "send_to_codex",
            "description": "Send a task or follow-up to your dedicated Codex conversation. Read the result before sending another message.",
            "inputSchema": {
                "type": "object",
                "properties": {"message": {"type": "string", "maxLength": 8192}},
                "required": ["message"],
                "additionalProperties": False,
            },
        },
        {
            "name": "codex_result",
            "description": "Read the status and final answer for your most recent Codex request.",
            "inputSchema": {
                "type": "object",
                "properties": {"request_id": {"type": "string"}},
                "required": ["request_id"],
                "additionalProperties": False,
            },
        },
    ]

    def __init__(self):
        self.changed = threading.Condition()
        self.request = {}
        self.deadline = 0.0

    def call(self, params):
        args = params.get("arguments", {})
        if not isinstance(args, dict):
            raise ValueError("Expected a tool arguments object.")
        with self.changed:
            if self.request.get("status") == "pending" and time.monotonic() >= self.deadline:
                self.request.update(status="failed", answer="No reply was received. Check the project before repeating a task.")
            if params.get("name") == "send_to_codex" and set(args) == {"message"}:
                message = args["message"]
                if not isinstance(message, str) or not message.strip() or len(message.encode()) > 8192:
                    raise ValueError("Provide a message of at most 8192 UTF-8 bytes.")
                if self.request.get("status") == "pending":
                    raise ValueError("A request is pending. Read its result before sending another.")
                self.deadline = time.monotonic() + 360
                self.request.clear()
                self.request.update(id=str(uuid.uuid4()), message=message, status="pending")
                self.changed.notify_all()
            elif params.get("name") == "codex_result" and set(args) == {"request_id"} and self.request.get("id") == args["request_id"] and self.request:
                self.changed.wait_for(lambda: self.request["status"] != "pending", timeout=2)
            else:
                raise ValueError("Unknown request or invalid arguments.")
            return {key: value for key, value in self.request.items() if key != "message"}

    def exchange(self, request):
        with self.changed:
            if request["action"] == "codex-read":
                self.changed.wait_for(lambda: self.request.get("status") == "pending", timeout=9)
                if self.request.get("status") == "pending":
                    return {"id": self.request["id"], "message": self.request["message"]}
            elif self.request and self.request["id"] == request["id"]:
                self.request.update(status=request["status"], answer=request["answer"])
                self.changed.notify_all()
            return {"ok": True}


if __name__ == "__main__":
    os.execvpe(sys.argv[1], sys.argv[1:], environment(os.environ))
