#!/usr/bin/env python3
"""Apply light or dark theme preferences across guest applications."""

from __future__ import annotations

import http.client
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
from pathlib import Path

PROFILE_ID_RE = re.compile(r"^[a-f0-9]{24}$")
CODE_THEME = {
    "light": "Default Light Modern",
    "dark": "Default Dark Modern",
}
CODE_SELECTION = {
    "light": "#d5e5ff",
    "dark": "#264f78",
}
DEFAULT_SELECTIONS = frozenset(v.lower() for v in CODE_SELECTION.values())
DEFAULT_CODE_THEMES = frozenset(CODE_THEME.values())
# Claude Code themes come in light/dark pairs; a chosen family is kept.
CLI_THEME_RE = re.compile(r"^(light|dark)(-daltonized|-ansi)?$")
GTK_THEME = {
    "light": ("Adwaita", "0", "prefer-light"),
    "dark": ("Adwaita-dark", "1", "prefer-dark"),
}
XSETTINGS_THEME_PREFIXES = ("Net/ThemeName ", "Gtk/ApplicationPreferDarkTheme ")


def is_safe_target(path: Path) -> bool:
    return not path.is_symlink()


def atomic_write_text(path: Path, content: str, mode: int | None = None) -> None:
    if not is_safe_target(path):
        return
    path.parent.mkdir(parents=True, exist_ok=True)
    if mode is None:
        try:
            mode = path.stat().st_mode & 0o777 if path.exists() else 0o600
        except OSError:
            mode = 0o600
    fd, tmp_name = tempfile.mkstemp(prefix="." + path.name + "-", dir=path.parent)
    tmp_path = Path(tmp_name)
    try:
        if hasattr(os, "fchmod"):
            os.fchmod(fd, mode)
        with os.fdopen(fd, "w", encoding="utf-8", newline="\n") as handle:
            handle.write(content)
            handle.flush()
            os.fsync(handle.fileno())
        if not hasattr(os, "fchmod"):
            os.chmod(tmp_path, mode)
        os.replace(tmp_path, path)
        try:
            dir_fd = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(dir_fd)
            finally:
                os.close(dir_fd)
        except OSError:
            pass
    except BaseException:
        try:
            tmp_path.unlink(missing_ok=True)
        except OSError:
            pass
        raise


def update_json_dict(
    path: Path,
    mutator,
    *,
    create_if_missing: bool,
    default_mode: int = 0o600,
) -> None:
    if not is_safe_target(path):
        return
    if not path.exists():
        if not create_if_missing:
            return
        data: dict = {}
        mode = default_mode
    else:
        if not path.is_file():
            return
        try:
            mode = path.stat().st_mode & 0o777
            loaded = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, ValueError):
            return
        if not isinstance(loaded, dict):
            return
        data = loaded
    if not mutator(data):
        return
    text = json.dumps(data, indent=2, ensure_ascii=False) + "\n"
    try:
        text.encode("utf-8")
    except UnicodeEncodeError:
        # A lone surrogate escape in the existing file cannot be written as
        # UTF-8; escaping keeps the value byte-for-byte equivalent as JSON.
        text = json.dumps(data, indent=2) + "\n"
    atomic_write_text(path, text, mode=mode)


def synced_cli_theme(current, variant: str):
    """The theme to store for variant, or None to leave the file alone."""
    if current is None:
        return variant
    if not isinstance(current, str):
        return None
    match = CLI_THEME_RE.fullmatch(current)
    if not match:
        return None
    target = variant + (match.group(2) or "")
    return None if target == current else target


def update_code_server(home: Path, variant: str) -> None:
    settings_path = home / ".local/share/code-server/User/settings.json"
    target_theme = CODE_THEME[variant]
    target_selection = CODE_SELECTION[variant]

    def mutate(data: dict) -> bool:
        changed = False
        current_theme = data.get("workbench.colorTheme")
        # A theme the user picked in the editor stays; only the stock pair follows.
        if current_theme != target_theme and (
            current_theme is None or current_theme in DEFAULT_CODE_THEMES
        ):
            data["workbench.colorTheme"] = target_theme
            changed = True
        custom = data.get("workbench.colorCustomizations")
        if custom is None:
            data["workbench.colorCustomizations"] = {
                "editor.selectionBackground": target_selection
            }
            changed = True
        elif isinstance(custom, dict):
            current = custom.get("editor.selectionBackground")
            if current is None or (
                isinstance(current, str) and current.lower() in DEFAULT_SELECTIONS
            ):
                if current != target_selection:
                    custom["editor.selectionBackground"] = target_selection
                    changed = True
        return changed

    update_json_dict(
        settings_path,
        mutate,
        create_if_missing=True,
        default_mode=0o644,
    )


