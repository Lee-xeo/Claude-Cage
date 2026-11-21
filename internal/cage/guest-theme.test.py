#!/usr/bin/env python3
"""Tests for internal/cage/guest-theme.py."""

from __future__ import annotations

from http.client import parse_headers
import importlib.util
import json
import os
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import unittest
from pathlib import Path
from unittest import mock

SCRIPT_PATH = Path(__file__).with_name("guest-theme.py")
SPEC = importlib.util.spec_from_file_location("guest_theme", SCRIPT_PATH)
assert SPEC and SPEC.loader
guest_theme = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(guest_theme)


class GuestThemeTests(unittest.TestCase):
    def test_cli_rejects_invalid_arguments(self) -> None:
        for args in ([], ["sepia"], ["light", "dark"]):
            proc = subprocess.run(
                [sys.executable, str(SCRIPT_PATH), *args],
                capture_output=True,
                text=True,
                check=False,
            )
            self.assertEqual(proc.returncode, 2, msg=f"args={args} stderr={proc.stderr}")

    def test_apply_dark_and_light_updates_code_server_gtk_and_claude_cli(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            home.mkdir()

            # Pre-seed code-server settings with custom unrelated keys.
            cs_path = home / ".local/share/code-server/User/settings.json"
            cs_path.parent.mkdir(parents=True)
            cs_path.write_text(
                json.dumps(
                    {
                        "editor.fontSize": 14,
                        "workbench.colorTheme": "Default Light Modern",
                        "workbench.colorCustomizations": {
                            "editor.selectionBackground": "#d5e5ff",
                            "editorCursor.foreground": "#112233",
                        },
                    }
                ),
                encoding="utf-8",
            )

            # Pre-seed GTK3 INI with unrelated section & key.
            gtk3 = home / ".config/gtk-3.0/settings.ini"
            gtk3.parent.mkdir(parents=True)
            gtk3.write_text(
                "# user comment\n[Settings]\ngtk-font-name=Sans 11\ngtk-theme-name=OldTheme\n\n[Custom]\nflag=yes\n",
                encoding="utf-8",
            )

            # Pre-seed Claude CLI existing files vs absent ~/.claude.json.
            claude_settings = home / ".claude/settings.json"
            claude_settings.parent.mkdir(parents=True)
            claude_settings.write_text(json.dumps({"model": "sonnet"}), encoding="utf-8")

            shared_settings = home / ".local/share/cage/claude-shared/settings.json"
            shared_settings.parent.mkdir(parents=True)
            shared_settings.write_text(json.dumps({"language": "zh"}), encoding="utf-8")

            acc_id = "a" * 24
            acc_dir = home / ".local/share/cage/claude-accounts" / acc_id
            (acc_dir / "config").mkdir(parents=True)
            (acc_dir / "config/settings.json").write_text(json.dumps({"env": {}}), encoding="utf-8")
            (acc_dir / "config/.claude.json").write_text(json.dumps({"numStartups": 3}), encoding="utf-8")
            (acc_dir / "shared-settings.json").write_text(
                json.dumps({"version": 1, "keys": {"theme": "light", "model": "opus"}}),
                encoding="utf-8",
            )

            prof_id = "b" * 24
            prof_dir = home / ".local/share/desktop-profiles" / prof_id
            (prof_dir / "home").mkdir(parents=True)
            (prof_dir / "code").mkdir(parents=True)
            (prof_dir / "code/settings.json").write_text(json.dumps({"model": "haiku"}), encoding="utf-8")
            (prof_dir / "code/.claude.json").write_text(json.dumps({"mcpServers": {}}), encoding="utf-8")

            guest_theme.apply_theme("dark", home=home)

            self.assertEqual(
                (home / ".config/claude-cage/theme-variant").read_text(encoding="utf-8"),
                "dark\n",
            )
            cs_data = json.loads(cs_path.read_text(encoding="utf-8"))
            self.assertEqual(cs_data["workbench.colorTheme"], "Default Dark Modern")
            self.assertEqual(cs_data["editor.fontSize"], 14)
            self.assertEqual(
                cs_data["workbench.colorCustomizations"],
                {
                    "editor.selectionBackground": "#264f78",
                    "editorCursor.foreground": "#112233",
                },
            )

            gtk3_text = gtk3.read_text(encoding="utf-8")
            self.assertIn("gtk-font-name=Sans 11", gtk3_text)
            self.assertIn("gtk-theme-name=Adwaita-dark", gtk3_text)
            self.assertIn("gtk-application-prefer-dark-theme=1", gtk3_text)
            self.assertIn("[Custom]\nflag=yes", gtk3_text)

            gtk4_text = (home / ".config/gtk-4.0/settings.ini").read_text(encoding="utf-8")
            self.assertIn("gtk-theme-name=Adwaita-dark", gtk4_text)
            self.assertIn("gtk-application-prefer-dark-theme=1", gtk4_text)

            prof_gtk3 = (prof_dir / "home/.config/gtk-3.0/settings.ini").read_text(encoding="utf-8")
            self.assertIn("gtk-theme-name=Adwaita-dark", prof_gtk3)

            # Never create missing ~/.claude.json.
            self.assertFalse((home / ".claude.json").exists())
            self.assertEqual(json.loads(claude_settings.read_text(encoding="utf-8")), {"model": "sonnet", "theme": "dark"})
            self.assertEqual(json.loads(shared_settings.read_text(encoding="utf-8")), {"language": "zh", "theme": "dark"})
            self.assertEqual(json.loads((acc_dir / "config/.claude.json").read_text(encoding="utf-8")), {"numStartups": 3, "theme": "dark"})
            self.assertEqual(
                json.loads((acc_dir / "shared-settings.json").read_text(encoding="utf-8")),
                {"version": 1, "keys": {"theme": "dark", "model": "opus"}},
            )
            self.assertEqual(json.loads((prof_dir / "code/settings.json").read_text(encoding="utf-8"))["theme"], "dark")
            self.assertEqual(json.loads((prof_dir / "code/.claude.json").read_text(encoding="utf-8")), {"mcpServers": {}, "theme": "dark"})

            # Custom code-server selection color is preserved when switching back to light, and existing ~/.claude.json IS updated.
            cs_data["workbench.colorCustomizations"]["editor.selectionBackground"] = "#ff00ff"
            cs_path.write_text(json.dumps(cs_data), encoding="utf-8")
            (home / ".claude.json").write_text(json.dumps({"numStartups": 5}), encoding="utf-8")

            guest_theme.apply_theme("light", home=home)

            self.assertEqual((home / ".config/claude-cage/theme-variant").read_text(encoding="utf-8"), "light\n")
            cs_after = json.loads(cs_path.read_text(encoding="utf-8"))
            self.assertEqual(cs_after["workbench.colorTheme"], "Default Light Modern")
            self.assertEqual(cs_after["workbench.colorCustomizations"]["editor.selectionBackground"], "#ff00ff")
            self.assertEqual(json.loads((home / ".claude.json").read_text(encoding="utf-8")), {"numStartups": 5, "theme": "light"})
            self.assertIn("gtk-theme-name=Adwaita\n", gtk3.read_text(encoding="utf-8"))
            self.assertIn("gtk-application-prefer-dark-theme=0\n", gtk3.read_text(encoding="utf-8"))

    def test_runtime_xsettings_pid_verification_and_gsettings(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            home = root / "home"
            home.mkdir()
            runtime = root / "run-desktop"
            runtime.mkdir()
            (runtime / "xsettings.fixed").write_text(
                'Gtk/DecorationLayout ":"\nGtk/EnableAnimations 0\nNet/ThemeName "Adwaita"\n',
                encoding="utf-8",
            )
            (runtime / "xsettings.conf").write_text(
                'Xft/DPI 147456\nGdk/WindowScalingFactor 1\nGdk/UnscaledDPI 147456\nGtk/DecorationLayout ":"\nGtk/EnableAnimations 0\nNet/ThemeName "Adwaita"\n',
                encoding="utf-8",
            )
            (runtime / "xsettings.pid").write_text("4242\n", encoding="utf-8")
            (runtime / "bus-address").write_text("unix:path=/tmp/test-dbus\n", encoding="utf-8")

            proc_root = root / "proc"
            (proc_root / "4242").mkdir(parents=True)
            (proc_root / "4242/comm").write_text("xsettingsd\n", encoding="utf-8")
            (proc_root / "9999").mkdir(parents=True)
            (proc_root / "9999/comm").write_text("python3\n", encoding="utf-8")

            killed: list[tuple[int, int]] = []
            sub_calls: list[tuple[list[str], str]] = []

            def fake_kill(pid: int, sig: int) -> None:
                killed.append((pid, sig))

            def fake_run(cmd, env=None, **_kwargs):
                sub_calls.append((cmd, env.get("DBUS_SESSION_BUS_ADDRESS", "")))
                return subprocess.CompletedProcess(cmd, 0)

            sighup = getattr(signal, "SIGHUP", 1)
            with (
                mock.patch.dict(os.environ, {"CAGE_THEME_RUNTIME_DIRS": str(runtime)}),
                mock.patch.object(guest_theme.os, "kill", side_effect=fake_kill),
                mock.patch.object(guest_theme.signal, "SIGHUP", sighup, create=True),
                mock.patch.object(guest_theme, "reload_xsettingsd", wraps=lambda d: guest_theme._orig_reload(d) if hasattr(guest_theme, "_orig_reload") else None),
            ):
                pass

            orig_reload = guest_theme.reload_xsettingsd
            with (
                mock.patch.dict(os.environ, {"CAGE_THEME_RUNTIME_DIRS": str(runtime)}),
                mock.patch.object(guest_theme.os, "kill", side_effect=fake_kill),
                mock.patch.object(guest_theme.signal, "SIGHUP", sighup, create=True),
                mock.patch.object(guest_theme, "gsettings_binary", return_value="/usr/bin/gsettings"),
                mock.patch.object(guest_theme.subprocess, "run", side_effect=fake_run),
                mock.patch.object(guest_theme, "reload_xsettingsd", side_effect=lambda d: orig_reload(d, proc_root=proc_root)),
            ):
                guest_theme.apply_theme("dark", home=home)
                # Switching to non-xsettingsd PID must not send SIGHUP.
                (runtime / "xsettings.pid").write_text("9999\n", encoding="utf-8")
                guest_theme.apply_theme("light", home=home)

            fixed = (runtime / "xsettings.fixed").read_text(encoding="utf-8")
            self.assertIn('Gtk/DecorationLayout ":"', fixed)
            self.assertIn("Gtk/EnableAnimations 0", fixed)
            self.assertIn('Net/ThemeName "Adwaita"', fixed)
            self.assertNotIn("Adwaita-dark", fixed)
            self.assertEqual(fixed.count("Net/ThemeName"), 1)

            conf = (runtime / "xsettings.conf").read_text(encoding="utf-8")
            self.assertIn("Xft/DPI 147456", conf)
            self.assertIn("Gtk/ApplicationPreferDarkTheme 0", conf)
            self.assertEqual(conf.count("Gtk/ApplicationPreferDarkTheme"), 1)

            self.assertEqual(killed, [(4242, sighup)])
            self.assertEqual(
                sub_calls,
                [
                    (["/usr/bin/gsettings", "set", "org.gnome.desktop.interface", "color-scheme", "prefer-dark"], "unix:path=/tmp/test-dbus"),
                    (["/usr/bin/gsettings", "set", "org.gnome.desktop.interface", "gtk-theme", "Adwaita-dark"], "unix:path=/tmp/test-dbus"),
                    (["/usr/bin/gsettings", "set", "org.gnome.desktop.interface", "color-scheme", "prefer-light"], "unix:path=/tmp/test-dbus"),
                    (["/usr/bin/gsettings", "set", "org.gnome.desktop.interface", "gtk-theme", "Adwaita"], "unix:path=/tmp/test-dbus"),
                ],
            )

    def test_corrupt_json_and_symlinks_are_not_overwritten(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            home = Path(tmp) / "home"
            home.mkdir()
            bad = home / ".claude/settings.json"
            bad.parent.mkdir(parents=True)
            bad.write_text("{corrupt", encoding="utf-8")

            secret = Path(tmp) / "outside.json"
            secret.write_text('{"keep":true}', encoding="utf-8")
            linked = home / ".claude.json"
            try:
                linked.symlink_to(secret)
                symlink_created = True
            except (OSError, NotImplementedError):
                symlink_created = False

            guest_theme.apply_theme("dark", home=home)

            self.assertEqual(bad.read_text(encoding="utf-8"), "{corrupt")
            if symlink_created:
                self.assertEqual(secret.read_text(encoding="utf-8"), '{"keep":true}')

    def test_notify_desktop_portal_posts_variant_over_unix_socket(self) -> None:
        af_unix = getattr(socket, "AF_UNIX", None)
        if af_unix is None:
            self.skipTest("AF_UNIX unavailable")
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            sock_path = root / "files.sock"
            received = []
            with socket.socket(af_unix, socket.SOCK_STREAM) as srv:
                try:
                    srv.bind(str(sock_path))
                    srv.listen(1)
                except OSError as exc:
                    self.skipTest(f"AF_UNIX bind unavailable: {exc}")
                srv.settimeout(3)

                def accept_one() -> None:
                    conn, _ = srv.accept()
                    with conn:
                        conn.settimeout(3)
                        with conn.makefile("rb") as request:
                            method, path, _ = request.readline().split()
                            headers = parse_headers(request)
                            body = json.loads(request.read(int(headers["Content-Length"])))
                            received.append((method, path, body))
                        conn.sendall(b"HTTP/1.1 200 OK\r\nContent-Length: 11\r\nConnection: close\r\n\r\n{\"ok\":true}")

                worker = threading.Thread(target=accept_one, daemon=True)
                worker.start()
                with mock.patch.dict(os.environ, {"CAGE_THEME_RUNTIME_DIRS": str(root)}):
                    guest_theme.apply_theme("dark", home=root)
                worker.join(timeout=4)
            self.assertEqual(received, [(b"POST", b"/appearance", {"variant": "dark"})])


if __name__ == "__main__":
    unittest.main()
