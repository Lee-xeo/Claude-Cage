#!/usr/bin/python3
"""Launch the official guest CLI and export only its subscription status fields."""
import runpy
from datetime import datetime
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import signal
import stat
import subprocess
import sys
import tempfile
import time

_controller_context = {**os.environ, **globals().get("_controller_context", {})}
runtime_source = Path(__file__).resolve()
runtime_path = runtime_source.with_name("client_runtime.py")
if not runtime_path.exists():
    runtime_path = runtime_source.parent.parent / "client_runtime.py"
_runtime = runpy.run_path(runtime_path, init_globals={"_controller_context": _controller_context})
PRIVATE_MODE = _runtime.get("PRIVATE_MODE", 0o700)


if os.name == "nt":
    platform_path = Path(__file__).with_name("windows_cli_platform.py")
    if not platform_path.exists():
        platform_path = Path(__file__).parent.parent / "workbench/windows_cli_platform.py"
    _platform = runpy.run_path(platform_path, init_globals={"_controller_context": _controller_context})
    fcntl = _platform["FileLock"]
else:
    import fcntl

SHIM = "/usr/local/libexec/claude-wrapper"
OPEN = "/usr/local/bin/xdg-open"
HELPER = "/usr/local/libexec/claude-account"
DEFAULT = "default"


def shared_root():
    return Path(_controller_context["CAGE_CLI_SHARED_ROOT"]) if os.name == "nt" else Path.home() / ".local/share/workbench/claude-shared"


class SharedResourceConflict(ValueError):
    pass


class SubscriptionStatusError(ValueError):
    pass


class SubscriptionStatusTimeout(SubscriptionStatusError):
    pass


def profile(account_id):
    if account_id != DEFAULT and not re.fullmatch("[0-9a-f]{24}", account_id):
        raise ValueError("invalid account ID")
    root = Path(_controller_context["CAGE_CLI_ACCOUNT_ROOT"]) if os.name == "nt" else Path.home() / ".local/share/workbench/claude-accounts"
    return root / account_id


def config_directory(base):
    return Path.home() / ".claude" if base.name == DEFAULT else base / "config"


def cli_environment(base):
    env = os.environ.copy()
    # Executing the real binary must never recurse into the terminal's claude shim.
    path = [p for p in env.get("PATH", os.defpath).split(os.pathsep) if p and p != SHIM]
    env["PATH"] = os.pathsep.join([str(Path.home() / ".local/bin"), *path])
    if base.name == DEFAULT:
        # Let Claude resolve its normal ~/.claude.json and ~/.claude itself.
        # Preserve provider variables: an API-authenticated shell is not a
        # signed-in default subscription, even if a previous login exists.
        return _runtime["environment"](env)
    env["CLAUDE_CONFIG_DIR"] = str(config_directory(base))
    for name in ("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL",
                 "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX",
                 "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_GATEWAY"):
        env.pop(name, None)
    return _runtime["environment"](env)


def identity(binary, env, cwd, settings=(), timeout=10, pass_fds=()):
    sink = subprocess.PIPE if _controller_context.get("CAGE_REMOTE_APP_CONTAINER") == "1" else subprocess.DEVNULL
    try:
        result = subprocess.run([_runtime["path"](binary), *settings, "auth", "status"], cwd=cwd, env=env, timeout=timeout,
                                stdin=sink, stdout=subprocess.PIPE, stderr=sink, pass_fds=pass_fds)
    except subprocess.TimeoutExpired as error:
        raise SubscriptionStatusTimeout("Subscription status timed out") from error
    # Exit code 1 with JSON is the CLI's documented logged-out response. Any
    # other failure (including missing JSON) must not sign a known user out.
    try:
        data = json.loads(result.stdout)
    except (ValueError, TypeError) as error:
        raise SubscriptionStatusError("Invalid subscription status") from error
    if not isinstance(data, dict):
        raise SubscriptionStatusError("Invalid subscription status")
    reported_dir = data.get("configDirectory")
    correct_dir = isinstance(reported_dir, str) and Path(reported_dir).resolve() == Path(env.get("CLAUDE_CONFIG_DIR") or Path.home() / ".claude").resolve()
    if result.returncode not in (0, 1) or not correct_dir:
        raise SubscriptionStatusError("Unexpected subscription status")
    ok = (result.returncode == 0 and data.get("loggedIn") is True and data.get("authMethod") == "claude.ai"
          and data.get("apiProvider") == "firstParty")
    if not ok:
        return {"logged_in": False}
    return {"logged_in": True, "email": data.get("email"), "organization": data.get("orgId") or data.get("orgName"),
            "plan": data.get("subscriptionType")}


def subject(summary):
    if not summary.get("logged_in") or not summary.get("email"):
        return None
    email = summary["email"].strip().lower()
    organization = (summary.get("organization") or "").strip()
    return hashlib.sha256(f"{email}\n{organization}".encode()).hexdigest()


def recover_login(binary, target, source_id, expected, *, archive=False):
    """Recover a missing file-backed login without moving an account's history."""
    if target.name == DEFAULT or (source_id == DEFAULT and not archive) or source_id == target.name:
        raise ValueError("recovery requires distinct private profiles")
    source = profile(source_id)
    env = cli_environment(target)
    # auth status itself initializes .claude.json in an empty profile. A new
    # archive needs to publish its verified identity before the CLI first runs.
    if not archive or any(config_directory(target).iterdir()):
        current = identity(binary, env, Path.home())
        if subject(current) == expected:
            return current
        if archive or subject(current) is not None:
            return {"logged_in": False}
    authenticated = identity(binary, cli_environment(source), Path.home())
    if not expected or subject(authenticated) != expected:
        return {"logged_in": False}
    credential = config_directory(source) / ".credentials.json"
    destination = config_directory(target) / ".credentials.json"
    paths = [target, config_directory(target), credential]
    if source_id != DEFAULT:
        paths += [source, config_directory(source)]
    elif config_directory(source).is_symlink() or os.name != "nt" and config_directory(source).stat().st_uid != os.getuid():
        raise ValueError("unexpected default login directory")
    for path in paths:
        info = path.lstat()
        if path.is_symlink() or getattr(info, "st_file_attributes", 0) & stat.FILE_ATTRIBUTE_REPARSE_POINT or (os.name != "nt" and (info.st_uid != os.getuid() or info.st_mode & 0o077)):
            raise ValueError("unexpected login file permissions")
    if not credential.is_file():
        raise ValueError("file-backed login required")
    if archive:
        # The controller allocated this fresh directory and publishes its ID
        # only after verification. O_EXCL protects concurrent file creation.
        if source.name == DEFAULT:
            source_directory = cli_environment(source).get("CLAUDE_CONFIG_DIR")
            if source_directory and Path(source_directory).resolve() != config_directory(source).resolve():
                raise ValueError("default subscription uses a different directory")
        metadata = (Path.home() if source.name == DEFAULT else config_directory(source)) / ".claude.json"
        if metadata.is_symlink():
            raise ValueError("unexpected login metadata")
        account = json_file(metadata).get("oauthAccount")
        if isinstance(account, dict):
            with (config_directory(target) / ".claude.json").open("x") as output:
                json.dump({"oauthAccount": account}, output)
        with credential.open("rb") as stream, destination.open("xb") as output:
            shutil.copyfileobj(stream, output)
        recovered = identity(binary, env, Path.home())
        return recovered if subject(recovered) == expected else {"logged_in": False}
    # Publish opaque bytes only inside the guest. The hard link is atomic and
    # refuses to overwrite a credential created by a concurrent original login.
    with tempfile.NamedTemporaryFile(dir=destination.parent) as temporary:
        with credential.open("rb") as stream:
            shutil.copyfileobj(stream, temporary)
        temporary.flush()
        os.fsync(temporary.fileno())
        os.link(temporary.name, destination)
    # The CLI can refresh or replace this path independently of the controller.
    # Retain published credentials on failure: checking file identity before an
    # unlink cannot prevent deleting a concurrent login's replacement.
    recovered = identity(binary, env, Path.home())
    return recovered if subject(recovered) == expected else {"logged_in": False}