def update_gtk_ini(path: Path, variant: str) -> None:
    if not is_safe_target(path):
        return
    if path.exists() and not path.is_file():
        return
    gtk_theme, prefer_dark, _ = GTK_THEME[variant]
    existing_lines: list[str] = []
    if path.exists():
        try:
            existing_lines = path.read_text(encoding="utf-8").splitlines()
        except (OSError, ValueError):
            return

    desired = {
        "gtk-application-prefer-dark-theme": prefer_dark,
        "gtk-theme-name": gtk_theme,
    }
    output: list[str] = []
    in_settings = False
    seen_settings = False
    written: set[str] = set()

    for line in existing_lines:
        stripped = line.strip()
        if stripped.startswith("[") and stripped.endswith("]"):
            if in_settings:
                for key, value in desired.items():
                    if key not in written:
                        output.append(f"{key}={value}")
                        written.add(key)
            section = stripped[1:-1].strip()
            in_settings = section == "Settings"
            if in_settings:
                seen_settings = True
            output.append(line)
            continue

        if in_settings and "=" in line and not stripped.startswith(("#", ";")):
            key = line.split("=", 1)[0].strip()
            if key in desired:
                if key not in written:
                    output.append(f"{key}={desired[key]}")
                    written.add(key)
                continue
        output.append(line)

    if not seen_settings:
        if output and output[-1] != "":
            output.append("")
        output.append("[Settings]")
        for key, value in desired.items():
            output.append(f"{key}={value}")
    elif in_settings:
        for key, value in desired.items():
            if key not in written:
                output.append(f"{key}={value}")

    new_text = "\n".join(output) + "\n"
    if existing_lines and "\n".join(existing_lines) + "\n" == new_text:
        return
    atomic_write_text(path, new_text, mode=0o644)


def update_gtk_configs(home: Path, variant: str) -> None:
    config_roots = [home / ".config"]
    profiles_dir = home / ".local/share/desktop-profiles"
    if profiles_dir.is_dir() and not profiles_dir.is_symlink():
        for entry in sorted(profiles_dir.iterdir()):
            if (
                PROFILE_ID_RE.fullmatch(entry.name)
                and entry.is_dir()
                and not entry.is_symlink()
                and (entry / "home").is_dir()
                and not (entry / "home").is_symlink()
            ):
                config_roots.append(entry / "home/.config")

    for root in config_roots:
        for version in ("gtk-3.0", "gtk-4.0"):
            update_gtk_ini(root / version / "settings.ini", variant)


def xsettings_theme_lines(variant: str) -> list[str]:
    gtk_theme, prefer_dark, _ = GTK_THEME[variant]
    return [
        f'Net/ThemeName "{gtk_theme}"',
        f"Gtk/ApplicationPreferDarkTheme {prefer_dark}",
    ]


def rewrite_xsettings_file(
    path: Path,
    variant: str,
    *,
    default_lines: list[str] | None = None,
) -> bool:
    if not is_safe_target(path):
        return False
    lines: list[str] = []
    if path.exists():
        if not path.is_file():
            return False
        try:
            lines = [
                line
                for line in path.read_text(encoding="utf-8").splitlines()
                if not line.startswith(XSETTINGS_THEME_PREFIXES)
            ]
        except (OSError, ValueError):
            return False
    elif default_lines is not None:
        lines = list(default_lines)
    else:
        return False

    new_text = "\n".join([*lines, *xsettings_theme_lines(variant)]) + "\n"
    atomic_write_text(path, new_text, mode=0o600)
    return True


def runtime_dirs() -> list[Path]:
    override = os.environ.get("CAGE_THEME_RUNTIME_DIRS", "")
    if override.strip():
        return [Path(part) for part in override.split(os.pathsep) if part]
    dirs = [Path("/run/cage-desktop")]
    run_root = Path("/run")
    if run_root.is_dir():
        dirs.extend(sorted(run_root.glob("desktop-*")))
    return dirs


