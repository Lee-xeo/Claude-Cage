import importlib.util
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("device_reset", Path(__file__).with_name("device_reset.py"))
MOD = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MOD)


class DeviceResetTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.work_home = root / "home" / "alex42"
        self.browser_home = root / "home" / "robin19"
        self.etc_dir = root / "etc"
        self.dbus_dir = root / "var" / "lib" / "dbus"
        self.ext_root = root / "usr" / "local" / "lib"
        for d in (
            self.work_home,
            self.browser_home,
            self.etc_dir / "netplan",
            self.etc_dir / "ssh",
            self.dbus_dir,
            self.ext_root,
        ):
            d.mkdir(parents=True, exist_ok=True)
        # Seed sample files
        (self.browser_home / "profile" / "Default").mkdir(parents=True)
        (self.browser_home / "profile" / "Default" / "Cookies").write_bytes(b"cookie-data")
        (self.browser_home / "phone-profiles" / ("a" * 64) / "Default").mkdir(parents=True)
        (self.browser_home / "phone-profiles" / ("a" * 64) / "Default" / "Cookies").write_bytes(b"phone-cookie")
        (self.work_home / ".claude").mkdir()
        (self.work_home / ".claude" / "settings.json").write_text("{}", encoding="utf-8")
        (self.work_home / ".claude.json").write_text('{"userID":"old"}', encoding="utf-8")
        (self.work_home / ".local" / "share" / "cage" / "claude-accounts" / ("a" * 24) / "config").mkdir(parents=True)
        (self.work_home / ".local" / "share" / "cage" / "claude-accounts" / ("a" * 24) / "config" / ".claude.json").write_text('{"userID":"acct-old"}', encoding="utf-8")
        (self.work_home / ".local" / "share" / "cage" / "claude-shared" / "skills").mkdir(parents=True)
        (self.work_home / ".local" / "share" / "cage" / "claude-shared" / "skills" / "keep.md").write_text("keep", encoding="utf-8")
        (self.work_home / ".config" / "Claude").mkdir(parents=True)
        (self.work_home / ".config" / "Claude" / "state.json").write_text("{}", encoding="utf-8")
        (self.work_home / ".config" / "Claude" / "ant-did").write_text("old-ant-did", encoding="utf-8")
        (self.work_home / ".local" / "share" / "desktop-profiles" / ("b" * 24) / "data").mkdir(parents=True)
        (self.work_home / ".local" / "share" / "desktop-profiles" / ("b" * 24) / "data" / "ant-did").write_text("acct-ant-did", encoding="utf-8")
        (self.work_home / ".local" / "share" / "keyrings").mkdir(parents=True)
        (self.work_home / ".local" / "share" / "keyrings" / "login.keyring").write_bytes(b"keyring-secret")
        (self.work_home / ".local" / "share" / "code-server").mkdir(parents=True)
        (self.work_home / ".local" / "share" / "code-server" / "layout.json").write_text("{}", encoding="utf-8")
        (self.work_home / ".bash_history").write_text("ls\n", encoding="utf-8")
        # Protected user project file outside target list
        (self.work_home / "my-project").mkdir()
        (self.work_home / "my-project" / "main.go").write_text("package main\n", encoding="utf-8")
        # OS identity files
        (self.etc_dir / "hostname").write_text("OLD-HOST\n", encoding="utf-8")
        (self.etc_dir / "hosts").write_text("127.0.0.1\tlocalhost\n127.0.1.1\tOLD-HOST\n", encoding="utf-8")
        (self.etc_dir / "machine-id").write_text("0123456789abcdef0123456789abcdef\n", encoding="utf-8")
        (self.etc_dir / "netplan" / "50-cloud-init.yaml").write_text(
            "ethernets:\n  eth0:\n    match:\n      macaddress: '02:aa:bb:cc:dd:ee'\n",
            encoding="utf-8",
        )
        (self.etc_dir / "ssh" / "ssh_host_ed25519_key").write_text("old-key", encoding="utf-8")
        (self.ext_root / "browser-panes-0123456789abcdef").mkdir()
        (self.ext_root / "browser-panes-0123456789abcdef" / "manifest.json").write_text("{}", encoding="utf-8")

        self.resetter = MOD.DeviceResetter(
            self.work_home,
            self.browser_home,
            etc_dir=self.etc_dir,
            dbus_dir=self.dbus_dir,
            ext_root=self.ext_root,
            live_system=False,
        )

    def tearDown(self):
        self.tmp.cleanup()

    def test_inspect_and_recoverable_reset_and_restore(self):
        inv = self.resetter.inspect()
        self.assertEqual(inv["hostname"], "OLD-HOST")
        self.assertEqual(inv["machine_id_prefix"], "01234567")
        by_id = {item["id"]: item for item in inv["items"]}
        self.assertTrue(by_id["browser_profile"]["present"])
        self.assertTrue(by_id["claude_code"]["present"])
        self.assertTrue(by_id["editor_state"]["present"])

        # Uncheck editor_state (user can uncheck individual items)
        res = self.resetter.reset({
            "items": ["browser_profile", "claude_code", "claude_desktop", "system_traces"],
            "irreversible": False,
            "rotate_os": True,
            "hostname": "NEW-PC123",
            "machine_id": "fedcba9876543210fedcba9876543210",
            "old_mac": "02:aa:bb:cc:dd:ee",
            "new_mac": "02:11:22:33:44:55",
            "old_extension_suffix": "0123456789abcdef",
        })
        self.assertEqual(res["status"], "ok")
        self.assertTrue(res["batch_id"])
        self.assertEqual(res["hostname"], "NEW-PC123")
        self.assertEqual(res["machine_id_prefix"], "fedcba98")
        # Cleaned items moved away
        self.assertFalse((self.browser_home / "profile").exists())
        self.assertFalse((self.browser_home / "phone-profiles").exists())
        self.assertFalse((self.work_home / ".claude.json").exists())
        self.assertFalse((self.work_home / ".local" / "share" / "cage" / "claude-accounts").exists())
        self.assertFalse((self.work_home / ".config" / "Claude").exists())
        self.assertFalse((self.work_home / ".local" / "share" / "desktop-profiles").exists())
        self.assertFalse((self.work_home / ".local" / "share" / "keyrings").exists())
        self.assertFalse((self.work_home / ".bash_history").exists())
        # Shared skills, unchecked editor_state & project files preserved
        self.assertTrue((self.work_home / ".local" / "share" / "cage" / "claude-shared" / "skills" / "keep.md").exists())
        self.assertTrue((self.work_home / ".local" / "share" / "code-server" / "layout.json").exists())
        self.assertEqual((self.work_home / "my-project" / "main.go").read_text(encoding="utf-8"), "package main\n")
        # OS files updated
        self.assertIn("127.0.1.1\tNEW-PC123", (self.etc_dir / "hosts").read_text(encoding="utf-8"))
        self.assertIn("02:11:22:33:44:55", (self.etc_dir / "netplan" / "50-cloud-init.yaml").read_text(encoding="utf-8"))
        self.assertFalse((self.etc_dir / "ssh" / "ssh_host_ed25519_key").exists())
        self.assertFalse((self.ext_root / "browser-panes-0123456789abcdef").exists())
        self.assertEqual((self.dbus_dir / "machine-id").read_text(encoding="utf-8").strip(), "fedcba9876543210fedcba9876543210")

        # Create a conflicting file to verify no-overwrite restore
        (self.work_home / ".bash_history").write_text("new-history\n", encoding="utf-8")
        restored = self.resetter.restore(res["batch_id"])
        self.assertEqual(restored["status"], "partial")
        self.assertTrue((self.browser_home / "profile" / "Default" / "Cookies").exists())
        self.assertTrue((self.browser_home / "phone-profiles" / ("a" * 64) / "Default" / "Cookies").exists())
        self.assertTrue((self.work_home / ".local" / "share" / "cage" / "claude-accounts" / ("a" * 24) / "config" / ".claude.json").exists())
        self.assertTrue((self.work_home / ".local" / "share" / "desktop-profiles" / ("b" * 24) / "data" / "ant-did").exists())
        self.assertTrue((self.work_home / ".local" / "share" / "keyrings" / "login.keyring").exists())
        self.assertEqual((self.work_home / ".bash_history").read_text(encoding="utf-8"), "new-history\n")

        # Explicit delete removes remaining batch
        deleted = self.resetter.delete_batch(res["batch_id"])
        self.assertEqual(deleted["status"], "ok")
        self.assertEqual(self.resetter.list_batches(), [])

    def test_irreversible_reset_leaves_no_batch(self):
        res = self.resetter.reset({
            "items": ["browser_profile", "claude_code"],
            "irreversible": True,
            "rotate_os": False,
        })
        self.assertEqual(res["status"], "ok")
        self.assertEqual(res["batch_id"], "")
        self.assertFalse((self.browser_home / "profile").exists())
        self.assertFalse((self.work_home / ".claude").exists())
        self.assertEqual(self.resetter.list_batches(), [])
        # Project file still intact
        self.assertTrue((self.work_home / "my-project" / "main.go").exists())

    def test_rejects_invalid_inputs(self):
        with self.assertRaises(MOD.Refused):
            self.resetter.reset({"items": ["../evil"]})
        with self.assertRaises(MOD.Refused):
            self.resetter.reset({"items": [], "rotate_os": True, "hostname": "bad;host"})
        with self.assertRaises(MOD.Refused):
            self.resetter.reset({"items": [], "rotate_os": True, "machine_id": "not-hex"})
        with self.assertRaises(MOD.Refused):
            self.resetter.restore("../escape")

    def test_skips_symlink_targets(self):
        # Symlink target must be skipped safely without touching outside files
        outside = Path(self.tmp.name) / "outside-secret.txt"
        outside.write_text("keep-me", encoding="utf-8")
        (self.work_home / ".bash_history").unlink()
        try:
            os.symlink(outside, self.work_home / ".bash_history")
        except OSError as exc:
            if os.name == "nt" and getattr(exc, "winerror", None) == 1314:
                self.skipTest("creating file symlinks on Windows requires Developer Mode or SeCreateSymbolicLinkPrivilege (WinError 1314)")
            raise
        res = self.resetter.reset({"items": ["system_traces"], "irreversible": True})
        self.assertEqual(res["status"], "partial")
        self.assertTrue(outside.exists())

    def test_irreversible_removes_readonly_desktop_profile_files(self):
        locked = self.work_home / ".local" / "share" / "desktop-profiles" / ("b" * 24) / "data" / "locked"
        locked.write_text("ro", encoding="utf-8")
        os.chmod(locked, 0o400)
        res = self.resetter.reset({"items": ["claude_desktop"], "irreversible": True, "rotate_os": False})
        self.assertEqual(res["status"], "ok")
        self.assertFalse((self.work_home / ".local" / "share" / "desktop-profiles").exists())
        self.assertFalse((self.work_home / ".local" / "share" / "keyrings").exists())

    def test_stop_guest_services_includes_claude_desktop_and_account_units(self):
        live = MOD.DeviceResetter(
            self.work_home,
            self.browser_home,
            etc_dir=self.etc_dir,
            dbus_dir=self.dbus_dir,
            ext_root=self.ext_root,
            live_system=True,
        )
        valid_unit = f"desktop-{'a' * 24}.service"
        invalid_unit = "desktop-not-valid.service"
        calls = []

        def fake_run(cmd, **kwargs):
            calls.append(cmd)
            if cmd[:2] == ["systemctl", "list-units"]:
                return mock.Mock(stdout=f"{valid_unit} loaded active running Account Desktop\n{invalid_unit} loaded active running Bad\n")
            return mock.Mock(stdout="")

        with mock.patch.object(MOD.subprocess, "run", side_effect=fake_run):
            live.stop_guest_services()

        stop_calls = [c for c in calls if c[:2] == ["systemctl", "stop"]]
        self.assertEqual(len(stop_calls), 1)
        stopped = stop_calls[0][2:]
        self.assertIn("cage-claude-desktop", stopped)
        self.assertIn(valid_unit, stopped)
        self.assertNotIn(invalid_unit, stopped)
        self.assertIn("cage-browser", stopped)


if __name__ == "__main__":
    unittest.main()