def json_file(path):
    try:
        return json.loads(path.read_text())
    except FileNotFoundError:
        return {}


def valid_window(data, now):
    if not isinstance(data, dict):
        return None
    used, reset = data.get("used_percentage"), data.get("resets_at")
    if type(used) not in (int, float) or not 0 <= used <= 100:
        return None
    if type(reset) != int or (reset != 0 and reset <= now):
        return None
    return {"used_percentage": used, "resets_at": reset}


def read_quota(base):
    try:
        stored = json_file(base / "quota.json")
    except (ValueError, OSError):
        return None
    if not isinstance(stored, dict):
        return None
    digest = stored.get("identity", "")
    seen = stored.get("observed_at")
    now = int(time.time())
    if not isinstance(digest, str) or not re.fullmatch("[a-f0-9]{64}", digest) or type(seen) != int or not 0 < seen <= now:
        return None
    result = {}
    for key in ("five_hour", "seven_day"):
        window = valid_window(stored.get(key), now)
        if window:
            window_seen = stored[key].get("observed_at", seen)
            if type(window_seen) == int and 0 < window_seen <= now:
                result[key] = {**window, "observed_at": window_seen}
    if result:
        return {**result, "identity": digest, "observed_at": seen}
    return None


def save_usage(base, data, expected, confirm_subject, query_started=None, deadline=None):
    rate = data.get("rate_limits")
    if not isinstance(rate, dict):
        return
    now = int(time.time())
    with (base / "quota.lock").open("a") as lock:
        if deadline is None:
            fcntl.flock(lock, fcntl.LOCK_EX)
        else:
            while True:
                try:
                    fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    if time.monotonic() >= deadline:
                        raise TimeoutError("usage cache busy")
                    time.sleep(.05)
        path = base / "quota.json"
        previous = read_quota(base) or {}
        if previous.get("identity") != expected:
            previous = {}
        windows = {}
        changed = False
        for key in ("five_hour", "seven_day"):
            old = previous.get(key)
            new = valid_window(rate.get(key), now)
            # A redraw of an older terminal must not roll back the more recent
            # account window observed in another conversation.
            redraw = query_started is None and old and new and all(old[field] == new[field] for field in ("resets_at", "used_percentage"))
            older = old and new and (0 < new["resets_at"] < old["resets_at"] or
                    (query_started is None and old["resets_at"] == new["resets_at"] and new["used_percentage"] < old["used_percentage"]))
            if old and (not new or redraw or older or (query_started is not None and old["observed_at"] > query_started)):
                windows[key] = old
            elif new:
                windows[key] = {**new, "observed_at": now}
                changed = True
        # A user can run /login in an open Claude process. Ask the CLI for
        # its identity on changed readings only; never infer it from a path.
        if changed and expected and confirm_subject() == expected:
            windows["observed_at"] = now
            windows["identity"] = expected
            with tempfile.NamedTemporaryFile(mode="w", dir=base, delete=False) as tmp:
                json.dump(windows, tmp, separators=(",", ":"))
            os.replace(tmp.name, path)


def reported_window(percent, stamp, now):
    reset = 0
    if stamp is not None:
        try:
            dt = datetime.fromisoformat(stamp.replace("Z", "+00:00"))
            if dt.tzinfo is None:
                return None
            reset = int(dt.timestamp())
        except (AttributeError, ValueError, OverflowError):
            return None
    return valid_window({"used_percentage": percent, "resets_at": reset}, now)


def usage_windows(raw, now):
    # SDKControlGetUsageResponse is the structured /usage report. The input
    # contains one control request and no user prompt or generate request.
    report = None
    for line in raw.splitlines():
        message = json.loads(line)
        if message.get("type") in ("user", "assistant"):
            raise ValueError("unexpected conversation during usage query")
        if message.get("type") == "result" and (message.get("is_error") or message.get("num_turns") != 0):
            raise ValueError("unexpected inference result")
        if message.get("type") == "control_response":
            response = message.get("response") or {}
            if response.get("request_id") == "quota":
                if report is not None or response.get("subtype") != "success":
                    raise ValueError("failed usage response")
                report = response.get("response")
    if not isinstance(report, dict) or type(report.get("rate_limits_available")) is not bool:
        raise ValueError("missing structured usage response")
    session = report.get("session")
    if not isinstance(session, dict) or session.get("total_cost_usd") != 0 or session.get("model_usage"):
        raise ValueError("unexpected model use")
    if not report["rate_limits_available"]:
        return False, {}
    rate = report.get("rate_limits") or {}
    windows = {}
    for key in ("five_hour", "seven_day"):
        meter = rate.get(key)
        if isinstance(meter, dict):
            reading = reported_window(meter.get("utilization"), meter.get("resets_at"), now)
            if reading:
                windows[key] = reading
    # Some servers supply the unscoped windows in limits[] instead. A
    # model-specific quota never substitutes for an account's weekly window.
    candidates = {"session": "five_hour", "weekly_all": "seven_day"}
    rows = {}
    ambiguous = set()
    for row in rate.get("limits") or ():
        if not isinstance(row, dict):
            continue
        key = candidates.get(row.get("kind"))
        if not key or key in windows or key in ambiguous:
            continue
        reading = reported_window(row.get("percent"), row.get("resets_at"), now)
        if reading and key in rows and rows[key] != reading:
            ambiguous.add(key)
            del rows[key]
        elif reading:
            rows[key] = reading
    return True, {**rows, **windows}


def changed_identity():
    return {"logged_in": False, "usage_status": "identity_changed", "usage_checked_at": int(time.time())}


def invoke_usage(binary, env, cwd, options, timeout):
    command = [_runtime["path"](binary), *options, "-p", "--input-format", "stream-json", "--verbose", "--output-format", "stream-json",
               "--no-session-persistence", "--strict-mcp-config", "--tools", ""]
    request = {"type": "control_request", "request_id": "quota", "request": {"subtype": "get_usage", "skip_behaviors": True}}
    result = subprocess.run(command, cwd=cwd, env=env, timeout=timeout, input=(json.dumps(request) + "\n").encode(),
                            stdout=subprocess.PIPE, stderr=subprocess.DEVNULL)
    if result.returncode != 0 or len(result.stdout) > (2 << 20):
        raise ValueError("usage command failed")
    return usage_windows(result.stdout, int(time.time()))