def reload_xsettingsd(runtime_dir: Path, *, proc_root: Path = Path("/proc")) -> None:
    pid_file = runtime_dir / "xsettings.pid"
    if not pid_file.is_file() or pid_file.is_symlink():
        return
    try:
        pid = int(pid_file.read_text(encoding="utf-8").strip())
    except (OSError, ValueError):
        return
    if pid <= 1:
        return
    if proc_root.is_dir():
        proc_dir = proc_root / str(pid)
        comm_file = proc_dir / "comm"
        try:
            if hasattr(os, "geteuid") and proc_dir.stat().st_uid != os.geteuid():
                return
            if comm_file.read_text(encoding="utf-8").strip() != "xsettingsd":
                return
        except OSError:
            return
    sighup = getattr(signal, "SIGHUP", None)
    if sighup is None:
        return
    try:
        os.kill(pid, sighup)
    except OSError:
        pass


def gsettings_binary() -> str | None:
    fixed = Path("/usr/bin/gsettings")
    if fixed.is_file() and os.access(fixed, os.X_OK):
        return str(fixed)
    return shutil.which("gsettings")


def update_gsettings(runtime_dir: Path, variant: str) -> None:
    bus_file = runtime_dir / "bus-address"
    if not bus_file.is_file() or bus_file.is_symlink():
        return
    try:
        bus_addr = bus_file.read_text(encoding="utf-8").strip()
    except OSError:
        return
    if not bus_addr or "\n" in bus_addr or "\0" in bus_addr:
        return
    binary = gsettings_binary()
    if not binary:
        return
    gtk_theme, _, color_scheme = GTK_THEME[variant]
    env = {
        **os.environ,
        "DBUS_SESSION_BUS_ADDRESS": bus_addr,
        "XDG_RUNTIME_DIR": str(runtime_dir),
    }
    for key, value in (("color-scheme", color_scheme), ("gtk-theme", gtk_theme)):
        try:
            subprocess.run(
                [binary, "set", "org.gnome.desktop.interface", key, value],
                env=env,
                stdin=subprocess.DEVNULL,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=3,
                check=False,
            )
        except (OSError, subprocess.SubprocessError):
            pass


class _UnixHTTPConnection(http.client.HTTPConnection):
    def __init__(self, socket_path: Path, timeout: float = 2.0) -> None:
        super().__init__("localhost", timeout=timeout)
        self._socket_path = str(socket_path)

    def connect(self) -> None:
        af_unix = getattr(socket, "AF_UNIX", None)
        if af_unix is None:
            raise OSError("unix sockets unavailable")
        sock = socket.socket(af_unix, socket.SOCK_STREAM)
        try:
            sock.settimeout(self.timeout)
            sock.connect(self._socket_path)
        except OSError:
            sock.close()
            raise
        self.sock = sock


def notify_desktop_portal(runtime_dir: Path, variant: str) -> None:
    sock_path = runtime_dir / "files.sock"
    try:
        if sock_path.is_symlink() or not sock_path.is_socket():
            return
    except OSError:
        return
    conn = _UnixHTTPConnection(sock_path, timeout=2.0)
    try:
        body = json.dumps({"variant": variant}).encode("utf-8")
        conn.request(
            "POST",
            "/appearance",
            body=body,
            headers={"Content-Type": "application/json"},
        )
        resp = conn.getresponse()
        resp.read()
    except (OSError, http.client.HTTPException):
        pass
    finally:
        conn.close()


def update_desktop_runtimes(variant: str) -> None:
    euid = os.geteuid() if hasattr(os, "geteuid") else None
    for runtime_dir in runtime_dirs():
        if not runtime_dir.exists() or runtime_dir.is_symlink() or not runtime_dir.is_dir():
            continue
        if euid is not None:
            try:
                if runtime_dir.stat().st_uid != euid:
                    continue
            except OSError:
                continue

        fixed_file = runtime_dir / "xsettings.fixed"
        conf_file = runtime_dir / "xsettings.conf"
        has_runtime = any(
            (runtime_dir / name).exists()
            for name in ("xsettings.conf", "xsettings.pid", "bus-address", "files.sock")
        )
        if not has_runtime:
            continue

        if fixed_file.exists():
            rewrite_xsettings_file(fixed_file, variant)
        elif conf_file.exists():
            rewrite_xsettings_file(
                fixed_file,
                variant,
                default_lines=['Gtk/DecorationLayout ":"'],
            )
        if conf_file.exists() and rewrite_xsettings_file(conf_file, variant):
            reload_xsettingsd(runtime_dir)
        update_gsettings(runtime_dir, variant)
        notify_desktop_portal(runtime_dir, variant)


