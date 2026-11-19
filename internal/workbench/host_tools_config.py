# Adds one MCP server to every Claude client of this user: Claude Code reads
# it over HTTP, the desktop app starts a small relay program instead.
# ENTRY and RELAY (JSON strings) are defined by the lines placed before this
# script; the server's name is the first argument and "add" or "remove" the
# second.
import json
import os
import sys
import tempfile
from pathlib import Path


def profile_files(home):
    files = [home / ".claude.json"]
    accounts = home / ".local/share/cage/claude-accounts"
    files += [d / ".claude.json" for d in sorted(accounts.glob("*/config")) if d.is_dir()]
    return files


def write_private(path, text, mode=0o600):
    """Replaces path in one step; returns whether its content changed."""
    try:
        if path.read_text(encoding="utf-8") == text:
            return False
    except (FileNotFoundError, ValueError):
        pass
    path.parent.mkdir(parents=True, exist_ok=True)
    handle, temporary = tempfile.mkstemp(dir=path.parent, prefix="." + path.name + ".")
    try:
        with os.fdopen(handle, "w", encoding="utf-8") as output:
            output.write(text)
        os.chmod(temporary, mode)
        os.replace(temporary, path)
    except BaseException:
        os.unlink(temporary)
        raise
    return True


def add_server(path, name, entry, ours):
    """Adds the entry to a client's configuration; returns whether it wrote.

    ours tells whether an existing entry of this name is one written here, as
    opposed to the user's own server, which is kept.
    """
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except FileNotFoundError:
        data = {}
    except ValueError:
        # The client may be part-way through rewriting it; the next start retries.
        return False
    if not isinstance(data, dict):
        return False
    servers = data.get("mcpServers")
    if not isinstance(servers, dict):
        servers = {}
    current = servers.get(name)
    if current == entry or (current is not None and not ours(current)):
        return False
    servers[name] = entry
    data["mcpServers"] = servers
    mode = path.stat().st_mode & 0o777 if path.exists() else 0o600
    return write_private(path, json.dumps(data, indent=2), mode)


def remove_server(path, name, ours):
    """Removes the entry written here from a client's configuration."""
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
    except (FileNotFoundError, ValueError):
        return False
    servers = data.get("mcpServers") if isinstance(data, dict) else None
    if not isinstance(servers, dict) or name not in servers or not ours(servers[name]):
        return False
    del servers[name]
    if not servers:
        del data["mcpServers"]
    return write_private(path, json.dumps(data, indent=2), path.stat().st_mode & 0o777)


def relay_files(home, name):
    return home / ".local/share/mcp-relay/relay.py", home / ".config/mcp-relay" / (name + ".json")


def configure(home, name, entry, relay):
    for file in profile_files(home):
        add_server(file, name, entry, lambda current: isinstance(current, dict) and current.get("url") == entry["url"])
    desktop = home / ".config/Claude"
    if not desktop.is_dir():
        return
    program, endpoint = relay_files(home, name)
    write_private(program, relay)
    write_private(endpoint, json.dumps({"url": entry["url"], "headers": entry.get("headers", {})}))
    local = {"command": "/usr/bin/python3", "args": [str(program), str(endpoint)]}
    add_server(desktop / "claude_desktop_config.json", name, local, lambda current: isinstance(current, dict) and current.get("args") == local["args"])


def unconfigure(home, name, entry):
    """Takes back what configure wrote, leaving the user's own servers."""
    for file in profile_files(home):
        remove_server(file, name, lambda current: isinstance(current, dict) and current.get("url") == entry["url"])
    program, endpoint = relay_files(home, name)
    args = [str(program), str(endpoint)]
    remove_server(home / ".config/Claude/claude_desktop_config.json", name, lambda current: isinstance(current, dict) and current.get("args") == args)
    # The endpoint file holds the token; the relay program has no other user.
    for file in (endpoint, program):
        try:
            file.unlink()
        except FileNotFoundError:
            pass
    for directory in (endpoint.parent, program.parent):
        try:
            directory.rmdir()
        except OSError:
            pass


if __name__ == "__main__":
    if sys.argv[2] == "remove":
        unconfigure(Path.home(), sys.argv[1], json.loads(ENTRY))  # noqa: F821
    else:
        configure(Path.home(), sys.argv[1], json.loads(ENTRY), RELAY)  # noqa: F821