def query_usage(binary, env, base, expected):
    if not expected or not re.fullmatch("[a-f0-9]{64}", expected):
        raise ValueError("missing expected account")
    deadline = time.monotonic() + 12
    started = int(time.time())
    status = "error"
    summary = {}
    options = ["--setting-sources", "user", "--settings", '{"disableAllHooks":true}']
    with tempfile.TemporaryDirectory(prefix="claude-usage-") as cwd:
        try:
            summary = identity(binary, env, cwd, options, timeout=4)
            if subject(summary) != expected:
                return changed_identity()
            # Reserve time to recheck the actual identity after the command.
            timeout = min(8, deadline - time.monotonic() - 2)
            try:
                available, windows = invoke_usage(binary, env, cwd, options, max(.1, timeout))
            except (OSError, ValueError, TypeError, AttributeError, subprocess.SubprocessError):
                windows = None
            after = identity(binary, env, cwd, options, timeout=max(.1, min(4, deadline - time.monotonic())))
            if subject(after) != expected:
                return changed_identity()
            summary = after
            if windows:
                base.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
                save_usage(base, {"rate_limits": windows}, expected, lambda: expected, started, deadline)
            if windows is not None:
                status = {0: "unavailable", 1: "partial", 2: "reported"}[len(windows)] if available else "not_applicable"
        except (OSError, ValueError, TypeError, AttributeError, subprocess.SubprocessError):
            # The CLI can print project/account data even on failure.
            # The host needs a reason category and only verified window data.
            pass
    if subject(summary) == expected:
        previous = read_quota(base)
        if previous and previous["identity"] == expected:
            summary["quota"] = previous
            if status == "unavailable":
                status = "cached"
    summary.update(usage_status=status, usage_checked_at=int(time.time()))
    return summary


def has_status_line(base):
    # Preserve existing user, project and managed status lines, including an
    # explicit disable. If a settings file is unreadable, defer to Claude.
    if os.name == "nt":
        policy = Path(os.environ.get("ProgramFiles", "C:/Program Files")) / "ClaudeCode"
    elif sys.platform == "darwin":
        policy = Path("/Library/Application Support/ClaudeCode")
    else:
        policy = Path("/etc/claude-code")
    paths = [config_directory(base) / "settings.json", policy / "managed-settings.json"]
    if os.name != "nt":
        paths.extend((policy / "managed-settings.d").glob("*.json"))
    for folder in (Path.cwd(), *Path.cwd().parents):
        paths += [folder / ".claude/settings.json", folder / ".claude/settings.local.json"]
    common = Path.home() / ".claude/settings.json"
    for path in paths:
        try:
            settings = json_file(path)
            if not isinstance(settings, dict) or settings.get("allowManagedHooksOnly"):
                return True
            # Cage's status line runs the shared command itself (user_status_line),
            # so quota readings continue without hiding the user's own line.
            if "statusLine" in settings and not (path == common and user_status_line() is not None):
                return True
        except (OSError, ValueError):
            return True
    return False


def user_status_line():
    """The shared command status line, if any, that Cage's status line delegates to."""
    try:
        line = json_file(Path.home() / ".claude/settings.json").get("statusLine")
    except (OSError, ValueError, AttributeError):
        return None
    if isinstance(line, dict) and line.get("type") == "command" and isinstance(line.get("command"), str) and line["command"].strip():
        return line
    return None


def status_line(command):
    setting = {"type": "command", "command": command}
    line = user_status_line()
    if line and type(line.get("padding")) is int:
        setting["padding"] = line["padding"]
    return setting


def settings_flags(args):
    flags = []
    iterator = iter(args)
    for arg in iterator:
        if arg == "--":
            break
        key, equals, _ = arg.partition("=")
        if key in ("--settings", "--setting-sources"):
            flags.append(arg)
            if not equals:
                value = next(iterator, None)
                if value is None:
                    raise ValueError("missing CLI setting")
                flags.append(value)
    return flags


# Only local behavior is shared. Never copy env, apiKeyHelper, login/org
# constraints, OAuth metadata, or account-synced remote configuration.
SHARED_SETTINGS = frozenset({"model", "permissions", "hooks", "statusLine", "attribution", "language",
                             "effortLevel", "alwaysThinkingEnabled", "includeCoAuthoredBy", "respectGitignore",
                             "spinnerTipsEnabled", "outputStyle", "disableAllHooks", "includeGitInstructions",
                             "theme", "skipDangerousModePermissionPrompt", "tui", "autoMemoryEnabled"})
# The terminal status line also records quota readings; a copied user status
# line would disable that for terminals, and Desktop does not draw one.
PROFILE_SETTINGS = SHARED_SETTINGS - {"statusLine"}


def file_digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def read_record(base):
    """Cage's private bookkeeping for one profile, outside its configuration."""
    try:
        record = json_file(base / "shared-settings.json")
    except ValueError:
        record = {}
    return record if isinstance(record, dict) else {}


def write_record(base, record):
    with tempfile.NamedTemporaryFile(mode="w", dir=base, delete=False) as stream:
        json.dump(record, stream, separators=(",", ":"))
        temporary = stream.name
    os.replace(temporary, base / "shared-settings.json")


def json_snapshot(path):
    """Return the raw bytes and parsed JSON; None means leave the file alone."""
    if path.is_symlink() or os.name == "nt" and path.is_junction():
        return b"", None
    try:
        raw = path.read_bytes()
    except FileNotFoundError:
        return None, {}
    try:
        data = json.loads(raw)
    except ValueError:
        # Leave a file the user is still editing to the CLI's own diagnostics.
        return raw, None
    return raw, data if isinstance(data, dict) else None


def replace_json(path, data, original, backup):
    """Atomically publish JSON unless another writer changed the file since it was read."""
    if original is not None:
        backup = backup.with_name(backup.name + "-" + str(time.time_ns()))
        backup.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
        with backup.open("xb") as output:
            output.write(original)
    path.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    with tempfile.NamedTemporaryFile(mode="w", dir=path.parent, delete=False) as stream:
        json.dump(data, stream, indent=2, ensure_ascii=False)
        stream.write("\n")
        temporary = stream.name
    try:
        # The official CLI may save its own change meanwhile. Keep that one and
        # let the next start synchronize again rather than failing this launch.
        try:
            current = path.read_bytes()
        except FileNotFoundError:
            current = None
        if current != original:
            Path(temporary).unlink(missing_ok=True)
            return False
        os.replace(temporary, path)
        return True
    except BaseException:
        Path(temporary).unlink(missing_ok=True)
        raise


MISSING = object()


def three_way(account, base, common):
    """Merge per entry; a side's change wins over the other's unchanged value.

    When both sides changed one entry differently, each keeps its own value and
    the entry stays unresolved until one of them changes again.
    """
    account_result, common_result, base_result = dict(account), dict(common), {}
    for key in set(account) | set(base) | set(common):
        a, b, c = (mapping.get(key, MISSING) for mapping in (account, base, common))
        if a == c:
            value = a
        elif a == b:
            value = c
        elif c == b:
            value = a
        else:
            if b is not MISSING:
                base_result[key] = b
            continue
        for result in (account_result, common_result):
            if value is MISSING:
                result.pop(key, None)
            else:
                result[key] = value
        if value is not MISSING:
            base_result[key] = value
    return account_result, common_result, base_result


def resource_link(local, shared):
    local.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    if os.name == "nt":
        _platform["resource_link"](local, shared)
    else:
        local.symlink_to(shared, target_is_directory=shared.is_dir())


def linked(path):
    return path.is_symlink() or os.name == "nt" and path.is_junction()


# Entries below a profile's plugins folder that hold account identity (synced
# from claude.ai, named by account and organization), per-account plugin data or
# per-account bookkeeping. They never become links; tests pin this list.
PRIVATE_PLUGIN_ENTRIES = frozenset({"synced", "store", "data", "installed_plugins.json", "installed_plugins_v2.json",
                                    "known_marketplaces.json", "plugin-directory-cache-v2.json",
                                    "install-counts-cache.json", ".last_inuse_sweep"})