def update_claude_cli(home: Path, variant: str) -> None:
    def set_top_theme(data: dict) -> bool:
        target = synced_cli_theme(data.get("theme"), variant)
        if target is None:
            return False
        data["theme"] = target
        return True

    def set_shared_theme(data: dict) -> bool:
        keys = data.get("keys")
        if not isinstance(keys, dict):
            return False
        target = synced_cli_theme(keys.get("theme"), variant)
        if target is None:
            return False
        keys["theme"] = target
        return True

    update_json_dict(home / ".claude/settings.json", set_top_theme, create_if_missing=True)
    update_json_dict(home / ".claude.json", set_top_theme, create_if_missing=False)
    update_json_dict(
        home / ".local/share/cage/claude-shared/settings.json",
        set_top_theme,
        create_if_missing=False,
    )

    accounts_dir = home / ".local/share/cage/claude-accounts"
    if accounts_dir.is_dir() and not accounts_dir.is_symlink():
        for entry in sorted(accounts_dir.iterdir()):
            if (
                not PROFILE_ID_RE.fullmatch(entry.name)
                or not entry.is_dir()
                or entry.is_symlink()
            ):
                continue
            cfg_dir = entry / "config"
            if cfg_dir.is_dir() and not cfg_dir.is_symlink():
                update_json_dict(
                    cfg_dir / "settings.json",
                    set_top_theme,
                    create_if_missing=True,
                )
                update_json_dict(
                    cfg_dir / ".claude.json",
                    set_top_theme,
                    create_if_missing=False,
                )
            update_json_dict(
                entry / "shared-settings.json",
                set_shared_theme,
                create_if_missing=False,
            )

    profiles_dir = home / ".local/share/desktop-profiles"
    if profiles_dir.is_dir() and not profiles_dir.is_symlink():
        for entry in sorted(profiles_dir.iterdir()):
            if (
                not PROFILE_ID_RE.fullmatch(entry.name)
                or not entry.is_dir()
                or entry.is_symlink()
            ):
                continue
            code_dir = entry / "code"
            if code_dir.is_dir() and not code_dir.is_symlink():
                update_json_dict(
                    code_dir / "settings.json",
                    set_top_theme,
                    create_if_missing=True,
                )
                update_json_dict(
                    code_dir / ".claude.json",
                    set_top_theme,
                    create_if_missing=False,
                )


def apply_theme(variant: str, *, home: Path | None = None) -> list[str]:
    """Apply variant everywhere it can be applied; return the steps that failed."""
    if variant not in ("light", "dark"):
        raise ValueError(f"unsupported theme variant: {variant!r}")
    root = home or Path.home()
    steps = (
        ("variant", lambda: atomic_write_text(root / ".config/claude-cage/theme-variant", variant + "\n", mode=0o600)),
        ("editor", lambda: update_code_server(root, variant)),
        ("gtk", lambda: update_gtk_configs(root, variant)),
        ("desktop", lambda: update_desktop_runtimes(variant)),
        ("cli", lambda: update_claude_cli(root, variant)),
    )
    failed: list[str] = []
    for name, step in steps:
        # Appearance is cosmetic: one unwritable or unreadable file must not
        # stop the remaining targets, nor the caller that starts the session.
        try:
            step()
        except (OSError, ValueError, subprocess.SubprocessError) as exc:
            failed.append(name)
            print(f"theme: {name}: {type(exc).__name__}: {exc}", file=sys.stderr)
    return failed


def main(argv: list[str] | None = None) -> int:
    args = sys.argv[1:] if argv is None else argv
    if len(args) != 1 or args[0] not in ("light", "dark"):
        print("usage: cage-apply-theme <light|dark>", file=sys.stderr)
        return 2
    return 1 if apply_theme(args[0]) else 0


if __name__ == "__main__":
    raise SystemExit(main())
