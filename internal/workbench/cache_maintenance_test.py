"""Temporary fixtures only; never inspect the actual user's home or processes."""
import importlib.util
import json
import os
import io
from contextlib import redirect_stdout
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("cache_maintenance", Path(__file__).with_name("cache_maintenance.py"))
care = importlib.util.module_from_spec(spec)
spec.loader.exec_module(care)


class CacheMaintenanceTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.home = Path(self.temporary.name).resolve()
        self.browser = self.home / "browser/Default"
        self.desktop = self.home / "desktop"
        self.accounts = self.home / "accounts"
        self.roles = set()
        self.service = care.Maintenance(self.home, "fixture", self.browser, self.desktop, self.accounts, lambda: self.roles)
        self.protected = [".claude.json", ".claude/.credentials.json", ".claude/settings.json",
                          ".claude/history.jsonl", ".claude/projects/session.jsonl", ".claude/skills/SKILL.md",
                          "browser/Default/Cookies", "browser/Default/Network/Cookies", "browser/Default/Local Storage/db",
                          "desktop/IndexedDB/db", "desktop/Sessions/current", "project/main.py", "persistent.img"]
        for name in self.protected:
            self.write(self.home / name, "protected:" + name)
        self.write(self.home / ".claude/cache/model-cache", "cache-data")
        self.write(self.browser / "Cache/Cache_Data/data_0", "browser-cache")
        self.write(self.desktop / "Code Cache/js/entry", "desktop-cache")

    def write(self, path, contents):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(contents)

    def assert_protected(self):
        for name in self.protected:
            self.assertEqual((self.home / name).read_text(), "protected:" + name)

    def selection(self, ids=None):
        inventory = self.service.inspect()
        return {"scope": inventory["scope"], "items": [{"id": row["id"], "fingerprint": row["fingerprint"]}
                for row in inventory["items"] if row["state"] == "ready" and (ids is None or row["id"] in ids)]}

    def test_inventory_writes_nothing_and_does_not_read_protected_contents(self):
        before = sorted(str(p.relative_to(self.home)) for p in self.home.rglob("*"))
        with patch.object(Path, "read_text", side_effect=AssertionError("inventory must not read user file contents")):
            report = self.service.inspect()
        self.assertEqual(before, sorted(str(p.relative_to(self.home)) for p in self.home.rglob("*")))
        self.assertFalse(self.service.recovery.exists())
        self.assertEqual(sum(row["state"] == "ready" for row in report["items"]), 3)
        self.assertNotIn("protected:", json.dumps(report))
        self.assert_protected()

    def test_three_layouts_clean_and_restore_preserve_all_user_data(self):
        for system in ("linux", "macos", "windows"):
            with self.subTest(system=system):
                self.service = care.Maintenance(self.home, system, self.browser, self.desktop, self.accounts, lambda: self.roles)
                result = self.service.clean(self.selection())
                self.assertEqual(result["status"], "complete")
                self.assertFalse((self.home / ".claude/cache").exists())
                self.assert_protected()
                self.assertEqual(self.service.inspect()["batches"][0]["count"], 3)
                restored = self.service.restore(result["batch"])
                self.assertEqual(restored["status"], "complete", restored)
                self.assertEqual((self.home / ".claude/cache/model-cache").read_text(), "cache-data")
                self.assertEqual(self.service.inspect()["batches"], [])
                self.assert_protected()

    def test_managed_accounts_do_not_use_default_paths(self):
        account = "a" * 24
        self.write(self.accounts / account / "config/cache/item", "independent-cache")
        self.write(self.accounts / account / "config/.credentials.json", "independent-login")
        self.service = care.Maintenance(self.home, "fixture", self.browser, self.desktop, self.accounts, lambda: set())
        result = self.service.clean(self.selection(["cli-" + account]))
        self.assertEqual(result["status"], "complete")
        self.assertEqual((self.accounts / account / "config/.credentials.json").read_text(), "independent-login")
        self.assertTrue((self.home / ".claude/cache/model-cache").exists())

    def test_active_or_uninspectable_processes_prevent_clean(self):
        request = self.selection()
        for roles in ({"cli", "browser", "desktop"}, None):
            self.roles = roles
            self.assertFalse(any(row["state"] == "ready" for row in self.service.inspect()["items"]))
            with self.assertRaises(care.Refused):
                self.service.clean(request)
            self.assertFalse(self.service.recovery.exists())
            self.assert_protected()

    def test_changed_scope_selection_and_cache_are_rejected(self):
        request = self.selection()
        for altered in ({**request, "scope": "wrong"}, {**request, "items": [{"id": "../../project", "fingerprint": "x"}]},
                        {**request, "items": request["items"] * 2}):
            with self.assertRaises(care.Refused):
                self.service.clean(altered)
        self.write(self.home / ".claude/cache/model-cache", "changed-cache")
        with self.assertRaises(care.Refused):
            self.service.clean(request)
        self.assertFalse(self.service.recovery.exists())
        self.assert_protected()

    def test_links_are_preserved_and_never_followed(self):
        path = self.home / ".claude/cache/link"
        try:
            path.symlink_to(self.home / "project", target_is_directory=True)
        except OSError:
            self.skipTest("OS does not permit test symlinks")
        report = self.service.inspect()
        self.assertEqual(next(item for item in report["items"] if item["id"] == "cli-default")["state"], "blocked")
        with self.assertRaises(care.Refused):
            care.snapshot(path / "main.py")
        self.assert_protected()

    def test_new_data_is_never_overwritten_on_restore(self):
        result = self.service.clean(self.selection(["cli-default"]))
        self.write(self.home / ".claude/cache/new", "new-cache")
        restored = self.service.restore(result["batch"])
        self.assertEqual(restored["status"], "partial")
        self.assertEqual((self.home / ".claude/cache/new").read_text(), "new-cache")
        self.assertTrue((self.service.recovery / result["batch"] / "cli-default/model-cache").exists())
        self.assert_protected()

    def test_rename_cannot_replace_a_destination_created_after_check(self):
        source, target = self.home / "source", self.home / "target"
        source.mkdir()
        target.mkdir()
        with self.assertRaises(OSError):
            care.move_cache(source, target)
        self.assertTrue(source.is_dir())
        self.assertTrue(target.is_dir())

    def test_partial_failure_and_interrupted_receipt_remain_recoverable(self):
        move = care.move_cache
        calls = []
        def fail_second(source, target):
            calls.append(source)
            if len(calls) == 2:
                raise OSError("fixture failure")
            move(source, target)
        with patch.object(care, "move_cache", side_effect=fail_second):
            result = self.service.clean(self.selection())
        self.assertEqual(result["status"], "partial")
        directory = self.service.recovery / result["batch"]
        receipt = json.loads((directory / "receipt.json").read_text())
        receipt["items"][0]["state"] = "pending"  # Simulate exit after rename, before receipt update.
        care.write_manifest(directory / "receipt.json", receipt)
        self.assertEqual(self.service.inspect()["batches"][0]["count"], 1)
        self.assertEqual(self.service.restore(result["batch"])["status"], "complete")
        self.assert_protected()

    def test_modified_recovery_and_invalid_batch_are_preserved(self):
        result = self.service.clean(self.selection(["cli-default"]))
        self.write(self.service.recovery / result["batch"] / "cli-default/model-cache", "changed")
        self.assertEqual(self.service.restore(result["batch"])["status"], "partial")
        for batch in ("../project", "x" * 32, None):
            with self.assertRaises(care.Refused):
                self.service.restore(batch)
        self.assert_protected()

    def test_process_classification_catches_native_and_node_cli_without_exposing_args(self):
        if os.name == "nt":
            self.skipTest("POSIX ps fixture")
        args = "101 /bin/node /bin/node /tools/@anthropic-ai/claude-code/cli.js private-token\n102 /app/chrome /app/chrome\n103 /app/Claude /app/Claude"
        names = "101 /bin/node\n102 /app/chrome\n103 /app/Claude"
        with patch.object(care.subprocess, "check_output", side_effect=[args, names]):
            self.assertEqual(care.running_roles(), {"cli", "browser", "desktop"})

    def test_program_entry_accepts_only_actions_and_writes_only_after_clean(self):
        def invoke(request):
            output = io.StringIO()
            stream = io.TextIOWrapper(io.BytesIO(json.dumps(request).encode()), encoding="utf-8")
            with patch.object(care, "production", return_value=self.service), patch.object(care.sys, "stdin", stream), redirect_stdout(output):
                care.main()
            return json.loads(output.getvalue())
        self.assertEqual(invoke({"action": "inspect"})["version"], 1)
        self.assertFalse(self.service.recovery.exists())
        self.assertIn("error", invoke({"action": "inspect", "path": str(self.home)}))
        result = invoke({"action": "clean", **self.selection(["cli-default"])})
        self.assertEqual(result["status"], "complete", result)
        self.assertEqual(invoke({"action": "restore", "batch": result["batch"]})["status"], "complete")
        self.assert_protected()

    def test_windows_process_failure_is_not_reported_as_idle(self):
        with patch.object(care.subprocess, "check_output", side_effect=OSError("denied")), patch.object(care.subprocess, "run", side_effect=OSError("denied")):
            self.assertIsNone(care.running_roles())

    def test_file_count_limit_leaves_target_untouched(self):
        with patch.object(care, "LIMIT", 1):
            row = next(row for row in self.service.inspect()["items"] if row["id"] == "cli-default")
            self.assertEqual(row["state"], "blocked")
        self.assertTrue((self.home / ".claude/cache/model-cache").exists())
        self.assert_protected()

    def test_protected_material_nested_in_a_cache_prevents_its_move(self):
        for marker in (".git/config", ".credentials.json", "Sessions/current", "personal.vhdx"):
            with self.subTest(marker=marker):
                path = self.home / ".claude/cache" / marker
                self.write(path, "unexpected-user-data")
                row = next(row for row in self.service.inspect()["items"] if row["id"] == "cli-default")
                self.assertEqual(row["state"], "blocked")
                self.assertIn("持久磁盘标记", row["reason"])
                self.assertEqual(path.read_text(), "unexpected-user-data")
                path.unlink()
                if path.parent.name in {".git", "Sessions"}:
                    path.parent.rmdir()
        self.assert_protected()

    def test_production_selects_owned_platform_roots_without_creating_them(self):
        with patch.object(care, "Maintenance", side_effect=lambda *args: args), patch.object(care.Path, "home", return_value=self.home), patch.dict(care.os.environ, {
            "APPDATA": str(self.home / "AppData/Roaming"), "LOCALAPPDATA": str(self.home / "AppData/Local"),
        }):
            with patch.object(care.sys, "platform", "darwin"):
                mac = care.production({})
            with patch.object(care.sys, "platform", "win32"):
                windows = care.production({"profile": "0123456789abcdef"})
                with self.assertRaises(care.Refused):
                    care.production({"profile": "../../other-user"})
            with patch.object(care.sys, "platform", "linux"), patch.object(care.os, "geteuid", return_value=0, create=True):
                linux = care.production({})
        self.assertEqual(mac[2], self.home / "Library/Application Support/Cage/Browser/Default")
        self.assertEqual(windows[2], self.home / "AppData/Local/Cage/Browser/Profile/Default")
        self.assertEqual(windows[4], self.home / "Cage/CLI/0123456789abcdef/accounts")
        self.assertEqual(linux[0], Path("/home/cage"))
        self.assertEqual(linux[2], Path("/home/cage-browser/profile/Default"))
        self.assertFalse(self.service.recovery.exists())

    @unittest.skipUnless(os.name == "nt", "Native Windows junction and ACL check runs in Windows CI")
    def test_windows_junction_and_recovery_acl(self):
        link = self.home / ".claude/cache/junction"
        executable = Path(os.environ["SystemRoot"]) / "System32/WindowsPowerShell/v1.0/powershell.exe"
        env = {**os.environ, "FIXTURE_LINK": str(link), "FIXTURE_TARGET": str(self.home / "project")}
        care.subprocess.run([str(executable), "-NoProfile", "-NonInteractive", "-Command",
                             "$ErrorActionPreference='Stop'; New-Item -ItemType Junction -Path $env:FIXTURE_LINK -Target $env:FIXTURE_TARGET | Out-Null"], env=env, check=True, capture_output=True)
        try:
            row = next(row for row in self.service.inspect()["items"] if row["id"] == "cli-default")
            self.assertEqual(row["state"], "blocked")
            self.assert_protected()
        finally:
            link.rmdir()
        care.private_directory(self.service.recovery)
        env["FIXTURE_TARGET"] = str(self.service.recovery)
        script = "$a=Get-Acl -LiteralPath $env:FIXTURE_TARGET; @{protected=$a.AreAccessRulesProtected; sids=@($a.GetAccessRules($true,$true,[Security.Principal.SecurityIdentifier]) | ForEach-Object {$_.IdentityReference.Value}); user=[Security.Principal.WindowsIdentity]::GetCurrent().User.Value} | ConvertTo-Json -Compress"
        output = care.subprocess.check_output([str(executable), "-NoProfile", "-NonInteractive", "-Command", script], env=env, text=True)
        acl = json.loads(output)
        self.assertTrue(acl["protected"])
        self.assertEqual(set(acl["sids"]), {acl["user"], "S-1-5-18"})


if __name__ == "__main__":
    unittest.main(verbosity=2)