# The CLI rebuilds marketplaces/<name> and adds cache/<marketplace>/<plugin>/<version>
# inside these folders, so linking the folders survives updates.
SHARED_PLUGIN_FOLDERS = (("marketplaces", 0), ("cache", 2))
PLUGIN_SETTINGS = ("enabledPlugins", "extraKnownMarketplaces")
PLUGIN_REGISTRIES = (("known_marketplaces.json", None), ("installed_plugins.json", "plugins"))


def share_plugin_folders(base, lock_dir):
    """Link the marketplace and plugin caches; False keeps this profile's plugins private."""
    config, common = config_directory(base), Path.home() / ".claude"
    local_root, shared_root_dir = config / "plugins", common / "plugins"
    local_root.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    shared_root_dir.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    known = []
    for root in (local_root, shared_root_dir):
        _, data = json_snapshot(root / "known_marketplaces.json")
        if data is None:
            return False
        known.append(data)
    def source(index, name):
        entry = known[index].get(name)
        return entry.get("source") if isinstance(entry, dict) else None
    def conflicting(local, shared, depth, marketplace):
        for child in local.iterdir():
            target, name = shared / child.name, marketplace or child.name
            if linked(child):
                return True
            if not (target.exists() or linked(target)):
                continue
            # A cached copy is reproducible from its marketplace. Two different
            # marketplaces under one name are not, so nothing is chosen for them.
            if None not in (source(0, name), source(1, name)) and source(0, name) != source(1, name):
                return True
            if child.is_dir() != target.is_dir() or linked(target):
                return True
            if depth and child.is_dir() and conflicting(child, target, depth - 1, name):
                return True
        return False
    plan = []
    for name, depth in SHARED_PLUGIN_FOLDERS:
        local, shared = local_root / name, shared_root_dir / name
        if linked(local):
            if local.resolve() != shared.resolve():
                return False
            continue
        if local.exists() and (not local.is_dir() or shared.exists() and not shared.is_dir()):
            return False
        if local.exists() and shared.exists() and conflicting(local, shared, depth, None):
            return False
        plan.append((local, shared, depth))
    def move(local, shared, depth, relative):
        for child in sorted(local.iterdir()):
            target = shared / child.name
            if not (target.exists() or linked(target)):
                target.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
                child.rename(target)
            elif depth and child.is_dir() and target.is_dir():
                move(child, target, depth - 1, relative / child.name)
            else:
                # Keep the profile's own copy recoverable instead of deleting it.
                backup = lock_dir / "backups" / base.name / "plugins" / relative / (child.name + "-" + str(time.time_ns()))
                backup.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
                child.rename(backup)
        local.rmdir()
    # Every rename is atomic, so an interrupted migration resumes from here.
    for local, shared, depth in plan:
        shared.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
        if local.exists():
            move(local, shared, depth, Path(local.name))
        resource_link(local, shared)
    return True


NEUTRAL_PLUGINS = "\0plugins" + os.sep


def plugin_root_forms(config):
    """Spellings under which the CLI may record a profile's plugins folder.

    The client runtime hands the CLI a stable alias of configuration paths that
    name the product (client_runtime.path); the CLI records locations in the form
    it was given. The first form is the one a new record uses.
    """
    real = str(config / "plugins") + os.sep
    if "cage" not in str(config).lower() or config == Path.home() / ".claude":
        return [real]
    owner = hashlib.sha256(str(Path.home()).encode()).hexdigest()[:16]
    alias = Path(tempfile.gettempdir()) / ("runtime-" + owner) / "paths" / hashlib.sha256(str(config.absolute()).encode()).hexdigest()[:32]
    return [str(alias / "plugins") + os.sep, real]


def legacy_plugin_root_forms(config):
    """Older plugin spellings from before ~/.local/share/cage moved to workbench."""
    if os.name == "nt":
        return []
    workbench = Path.home() / ".local/share/workbench/claude-accounts"
    try:
        rel = config.relative_to(workbench)
    except ValueError:
        return []
    former = Path.home() / ".local/share/cage/claude-accounts" / rel
    owner = hashlib.sha256(str(Path.home()).encode()).hexdigest()[:16]
    alias = Path(tempfile.gettempdir()) / ("runtime-" + owner) / "paths" / hashlib.sha256(str(former.absolute()).encode()).hexdigest()[:32]
    return [str(alias / "plugins") + os.sep, str(former / "plugins") + os.sep]


def plugin_paths(value, old, new):
    """Re-anchor recorded locations from one plugins folder spelling to another."""
    if isinstance(value, dict):
        result = {}
        for key, item in value.items():
            if key in ("installPath", "installLocation") and isinstance(item, str):
                prefix = next((form for form in old if item.startswith(form)), None)
                result[key] = new + item[len(prefix):] if prefix is not None else item
            else:
                result[key] = plugin_paths(item, old, new)
        return result
    if isinstance(value, list):
        return [plugin_paths(item, old, new) for item in value]
    return value


def plugin_form(value, forms):
    """The spelling a registry already uses, so rewritten entries match the CLI's own."""
    found = []
    def walk(item):
        if isinstance(item, dict):
            for key, child in item.items():
                if key in ("installPath", "installLocation") and isinstance(child, str):
                    found.extend(form for form in forms if child.startswith(form))
                else:
                    walk(child)
        elif isinstance(item, list):
            for child in item:
                walk(child)
    walk(value)
    return found[0] if found else forms[0]


def sync_plugin_registries(base, lock_dir, record):
    """Merge installed plugins and known marketplaces in both directions."""
    config, common = config_directory(base), Path.home() / ".claude"
    local_root, common_root = config / "plugins", common / "plugins"
    local_forms, common_forms = plugin_root_forms(config), plugin_root_forms(common)
    local_read_forms = [*local_forms, *legacy_plugin_root_forms(config)]
    bases = record.setdefault("plugins", {})
    for name, field in PLUGIN_REGISTRIES:
        local_raw, local = json_snapshot(local_root / name)
        common_raw, shared = json_snapshot(common_root / name)
        if local is None or shared is None:
            continue
        if field and (local.get("version", 2) != 2 or shared.get("version", 2) != 2):
            continue
        local_map = local.get(field, {}) if field else local
        common_map = shared.get(field, {}) if field else shared
        if not isinstance(local_map, dict) or not isinstance(common_map, dict):
            continue
        previous = bases.get(name) if isinstance(bases.get(name), dict) else {}
        local_form, common_form = plugin_form(local_map, local_forms), plugin_form(common_map, common_forms)
        merged_local, merged_common, merged_base = three_way(plugin_paths(local_map, local_read_forms, NEUTRAL_PLUGINS), previous,
                                                             plugin_paths(common_map, common_forms, NEUTRAL_PLUGINS))
        merged_common = plugin_paths(merged_common, [NEUTRAL_PLUGINS], common_form)
        if merged_common != common_map:
            data = {**shared, "version": 2, field: merged_common} if field else merged_common
            if not replace_json(common_root / name, data, common_raw, lock_dir / "backups" / "common" / "plugins" / name):
                continue
        merged_local = plugin_paths(merged_local, [NEUTRAL_PLUGINS], local_form)
        if merged_local != local_map:
            data = {**local, "version": 2, field: merged_local} if field else merged_local
            if not replace_json(local_root / name, data, local_raw, lock_dir / "backups" / base.name / "plugins" / name):
                continue
        bases[name] = merged_base


def withdraw_additions(result, additions):
    for key, value in (additions.get("keys") or {}).items():
        if key in result and result[key] == value:
            del result[key]
    hooks = result.get("hooks")
    if isinstance(hooks, dict):
        for event, groups in (additions.get("hooks") or {}).items():
            present = hooks.get(event)
            if not isinstance(present, list) or not isinstance(groups, list):
                continue
            for group in groups:
                if group in present:
                    present.remove(group)
            if not present:
                del hooks[event]
        if not hooks and additions.get("hooks_created"):
            del result["hooks"]


def sync_profile_settings(base, lock_dir, record, plugins_shared):
    """Copy shared settings into a profile for clients that take no flags.

    Only keys the profile does not set are added, and hook groups are appended
    per event. The record lists the additions, so later runs refresh or withdraw
    exactly those entries and never a value the user changed. Plugin choices are
    merged in both directions. Callers hold resources.lock.
    """
    config = config_directory(base)
    path, common_path = config / "settings.json", Path.home() / ".claude/settings.json"
    original, current = json_snapshot(path)
    common_raw, common = json_snapshot(common_path)
    if current is None or common is None or "hooks" in current and not isinstance(current["hooks"], dict):
        return
    result = json.loads(json.dumps(current))
    previous = {key: record.get(key) for key in ("keys", "hooks", "hooks_created") if key in record}
    if not all(isinstance(previous.get(name, {}), dict) for name in ("keys", "hooks")):
        previous = {}
    pending = record.get("pending") if isinstance(record.get("pending"), dict) else {}
    # An interrupted earlier run may have written its additions without
    # completing the record; its intent lists them, so they are ours too.
    withdraw_additions(result, previous)
    withdraw_additions(result, pending)
    added = {key: common[key] for key in sorted(PROFILE_SETTINGS - {"hooks"}) if key in common and key not in result}
    result.update(added)
    added_hooks, created = {}, "hooks" not in result
    if isinstance(common.get("hooks"), dict):
        target = result.setdefault("hooks", {})
        for event, groups in common["hooks"].items():
            present = target.get(event, [])
            if not isinstance(groups, list) or not isinstance(present, list):
                continue
            missing = []
            for group in groups:
                if group not in present and group not in missing:
                    missing.append(group)
            if missing:
                target[event] = present + missing
                added_hooks[event] = missing
        if created and not target:
            del result["hooks"]
    additions = {"keys": added, "hooks": added_hooks, "hooks_created": created and bool(added_hooks)}
    common_result, plugin_bases = dict(common), dict(record.get("plugins") or {})
    if plugins_shared:
        for key in PLUGIN_SETTINGS:
            mine, theirs = result.get(key, {}), common.get(key, {})
            if not isinstance(mine, dict) or not isinstance(theirs, dict):
                continue
            known = plugin_bases.get(key) if isinstance(plugin_bases.get(key), dict) else {}
            mine, theirs, plugin_bases[key] = three_way(mine, known, theirs)
            for target, value, original_map in ((result, mine, current), (common_result, theirs, common)):
                if value or key in original_map:
                    target[key] = value
    if common_result != common:
        # Publish plugin choices first: if this is refused, nothing else moves.
        if not replace_json(common_path, common_result, common_raw, lock_dir / "backups" / "common" / "settings.json"):
            return
    if result != current:
        write_record(base, {**record, "pending": additions})
        if not replace_json(path, result, original, lock_dir / "backups" / base.name / "settings.json"):
            record.pop("pending", None)
            write_record(base, record)
            return
    record.pop("pending", None)
    record.update(additions)
    record["plugins"] = {**(record.get("plugins") or {}), **{key: plugin_bases[key] for key in PLUGIN_SETTINGS if key in plugin_bases}}


def global_config_path(base):
    return Path.home() / ".claude.json" if base.name == DEFAULT else config_directory(base) / ".claude.json"


def shared_mcp_servers():
    """User-scope servers of the default configuration; never its account fields."""
    _, data = json_snapshot(Path.home() / ".claude.json")
    servers = data.get("mcpServers") if isinstance(data, dict) else None
    return {name: value for name, value in servers.items() if isinstance(value, dict)} if isinstance(servers, dict) else {}


def sync_profile_mcp(base, lock_dir, record, add_missing):
    """Add shared user-scope MCP servers the profile lacks; refresh or withdraw earlier additions.

    Only the mcpServers entry changes. OAuth tokens for MCP servers stay in each
    profile's credential store and are never read here.
    """
    path = global_config_path(base)
    original, current = json_snapshot(path)
    if current is None or "mcpServers" in current and not isinstance(current["mcpServers"], dict):
        return
    servers = dict(current.get("mcpServers") or {})
    ours = record.get("mcp") if isinstance(record.get("mcp"), dict) else {}
    pending = record.get("pending_mcp") if isinstance(record.get("pending_mcp"), dict) else {}
    for added in (ours, pending):
        for name, value in added.items():
            if servers.get(name) == value:
                del servers[name]
    shared, additions = shared_mcp_servers(), {}
    for name, value in shared.items():
        if name not in servers and (add_missing or name in ours):
            servers[name] = additions[name] = value
    result = {key: value for key, value in current.items() if key != "mcpServers"}
    if servers or "mcpServers" in current:
        result["mcpServers"] = servers
    if result != current:
        write_record(base, {**record, "pending_mcp": additions})
        if not replace_json(path, result, original, lock_dir / "backups" / base.name / "claude.json"):
            write_record(base, record)
            return
    record["mcp"] = additions


def launch_mcp_servers(base):
    """Shared servers this profile's own configuration does not already define."""
    if base.name == DEFAULT:
        return {}
    _, own = json_snapshot(global_config_path(base))
    present = own.get("mcpServers") if isinstance(own, dict) else None
    present = present if isinstance(present, dict) else {}
    return {name: value for name, value in shared_mcp_servers().items() if name not in present}


def shared_mcp_arguments(base, args):
    """Offer the shared user-scope MCP servers to a terminal through --mcp-config."""
    options = args[:args.index("--")] if "--" in args else args
    if "--strict-mcp-config" in options:
        return args
    servers = launch_mcp_servers(base)
    if not servers:
        return args
    data = json.dumps({"mcpServers": servers}, separators=(",", ":")).encode()
    folder = base / "launch-settings"
    folder.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    path = folder / ("mcp-" + hashlib.sha256(data).hexdigest() + ".json")
    if not path.exists():
        with tempfile.NamedTemporaryFile(dir=folder, delete=False) as stream:
            stream.write(data)
            temporary = stream.name
        os.replace(temporary, path)
    value = _runtime["path"](path)
    # --mcp-config takes several space-separated files. Add this one to an
    # existing flag rather than relying on how repeated flags combine.
    if "--mcp-config" in options:
        index = args.index("--mcp-config")
        return [*args[:index + 1], value, *args[index + 1:]]
    # The next argument is always an option (--settings), which ends the list.
    return ["--mcp-config", value, *args]


def heal_shared_file(base, local, shared, record):
    """Fold back a linked instruction file that an atomic save turned into a copy."""
    links = record.setdefault("links", {})
    name = str(local.relative_to(config_directory(base)))
    if linked(local):
        if local.resolve() == shared.resolve() and shared.is_file():
            links[name] = file_digest(shared)
        return
    if not (local.is_file() and shared.is_file()) or local.read_bytes() == shared.read_bytes():
        return
    # Only when the shared copy is still exactly what this profile last linked
    # is the profile's copy the newer edit. Otherwise both changed: keep both.
    if links.get(name) != file_digest(shared):
        return
    backup = shared_root() / "backups" / "common" / (shared.name + "-" + str(time.time_ns()))
    backup.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    shutil.copy2(shared, backup)
    with tempfile.NamedTemporaryFile(dir=shared.parent, delete=False) as stream:
        stream.write(local.read_bytes())
        temporary = stream.name
    os.chmod(temporary, stat.S_IMODE(shared.stat().st_mode))
    os.replace(temporary, shared)


def shared_profile_resources(base, desktop=False):
    """Share local customizations; credentials, synced skills and history stay private."""
    config = config_directory(base)
    common = Path.home() / ".claude"
    common.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    if config == common:
        return
    lock_dir = shared_root()
    lock_dir.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    with (lock_dir / "resources.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        record = read_record(base)
        conflict = "账号的技能或指令与共用资料同名冲突，请先核对 ~/.claude 中的对应文件；原文件已保留"
        def check(local, shared):
            if linked(local):
                if local.resolve() != shared.resolve():
                    raise SharedResourceConflict(conflict)
            elif local.exists() and shared.exists():
                if local.is_dir() and shared.is_dir():
                    for child in local.iterdir():
                        check(child, shared / child.name)
                elif not (local.is_file() and shared.is_file() and local.read_bytes() == shared.read_bytes()):
                    raise SharedResourceConflict(conflict)
        def merge(local, shared):
            if linked(local):
                if local.resolve() == shared.resolve():
                    return
                raise SharedResourceConflict(conflict)
            if local.exists():
                if local.is_dir() and shared.is_dir():
                    for child in list(local.iterdir()):
                        merge(child, shared / child.name)
                        if child.is_symlink():
                            child.unlink()
                    local.rmdir()
                elif not shared.exists():
                    shared.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
                    local.rename(shared)
                elif local.is_file() and shared.is_file() and local.read_bytes() == shared.read_bytes():
                    # Keep a recoverable copy before replacing a local resource.
                    backup = lock_dir / "backups" / base.name / local.relative_to(config)
                    backup.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
                    if not backup.exists():
                        shutil.copy2(local, backup)
                    local.unlink()
                else:
                    # Never choose which conflicting instruction or skill wins.
                    raise SharedResourceConflict(conflict)
            if not shared.exists():
                if local.name in ("CLAUDE.md", "AGENTS.md"):
                    shared.touch(mode=0o600)
                else:
                    shared.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
            resource_link(local, shared)
        # claude.ai downloads skills into skills/synced. Keep that subtree
        # account-specific; link only locally installed skill entries.
        local, shared = config / "skills", common / "skills"
        local.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
        shared.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
        names = {p.name for folder in (local, shared) for p in folder.iterdir()} - {"synced"}
        resources = [(config / name, common / name) for name in
                     ("commands", "agents", "rules", "agent-memory", "CLAUDE.md", "AGENTS.md")]
        resources += [(local / name, shared / name) for name in sorted(names)]
        for source, destination in resources[4:6]:
            heal_shared_file(base, source, destination, record)
        # Validate every resource before moving any of them. A late conflict
        # must leave the account's existing directory layout usable.
        for source, destination in resources:
            check(source, destination)
        for source, destination in resources:
            if source.exists() and not source.is_symlink():
                backup = lock_dir / "backups" / base.name / source.relative_to(config)
                if backup.exists():
                    backup = backup.with_name(backup.name + "-" + str(time.time_ns()))
                backup.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
                if source.is_dir():
                    shutil.copytree(source, backup, symlinks=True)
                else:
                    shutil.copy2(source, backup)
            merge(source, destination)
        for source, destination in resources[4:6]:
            heal_shared_file(base, source, destination, record)
        plugins_shared = share_plugin_folders(base, lock_dir)
        if plugins_shared:
            sync_plugin_registries(base, lock_dir, record)
        else:
            print("账号已有的插件与共用插件来源不同，插件继续按账号分开保存；其他共用资料不受影响", file=sys.stderr)
        sync_profile_settings(base, lock_dir, record, plugins_shared)
        sync_profile_mcp(base, lock_dir, record, desktop)
        if desktop:
            link_known_project_memory(base)
        write_record(base, record)


def project_key(directory):
    """The official CLI's project folder name: UTF-16 units, 200 characters, then a hash."""
    units = str(directory).encode("utf-16-le")
    codes = [int.from_bytes(units[index:index + 2], "little") for index in range(0, len(units), 2)]
    key = "".join(chr(code) if code < 128 and (chr(code).isascii() and chr(code).isalnum()) else "-" for code in codes)
    if len(key) <= 200:
        return key
    value = 0
    for code in codes:
        value = (value * 31 + code) & 0xFFFFFFFF
    value = abs(value - (1 << 32) if value >= 1 << 31 else value)
    digits = ""
    while True:
        value, rest = divmod(value, 36)
        digits = "0123456789abcdefghijklmnopqrstuvwxyz"[rest] + digits
        if not value:
            break
    return key[:200] + "-" + digits


def legacy_project_key(directory):
    key = re.sub(r"[^a-zA-Z0-9]", "-", str(directory))
    return key[:140] + "-" + hashlib.sha256(str(directory).encode()).hexdigest()[:32] if len(key) > 180 else key


def link_project_memory(own, memory, backup):
    """Move a memory folder's notes into the shared one and leave a link behind."""
    if own == memory or linked(own):
        return
    if own.exists():
        sources = [source for source in own.rglob("*") if source.is_file()]
        for source in sources:
            destination = memory / source.relative_to(own)
            if destination.exists() and (not destination.is_file() or destination.read_bytes() != source.read_bytes()):
                raise SharedResourceConflict("账号的项目记忆与共用记忆冲突，原记忆已保留，请先核对")
        for source in sources:
            destination = memory / source.relative_to(own)
            if destination.exists() and destination.read_bytes() != source.read_bytes():
                raise SharedResourceConflict("账号的项目记忆与共用记忆冲突，原记忆已保留，请先核对")
            destination.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
            if not destination.exists():
                shutil.copy2(source, destination)
        backup.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
        if backup.exists():
            backup = backup.with_name(backup.name + "-" + str(time.time_ns()))
        own.rename(backup)
    resource_link(own, memory)


def shared_project_memory(base, directory):
    """Locate the shared memory of a project, folding in older Cage locations."""
    key = project_key(directory)
    projects = Path.home() / ".claude/projects"
    memory = projects / key / "memory"
    memory.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    legacy = legacy_project_key(directory)
    if legacy != key and (projects / legacy / "memory").is_dir():
        link_project_memory(projects / legacy / "memory", memory, shared_root() / "backups/common/projects" / legacy / "memory")
    for name in sorted({key, legacy}):
        own = config_directory(base) / "projects" / name / "memory"
        if name == key or own.is_dir() and not linked(own):
            link_project_memory(own, memory, shared_root() / "backups" / base.name / "projects" / name / "memory")
    return key, memory


def link_known_project_memory(base):
    """Give clients without launch flags (Desktop) the shared memory of known projects."""
    shared, own = Path.home() / ".claude/projects", config_directory(base) / "projects"
    keys = {path.parent.name for path in shared.glob("*/memory") if path.is_dir() and not linked(path)}
    keys |= {path.parent.name for path in own.glob("*/memory") if path.is_dir() and not linked(path)}
    for key in sorted(keys):
        try:
            memory = shared / key / "memory"
            memory.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
            link_project_memory(own / key / "memory", memory, shared_root() / "backups" / base.name / "projects" / key / "memory")
        except SharedResourceConflict as error:
            print(str(error), file=sys.stderr)


def shared_launch_arguments(base, args, settings_fd=None):
    """Apply a stable project memory location without sharing auth/settings files."""
    lock_dir = shared_root()
    lock_dir.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    with (lock_dir / "resources.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        return shared_launch_arguments_locked(base, args, settings_fd)


def shared_memory_path(memory):
    """Recover only an owned memory directory; never replace runtime executables."""
    try:
        return _runtime["path"](memory)
    except _runtime["RuntimePathConflict"] as error:
        alias = error.alias
    if alias.is_symlink() or not alias.is_dir() or os.name == "nt" and alias.is_junction():
        raise SharedResourceConflict("项目记忆的运行路径异常，原数据已保留，请同步环境组件后重试")
    if os.name != "nt" and alias.stat().st_uid != os.getuid():
        raise SharedResourceConflict("项目记忆的运行路径归属异常，原数据已保留，请检查目录权限")
    # Inspect the entire tree before copying. A conflicting note must never be
    # overwritten, and links must not redirect recovery outside this directory.
    entries = sorted(alias.rglob("*"))
    for source in entries:
        destination = memory / source.relative_to(alias)
        if source.is_symlink() or os.name == "nt" and source.is_junction() or destination.is_symlink():
            raise SharedResourceConflict("项目记忆包含异常链接，原数据已保留，请先核对记忆目录")
        if source.is_dir():
            conflict = destination.exists() and not destination.is_dir()
        elif source.is_file():
            conflict = destination.exists() and (not destination.is_file() or source.read_bytes() != destination.read_bytes())
        else:
            conflict = True
        if conflict:
            raise SharedResourceConflict("项目记忆存在同名内容冲突，两份记忆均已保留，请先核对后重试")
    backup = shared_root() / "backups/runtime-memory" / (alias.name + "-" + str(time.time_ns()))
    backup.parent.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    shutil.copytree(alias, backup, symlinks=True)
    for source in entries:
        destination = memory / source.relative_to(alias)
        if source.is_dir():
            destination.mkdir(mode=PRIVATE_MODE, exist_ok=True)
        else:
            # Exclusive publication also preserves a note written by an active
            # CLI after preflight. On failure both the source and backup remain.
            try:
                with destination.open("xb") as output, source.open("rb") as input:
                    shutil.copyfileobj(input, output)
            except FileExistsError:
                if not destination.is_file() or destination.read_bytes() != source.read_bytes():
                    raise SharedResourceConflict("项目记忆在恢复期间发生冲突，两份记忆均已保留，请先核对后重试")
    # Retain the original directory too: an older process may still hold it
    # open or write to it. Nothing in recovery recursively deletes user notes.
    alias.rename(alias.with_name(backup.name + ".recovered"))
    return _runtime["path"](memory)


def merged_hooks(shared, supplied):
    """Run launch-specific hooks in addition to the shared user hooks."""
    if not isinstance(shared, dict) or not isinstance(supplied, dict):
        return None
    result = {event: list(groups) for event, groups in shared.items() if isinstance(groups, list)}
    for event, groups in supplied.items():
        if not isinstance(groups, list):
            return None
        result[event] = result.get(event, []) + groups
    return result


def shared_launch_arguments_locked(base, args, settings_fd):
    directory = Path.cwd().resolve()
    try:
        git = subprocess.run(["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
                             cwd=directory, capture_output=True, text=True, timeout=2)
        if git.returncode == 0:
            common_git = Path(git.stdout.strip())
            if common_git.name == ".git":
                directory = common_git.parent.resolve()
    except (OSError, subprocess.SubprocessError):
        pass
    _, memory = shared_project_memory(base, directory)
    settings = {"autoMemoryDirectory": shared_memory_path(memory)}
    common_settings = json_file(Path.home() / ".claude/settings.json")
    shared_keys = SHARED_SETTINGS
    if isinstance(common_settings, dict):
        settings.update({key: value for key, value in common_settings.items() if key in shared_keys})
    # Hook groups already present in the profile's own settings (including
    # those copied there for Desktop) run from that file. Pass only the rest.
    own_hooks = json_file(config_directory(base) / "settings.json")
    own_hooks = own_hooks.get("hooks") if isinstance(own_hooks, dict) else None
    if isinstance(settings.get("hooks"), dict) and isinstance(own_hooks, dict):
        remaining = {}
        for event, groups in settings["hooks"].items():
            present = own_hooks.get(event)
            groups = [group for group in groups if not (isinstance(present, list) and group in present)] if isinstance(groups, list) else groups
            if groups:
                remaining[event] = groups
        settings["hooks"] = remaining
        if not remaining:
            del settings["hooks"]
    # Respect an explicit memory location in existing user/project settings.
    paths = [Path.home() / ".claude/settings.json", config_directory(base) / "settings.json"]
    for parent in reversed((Path.cwd(), *Path.cwd().parents)):
        paths += [parent / ".claude/settings.json", parent / ".claude/settings.local.json"]
    for path in paths:
        stored = json_file(path)
        if path != Path.home() / ".claude/settings.json" and isinstance(stored, dict):
            # Let the CLI's normal profile/project precedence decide these
            # values. A shared default must not turn into a command-line override.
            # The CLI combines hooks from every source instead of choosing one,
            # so project or account hooks must not disable the shared ones.
            for key in shared_keys & stored.keys() - {"hooks"}:
                settings.pop(key, None)
        if isinstance(stored, dict) and "autoMemoryDirectory" in stored:
            settings["autoMemoryDirectory"] = stored["autoMemoryDirectory"]
    result, iterator = [], iter(args)
    for arg in iterator:
        if arg == "--":
            result += [arg, *iterator]
            break
        if arg == "--settings" or arg.startswith("--settings="):
            value = next(iterator) if arg == "--settings" else arg.split("=", 1)[1]
            supplied = json.loads(value) if value.lstrip().startswith("{") else json.loads(Path(value).read_text())
            hooks = merged_hooks(settings.get("hooks"), supplied.get("hooks")) if isinstance(supplied, dict) else None
            settings.update(supplied)
            if hooks is not None:
                settings["hooks"] = hooks
        else:
            result.append(arg)
    data = json.dumps(settings, separators=(",", ":")).encode()
    if settings_fd is not None:
        # Mobile launch deliberately keeps settings in an anonymous file.
        # Sharing local defaults must not persist its provider material.
        with os.fdopen(os.dup(settings_fd), "r+b") as snapshot:
            snapshot.seek(0)
            snapshot.truncate()
            snapshot.write(data)
            snapshot.flush()
            snapshot.seek(0)
        return ["--settings", "/dev/fd/" + str(settings_fd), *result]
    folder = base / "launch-settings"
    folder.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    path = folder / (hashlib.sha256(data).hexdigest() + ".json")
    if not path.exists():
        with tempfile.NamedTemporaryFile(dir=folder, delete=False) as stream:
            stream.write(data)
            temporary = stream.name
        os.replace(temporary, path)
    return ["--settings", _runtime["path"](path), *result]


def main(args, *, mobile_settings_fd=None, verify_mobile=None):
    mode, account_id, *rest = args
    if mode == "quotas":
        result = {}
        for candidate in [account_id, *rest]:
            quota = read_quota(profile(candidate))
            if quota:
                result[candidate] = quota
        print(json.dumps(result))
        return 0
    base = profile(account_id)
    if account_id == DEFAULT and mode == "login":
        raise ValueError("new logins require a private profile")
    if mode in ("usage", "snapshot") and account_id == DEFAULT and not config_directory(base).is_dir():
        print(json.dumps(changed_identity() if mode == "usage" else {"logged_in": False}))
        return 0
    original_umask = os.umask(0o077)
    if account_id != DEFAULT:
        config_directory(base).mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    if mode == "share":
        # Desktop starts its own CLI without arguments; prepare the profile.
        if account_id != DEFAULT:
            shared_profile_resources(base, desktop=True)
        return 0
    elif mode == "statusline":
        base.mkdir(mode=PRIVATE_MODE, parents=True, exist_ok=True)
    env = cli_environment(base)
    if account_id == DEFAULT:
        directory = env.get("CLAUDE_CONFIG_DIR") or str(config_directory(base))
        endpoint = env.get("ANTHROPIC_BASE_URL", "").rstrip("/")
        if Path(directory).resolve() != config_directory(base).resolve() or endpoint not in ("", "https://api.anthropic.com"):
            if mode in ("snapshot", "usage"):
                print(json.dumps(changed_identity() if mode == "usage" else {"logged_in": False}))
                return 0
            raise ValueError("not the default subscription configuration")
    binary = os.environ.get("CAGE_CLI_BINARY") if os.name == "nt" else shutil.which("claude", path=env["PATH"])
    if not binary:
        raise ValueError("Claude is not installed")
    if mode == "archive-login":
        # Only authentication follows a user who signs into a different account.
        if len(rest) != 2 or base.name == DEFAULT or base.name == rest[0]:
            raise ValueError("invalid account archive")
        print(json.dumps(recover_login(binary, base, *rest, archive=True)))
        return 0
    if mode == "recover-login":
        if len(rest) != 2:
            raise ValueError("recovery requires source and expected identity")
        print(json.dumps(recover_login(binary, base, *rest)))
        return 0
    if mode == "usage":
        if len(rest) != 1:
            raise ValueError("usage requires an expected account")
        print(json.dumps(query_usage(binary, env, base, rest[0])))
        return 0
    expected = _controller_context.get("ACCOUNT_SUBJECT")
    if mode == "statusline":
        raw = sys.stdin.buffer.read((1 << 20) + 1)
        if len(raw) <= 1 << 20:
            save_usage(base, json.loads(raw), expected, lambda: subject(identity(binary, env, Path.cwd())))
        line = user_status_line()
        if line is None:
            print("Claude")
            return 0
        # Draw the user's own status line from the same input, as the CLI would.
        try:
            shown = subprocess.run(line["command"], shell=True, input=raw, stdout=subprocess.PIPE,
                                   stderr=subprocess.DEVNULL, timeout=10).stdout
        except (OSError, subprocess.SubprocessError):
            shown = b""
        sys.stdout.buffer.write(shown)
        return 0
    if mode == "snapshot":
        summary = identity(binary, env, Path.home())
        quota = read_quota(base)
        if quota and quota["identity"] == subject(summary):
            summary["quota"] = quota
        print(json.dumps(summary))
        return 0
    owner = _controller_context.get("LOGIN_OWNER", "")
    owner_args = ["--account-id", owner] if re.fullmatch("[0-9a-f]{24}", owner) else []
    scoped = _controller_context.get("ACCOUNT_BROWSER")
    if mode == "login":
        if os.name != "nt":
            env["BROWSER"] = _runtime["launcher"](scoped or OPEN, executable=True, arguments=[*owner_args, "--login-id", account_id])
        # Set the handler before spawning so SSH/window closure also disposes
        # the ephemeral browser and its cookies.
        def interrupted(_signum, _frame):
            raise KeyboardInterrupt()
        if hasattr(signal, "SIGHUP"):
            signal.signal(signal.SIGHUP, interrupted)
        signal.signal(signal.SIGTERM, interrupted)
        try:
            (base / "quota.json").unlink(missing_ok=True)
            return subprocess.call([_runtime["path"](binary), "auth", "login", *([] if os.name == "nt" else ["--claudeai"])], cwd=Path.home(), env=env)
        finally:
            if os.name != "nt":
                finish = [scoped or OPEN, *owner_args, "--login-id", account_id, "--finish-login"]
                subprocess.run(finish, env=os.environ, timeout=8, check=False,
                               stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    if mode == "run":
        if os.name != "nt" and (owner_args or scoped or sys.platform == "darwin"):
            env["BROWSER"] = _runtime["launcher"](scoped or OPEN, executable=True, arguments=owner_args, login_config=config_directory(base))
        shared_profile_resources(base)
        if not settings_flags(rest) and not has_status_line(base):
            rest = ["--settings", json.dumps({"statusLine": status_line(_runtime["command_text"](__file__, ["statusline", account_id]))}), *rest]
        rest = shared_launch_arguments(base, rest, mobile_settings_fd)
        rest = shared_mcp_arguments(base, rest)
        # Profile/project settings can override inherited authentication. Check
        # the actual CLI method in the project before launching the conversation.
        options = settings_flags(rest)
        # A mobile turn verifies and executes with the same anonymous settings
        # snapshot. Keep its descriptor open across the auth-status child.
        pass_fds = () if mobile_settings_fd is None else (mobile_settings_fd,)
        reading = identity(binary, env, Path.cwd(), options, pass_fds=pass_fds) if expected else None
        if not expected or subject(reading) != expected:
            print("此项目未使用所选 Claude 订阅。请检查项目配置和账号登录状态。", file=sys.stderr)
            return 1
        if mobile_settings_fd is not None:
            verify_mobile(reading)
            os.lseek(mobile_settings_fd, 0, os.SEEK_SET)
        # The private profile is ready. Claude and its tools must create files
        # in the shared project using the user's usual umask.
        os.umask(original_umask)
        if os.name == "nt":
            return subprocess.call([_runtime["path"](binary), *rest], env=env)
        os.execve(binary, [binary, *rest], env)
    raise ValueError("unknown operation")


def entrypoint(args):
    try:
        return main(args)
    except KeyboardInterrupt:
        return 130
    except SharedResourceConflict as error:
        print(str(error), file=sys.stderr)
    except SubscriptionStatusTimeout:
        print("读取 Claude 订阅状态超时，请稍后重试；现有登录信息已保留。", file=sys.stderr)
    except SubscriptionStatusError:
        print("无法读取 Claude 订阅状态，请确认官方 CLI 可正常运行后重试；现有登录信息已保留。", file=sys.stderr)
    except _runtime["RuntimePathConflict"]:
        print("终端运行路径发生冲突，原数据已保留，请同步环境组件后重试。", file=sys.stderr)
    except (OSError, ValueError, subprocess.SubprocessError, TypeError):
        # No raw CLI output, paths, credentials or callback URLs go to logs.
        print("准备终端或启动官方 Claude CLI 失败，请检查目录权限并同步环境组件后重试；现有登录信息已保留。", file=sys.stderr)
    return 1


if __name__ == "__main__":
    sys.exit(entrypoint(sys.argv[1:]))
