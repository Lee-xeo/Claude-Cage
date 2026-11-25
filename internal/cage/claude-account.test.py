import hashlib
import contextlib
import io
from datetime import datetime, timezone
import importlib.util
import json
import os
import runpy
import shlex
import shutil
from pathlib import Path
import subprocess
import sys
import tempfile
import time
import unittest
from unittest.mock import patch

sys.dont_write_bytecode = True
source = Path(__file__).with_name("claude-account.py")
spec = importlib.util.spec_from_file_location("account", source)
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)
FIRST = "1" * 24
SECOND = "2" * 24


class ClaudeAccountTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.home = Path(tmp.name)
        # A guest's temporary directory is outside HOME; the usage query relies
        # on that to run the CLI from a directory without project settings.
        # Create it before in-process temporary files are redirected into HOME.
        scratch = tempfile.TemporaryDirectory(prefix="account-scratch-")
        self.addCleanup(scratch.cleanup)
        self.scratch = Path(scratch.name)
        if self.scratch.is_relative_to(self.home):
            raise AssertionError("the helper's temporary directory must be outside HOME")
        temp_patch = patch.object(tempfile, "tempdir", str(self.home))
        temp_patch.start()
        self.addCleanup(temp_patch.stop)
        fake = self.home / ".local/bin/claude"
        fake.parent.mkdir(parents=True)
        # Emulate the documented status shape of the official CLI. Its response
        # deliberately includes fields that must never cross the guest boundary.
        fake.write_text("""#!/bin/sh
if [ -n "$TEST_CAPTURE_ENV" ]; then env > "$TEST_CAPTURE_ENV"; fi
original_settings="$2"
if [ "$1" = --settings ]; then shift 2; fi
if [ "$1" = auth ] && [ "$2" = status ]; then
  method=claude.ai; if [ -n "$ANTHROPIC_API_KEY" ]; then method=api_key; fi
  printf '{"configDirectory":"%s","authMethod":"%s","apiProvider":"%s","loggedIn":true,"email":"%s","orgId":"test-org","subscriptionType":"pro","access_token":"must-not-appear"}' "${TEST_CONFIG_RESULT:-${CLAUDE_CONFIG_DIR:-$HOME/.claude}}" "$method" "${TEST_PROVIDER:-firstParty}" "$TEST_EMAIL"
  exit 0
fi
if [ -n "$original_settings" ]; then printf '%s\\n' --settings "$original_settings"; fi
printf '%s\\n' "$@"
""")
        fake.chmod(0o700)
        self.email = "owner@example.test"
        self.subject = hashlib.sha256((self.email + "\ntest-org").encode()).hexdigest()
        self.env = {"HOME": str(self.home), "PATH": os.defpath, "TEST_EMAIL": self.email,
                    "CAGE_CLAUDE_SUBJECT": self.subject, "PYTHONDONTWRITEBYTECODE": "1", "TMPDIR": str(self.scratch)}

    def run_helper(self, mode, account, *args, data=None, env=None, check=True, cwd=None):
        return subprocess.run([sys.executable, str(source), mode, account, *args], env=env or self.env,
                              cwd=cwd or self.home, input=json.dumps(data) if data else None, text=True,
                              capture_output=True, check=check)

    def test_isolated_cli_snapshot_and_subject_bound_quota(self):
        reset = int(time.time()) + 3600
        window = {"used_percentage": 73.4, "resets_at": reset}
        payload = {"rate_limits": {"five_hour": window, "private": "must-not-appear"},
                   "last_assistant_message": "must-not-appear"}
        self.run_helper("statusline", FIRST, data=payload)
        snapshot = self.run_helper("snapshot", FIRST).stdout
        self.assertNotIn("must-not-appear", snapshot)
        window = json.loads(snapshot)["quota"]["five_hour"]
        self.assertEqual(window["used_percentage"], 73.4)
        self.assertGreater(window["observed_at"], 0)
        self.assertNotIn("quota", json.loads(self.run_helper("snapshot", SECOND).stdout))
        batch = json.loads(self.run_helper("quotas", FIRST, SECOND).stdout)
        self.assertEqual(list(batch), [FIRST])
        self.assertEqual(set(batch[FIRST]), {"identity", "observed_at", "five_hour"})
        self.assertEqual(batch[FIRST]["identity"], self.subject)
        # A later redraw by an older terminal cannot undo a consumed window.
        payload["rate_limits"]["five_hour"] = {**window, "used_percentage": 2}
        self.run_helper("statusline", FIRST, data=payload)
        self.assertEqual(json.loads(self.run_helper("quotas", FIRST).stdout)[FIRST]["five_hour"], window)
        # An interactive /login changes the credential while the old session is alive.
        other_env = {**self.env, "TEST_EMAIL": "another@example.test"}
        payload["rate_limits"]["five_hour"] = {**window, "used_percentage": 99}
        self.run_helper("statusline", FIRST, data=payload, env=other_env)
        self.assertNotIn("quota", json.loads(self.run_helper("snapshot", FIRST, env=other_env).stdout))
        self.assertEqual(json.loads(self.run_helper("quotas", FIRST).stdout)[FIRST]["five_hour"], window)
        self.assertNotEqual(self.run_helper("run", FIRST, env=other_env, check=False).returncode, 0)
        self.assertNotEqual(self.run_helper("quotas", "../outside", check=False).returncode, 0)

    def recovery_profiles(self):
        with patch.dict(os.environ, self.env):
            target, source = helper.profile(FIRST), helper.profile(SECOND)
        for base in (target, source):
            base.mkdir(mode=0o700, parents=True)
            (base / "config").mkdir(mode=0o700)
        credential = source / "config/.credentials.json"
        credential.write_bytes(b"opaque-private-fixture")
        credential.chmod(0o600)
        reading = {"logged_in": True, "email": self.email, "organization": "test-org", "plan": "pro"}
        return target, source, credential, reading

    def test_archive_seeds_the_empty_profile_before_cli_status_initializes_it(self):
        target, source, credential, reading = self.recovery_profiles()
        account = {"emailAddress": self.email}
        (source / "config/.claude.json").write_text(json.dumps({"oauthAccount": account}))

        def status(binary, env, cwd):
            config = Path(env["CLAUDE_CONFIG_DIR"])
            if config == target / "config":
                if not (config / ".credentials.json").exists():
                    (config / ".claude.json").write_text("{}")
                    return {"logged_in": False}
                self.assertEqual(json.loads((config / ".claude.json").read_text()), {"oauthAccount": account})
            return reading

        with patch.dict(os.environ, self.env), patch.object(helper, "identity", side_effect=status):
            self.assertEqual(helper.recover_login("claude", target, SECOND, self.subject, archive=True), reading)
        self.assertEqual((target / "config/.credentials.json").read_bytes(), credential.read_bytes())

    def test_duplicate_login_recovers_missing_credentials_without_moving_history(self):
        target, source, credential, reading = self.recovery_profiles()
        history = target / "config/history.jsonl"
        history.write_bytes(b"original private conversation\n")
        with patch.dict(os.environ, self.env), patch.object(helper, "identity", side_effect=[{"logged_in": False}, reading, reading]):
            result = helper.recover_login("claude", target, SECOND, self.subject)
        destination = target / "config/.credentials.json"
        self.assertEqual(result, reading)
        self.assertEqual(destination.read_bytes(), credential.read_bytes())
        self.assertEqual(destination.stat().st_mode & 0o777, 0o600)
        self.assertEqual(history.read_bytes(), b"original private conversation\n")
        self.assertNotIn("opaque-private-fixture", json.dumps(result))
        self.assertEqual(set(p.name for p in destination.parent.iterdir()), {".credentials.json", "history.jsonl"})

    def test_duplicate_login_recovery_refuses_other_identities_and_existing_credentials(self):
        target, source, credential, reading = self.recovery_profiles()
        destination = target / "config/.credentials.json"
        other = {**reading, "email": "other@example.test"}
        for observations in ([other], [{"logged_in": False}, other], [{"logged_in": False}, {**reading, "organization": "another-org"}]):
            with self.subTest(observations=observations), patch.dict(os.environ, self.env), patch.object(helper, "identity", side_effect=observations):
                self.assertFalse(helper.recover_login("claude", target, SECOND, self.subject)["logged_in"])
                self.assertFalse(destination.exists())
        destination.write_bytes(b"concurrent original login")
        with patch.dict(os.environ, self.env), patch.object(helper, "identity", side_effect=[{"logged_in": False}, reading]):
            with self.assertRaises(FileExistsError):
                helper.recover_login("claude", target, SECOND, self.subject)
        self.assertEqual(destination.read_bytes(), b"concurrent original login")

    def test_duplicate_login_recovery_preserves_credentials_on_failed_verification(self):
        target, source, credential, reading = self.recovery_profiles()
        with patch.dict(os.environ, self.env), patch.object(helper, "identity", side_effect=[{"logged_in": False}, reading, {"logged_in": False}]):
            self.assertFalse(helper.recover_login("claude", target, SECOND, self.subject)["logged_in"])
        self.assertEqual((target / "config/.credentials.json").read_bytes(), credential.read_bytes())

    def test_duplicate_login_recovery_preserves_credentials_on_verification_error(self):
        target, source, credential, reading = self.recovery_profiles()
        with patch.dict(os.environ, self.env), patch.object(helper, "identity", side_effect=[{"logged_in": False}, reading, ValueError("unexpected auth status")]):
            with self.assertRaises(ValueError):
                helper.recover_login("claude", target, SECOND, self.subject)
        self.assertEqual((target / "config/.credentials.json").read_bytes(), credential.read_bytes())

    def test_duplicate_login_recovery_preserves_replacement_at_publication(self):
        target, source, credential, reading = self.recovery_profiles()
        destination = target / "config/.credentials.json"
        original_link = os.link

        def publish_then_replace(src, dst):
            original_link(src, dst)
            replacement = target / "concurrent-login"
            replacement.write_bytes(b"concurrent login credentials")
            replacement.chmod(0o600)
            replacement.replace(dst)

        with patch.dict(os.environ, self.env), patch.object(helper.os, "link", side_effect=publish_then_replace), patch.object(helper, "identity", side_effect=[{"logged_in": False}, reading, {"logged_in": False}]):
            self.assertFalse(helper.recover_login("claude", target, SECOND, self.subject)["logged_in"])
        self.assertEqual(destination.read_bytes(), b"concurrent login credentials")
        self.assertEqual(credential.read_bytes(), b"opaque-private-fixture")

    def test_duplicate_login_recovery_does_not_remove_a_concurrent_replacement(self):
        target, source, credential, reading = self.recovery_profiles()
        destination = target / "config/.credentials.json"
        def changed_login(*args, **kwargs):
            replacement = target / "replacement"
            replacement.write_bytes(b"another login finished meanwhile")
            replacement.replace(destination)
            return {"logged_in": False}
        observations = iter([{"logged_in": False}, reading])
        def observe(*args, **kwargs):
            return next(observations, None) or changed_login()
        with patch.dict(os.environ, self.env), patch.object(helper, "identity", side_effect=observe):
            self.assertFalse(helper.recover_login("claude", target, SECOND, self.subject)["logged_in"])
        self.assertEqual(destination.read_bytes(), b"another login finished meanwhile")

    def test_duplicate_login_recovery_preserves_valid_original_and_rejects_unsafe_source(self):
        target, source, credential, reading = self.recovery_profiles()
        with patch.dict(os.environ, self.env), patch.object(helper, "identity", return_value=reading) as probe:
            self.assertEqual(helper.recover_login("claude", target, SECOND, self.subject), reading)
            self.assertEqual(probe.call_count, 1)
        self.assertFalse((target / "config/.credentials.json").exists())
        credential.chmod(0o644)
        with patch.dict(os.environ, self.env), patch.object(helper, "identity", side_effect=[{"logged_in": False}, reading]):
            with self.assertRaises(ValueError):
                helper.recover_login("claude", target, SECOND, self.subject)
        credential.chmod(0o600)
        credential.rename(source / "private-backup")
        credential.symlink_to(source / "private-backup")
        with patch.dict(os.environ, self.env), patch.object(helper, "identity", side_effect=[{"logged_in": False}, reading]):
            with self.assertRaises(ValueError):
                helper.recover_login("claude", target, SECOND, self.subject)

    def test_duplicate_login_recovery_command_exports_only_status(self):
        result = self.run_helper("recover-login", FIRST, SECOND, self.subject)
        status = json.loads(result.stdout)
        self.assertTrue(status["logged_in"])
        self.assertEqual(status["email"], self.email)
        self.assertNotIn("must-not-appear", result.stdout + result.stderr)
        self.assertNotEqual(self.run_helper("recover-login", FIRST, FIRST, self.subject, check=False).returncode, 0)
        self.assertNotEqual(self.run_helper("recover-login", FIRST, "../outside", self.subject, check=False).returncode, 0)

    def test_duplicate_login_recovery_command_copies_missing_login(self):
        target, source, credential, reading = self.recovery_profiles()
        fake = self.home / ".local/bin/claude"
        original = fake.read_text()
        fake.write_text(original.replace(
            '  method=claude.ai;',
            '''  if [ ! -f "$CLAUDE_CONFIG_DIR/.credentials.json" ]; then
    printf '{"configDirectory":"%s","loggedIn":false}' "$CLAUDE_CONFIG_DIR"
    exit 1
  fi
  method=claude.ai;''',
        ))
        history = target / "config/history.jsonl"
        history.write_bytes(b"original conversation\n")
        result = self.run_helper("recover-login", FIRST, SECOND, self.subject)
        self.assertEqual(json.loads(result.stdout), reading)
        destination = target / "config/.credentials.json"
        self.assertEqual(destination.read_bytes(), credential.read_bytes())
        self.assertEqual(destination.stat().st_mode & 0o777, 0o600)
        self.assertEqual(history.read_bytes(), b"original conversation\n")
        self.assertNotIn("opaque-private-fixture", result.stdout + result.stderr)
        self.assertNotIn("must-not-appear", result.stdout + result.stderr)

    def test_missing_window_keeps_its_observation_time(self):
        now = int(time.time())
        with patch.dict(os.environ, self.env):
            base = helper.profile(FIRST)
            base.mkdir(parents=True)
            week = {"used_percentage": 40, "resets_at": now + 86400}
            # Legacy snapshot: its timestamp applied to both windows.
            (base / "quota.json").write_text(json.dumps({
                "identity": self.subject, "observed_at": now - 3600, "seven_day": week,
            }))
            helper.save_usage(base, {"rate_limits": {"five_hour": {
                "used_percentage": 20, "resets_at": now + 18000,
            }}}, self.subject, lambda: self.subject)
            quota = helper.read_quota(base)
        self.assertEqual(quota["seven_day"]["observed_at"], now - 3600)
        self.assertGreaterEqual(quota["five_hour"]["observed_at"], now)

    def test_cli_arguments_and_existing_statusline(self):
        args = self.run_helper("run", FIRST, "--model", "sonnet").stdout.splitlines()
        self.assertEqual(args[-2:], ["--model", "sonnet"])
        self.assertIn("autoMemoryDirectory", json.loads(Path(args[1]).read_text()))
        config = self.home / ".local/share/cage/claude-accounts" / FIRST / "config"
        original = '{"statusLine":{"type":"command","command":"my-original-status"}}'
        (config / "settings.json").write_text(original)
        args = self.run_helper("run", FIRST, "--model", "sonnet").stdout.splitlines()
        self.assertEqual(args[-2:], ["--model", "sonnet"])
        self.assertEqual((config / "settings.json").read_text(), original)

    def replaced_memory_alias(self):
        project = self.home / "Cage/Projects/example"
        project.mkdir(parents=True)
        args = self.run_helper("run", FIRST, cwd=project).stdout.splitlines()
        alias = Path(json.loads(Path(args[1]).read_text())["autoMemoryDirectory"])
        target = alias.resolve()
        self.assertTrue(alias.is_symlink())
        (target / "existing.md").write_text("original memory")
        alias.unlink()
        alias.mkdir()
        (alias / "new.md").write_text("new memory")
        return project, alias, target

    def test_launch_recovers_replaced_memory_alias_without_losing_notes(self):
        project, alias, target = self.replaced_memory_alias()
        (alias / "nested").mkdir()
        (alias / "nested/detail.md").write_text("nested memory")
        self.run_helper("run", FIRST, cwd=project)
        self.assertTrue(alias.is_symlink())
        self.assertEqual(alias.resolve(), target)
        self.assertEqual((target / "existing.md").read_text(), "original memory")
        self.assertEqual((alias / "new.md").read_text(), "new memory")
        self.assertEqual((target / "nested/detail.md").read_text(), "nested memory")
        backups = list((self.home / ".local/share/cage/claude-shared/backups/runtime-memory").glob("*/new.md"))
        self.assertEqual(len(backups), 1)
        self.assertEqual(backups[0].read_text(), "new memory")
        self.run_helper("run", FIRST, cwd=project)

    def test_memory_alias_conflict_merges_without_losing_either_note(self):
        project, alias, target = self.replaced_memory_alias()
        (alias / "existing.md").write_text("changed memory")
        (target / "MEMORY.md").write_text("- [Existing](existing.md) — kept\n")
        result = self.run_helper("run", FIRST, cwd=project)
        self.assertIn("自动合并", result.stderr)
        self.assertNotIn(str(self.home), result.stderr)
        self.assertEqual((target / "existing.md").read_text(), "original memory")
        renamed = [path for path in target.glob("existing-*.md")]
        self.assertEqual([path.read_text() for path in renamed], ["changed memory"])
        self.assertEqual((target / "new.md").read_text(), "new memory")
        self.assertEqual((target / "MEMORY.md").read_text().splitlines(),
                         ["- [Existing](existing.md) — kept", f"- [{renamed[0].name}]({renamed[0].name})"])
        self.assertTrue(alias.is_symlink())

    def test_memory_alias_backup_failure_preserves_source_and_target(self):
        project, alias, target = self.replaced_memory_alias()
        with patch.dict(os.environ, self.env, clear=True), patch.object(helper.Path, "cwd", return_value=project), \
                patch.dict(helper._runtime["path"].__globals__, {"root": lambda: alias.parent.parent}), \
                patch.object(helper.shutil, "copytree", side_effect=OSError("private path or credential")):
            with self.assertRaises(OSError):
                helper.shared_launch_arguments(helper.profile(FIRST), [])
        self.assertFalse(alias.is_symlink())
        self.assertEqual((alias / "new.md").read_text(), "new memory")
        self.assertEqual((target / "existing.md").read_text(), "original memory")

    def test_memory_alias_recovery_does_not_follow_links(self):
        project, alias, target = self.replaced_memory_alias()
        outside = self.home / "outside.md"
        outside.write_text("unrelated data")
        (alias / "linked.md").symlink_to(outside)
        result = self.run_helper("run", FIRST, cwd=project, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("异常链接", result.stderr)
        self.assertEqual(outside.read_text(), "unrelated data")
        self.assertFalse((target / "linked.md").exists())
        self.assertEqual((alias / "new.md").read_text(), "new memory")

    def test_memory_alias_recovery_preserves_concurrent_destination(self):
        project, alias, target = self.replaced_memory_alias()
        original = helper.shutil.copytree
        def backup_then_write(*args, **kwargs):
            # copytree recurses through the patched module; this tree is flat.
            result = original(*args, **kwargs)
            (target / "new.md").write_text("concurrent memory")
            return result
        with patch.dict(os.environ, self.env, clear=True), patch.object(helper.Path, "cwd", return_value=project), \
                patch.dict(helper._runtime["path"].__globals__, {"root": lambda: alias.parent.parent}), \
                patch.object(helper.shutil, "copytree", side_effect=backup_then_write):
            flags = helper.shared_launch_arguments(helper.profile(FIRST), [])
        # Changed while merging: this launch uses the account's own memory.
        self.assertNotIn("autoMemoryDirectory", json.loads(Path(flags[1]).read_text()))
        self.assertEqual((target / "new.md").read_text(), "concurrent memory")
        self.assertEqual((alias / "new.md").read_text(), "new memory")
        self.assertFalse(alias.is_symlink())

    def test_launch_errors_are_specific_and_do_not_expose_private_details(self):
        private = "private/path credential=secret"
        cases = [
            (helper.SubscriptionStatusTimeout(private), "读取 Claude 订阅状态超时"),
            (helper.SubscriptionStatusError(private), "无法读取 Claude 订阅状态"),
            (helper.ClaudeCLINotInstalled(private), "未找到官方 Claude Code CLI"),
            (helper._runtime["RuntimePathConflict"](Path(private), Path(private)), "运行路径"),
            (OSError(private), "准备终端"),
        ]
        for error, message in cases:
            with self.subTest(error=type(error).__name__), patch.object(helper, "main", side_effect=error):
                output = io.StringIO()
                with contextlib.redirect_stderr(output):
                    self.assertEqual(helper.entrypoint(["run", FIRST]), 1)
                self.assertIn(message, output.getvalue())
                self.assertNotIn(private, output.getvalue())

    def test_subscription_probe_distinguishes_timeout_from_invalid_response(self):
        with patch.object(helper.subprocess, "run", side_effect=subprocess.TimeoutExpired("private-command", 10)):
            with self.assertRaises(helper.SubscriptionStatusTimeout):
                helper.identity("claude", self.env, self.home)
        for raw in (b"invalid json with private credentials", b"[]"):
            with patch.object(helper.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, raw)):
                with self.assertRaises(helper.SubscriptionStatusError):
                    helper.identity("claude", self.env, self.home)

    def test_official_process_receives_no_controller_flags_and_callbacks_still_work(self):
        capture = self.home / "child-environment"
        env = {**self.env, "CAGE_LANGUAGE": "en-US", "CAGE_WORKBENCH": "1", "TEST_CAPTURE_ENV": str(capture)}
        args = self.run_helper("run", FIRST, "--model", "sonnet", env=env).stdout.splitlines()
        child_env = dict(line.split("=", 1) for line in capture.read_text().splitlines())
        self.assertFalse(any(key.upper().startswith("CAGE_") for key in child_env))
        self.assertNotIn("cage", child_env["CLAUDE_CONFIG_DIR"].lower())
        self.assertNotIn("cage", args[1].lower())
        settings = json.loads(Path(args[1]).read_text())
        command = settings["statusLine"]["command"]
        self.assertNotIn("cage", command.lower())
        window = {"used_percentage": 20, "resets_at": int(time.time()) + 3600}
        callback = subprocess.run(shlex.split(command), env=child_env, cwd=self.home,
                                  input=json.dumps({"rate_limits": {"five_hour": window}}), text=True, capture_output=True, check=True)
        self.assertEqual(callback.stdout.strip(), "Claude")
        result = json.loads(self.run_helper("snapshot", FIRST, env=env).stdout)
        self.assertEqual(result["quota"]["five_hour"]["used_percentage"], 20)

    def test_callback_state_does_not_enter_its_process_environment(self):
        runtime = runpy.run_path(source.with_name("client_runtime.py"), init_globals={"_controller_context": self.env})
        probe = self.home / "probe.py"
        probe.write_text('import json,os\nprint(json.dumps({"flags":[k for k in os.environ if k.startswith("CAGE_")],"subject":globals()["_controller_context"]["CAGE_CLAUDE_SUBJECT"]}))\n')
        result = subprocess.run(runtime["command"](probe), env=runtime["environment"](self.env),
                                text=True, capture_output=True, check=True)
        self.assertEqual(json.loads(result.stdout), {"flags": [], "subject": self.subject})

    def test_installed_account_symlink_finds_its_versioned_runtime(self):
        installed = self.home / "version"
        (installed / "bin").mkdir(parents=True)
        account = installed / "bin/cage-claude-account"
        account.write_bytes(source.read_bytes())
        (installed / "client_runtime.py").write_bytes(source.with_name("client_runtime.py").read_bytes())
        entry = self.home / "cage-claude-account"
        entry.symlink_to(account)
        result = subprocess.run([sys.executable, str(entry), "snapshot", FIRST], env=self.env,
                                cwd=self.home, text=True, capture_output=True, check=True)
        self.assertEqual(json.loads(result.stdout)["email"], self.email)

    def test_default_drops_an_inherited_config_dir_naming_home_claude(self):
        # The session channel exports CLAUDE_CONFIG_DIR, through a symlink,
        # for the default ~/.claude; Claude must still read ~/.claude.json (#662).
        home_config = self.home / ".claude"
        home_config.mkdir()
        link = self.home / "runtime-paths"
        link.symlink_to(home_config)
        capture = self.home / "captured-env"
        for inherited in (str(link), str(home_config)):
            env = {**self.env, "CLAUDE_CONFIG_DIR": inherited, "TEST_CAPTURE_ENV": str(capture)}
            result = self.run_helper("run", "default", env=env, check=False)
            self.assertEqual(result.returncode, 0, (inherited, result.stderr))
            seen = dict(line.split("=", 1) for line in capture.read_text().splitlines() if "=" in line)
            self.assertNotIn("CLAUDE_CONFIG_DIR", seen, inherited)

    def test_default_reuses_the_existing_configuration_and_pin(self):
        home_config = self.home / ".claude"
        self.assertEqual(json.loads(self.run_helper("snapshot", "default").stdout), {"logged_in": False})
        self.assertFalse(home_config.exists())
        home_config.mkdir()
        history = home_config / "example-history"
        history.write_text("previous conversation")
        snap = lambda env: json.loads(self.run_helper("snapshot", "default", env=env).stdout)
        self.assertEqual(snap(self.env)["email"], self.email)
        self.assertFalse((self.home / ".local/share/cage/claude-accounts/default").exists())
        payload = {"rate_limits": {"five_hour": {"used_percentage": 10, "resets_at": int(time.time()) + 3600}}}
        self.run_helper("statusline", "default", data=payload)
        self.assertIn("quota", snap(self.env))
        changed = {**self.env, "TEST_EMAIL": "b@example.test"}
        self.assertNotIn("quota", snap(changed))
        self.assertNotEqual(self.run_helper("run", "default", env=changed, check=False).returncode, 0)
        # Re-observing a different identity in an old process may not assign B's
        # quota to A, nor change the underlying config or history.
        payload["rate_limits"]["five_hour"]["used_percentage"] = 99
        self.run_helper("statusline", "default", env=changed, data=payload)
        self.assertEqual(snap(self.env)["quota"]["five_hour"]["used_percentage"], 10)
        self.assertNotEqual(self.run_helper("login", "default", check=False).returncode, 0)
        self.assertEqual(history.read_text(), "previous conversation")
        for override in ({"ANTHROPIC_API_KEY": "fixture-api-key"}, {"CLAUDE_CONFIG_DIR": str(self.home / "different")},
                         {"ANTHROPIC_BASE_URL": "https://example.test"}, {"TEST_PROVIDER": "gateway"}):
            self.assertEqual(snap({**self.env, **override}), {"logged_in": False})
        unknown = self.run_helper("snapshot", "default", env={**self.env, "TEST_CONFIG_RESULT": str(self.home / "different")}, check=False)
        self.assertNotEqual(unknown.returncode, 0)

    def test_local_skills_instructions_and_memory_share_without_credentials(self):
        with patch.dict(os.environ,self.env,clear=True),patch.object(helper.Path,"cwd",return_value=self.home):
            common=self.home/".claude"
            (common/"skills/local-skill").mkdir(parents=True)
            (common/"skills/local-skill/SKILL.md").write_text("local instructions")
            (common/"skills/synced").mkdir()
            (common/"skills/synced/remote.md").write_text("account-specific download")
            (common/"CLAUDE.md").write_text("global preferences")
            for account in (FIRST,SECOND):
                base=helper.profile(account);config=helper.config_directory(base);config.mkdir(parents=True)
                (config/".credentials.json").write_text(account)
                helper.shared_profile_resources(base)
                self.assertEqual((config/"skills/local-skill/SKILL.md").read_text(),"local instructions")
                self.assertFalse((config/"skills/synced").exists())
                self.assertEqual((config/"CLAUDE.md").read_text(),"global preferences")
                flags=helper.shared_launch_arguments(base,["--model","sonnet"])
                memory=Path(json.loads(Path(flags[1]).read_text())["autoMemoryDirectory"])
                if account==FIRST:(memory/"MEMORY.md").write_text("project learning")
                self.assertEqual((memory/"MEMORY.md").read_text(),"project learning")
                self.assertEqual((config/".credentials.json").read_text(),account)
            (common/"skills/local-skill/SKILL.md").write_text("updated once")
            self.assertEqual((helper.config_directory(helper.profile(FIRST))/"skills/local-skill/SKILL.md").read_text(),"updated once")

    def test_shared_hooks_combine_with_project_and_launch_hooks(self):
        with patch.dict(os.environ,self.env,clear=True),patch.object(helper.Path,"cwd",return_value=self.home/"project"):
            common=self.home/".claude";common.mkdir()
            shared={"type":"command","command":"shared-hook"}
            (common/"settings.json").write_text(json.dumps({"model":"opus","hooks":{"Stop":[{"hooks":[shared]}]}}))
            project=self.home/"project/.claude";project.mkdir(parents=True)
            (project/"settings.json").write_text(json.dumps({"model":"sonnet","hooks":{"Stop":[{"hooks":[{"type":"command","command":"project-hook"}]}]}}))
            launch={"type":"command","command":"launch-hook"}
            supplied={"hooks":{"Stop":[{"hooks":[launch]}],"SessionStart":[{"hooks":[launch]}]},"statusLine":{"type":"command","command":"status"}}
            flags=helper.shared_launch_arguments(helper.profile(FIRST),["--settings",json.dumps(supplied),"--model","haiku"])
            settings=json.loads(Path(flags[1]).read_text())
            # The CLI reads project hooks itself and combines every source.
            self.assertEqual(settings["hooks"],{"Stop":[{"hooks":[shared]},{"hooks":[launch]}],"SessionStart":[{"hooks":[launch]}]})
            self.assertNotIn("model",settings)
            self.assertEqual(settings["statusLine"],supplied["statusLine"])
            self.assertEqual(flags[2:],["--model","haiku"])

    def test_desktop_share_adds_only_missing_settings_and_is_idempotent(self):
        # Every path, including the helper's own profile lookup, stays in the fixture home.
        fixture=patch.dict(os.environ,self.env,clear=True);fixture.start();self.addCleanup(fixture.stop)
        common=self.home/".claude";(common/"skills/local").mkdir(parents=True)
        shared={"hooks":[{"type":"command","command":"shared-hook"}]}
        user={"hooks":[{"type":"command","command":"account-hook"}]}
        def write_common(data):(common/"settings.json").write_text(json.dumps(data))
        write_common({"model":"opus","theme":"dark","statusLine":{"type":"command","command":"line"},
                      "env":{"ANTHROPIC_AUTH_TOKEN":"fixture-private"},"apiKeyHelper":"helper","hooks":{"Stop":[shared]}})
        base=helper.profile(FIRST);config=helper.config_directory(base)
        self.assertTrue(base.is_relative_to(self.home))
        config.mkdir(parents=True)
        (config/".credentials.json").write_text("private")
        original={"model":"sonnet","custom":1,"hooks":{"Stop":[user]}}
        (config/"settings.json").write_text(json.dumps(original))
        def share():
            self.run_helper("share",FIRST)
            return json.loads((config/"settings.json").read_text())
        backups=lambda:sorted((self.home/".local/share/cage/claude-shared/backups"/FIRST).glob("settings.json-*"))
        stored=share()
        self.assertEqual(stored,{"model":"sonnet","custom":1,"theme":"dark","hooks":{"Stop":[user,shared]}})
        self.assertTrue((config/"skills/local").is_symlink())
        self.assertEqual((config/".credentials.json").read_text(),"private")
        self.assertEqual(json.loads(backups()[0].read_text()),original)
        self.assertFalse(any(p.name.startswith("tmp") for p in config.iterdir()))
        first=(config/"settings.json").read_bytes()
        share()
        self.assertEqual((config/"settings.json").read_bytes(),first)
        self.assertEqual(len(backups()),1)
        # Terminals do not pass hook groups the profile already runs.
        with patch.object(helper.Path,"cwd",return_value=self.home):
            flags=helper.shared_launch_arguments(base,[])
        self.assertNotIn("hooks",json.loads(Path(flags[1]).read_text()))
        # Shared changes refresh earlier additions; a value the user edited stays.
        stored["theme"]="mine";(config/"settings.json").write_text(json.dumps(stored))
        renewed={"type":"command","command":"renewed-hook"}
        write_common({"model":"opus","theme":"light","effortLevel":"high","hooks":{"Stop":[renewed]}})
        self.assertEqual(share(),{"model":"sonnet","custom":1,"theme":"mine","effortLevel":"high","hooks":{"Stop":[user,renewed]}})
        # Removing the shared settings withdraws exactly what was added.
        write_common({})
        self.assertEqual(share(),{**original,"theme":"mine"})

    def test_desktop_share_withdraws_to_the_original_and_keeps_concurrent_edits(self):
        fixture=patch.dict(os.environ,self.env,clear=True);fixture.start();self.addCleanup(fixture.stop)
        common=self.home/".claude";common.mkdir()
        (common/"settings.json").write_text(json.dumps({"theme":"dark","hooks":{"Stop":[{"hooks":[]}]}}))
        base=helper.profile(FIRST);config=helper.config_directory(base)
        self.assertTrue(base.is_relative_to(self.home))
        config.mkdir(parents=True)
        self.run_helper("share",FIRST)
        self.assertEqual(json.loads((config/"settings.json").read_text()),{"theme":"dark","hooks":{"Stop":[{"hooks":[]}]}})
        (common/"settings.json").write_text("{}")
        self.run_helper("share",FIRST)
        self.assertEqual(json.loads((config/"settings.json").read_text()),{})
        # A CLI save during the sync wins; the helper skips this run and leaves no temporary file.
        (common/"settings.json").write_text(json.dumps({"theme":"dark"}))
        real=helper.json.dump
        def racing(data,stream,**options):
            (config/"settings.json").write_text('{"model":"cli"}')
            return real(data,stream,**options)
        with patch.object(helper.json,"dump",side_effect=racing):
            helper.sync_profile_settings(base,helper.shared_root(),{},False)
        self.assertEqual((config/"settings.json").read_text(),'{"model":"cli"}')
        self.assertEqual(sorted(p.name for p in config.iterdir() if p.is_file() and not p.is_symlink()),["settings.json"])
        # An unreadable file is left for the official CLI to report.
        (config/"settings.json").write_text("{ editing")
        self.run_helper("share",FIRST)
        self.assertEqual((config/"settings.json").read_text(),"{ editing")
        self.assertEqual(self.run_helper("share","default").returncode,0)

    def test_conflicting_shared_resource_preserves_both_versions(self):
        with patch.dict(os.environ,self.env,clear=True):
            base=helper.profile(FIRST);config=helper.config_directory(base);config.mkdir(parents=True)
            common=self.home/".claude";common.mkdir()
            (common/"CLAUDE.md").write_text("global")
            (config/"CLAUDE.md").write_text("account")
            (config/"commands").mkdir()
            (config/"commands/keep.md").write_text("shared despite the conflict elsewhere")
            # A terminal behaves like Desktop: the conflicting entry is skipped
            # on both sides, everything else is shared, and the launch goes on.
            stderr=io.StringIO()
            with contextlib.redirect_stderr(stderr):helper.shared_profile_resources(base)
            self.assertEqual((common/"CLAUDE.md").read_text(),"global")
            self.assertEqual((config/"CLAUDE.md").read_text(),"account")
            self.assertFalse((config/"CLAUDE.md").is_symlink())
            self.assertTrue((config/"commands").is_symlink())
            self.assertEqual((common/"commands/keep.md").read_text(),"shared despite the conflict elsewhere")
            # The notice names both files and how to resolve the conflict.
            notice=stderr.getvalue()
            self.assertIn("同名冲突",notice)
            self.assertIn("~/.claude/CLAUDE.md",notice)
            self.assertIn("~/"+(config/"CLAUDE.md").relative_to(self.home).as_posix(),notice)
            self.assertIn("并入",notice)
        launch=self.run_helper("run",FIRST,check=False)
        self.assertEqual(launch.returncode,0,launch.stderr)
        # Explained once; an unchanged conflict is not repeated on every launch.
        self.assertNotIn("同名冲突",launch.stderr)
        self.assertNotIn("同名冲突",self.run_helper("run",FIRST).stderr)
        # It is explained again once a day, or as soon as either file changes.
        record=json.loads((base/"shared-settings.json").read_text())
        for entry in record["notices"].values():entry["at"]-=helper.NOTICE_INTERVAL
        (base/"shared-settings.json").write_text(json.dumps(record))
        self.assertIn("同名冲突",self.run_helper("run",FIRST).stderr)
        self.assertNotIn("同名冲突",self.run_helper("run",FIRST).stderr)
        (config/"CLAUDE.md").write_text("account, edited again")
        self.assertIn("同名冲突",self.run_helper("run",FIRST).stderr)
        # Resolved: the file is shared and the notice is forgotten.
        (config/"CLAUDE.md").unlink()
        resolved=self.run_helper("run",FIRST)
        self.assertNotIn("同名冲突",resolved.stderr)
        self.assertTrue((config/"CLAUDE.md").is_symlink())
        self.assertNotIn("notices",json.loads((base/"shared-settings.json").read_text()))

    def test_existing_project_memory_is_backed_up_and_remains_shared(self):
        with patch.dict(os.environ,self.env,clear=True),patch.object(helper.Path,"cwd",return_value=self.home):
            base=helper.profile(FIRST)
            key=helper.re.sub(r"[^a-zA-Z0-9]","-",str(self.home.resolve()))
            original=helper.config_directory(base)/"projects"/key/"memory"
            original.mkdir(parents=True)
            (original/"MEMORY.md").write_text("account learning")
            flags=helper.shared_launch_arguments(base,[])
            memory=Path(json.loads(Path(flags[1]).read_text())["autoMemoryDirectory"])
            self.assertEqual((memory/"MEMORY.md").read_text(),"account learning")
            self.assertTrue(original.is_symlink())
            backups=list((self.home/".local/share/cage/claude-shared/backups"/FIRST/"projects"/key).glob("memory-*"))
            self.assertEqual([(path/"MEMORY.md").read_text() for path in backups],["account learning"])
            self.assertTrue(Path(flags[1]).resolve().is_relative_to(base.resolve()))

    def test_mobile_settings_remain_anonymous_when_adding_shared_defaults(self):
        with patch.dict(os.environ,self.env,clear=True),patch.object(helper.Path,"cwd",return_value=self.home),tempfile.TemporaryFile() as snapshot:
            snapshot.write(json.dumps({"env":{"ANTHROPIC_AUTH_TOKEN":"fixture-private"}}).encode())
            snapshot.flush()
            snapshot.seek(0)
            base=helper.profile(FIRST)
            flags=helper.shared_launch_arguments(base,["--settings","/dev/fd/"+str(snapshot.fileno())],snapshot.fileno())
            self.assertEqual(flags,["--settings","/dev/fd/"+str(snapshot.fileno())])
            settings=json.load(snapshot)
            self.assertEqual(settings["env"]["ANTHROPIC_AUTH_TOKEN"],"fixture-private")
            self.assertIn("autoMemoryDirectory",settings)
            self.assertFalse((base/"launch-settings").exists())

    def test_cancel_login_closes_browser_even_when_cli_fails(self):
        env = {**self.env}
        with patch.dict(os.environ, env, clear=True), patch.object(helper, "OPEN", "/bin/true"), \
                patch.object(helper.subprocess, "call", side_effect=KeyboardInterrupt), \
                patch.object(helper.subprocess, "run") as run:
            with self.assertRaises(KeyboardInterrupt):
                helper.main(["login", FIRST])
            self.assertEqual(run.call_args.args[0], ["/bin/true", "--login-id", FIRST, "--finish-login"])

    def test_login_callback_retains_account_without_passing_controller_flags(self):
        opener = self.home / "cage-open"
        opener.write_text('#!/bin/sh\n[ "$1" = --login-id ] || exit 2\nlogin="$2"; shift 2\nprintf "%s:%s\\n" "$login" "$1" >> "$HOME/opened"\n')
        opener.chmod(0o700)
        capture = self.home / "login-environment"
        fake = self.home / ".local/bin/claude"
        fake.write_text('#!/bin/sh\nenv > "$TEST_CAPTURE_ENV"\n"$BROWSER" "https://claude.ai/oauth/authorize"\n')
        env = {**self.env, "TEST_CAPTURE_ENV": str(capture)}
        with patch.dict(os.environ, env, clear=True), patch.object(helper, "OPEN", str(opener)):
            self.assertEqual(helper.main(["login", FIRST]), 0)
        self.assertEqual((self.home / "opened").read_text().splitlines(),
                         [FIRST + ":https://claude.ai/oauth/authorize", FIRST + ":--finish-login"])
        child_env = dict(line.split("=", 1) for line in capture.read_text().splitlines())
        self.assertFalse(any(key.upper().startswith("CAGE_") for key in child_env))
        self.assertNotIn("cage", child_env["BROWSER"].lower())
        self.assertNotIn("cage", child_env["CLAUDE_CONFIG_DIR"].lower())

    def install_usage_fixture(self):
        cli = self.home / ".local/bin/claude"
        cli.write_text("#!" + sys.executable + "\n" + """import json, os, sys
from pathlib import Path
home = Path.home()
args = sys.argv[1:]
def log(text):
    with (home / 'calls').open('a') as output: output.write(text + '\\n')
if args[-2:] == ['auth','status']:
    log('auth')
    email = os.environ['TEST_EMAIL']
    if (home / 'swapped').exists():
        email = 'b@example.test'
    print(json.dumps({'authMethod':'claude.ai','apiProvider':'firstParty','loggedIn':True,
                      'configDirectory':os.environ.get('CLAUDE_CONFIG_DIR',str(home/'.claude')),
                      'email':email,'orgId':'test-org','subscriptionType':'pro'}))
elif '-p' in args:
    request = json.loads(sys.stdin.read())
    assert request == {'type':'control_request','request_id':'quota','request':{'subtype':'get_usage','skip_behaviors':True}}
    assert '--no-session-persistence' in args and '--strict-mcp-config' in args and '--verbose' in args
    assert args[args.index('--output-format') + 1] == 'stream-json'
    assert args[args.index('--input-format') + 1] == 'stream-json'
    assert args[args.index('--tools') + 1] == ''
    assert json.loads(args[args.index('--settings')+1])['disableAllHooks'] is True
    assert Path.cwd() != home and not Path.cwd().is_relative_to(home)
    log('usage')
    if os.environ.get('SWAP') == '1':
        (home / 'swapped').touch()
    data = {'rate_limits_available':os.environ.get('AVAILABLE') != 'false',
            'rate_limits':{'limits':json.loads(os.environ['ROWS']), **json.loads(os.environ.get('LEGACY','{}'))},
            'session':{'total_cost_usd':0,'model_usage':{}}, 'private':'private-raw-output'}
    print(json.dumps({'type':'control_response','response':{'request_id':'quota','subtype':'success','response':data}}))
    if os.environ.get('TURNS'):
        print(json.dumps({'type':'result','num_turns':int(os.environ['TURNS']),'is_error':False}))
    sys.exit(int(os.environ.get('EXIT','0')))
else:
    sys.exit(7)
""")
        cli.chmod(0o700)

    def test_official_usage_windows_are_subject_bound_and_independently_cached(self):
        self.install_usage_fixture()
        reset = int(time.time()) + 18000
        date = datetime.fromtimestamp(reset, timezone.utc).isoformat()
        weekly = {"kind": "weekly_all", "percent": 34.5, "resets_at": date}
        session = {"kind": "session", "percent": 0, "resets_at": None}
        def query(rows, account="default", **extra):
            env = {**self.env, "ROWS": json.dumps(rows), **extra}
            result = self.run_helper("usage", account, self.subject, env=env)
            self.assertNotIn("private-raw-output", result.stdout + result.stderr)
            return json.loads(result.stdout)
        (self.home / ".claude").mkdir()
        first = query([weekly, session, {"kind":"weekly_scoped","percent":99,"resets_at":date}])
        self.assertEqual(first["usage_status"], "reported")
        self.assertEqual(first["quota"]["seven_day"]["resets_at"], reset)
        self.assertEqual(first["quota"]["five_hour"]["used_percentage"], 0)
        self.assertEqual(first["quota"]["five_hour"]["resets_at"], 0)
        # A passive redraw doesn't refresh either age. A completed control
        # query refreshes only the window actually returned by the CLI.
        path = self.home / ".local/share/cage/claude-accounts/default/quota.json"
        stored = json.loads(path.read_text())
        old = int(time.time()) - 300
        stored["observed_at"] = stored["five_hour"]["observed_at"] = stored["seven_day"]["observed_at"] = old
        path.write_text(json.dumps(stored))
        self.run_helper("statusline", "default", data={"rate_limits": {
            "five_hour": stored["five_hour"], "seven_day": stored["seven_day"],
        }})
        self.assertEqual(json.loads(path.read_text()), stored)
        partial = query([weekly])
        self.assertEqual(partial["usage_status"], "partial")
        self.assertGreater(partial["usage_checked_at"], old)
        self.assertEqual(partial["quota"]["five_hour"]["observed_at"], old)
        self.assertGreater(partial["quota"]["seven_day"]["observed_at"], old)
        stored = json.loads(path.read_text())
        self.assertEqual(query([], FIRST)["usage_status"], "unavailable")
        self.assertEqual(query(None)["usage_status"], "cached")
        self.assertEqual(query(None, AVAILABLE="false")["usage_status"], "not_applicable")
        self.assertEqual(query([weekly], EXIT="1")["usage_status"], "error")
        self.assertEqual(query([weekly], TURNS="1")["usage_status"], "error")
        self.assertEqual(json.loads(path.read_text()), stored)
        changed = query([dict(session, percent=91)], SWAP="1")
        self.assertEqual(changed["usage_status"], "identity_changed")
        self.assertNotIn("quota", changed)
        self.assertEqual(json.loads(path.read_text()), stored)
        calls = self.home / "calls"
        calls.write_text("")
        self.assertEqual(query([weekly])["usage_status"], "identity_changed")
        self.assertEqual(calls.read_text().splitlines(), ["auth"])

    def test_legacy_control_windows_and_row_fallback(self):
        self.install_usage_fixture()
        now = int(time.time())
        stamp = datetime.fromtimestamp(now + 3600, timezone.utc).isoformat()
        legacy = {"five_hour":{"utilization":45.5,"resets_at":stamp}, "seven_day":{"utilization":None,"resets_at":stamp},
                  "seven_day_sonnet":{"utilization":98,"resets_at":stamp}}
        rows = [{"kind":"session","percent":12,"resets_at":stamp},{"kind":"weekly_all","percent":67,"resets_at":None}]
        env = {**self.env,"ROWS":json.dumps(rows),"LEGACY":json.dumps(legacy)}
        observed = json.loads(self.run_helper("usage", FIRST, self.subject, env=env).stdout)
        self.assertEqual(observed["usage_status"], "reported")
        self.assertEqual(observed["quota"]["five_hour"]["used_percentage"], 45.5)
        self.assertEqual(observed["quota"]["five_hour"]["resets_at"], now + 3600)
        self.assertEqual(observed["quota"]["seven_day"]["used_percentage"], 67)
        self.assertEqual(observed["quota"]["seven_day"]["resets_at"], 0)


class SharedPluginsMcpAndMemoryTests(unittest.TestCase):
    """Cross-account sharing of plugins, MCP servers, memory and instructions."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.home = Path(tmp.name).resolve()
        temp_patch = patch.object(tempfile, "tempdir", str(self.home))
        temp_patch.start()
        self.addCleanup(temp_patch.stop)
        fake = self.home / ".local/bin/claude"
        fake.parent.mkdir(parents=True)
        fake.write_text("#!/bin/sh\nexit 0\n")
        fake.chmod(0o700)
        self.env = {"HOME": str(self.home), "PATH": os.defpath, "PYTHONDONTWRITEBYTECODE": "1", "TMPDIR": str(self.home)}
        fixture = patch.dict(os.environ, self.env, clear=True)
        fixture.start()
        self.addCleanup(fixture.stop)
        self.common = self.home / ".claude"
        (self.common / "plugins").mkdir(parents=True)

    def profile(self, account):
        base = helper.profile(account)
        # Never touch a real profile: every path must stay in the fixture home.
        self.assertTrue(base.resolve().is_relative_to(self.home))
        helper.config_directory(base).mkdir(parents=True, exist_ok=True)
        return base, helper.config_directory(base)

    def share(self, account, check=True):
        result = subprocess.run([sys.executable, str(source), "share", account], env=self.env, cwd=self.home,
                                text=True, capture_output=True)
        if check:
            self.assertEqual(result.returncode, 0, result.stderr)
        return result

    def write(self, path, data):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(data) if not isinstance(data, str) else data)

    def read(self, path):
        return json.loads(path.read_text())

    def install(self, plugins, marketplace, source_repo, plugin, version="1.0.0"):
        """Lay out a profile's plugins folder the way the official CLI writes it."""
        (plugins / "marketplaces" / marketplace / ".claude-plugin").mkdir(parents=True, exist_ok=True)
        (plugins / "marketplaces" / marketplace / ".claude-plugin/marketplace.json").write_text(source_repo)
        folder = plugins / "cache" / marketplace / plugin / version
        folder.mkdir(parents=True, exist_ok=True)
        (folder / "plugin.md").write_text(plugin + version)
        known = self.read(plugins / "known_marketplaces.json") if (plugins / "known_marketplaces.json").exists() else {}
        known[marketplace] = {"source": {"source": "github", "repo": source_repo},
                              "installLocation": str(plugins / "marketplaces" / marketplace), "lastUpdated": "2026-11-01T00:00:00.000Z"}
        self.write(plugins / "known_marketplaces.json", known)
        installed = self.read(plugins / "installed_plugins.json") if (plugins / "installed_plugins.json").exists() else {"version": 2, "plugins": {}}
        stamp = datetime.now(timezone.utc).isoformat().replace("+00:00", "Z")
        installed["plugins"][plugin + "@" + marketplace] = [{"scope": "user", "installPath": str(folder), "version": version,
                                                             "installedAt": stamp, "lastUpdated": stamp}]
        self.write(plugins / "installed_plugins.json", installed)

    def test_private_plugin_entries_and_identity_files_never_become_links(self):
        self.assertFalse(helper.PRIVATE_PLUGIN_ENTRIES & {name for name, _ in helper.SHARED_PLUGIN_FOLDERS})
        self.assertTrue({"synced", "store", "data", "installed_plugins.json", "known_marketplaces.json"} <= helper.PRIVATE_PLUGIN_ENTRIES)
        base, config = self.profile(FIRST)
        identity = {".credentials.json": "credential", ".claude.json": json.dumps({"oauthAccount": {"accountUuid": "a"}, "userID": "u"}),
                    "plugins/synced/org_account/x.json": "synced", "plugins/store/s.json": "store", "plugins/data/p/state": "data",
                    "telemetry/t.json": "t", "statsig/s": "s", "history.jsonl": "h", "projects/p/s.jsonl": "{}",
                    "sessions/1.json": "{}", "todos/t.json": "[]", "file-history/s/b": "b", "shell-snapshots/s.sh": "s"}
        for name, text in identity.items():
            (config / name).parent.mkdir(parents=True, exist_ok=True)
            (config / name).write_text(text)
        self.share(FIRST)
        allowed = {"commands", "agents", "rules", "agent-memory", "CLAUDE.md", "AGENTS.md", "plugins/marketplaces", "plugins/cache"}
        for path in config.rglob("*"):
            relative = str(path.relative_to(config))
            if path.is_symlink():
                self.assertTrue(relative in allowed or relative.startswith("skills/") or
                                relative.startswith("projects/") and relative.endswith("/memory"), relative)
        for name, text in identity.items():
            self.assertFalse(any(parent.is_symlink() for parent in [config / name, *(config / name).parents] if parent != config and config in parent.parents or parent == config / name), name)
            self.assertEqual((config / name).read_text(), text)
        self.assertFalse((self.common / "plugins/synced").exists())
        self.assertFalse((self.common / ".credentials.json").exists())

    def test_existing_private_plugins_migrate_and_choices_flow_between_accounts(self):
        self.install(self.common / "plugins", "team", "example/team", "lint")
        self.write(self.common / "settings.json", {"enabledPlugins": {"lint@team": True}, "theme": "dark"})
        first, config = self.profile(FIRST)
        plugins = config / "plugins"
        self.install(plugins, "official", "example/official", "review")
        self.install(plugins, "team", "example/team", "lint")
        (plugins / "cache/team/lint/1.0.0/.in_use").write_text("account copy")
        self.write(config / "settings.json", {"enabledPlugins": {"review@official": True}})
        self.share(FIRST)
        for name in ("marketplaces", "cache"):
            self.assertTrue((plugins / name).is_symlink())
        self.assertTrue((self.common / "plugins/cache/official/review/1.0.0/plugin.md").exists())
        # The redundant copy of an already shared version is kept, not deleted.
        kept = list((self.home / ".local/share/cage/claude-shared/backups" / FIRST / "plugins").rglob(".in_use"))
        self.assertEqual([path.read_text() for path in kept], ["account copy"])
        common_installed = self.read(self.common / "plugins/installed_plugins.json")["plugins"]
        self.assertEqual(common_installed["review@official"][0]["installPath"], str(self.common / "plugins/cache/official/review/1.0.0"))
        self.assertEqual(self.read(self.common / "settings.json")["enabledPlugins"], {"lint@team": True, "review@official": True})
        self.assertEqual(self.read(self.common / "settings.json")["theme"], "dark")
        second, other = self.profile(SECOND)
        self.share(SECOND)
        installed = self.read(other / "plugins/installed_plugins.json")["plugins"]
        # A new registry uses the alias the runtime gives the CLI for this
        # profile, exactly as the CLI itself would record it.
        with patch.object(helper.tempfile, "tempdir", str(self.home)):
            alias, real = helper.plugin_root_forms(other)
        self.assertEqual(real, str(other / "plugins") + os.sep)
        self.assertNotIn("cage", alias)
        self.assertEqual(installed["review@official"][0]["installPath"], alias + "cache/official/review/1.0.0")
        self.assertTrue((other / "plugins/cache/official/review/1.0.0/plugin.md").exists())
        known = self.read(other / "plugins/known_marketplaces.json")
        self.assertEqual(known["official"]["installLocation"], alias + "marketplaces/official")
        # A registry written in the real spelling keeps it.
        first_installed = self.read(plugins / "installed_plugins.json")["plugins"]
        self.assertEqual(first_installed["lint@team"][0]["installPath"], str(plugins / "cache/team/lint/1.0.0"))
        self.assertEqual(self.read(other / "settings.json")["enabledPlugins"], {"lint@team": True, "review@official": True})
        # The second account uninstalls; the removal reaches the first one.
        installed.pop("review@official")
        self.write(other / "plugins/installed_plugins.json", {"version": 2, "plugins": installed})
        settings = self.read(other / "settings.json"); settings["enabledPlugins"].pop("review@official")
        self.write(other / "settings.json", settings)
        self.share(SECOND)
        self.share(FIRST)
        self.assertNotIn("review@official", self.read(plugins / "installed_plugins.json")["plugins"])
        self.assertEqual(self.read(config / "settings.json")["enabledPlugins"], {"lint@team": True})
        # Repeated runs change nothing further.
        before = {path: path.read_bytes() for path in (plugins / "installed_plugins.json", config / "settings.json", self.common / "settings.json")}
        self.share(FIRST)
        self.assertEqual({path: path.read_bytes() for path in before}, before)

    def test_plugin_conflict_keeps_profile_plugins_private_and_launch_continues(self):
        self.install(self.common / "plugins", "tools", "example/upstream", "a")
        first, config = self.profile(FIRST)
        self.install(config / "plugins", "tools", "someone/fork", "a")
        (self.common / "CLAUDE.md").write_text("shared")
        result = self.share(FIRST)
        self.assertIn("插件继续按账号分开", result.stderr)
        self.assertFalse((config / "plugins/marketplaces").is_symlink())
        self.assertEqual((config / "plugins/marketplaces/tools/.claude-plugin/marketplace.json").read_text(), "someone/fork")
        self.assertEqual(self.read(self.common / "plugins/known_marketplaces.json")["tools"]["source"]["repo"], "example/upstream")
        self.assertTrue((config / "CLAUDE.md").is_symlink())

    def test_interrupted_plugin_migration_resumes_without_losing_files(self):
        first, config = self.profile(FIRST)
        for plugin in ("a", "b", "c"):
            self.install(config / "plugins", "tools", "example/tools", plugin)
        files = sorted(str(path.relative_to(config / "plugins")) for path in (config / "plugins").rglob("*") if path.is_file())
        real, calls = Path.rename, []
        def failing(path, target):
            calls.append(path)
            if len(calls) == 2:
                raise OSError("interrupted")
            return real(path, target)
        with patch.object(helper.Path, "rename", failing), self.assertRaises(OSError):
            helper.share_plugin_folders(first, helper.shared_root())
        self.assertTrue(helper.share_plugin_folders(first, helper.shared_root()))
        self.assertTrue((config / "plugins/cache").is_symlink())
        found = sorted(str(path.relative_to(config / "plugins")) for path in (config / "plugins").rglob("*") if path.is_file())
        found += sorted("cache/" + str(path.relative_to(self.common / "plugins/cache")) for path in (self.common / "plugins/cache").rglob("*") if path.is_file())
        found += sorted("marketplaces/" + str(path.relative_to(self.common / "plugins/marketplaces")) for path in (self.common / "plugins/marketplaces").rglob("*") if path.is_file())
        self.assertEqual(sorted(set(found)), files)

    def test_shared_mcp_servers_reach_terminals_and_desktop_without_account_fields(self):
        self.write(self.home / ".claude.json", {"oauthAccount": {"emailAddress": "default@example.test"}, "userID": "default-user",
                                               "mcpServers": {"notes": {"command": "notes-server"}, "search": {"command": "shared-search"}}})
        first, config = self.profile(FIRST)
        own = {"oauthAccount": {"emailAddress": "first@example.test"}, "userID": "first-user", "mcpServers": {"search": {"command": "own-search"}}}
        self.write(config / ".claude.json", own)
        flags = helper.shared_mcp_arguments(first, ["--settings", "x", "prompt"])
        self.assertEqual(flags[0], "--mcp-config")
        offered = Path(flags[1]).read_text()
        self.assertEqual(json.loads(offered), {"mcpServers": {"notes": {"command": "notes-server"}}})
        self.assertNotIn("default", offered)
        joined = helper.shared_mcp_arguments(first, ["--settings", "x", "--mcp-config", "channel.json", "--dangerously-load-development-channels", "c"])
        self.assertEqual(joined[:4], ["--settings", "x", "--mcp-config", flags[1]])
        self.assertEqual(joined[4], "channel.json")
        self.assertEqual(helper.shared_mcp_arguments(first, ["--strict-mcp-config", "--settings", "x"]), ["--strict-mcp-config", "--settings", "x"])
        self.share(FIRST)
        stored = self.read(config / ".claude.json")
        self.assertEqual(stored["mcpServers"], {"search": {"command": "own-search"}, "notes": {"command": "notes-server"}})
        self.assertEqual((stored["oauthAccount"], stored["userID"]), (own["oauthAccount"], own["userID"]))
        self.assertEqual(helper.shared_mcp_arguments(first, ["--settings", "x"]), ["--settings", "x"])
        self.write(self.home / ".claude.json", {"oauthAccount": {}, "mcpServers": {"search": {"command": "shared-search"}}})
        self.share(FIRST)
        self.assertEqual(self.read(config / ".claude.json"), own)

    def test_project_key_matches_the_official_cli_and_old_memory_moves(self):
        self.assertEqual(helper.project_key("/home/u/my repo"), "-home-u-my-repo")
        self.assertEqual(helper.project_key("/p/\U0001F600"), "-p---")
        long = "/w/" + "x" * 230
        # (h << 5) - h + code over UTF-16 units, as a signed 32-bit value, base 36.
        value = 0
        for code in long.encode("utf-16-le")[::2]:
            value = (value * 31 + code) & 0xFFFFFFFF
        value = abs(value - (1 << 32) if value >= 1 << 31 else value)
        self.assertEqual(helper.project_key(long), ("-w-" + "x" * 230)[:200] + "-" + numpy_base36(value))
        first, config = self.profile(FIRST)
        project = self.home / ("p" * 190)
        project.mkdir()
        old = self.common / "projects" / helper.legacy_project_key(project) / "memory"
        old.mkdir(parents=True)
        (old / "MEMORY.md").write_text("long project note")
        with patch.object(helper.Path, "cwd", return_value=project):
            flags = helper.shared_launch_arguments(first, [])
        memory = Path(json.loads(Path(flags[1]).read_text())["autoMemoryDirectory"])
        self.assertEqual(memory.resolve(), (self.common / "projects" / helper.project_key(project) / "memory").resolve())
        self.assertEqual((memory / "MEMORY.md").read_text(), "long project note")
        self.assertTrue(old.is_symlink())

    def test_desktop_share_links_known_project_memory_before_any_terminal(self):
        first, config = self.profile(FIRST)
        shared = self.common / "projects/-work-app/memory"
        shared.mkdir(parents=True)
        (shared / "MEMORY.md").write_text("from another account")
        own = config / "projects/-work-lib/memory"
        own.mkdir(parents=True)
        (own / "MEMORY.md").write_text("only here")
        self.share(FIRST)
        self.assertEqual((config / "projects/-work-app/memory/MEMORY.md").read_text(), "from another account")
        self.assertTrue((config / "projects/-work-lib/memory").is_symlink())
        self.assertEqual((self.common / "projects/-work-lib/memory/MEMORY.md").read_text(), "only here")

    def test_atomically_saved_instructions_fold_back_into_the_shared_file(self):
        (self.common / "CLAUDE.md").write_text("v1")
        first, config = self.profile(FIRST)
        second, other = self.profile(SECOND)
        self.share(FIRST); self.share(SECOND)
        # The CLI saved through rename: the link became a private copy.
        (config / "CLAUDE.md").unlink()
        (config / "CLAUDE.md").write_text("v2 from first")
        self.share(FIRST)
        self.assertTrue((config / "CLAUDE.md").is_symlink())
        self.assertEqual((other / "CLAUDE.md").read_text(), "v2 from first")
        backups = list((self.home / ".local/share/cage/claude-shared/backups/common").glob("CLAUDE.md-*"))
        self.assertEqual([path.read_text() for path in backups], ["v1"])
        # Both sides changed: nothing is chosen and the launch reports it.
        (other / "CLAUDE.md").unlink(); (other / "CLAUDE.md").write_text("second's edit")
        (self.common / "CLAUDE.md").write_text("edited elsewhere")
        failure = self.share(SECOND, check=False)
        self.assertIn("同名冲突", failure.stderr)
        self.assertEqual((other / "CLAUDE.md").read_text(), "second's edit")
        self.assertEqual((self.common / "CLAUDE.md").read_text(), "edited elsewhere")

    def test_interrupted_settings_record_still_withdraws_its_additions(self):
        self.write(self.common / "settings.json", {"theme": "dark"})
        first, config = self.profile(FIRST)
        real, calls = helper.write_record, []
        def crash(base, record):
            calls.append(record)
            if "pending" not in record:
                raise OSError("crash before the record is final")
            return real(base, record)
        with patch.object(helper, "write_record", crash), self.assertRaises(OSError):
            helper.shared_profile_resources(first, desktop=True)
        self.assertEqual(self.read(config / "settings.json"), {"theme": "dark"})
        self.write(self.common / "settings.json", {})
        self.share(FIRST)
        self.assertEqual(self.read(config / "settings.json"), {})

    def test_status_line_keeps_the_users_command_and_quota_reading(self):
        self.write(self.common / "settings.json", {"statusLine": {"type": "command", "command": "cat > /dev/null; printf mine", "padding": 1}})
        first, config = self.profile(FIRST)
        with patch.object(helper.Path, "cwd", return_value=self.home):
            self.assertFalse(helper.has_status_line(first))
            self.assertEqual(helper.status_line("cage-line")["padding"], 1)
            shown = subprocess.run([sys.executable, str(source), "statusline", FIRST], env=self.env, cwd=self.home,
                                   input=json.dumps({"session_id": "s"}), text=True, capture_output=True)
            self.assertEqual(shown.stdout, "mine")
            self.write(config / "settings.json", {"statusLine": {"type": "command", "command": "own"}})
            self.assertTrue(helper.has_status_line(first))

    def test_active_shared_plugin_clears_stale_orphaned_marker_while_uninstall_keeps_it(self):
        self.install(self.common / "plugins", "team", "example/team", "lint")
        first, _ = self.profile(FIRST)
        second, other = self.profile(SECOND)
        self.share(FIRST)
        self.share(SECOND)
        marker = self.common / "plugins/cache/team/lint/1.0.0/.orphaned_at"
        marker.write_text("1700000000000")
        # Still installed in shared registry: stale .orphaned_at is cleared so
        # the official CLI's cache sweep cannot delete an active shared plugin.
        self.share(FIRST)
        self.assertFalse(marker.exists())
        # Once uninstalled, removal synchronizes and .orphaned_at stays for CLI cleanup.
        self.write(other / "plugins/installed_plugins.json", {"version": 2, "plugins": {}})
        marker.write_text("1700000000000")
        self.share(SECOND)
        self.assertTrue(marker.exists())
        self.assertNotIn("lint@team", self.read(self.common / "plugins/installed_plugins.json")["plugins"])

    def test_backups_prune_by_creation_time_and_always_keep_the_newest(self):
        backups = self.home / ".local/share/cage/claude-shared/backups" / FIRST
        backups.mkdir(parents=True)
        now = 1_800_000_000.0
        ns = lambda seconds: int(seconds * 1e9)
        old = now - (helper.BACKUP_MAX_AGE + 10 * 86400)
        recent = now - 3600
        # Count pruning: 25 recent backups -> keep newest 20 by their names.
        prefix = backups / "settings.json"
        for i in range(25):
            entry = backups / f"settings.json-{ns(recent) + i}"
            entry.write_text(f"v{i}")
            # Copies keep their source's time; it must not decide the order.
            os.utime(entry, (old - i, old - i))
        unrelated = backups / "settings.json.keep"
        unrelated.write_text("untouched")
        helper.prune_backup_series(prefix, now=now)
        kept = sorted(p.name for p in backups.glob("settings.json-*"))
        self.assertEqual(len(kept), helper.BACKUP_MAX_COUNT)
        self.assertEqual(kept[0], f"settings.json-{ns(recent) + 5}")
        self.assertEqual(kept[-1], f"settings.json-{ns(recent) + 24}")
        self.assertTrue(unrelated.exists())
        # Age pruning by creation time, even when the copied mtime is recent.
        aged_prefix = backups / "claude.json"
        for i in range(3):
            entry = backups / f"claude.json-{ns(old) + i}"
            entry.write_text(f"old{i}")
            os.utime(entry, (now, now))
        fresh = backups / f"claude.json-{ns(recent)}"
        fresh.write_text("fresh")
        os.utime(fresh, (old, old))
        helper.prune_backup_series(aged_prefix, now=now)
        self.assertEqual([p.name for p in backups.glob("claude.json-*")], [fresh.name])
        # The newest backup of a series is kept regardless of age.
        lone = backups / f"lone.json-{ns(old)}"
        lone.write_text("lone")
        helper.prune_backup_series(backups / "lone.json", now=now)
        self.assertTrue(lone.exists())
        newer_old = backups / f"lone.json-{ns(old) + 60}"
        newer_old.write_text("newer-old")
        helper.prune_backup_series(backups / "lone.json", now=now)
        self.assertEqual([p.name for p in backups.glob("lone.json-*")], [newer_old.name])
        # A backup at the bare name (older versions) has no known creation
        # time: never removed by age, only beyond the count, oldest first.
        sole_dir = backups / "projects/demo/memory"
        sole_dir.mkdir(parents=True)
        (sole_dir / "MEMORY.md").write_text("original note")
        os.utime(sole_dir, (old, old))
        helper.prune_backup_series(sole_dir, now=now)
        self.assertTrue((sole_dir / "MEMORY.md").is_file())
        later = backups / f"projects/demo/memory-{ns(recent)}"
        later.mkdir()
        helper.prune_backup_series(sole_dir, now=now)
        self.assertTrue((sole_dir / "MEMORY.md").is_file())
        helper.prune_backup_series(sole_dir, now=now, keep_count=1)
        self.assertFalse(sole_dir.exists())
        self.assertTrue(later.is_dir())
        # A real backup is named by when it was made, so a copy of an old file
        # is not pruned the moment it is created.
        source_file = self.home / "old-source.md"
        source_file.write_text("old content")
        os.utime(source_file, (old, old))
        earlier = backups / f"rules-{ns(time.time() - helper.BACKUP_MAX_AGE - 86400)}"
        earlier.write_text("earlier")
        created = helper.unique_backup_path(backups / "rules")
        shutil.copy2(source_file, created)
        helper.prune_backup_series(backups / "rules")
        self.assertEqual([p.name for p in backups.glob("rules-*")], [created.name])
        # Outside backups root: ignored.
        outside = self.home / f"outside-settings.json-{ns(old)}"
        outside2 = self.home / f"outside-settings.json-{ns(old) + 10}"
        outside.write_text("a"); outside2.write_text("b")
        helper.prune_backup_series(self.home / "outside-settings.json", now=now)
        self.assertTrue(outside.exists() and outside2.exists())

    def test_private_plugin_registries_and_credentials_never_leak_across_accounts(self):
        first, config = self.profile(FIRST)
        second, other = self.profile(SECOND)
        plugins = config / "plugins"
        self.install(plugins, "team", "example/team", "lint")
        # Add private claude.ai/org marketplace, credentialed URL marketplace,
        # synced/arbitrary/traversal installPaths, and header-authenticated extra marketplace.
        known = self.read(plugins / "known_marketplaces.json")
        known["org-mkt"] = {"source": {"source": "url", "url": "https://claude.ai/api/marketplace", "claudeaiPluginId": "pid-1"},
                            "installLocation": str(plugins / "synced/org_uuid/org-mkt"), "marketplaceId": "org-mkt-id"}
        known["token-mkt"] = {"source": {"source": "git", "url": "https://ghp_secret@github.com/org/private.git"},
                              "installLocation": str(plugins / "marketplaces/token-mkt")}
        self.write(plugins / "known_marketplaces.json", known)
        installed = self.read(plugins / "installed_plugins.json")
        installed["plugins"]["secret@org-mkt"] = [{"scope": "user", "installPath": str(plugins / "synced/org_uuid/secret/1.0.0"), "version": "1.0.0"}]
        installed["plugins"]["priv@token-mkt"] = [{"scope": "user", "installPath": str(plugins / "cache/token-mkt/priv/1.0.0"), "version": "1.0.0"}]
        installed["plugins"]["outside@team"] = [{"scope": "user", "installPath": "/tmp/outside/plugin", "version": "1.0.0"}]
        installed["plugins"]["escape@team"] = [{"scope": "user", "installPath": str(plugins / "cache") + "/../synced/x", "version": "1.0.0"}]
        self.write(plugins / "installed_plugins.json", installed)
        self.write(config / "settings.json", {
            "enabledPlugins": {"lint@team": True, "secret@org-mkt": True, "priv@token-mkt": True, "ext@cred-extra": True, "outside@team": True},
            "extraKnownMarketplaces": {
                "pub-extra": {"source": {"source": "github", "repo": "example/pub"}},
                "cred-extra": {"source": {"source": "url", "url": "https://example.com/mkt.json", "headers": {"Authorization": "Bearer topsecret"}}},
            },
        })
        self.share(FIRST)
        self.share(SECOND)
        # Public plugin & public extra marketplace sync across accounts.
        self.assertIn("lint@team", self.read(other / "plugins/installed_plugins.json")["plugins"])
        self.assertIn("pub-extra", self.read(other / "settings.json")["extraKnownMarketplaces"])
        self.assertEqual(self.read(other / "settings.json")["enabledPlugins"], {"lint@team": True})
        # None of the private/credentialed/org/synced/arbitrary entries ever leak to common or SECOND.
        for target_root in (self.common, other):
            combined = "\n".join(p.read_text(encoding="utf-8") for p in (
                target_root / "plugins/known_marketplaces.json",
                target_root / "plugins/installed_plugins.json",
                target_root / "settings.json",
            ) if p.exists())
            for secret in ("org-mkt", "token-mkt", "cred-extra", "secret@org-mkt", "priv@token-mkt",
                           "outside@team", "escape@team", "ext@cred-extra", "topsecret", "ghp_secret",
                           "claudeaiPluginId", "org_uuid", "marketplaceId"):
                self.assertNotIn(secret, combined, f"leaked {secret} into {target_root}")
        # Account A retains all of its own private entries untouched.
        first_known = self.read(plugins / "known_marketplaces.json")
        self.assertIn("org-mkt", first_known)
        self.assertIn("token-mkt", first_known)
        self.assertIn("secret@org-mkt", self.read(plugins / "installed_plugins.json")["plugins"])
        self.assertIn("cred-extra", self.read(config / "settings.json")["extraKnownMarketplaces"])
        # share_plugin_folders refuses to link when local/marketplaces contains a private/org directory.
        third = "3" * 24
        base3, cfg3 = self.profile(third)
        (cfg3 / "plugins/marketplaces/org-dir").mkdir(parents=True)
        self.write(cfg3 / "plugins/known_marketplaces.json", {
            "org-dir": {"source": {"source": "url", "url": "https://claude.ai/api/org"}, "installLocation": str(cfg3 / "plugins/marketplaces/org-dir")}
        })
        self.assertFalse(helper.share_plugin_folders(base3, helper.shared_root()))
        self.assertFalse((cfg3 / "plugins/marketplaces").is_symlink())
        self.assertFalse((self.common / "plugins/marketplaces/org-dir").exists())

    def test_malformed_settings_and_corrupt_record_subkeys_do_not_block_launch(self):
        first, config = self.profile(FIRST)
        project = self.home / "proj"
        (project / ".claude").mkdir(parents=True)
        for bad in (self.common / "settings.json", config / "settings.json",
                    project / ".claude/settings.json", project / ".claude/settings.local.json"):
            bad.write_text("{broken json", encoding="utf-8")
        (first / "shared-settings.json").write_text(json.dumps({"plugins": "not-a-dict", "links": ["bad"]}), encoding="utf-8")
        with patch.object(helper.Path, "cwd", return_value=project):
            flags = helper.shared_launch_arguments(first, [])
        self.assertEqual(flags[0], "--settings")
        self.assertIn("autoMemoryDirectory", json.loads(Path(flags[1]).read_text(encoding="utf-8")))
        helper.shared_profile_resources(first, desktop=True)
        stored = helper.read_record(first)
        self.assertIsInstance(stored["plugins"], dict)
        self.assertIsInstance(stored["links"], dict)

    def test_plugin_root_forms_uses_runtime_path_and_utf8_and_junction_removal(self):
        first, config = self.profile(FIRST)
        # Simulate _remote_windows where _runtime["path"] returns the real path directly.
        with patch.dict(helper._runtime, {"path": lambda p: str(p)}):
            self.assertEqual(helper.plugin_root_forms(config), [str(config / "plugins") + os.sep])
        # Non-ASCII UTF-8 round-trip through write_record and replace_json.
        helper.write_record(first, {"note": "中文配置"})
        self.assertEqual(helper.read_record(first)["note"], "中文配置")
        target = config / "utf8.json"
        self.assertTrue(helper.replace_json(target, {"title": "测试🚀"}, None, helper.shared_root() / "backups" / FIRST / "utf8.json"))
        self.assertEqual(helper.json_file(target)["title"], "测试🚀")
        # remove_link uses rmdir() for Windows junctions and unlink() otherwise.
        class FakeJunction:
            def __init__(self): self.calls = []
            def is_junction(self): return True
            def rmdir(self): self.calls.append("rmdir")
            def unlink(self): self.calls.append("unlink")
        fake = FakeJunction()
        with patch.object(helper.os, "name", "nt"):
            helper.remove_link(fake)
        self.assertEqual(fake.calls, ["rmdir"])

    def test_self_aliased_skills_symlink_never_wipes_shared_skills(self):
        first, config = self.profile(FIRST)
        skill = self.common / "skills/my-skill/SKILL.md"
        skill.parent.mkdir(parents=True)
        skill.write_text("precious skill", encoding="utf-8")
        (config / "skills").symlink_to(self.common / "skills", target_is_directory=True)
        self.share(FIRST)
        helper.shared_profile_resources(first, desktop=False)
        self.assertTrue(skill.is_file() and not skill.is_symlink())
        self.assertEqual(skill.read_text(encoding="utf-8"), "precious skill")

    def test_pending_mcp_popped_and_raced_writes_preserve_pending_and_common_baseline(self):
        self.write(self.home / ".claude.json", {"mcpServers": {"notes": {"command": "notes-srv"}}})
        first, config = self.profile(FIRST)
        self.share(FIRST)
        self.assertNotIn("pending_mcp", helper.read_record(first))
        # Pre-existing pending_mcp survives a raced replace_json failure.
        record = helper.read_record(first)
        record["pending_mcp"] = {"prior": {"command": "old-srv"}}
        helper.write_record(first, record)
        self.write(self.home / ".claude.json", {"mcpServers": {"notes": {"command": "notes-srv"}, "new": {"command": "new-srv"}}})
        with patch.object(helper, "replace_json", return_value=False):
            helper.sync_profile_mcp(first, helper.shared_root(), record, True)
        self.assertEqual(helper.read_record(first).get("pending_mcp"), {"prior": {"command": "old-srv"}})
        # Common-side plugin baseline persists immediately after common write even if local write races.
        self.install(self.common / "plugins", "team", "example/team", "lint")
        self.write(self.common / "settings.json", {"enabledPlugins": {"lint@team": True}, "theme": "dark"})
        self.share(FIRST)
        # Account disables lint@team while common adds a new setting (language) that triggers a local write.
        self.write(config / "settings.json", {"enabledPlugins": {"lint@team": False}, "theme": "dark"})
        self.write(self.common / "settings.json", {"enabledPlugins": {"lint@team": True}, "theme": "dark", "language": "zh"})
        real_replace = helper.replace_json
        def fail_local(path, data, original, backup):
            if path == config / "settings.json":
                return False
            return real_replace(path, data, original, backup)
        with patch.object(helper, "replace_json", side_effect=fail_local):
            helper.shared_profile_resources(first, desktop=True)
        # common already received lint@team=False and record persisted that baseline.
        self.assertEqual(self.read(self.common / "settings.json")["enabledPlugins"], {"lint@team": False})
        self.assertEqual(helper.read_record(first)["plugins"]["enabledPlugins"]["lint@team"], False)
        # Subsequent sync must NOT resurrect lint@team=True in common.
        self.share(FIRST)
        self.assertEqual(self.read(self.common / "settings.json")["enabledPlugins"], {"lint@team": False})

    def test_stale_mcp_cleanup_no_leaked_backup_unique_names_and_desktop_conflict_isolation(self):
        first, config = self.profile(FIRST)
        # Stale launch-settings/mcp-*.json cleanup on rotation and removal.
        self.write(self.home / ".claude.json", {"mcpServers": {"srv": {"command": "v1", "env": {"TOKEN": "s1"}}}})
        flags1 = helper.shared_mcp_arguments(first, ["--settings", "x"])
        mcp1 = Path(flags1[1]).resolve()
        self.assertTrue(mcp1.exists())
        self.write(self.home / ".claude.json", {"mcpServers": {"srv": {"command": "v2", "env": {"TOKEN": "s2"}}}})
        flags2 = helper.shared_mcp_arguments(first, ["--settings", "x"])
        mcp2 = Path(flags2[1]).resolve()
        # A terminal started with the first file may still be running and read
        # it again: a recently used launch file is kept.
        self.assertTrue(mcp1.exists())
        self.assertTrue(mcp2.exists())
        aged = time.time() - helper.MCP_LAUNCH_FILE_MAX_AGE - 60
        os.utime(mcp1, (aged, aged))
        os.utime(mcp2, (aged, aged))
        # Reusing a file marks it as used again; only the unused old one goes.
        self.assertEqual(Path(helper.shared_mcp_arguments(first, ["--settings", "x"])[1]).resolve(), mcp2)
        self.assertFalse(mcp1.exists())
        self.assertTrue(mcp2.exists())
        self.assertGreater(mcp2.stat().st_mtime, aged + 60)
        self.write(self.home / ".claude.json", {"mcpServers": {}})
        helper.shared_mcp_arguments(first, ["--settings", "x"])
        self.assertEqual(list((first / "launch-settings").glob("mcp-*.json")), [mcp2])
        # A terminal running for longer than that, started with the alias path
        # the CLI receives, still holds on to its file.
        running = subprocess.Popen([sys.executable, "-c", "import time; time.sleep(60)", "--mcp-config", flags2[1]])
        self.addCleanup(running.wait)
        self.addCleanup(running.kill)
        time.sleep(0.2)
        os.utime(mcp2, (aged, aged))
        helper.shared_mcp_arguments(first, ["--settings", "x"])
        self.assertEqual(list((first / "launch-settings").glob("mcp-*.json")), [mcp2])
        running.kill()
        running.wait()
        helper.shared_mcp_arguments(first, ["--settings", "x"])
        self.assertEqual(list((first / "launch-settings").glob("mcp-*.json")), [])
        # Aborted replace_json (current != original) leaks zero backups.
        settings_path = config / "settings.json"
        settings_path.write_text('{"a":1}\n', encoding="utf-8")
        backup_dir = helper.shared_root() / "backups" / FIRST
        before_backups = set(backup_dir.glob("settings.json-*")) if backup_dir.exists() else set()
        self.assertFalse(helper.replace_json(settings_path, {"a": 2}, b'{"a":0}\n', backup_dir / "settings.json"))
        after_backups = set(backup_dir.glob("settings.json-*")) if backup_dir.exists() else set()
        self.assertEqual(before_backups, after_backups)
        # Unique backup filenames under identical time_ns().
        with patch.object(helper.time, "time_ns", return_value=1_700_000_000_000_000_000):
            b1 = helper.write_backup_bytes(backup_dir / "collision.json", b"one")
            b2 = helper.write_backup_bytes(backup_dir / "collision.json", b"two")
        self.assertNotEqual(b1, b2)
        self.assertEqual((b1.read_bytes(), b2.read_bytes()), (b"one", b"two"))
        # share_plugin_folders rejects non-dir shared cache even when local cache is absent,
        # and rejects overlapping marketplace when source metadata is missing.
        second, other = self.profile(SECOND)
        (self.common / "plugins/cache").write_text("not a dir", encoding="utf-8")
        self.assertFalse(helper.share_plugin_folders(second, helper.shared_root()))
        (self.common / "plugins/cache").unlink()
        (self.common / "plugins/marketplaces/mystery").mkdir(parents=True)
        (other / "plugins/marketplaces/mystery").mkdir(parents=True)
        self.assertFalse(helper.share_plugin_folders(second, helper.shared_root()))
        shutil.rmtree(self.common / "plugins/marketplaces/mystery")
        shutil.rmtree(other / "plugins/marketplaces/mystery")
        # heal_shared_file never destroys a user dotfiles symlink at ~/.claude/CLAUDE.md,
        # and Desktop isolates a conflicting CLAUDE.md so settings/MCP still sync.
        dotfile = self.home / "dotfiles-CLAUDE.md"
        dotfile.write_text("dotfile", encoding="utf-8")
        (self.common / "CLAUDE.md").symlink_to(dotfile)
        (config / "CLAUDE.md").write_text("different local", encoding="utf-8")
        self.write(self.common / "settings.json", {"theme": "dark"})
        self.write(self.home / ".claude.json", {"mcpServers": {"notes": {"command": "srv"}}})
        res = self.share(FIRST)
        self.assertIn("同名冲突", res.stderr)
        self.assertTrue((self.common / "CLAUDE.md").is_symlink())
        self.assertEqual(dotfile.read_text(encoding="utf-8"), "dotfile")
        # Despite the CLAUDE.md conflict, Desktop still synced settings and MCP!
        self.assertEqual(self.read(config / "settings.json").get("theme"), "dark")
        self.assertIn("notes", self.read(config / ".claude.json")["mcpServers"])
        # A terminal isolates a conflicting AGENTS.md too: the healable CLAUDE.md
        # still folds back, and both versions of AGENTS.md stay untouched.
        (self.common / "CLAUDE.md").unlink()
        (self.common / "CLAUDE.md").write_text("v1", encoding="utf-8")
        (self.common / "AGENTS.md").write_text("shared-agents", encoding="utf-8")
        third = "4" * 24
        base3, cfg3 = self.profile(third)
        helper.shared_profile_resources(base3, desktop=False)
        (cfg3 / "CLAUDE.md").unlink()
        (cfg3 / "CLAUDE.md").write_text("v2-edited", encoding="utf-8")
        (cfg3 / "AGENTS.md").unlink()
        (cfg3 / "AGENTS.md").write_text("conflicting-agents", encoding="utf-8")
        (self.common / "AGENTS.md").write_text("other-agents", encoding="utf-8")
        stderr = io.StringIO()
        with contextlib.redirect_stderr(stderr):
            helper.shared_profile_resources(base3, desktop=False)
        self.assertIn("AGENTS.md", stderr.getvalue())
        self.assertEqual((self.common / "CLAUDE.md").read_text(encoding="utf-8"), "v2-edited")
        self.assertTrue((cfg3 / "CLAUDE.md").is_symlink())
        self.assertEqual((cfg3 / "AGENTS.md").read_text(encoding="utf-8"), "conflicting-agents")
        self.assertEqual((self.common / "AGENTS.md").read_text(encoding="utf-8"), "other-agents")

    def test_volatile_last_updated_merges_memory_rejects_symlinks_and_statusline_hardening(self):
        first, config = self.profile(FIRST)
        second, other = self.profile(SECOND)
        self.install(self.common / "plugins", "team", "example/team", "lint")
        self.share(FIRST)
        self.share(SECOND)
        # Both accounts independently auto-refresh lastUpdated to different timestamps.
        k1 = self.read(config / "plugins/known_marketplaces.json")
        k1["team"]["lastUpdated"] = "2026-11-10T00:00:00.000Z"
        self.write(config / "plugins/known_marketplaces.json", k1)
        k2 = self.read(other / "plugins/known_marketplaces.json")
        k2["team"]["lastUpdated"] = "2026-11-18T00:00:00.000Z"
        self.write(other / "plugins/known_marketplaces.json", k2)
        self.share(SECOND)
        self.share(FIRST)
        self.assertEqual(self.read(config / "plugins/known_marketplaces.json")["team"]["lastUpdated"], "2026-11-18T00:00:00.000Z")
        self.assertEqual(self.read(self.common / "plugins/known_marketplaces.json")["team"]["lastUpdated"], "2026-11-18T00:00:00.000Z")
        # link_project_memory rejects symlinks inside own memory directory.
        own_mem = config / "projects/p-sym/memory"
        own_mem.mkdir(parents=True)
        (own_mem / "evil.md").symlink_to(self.home / "outside.txt")
        shared_mem = self.common / "projects/p-sym/memory"
        shared_mem.mkdir(parents=True)
        with self.assertRaisesRegex(helper.SharedResourceConflict, "异常链接"):
            helper.link_project_memory(own_mem, shared_mem, helper.shared_root() / "backups" / FIRST / "projects/p-sym/memory")
        # has_status_line checks common ~/.claude/settings.json even when cwd is outside HOME.
        self.write(self.common / "settings.json", {"statusLine": None})
        outside_cwd = Path("/tmp")
        with patch.object(helper.Path, "cwd", return_value=outside_cwd):
            self.assertTrue(helper.has_status_line(first))
        # statusline mode tolerates malformed and non-dict JSON on stdin without crashing.
        self.write(self.common / "settings.json", {})
        for bad_input in ("not-json", "[1, 2, 3]", "null"):
            shown = subprocess.run([sys.executable, str(source), "statusline", FIRST], env=self.env, cwd=self.home,
                                   input=bad_input, text=True, capture_output=True)
            self.assertEqual(shown.returncode, 0, shown.stderr)
            self.assertEqual(shown.stdout.strip(), "Claude")


    def two_plugins_shared_by_two_accounts(self):
        self.install(self.common / "plugins", "team", "example/team", "lint")
        self.install(self.common / "plugins", "team", "example/team", "fmt")
        self.write(self.common / "settings.json", {"enabledPlugins": {"lint@team": True, "fmt@team": True},
                                                   "extraKnownMarketplaces": {"team": {"source": {"source": "github", "repo": "example/team"}}}})
        first, config = self.profile(FIRST)
        second, other = self.profile(SECOND)
        self.share(FIRST)
        self.share(SECOND)
        both = ["fmt@team", "lint@team"]
        for root in (self.common, config, other):
            self.assertEqual(sorted(self.read(root / "plugins/installed_plugins.json")["plugins"]), both)
            self.assertEqual(sorted(self.read(root / "settings.json")["enabledPlugins"]), both)
        return config, other, both

    def cli_uninstall(self, root, plugin_id):
        """What `claude plugin uninstall` leaves behind (measured with CLI 2.1.314)."""
        installed = self.read(root / "plugins/installed_plugins.json")
        for entry in installed["plugins"].pop(plugin_id):
            (Path(entry["installPath"]) / ".orphaned_at").write_text("1795163578718")
        self.write(root / "plugins/installed_plugins.json", installed)
        settings = self.read(root / "settings.json")
        settings.get("enabledPlugins", {}).pop(plugin_id, None)
        self.write(root / "settings.json", settings)

    def cli_remove_marketplace(self, root, marketplace):
        """`claude plugin marketplace remove`: its plugins uninstalled, both lists emptied of it."""
        for plugin_id in list(self.read(root / "plugins/installed_plugins.json")["plugins"]):
            if plugin_id.endswith("@" + marketplace):
                self.cli_uninstall(root, plugin_id)
        known = self.read(root / "plugins/known_marketplaces.json")
        known.pop(marketplace)
        self.write(root / "plugins/known_marketplaces.json", known)
        settings = self.read(root / "settings.json")
        settings.setdefault("extraKnownMarketplaces", {}).pop(marketplace, None)
        self.write(root / "settings.json", settings)

    def assert_everyone_has(self, roots, both):
        for root in roots:
            self.assertEqual(sorted(self.read(root / "plugins/installed_plugins.json")["plugins"]), both, root)
            self.assertEqual(sorted(self.read(root / "plugins/known_marketplaces.json")), ["team"], root)
            self.assertEqual(sorted(self.read(root / "settings.json")["enabledPlugins"]), both, root)

    def test_missing_plugin_registries_are_restored_instead_of_uninstalling_everywhere(self):
        config, other, both = self.two_plugins_shared_by_two_accounts()
        # The account's plugins folder was reset: both registries are gone.
        (config / "plugins/installed_plugins.json").unlink()
        (config / "plugins/known_marketplaces.json").unlink()
        self.share(FIRST)
        self.share(SECOND)
        self.assert_everyone_has((self.common, config, other), both)
        # The shared registries go missing: rebuilt from the account, nothing removed.
        (self.common / "plugins/installed_plugins.json").unlink()
        (self.common / "plugins/known_marketplaces.json").unlink()
        self.share(SECOND)
        self.share(FIRST)
        self.assert_everyone_has((self.common, config, other), both)
        # A single plugin, its registry lost: still no uninstall.
        self.cli_uninstall(other, "fmt@team")
        self.share(SECOND)
        self.share(FIRST)
        for root in (self.common, config, other):
            self.assertEqual(sorted(self.read(root / "plugins/installed_plugins.json")["plugins"]), ["lint@team"])
        (config / "plugins/installed_plugins.json").unlink()
        self.share(FIRST)
        self.share(SECOND)
        for root in (self.common, config, other):
            self.assertEqual(sorted(self.read(root / "plugins/installed_plugins.json")["plugins"]), ["lint@team"])
        # Nor is a lost settings file a reason to disable its one plugin.
        (config / "settings.json").unlink()
        self.share(FIRST)
        self.share(SECOND)
        for root in (self.common, config, other):
            self.assertEqual(self.read(root / "settings.json")["enabledPlugins"], {"lint@team": True})
        # An emptied marketplace list while its plugin stays installed and the
        # settings still declare it: no `marketplace remove` happened.
        self.write(config / "plugins/known_marketplaces.json", {})
        self.assertIn("插件市场登记", self.share(FIRST).stderr)
        self.share(SECOND)
        for root in (self.common, config, other):
            self.assertEqual(sorted(self.read(root / "plugins/known_marketplaces.json")), ["team"])

    def test_each_uninstall_trace_alone_lets_the_removal_spread(self):
        config, other, both = self.two_plugins_shared_by_two_accounts()
        # Only the .orphaned_at marker (the enabled entry left behind).
        installed = self.read(config / "plugins/installed_plugins.json")
        for entry in installed["plugins"].pop("fmt@team"):
            (Path(entry["installPath"]) / ".orphaned_at").write_text("1795163578718")
        self.write(config / "plugins/installed_plugins.json", installed)
        self.assertNotIn("再移除一次", self.share(FIRST).stderr)
        self.assertEqual(sorted(self.read(self.common / "plugins/installed_plugins.json")["plugins"]), ["lint@team"])
        # Only the enabled entry gone (no marker written).
        installed = self.read(other / "plugins/installed_plugins.json")
        del installed["plugins"]["lint@team"]
        self.write(other / "plugins/installed_plugins.json", installed)
        settings = self.read(other / "settings.json")
        del settings["enabledPlugins"]["lint@team"]
        self.write(other / "settings.json", settings)
        self.assertNotIn("再移除一次", self.share(SECOND).stderr)
        self.assertEqual(self.read(self.common / "plugins/installed_plugins.json")["plugins"], {})

    def test_cli_uninstalls_spread_even_all_at_once_while_resets_are_held_until_repeated(self):
        config, other, both = self.two_plugins_shared_by_two_accounts()
        # A registry rewritten without its structure and no uninstall traces.
        self.write(config / "plugins/installed_plugins.json", {})
        result = self.share(FIRST)
        self.assertIn("已安装插件登记", result.stderr)
        self.assertIn("再移除一次", result.stderr)
        self.share(SECOND)
        self.assert_everyone_has((self.common, config, other), both)
        self.assertEqual(self.read(config / "plugins/installed_plugins.json")["version"], 2)
        # The workbench lists the event beside the account.
        listed = json.loads(subprocess.run([sys.executable, str(source), "notices", FIRST, SECOND], env=self.env,
                                           cwd=self.home, capture_output=True, text=True, check=True).stdout)
        self.assertEqual([entry["kind"] for entry in listed[FIRST]], ["removal_blocked"])
        self.assertNotIn(SECOND, listed)
        # Settings reset to {} or deleted: plugins stay enabled everywhere.
        self.write(other / "settings.json", {})
        self.share(SECOND)
        self.share(FIRST)
        self.assert_everyone_has((self.common, config, other), both)
        # The enabled list emptied by hand while both stay installed: held.
        settings = self.read(config / "settings.json")
        settings["enabledPlugins"] = {}
        self.write(config / "settings.json", settings)
        self.assertIn("插件启用设置", self.share(FIRST).stderr)
        self.assert_everyone_has((self.common, config, other), both)
        # The same removal made again is the user's choice and spreads.
        self.write(config / "settings.json", settings)
        self.share(FIRST)
        self.share(SECOND)
        for root in (self.common, config, other):
            self.assertEqual(self.read(root / "settings.json")["enabledPlugins"], {})
        settings["enabledPlugins"] = {"lint@team": True, "fmt@team": True}
        for root in (self.common, config, other):
            data = self.read(root / "settings.json")
            data["enabledPlugins"] = dict(settings["enabledPlugins"])
            self.write(root / "settings.json", data)
        self.share(FIRST)
        self.share(SECOND)
        # Uninstalling every plugin with the CLI spreads at once, no question asked.
        self.cli_uninstall(config, "lint@team")
        self.cli_uninstall(config, "fmt@team")
        result = self.share(FIRST)
        self.assertNotIn("再移除一次", result.stderr)
        self.share(SECOND)
        for root in (self.common, config, other):
            self.assertEqual(self.read(root / "plugins/installed_plugins.json")["plugins"], {})
            self.assertEqual(self.read(root / "settings.json")["enabledPlugins"], {})
        # Removing the marketplace with the CLI spreads too.
        self.cli_remove_marketplace(other, "team")
        self.share(SECOND)
        self.share(FIRST)
        for root in (self.common, config, other):
            self.assertEqual(self.read(root / "plugins/known_marketplaces.json"), {})
            self.assertEqual(self.read(root / "settings.json")["extraKnownMarketplaces"], {})

    def test_an_older_snapshot_does_not_remove_a_plugin_its_account_never_saw(self):
        self.install(self.common / "plugins", "team", "example/team", "lint")
        first, config = self.profile(FIRST)
        second, other = self.profile(SECOND)
        self.share(FIRST)
        self.share(SECOND)
        snapshot = (config / "plugins/installed_plugins.json").read_text()
        # The second account installs fmt; the first account's running CLI
        # then saves the registry it loaded before that install.
        self.install(other / "plugins", "team", "example/team", "fmt")
        self.share(SECOND)
        (config / "plugins/installed_plugins.json").write_text(snapshot)
        self.share(FIRST)
        for root in (self.common, config, other):
            self.assertIn("fmt@team", self.read(root / "plugins/installed_plugins.json")["plugins"])

    def test_a_stale_save_after_a_later_sync_is_brought_up_to_date_not_spread(self):
        # The reviewer's M2: the account already synchronized the new plugin
        # before its long-running CLI saved the registry it had loaded earlier.
        self.install(self.common / "plugins", "team", "example/team", "lint")
        first, config = self.profile(FIRST)
        second, other = self.profile(SECOND)
        self.share(FIRST)
        self.share(SECOND)
        old = self.read(config / "plugins/installed_plugins.json")
        self.install(other / "plugins", "team", "example/team", "fmt")
        self.share(SECOND)
        self.share(FIRST)
        self.assertIn("fmt@team", self.read(config / "plugins/installed_plugins.json")["plugins"])
        for _ in range(2):
            # Saved twice: a repeat of a stale save never counts as confirmation.
            self.write(config / "plugins/installed_plugins.json", old)
            result = self.share(FIRST)
            self.assertNotIn("再移除一次", result.stderr)
            self.share(SECOND)
            for root in (self.common, config, other):
                self.assertIn("fmt@team", self.read(root / "plugins/installed_plugins.json")["plugins"], root)

    # The helper runs in the Linux guest and finds running CLIs through /proc,
    # which a macOS host (where CI also runs this file) does not have.
    @unittest.skipUnless(Path("/proc/stat").exists(), "needs /proc to see the running CLI")
    def test_entries_newer_than_a_running_cli_are_not_removed_by_its_save(self):
        self.install(self.common / "plugins", "team", "example/team", "lint")
        first, config = self.profile(FIRST)
        second, other = self.profile(SECOND)
        self.share(FIRST)
        self.share(SECOND)
        # A CLI of the first account is running since before the next install.
        running = subprocess.Popen(["sleep", "60"], env={**self.env, "CLAUDE_CONFIG_DIR": str(config)})
        self.addCleanup(running.wait)
        self.addCleanup(running.kill)
        loaded = self.read(config / "plugins/installed_plugins.json")
        time.sleep(0.2)
        self.install(other / "plugins", "team", "example/team", "fmt")
        self.share(SECOND)
        self.share(FIRST)
        # It saves its own state: what it loaded plus a change of its own.
        loaded["plugins"]["lint@team"][0]["gitCommitSha"] = "0" * 40
        self.write(config / "plugins/installed_plugins.json", loaded)
        result = self.share(FIRST)
        self.assertNotIn("再移除一次", result.stderr)
        for root in (self.common, config, other):
            self.assertIn("fmt@team", self.read(root / "plugins/installed_plugins.json")["plugins"], root)
        self.assertEqual(self.read(self.common / "plugins/installed_plugins.json")["plugins"]["lint@team"][0]["gitCommitSha"], "0" * 40)

    def test_unreadable_registry_is_set_aside_and_rebuilt(self):
        self.install(self.common / "plugins", "team", "example/team", "lint")
        first, config = self.profile(FIRST)
        self.share(FIRST)
        (config / "plugins/installed_plugins.json").write_text("{ broken")
        result = self.share(FIRST)
        self.assertIn("无法读取", result.stderr)
        self.assertEqual(sorted(self.read(config / "plugins/installed_plugins.json")["plugins"]), ["lint@team"])
        # The rebuilt registry uses this account's own spelling of the plugins folder.
        self.assertTrue(self.read(config / "plugins/installed_plugins.json")["plugins"]["lint@team"][0]["installPath"]
                        .startswith(tuple(helper.plugin_root_forms(config))))
        kept = list((self.home / ".local/share/cage/claude-shared/backups" / FIRST / "plugins").glob("installed_plugins.json.corrupt-*"))
        self.assertEqual([path.read_text() for path in kept], ["{ broken"])
        self.assertEqual(sorted(self.read(self.common / "plugins/installed_plugins.json")["plugins"]), ["lint@team"])
        # The shared one, too, is rebuilt from the account.
        (self.common / "plugins/installed_plugins.json").write_text("[]")
        self.share(FIRST)
        self.assertEqual(sorted(self.read(self.common / "plugins/installed_plugins.json")["plugins"]), ["lint@team"])
        self.assertTrue(list((self.home / ".local/share/cage/claude-shared/backups/common/plugins").glob("installed_plugins.json.corrupt-*")))

    def test_old_pending_records_without_digest_claim_only_values_equal_to_the_shared_ones(self):
        self.write(self.common / "settings.json", {"language": "zh", "theme": "dark", "model": "sonnet"})
        self.write(self.home / ".claude.json", {"firstStartTime": "x", "mcpServers": {"notes": {"command": "srv"}, "own": {"command": "shared"}}})
        first, config = self.profile(FIRST)
        # model equals the old intent but no longer the shared value: not claimed.
        self.write(config / "settings.json", {"language": "zh", "theme": "light", "model": "opus"})
        self.write(config / ".claude.json", {"mcpServers": {"notes": {"command": "srv"}, "own": {"command": "mine"}}})
        helper.write_record(first, {"pending": {"keys": {"language": "zh", "theme": "dark", "model": "opus"}, "hooks": {}, "hooks_created": False},
                                    "pending_mcp": {"notes": {"command": "srv"}, "own": {"command": "shared"}}})
        self.share(FIRST)
        record = helper.read_record(first)
        self.assertEqual(record["keys"], {"language": "zh"})
        self.assertEqual(record["mcp"], {"notes": {"command": "srv"}})
        self.assertNotIn("pending", record)
        # Claimed: follows the shared value away. Not claimed: stays the user's.
        self.write(self.common / "settings.json", {"theme": "dark"})
        self.write(self.home / ".claude.json", {"firstStartTime": "x", "mcpServers": {}})
        self.share(FIRST)
        self.assertEqual(self.read(config / "settings.json"), {"theme": "light", "model": "opus"})
        self.assertEqual(self.read(config / ".claude.json")["mcpServers"], {"own": {"command": "mine"}})

    def memory_fixture(self):
        first, config = self.profile(FIRST)
        project = self.home / "work/demo"
        project.mkdir(parents=True)
        key = helper.project_key(project.resolve())
        own, shared = config / "projects" / key / "memory", self.common / "projects" / key / "memory"
        own.mkdir(parents=True)
        shared.mkdir(parents=True)
        return first, project, key, own, shared

    def launch_in(self, first, project):
        stderr = io.StringIO()
        with patch.object(helper.Path, "cwd", return_value=project), contextlib.redirect_stderr(stderr):
            flags = helper.shared_launch_arguments(first, [])
        return json.loads(Path(flags[1]).read_text()), stderr.getvalue()

    def test_differing_project_memory_merges_the_index_and_keeps_both_notes(self):
        first, project, key, own, shared = self.memory_fixture()
        (own / "MEMORY.md").write_text("- [A](a.md) — account\n- [Same](same.md) — both\n")
        (own / "a.md").write_text("only in the account")
        (own / "same.md").write_text("account version")
        (shared / "MEMORY.md").write_text("- [Same](same.md) — both\n- [B](b.md) — shared")
        (shared / "b.md").write_text("only shared")
        (shared / "same.md").write_text("shared version")
        settings, stderr = self.launch_in(first, project)
        self.assertIn("自动合并", stderr)
        self.assertIn("autoMemoryDirectory", settings)
        self.assertTrue(own.is_symlink())
        renamed = list(shared.glob("same-*.md"))
        self.assertEqual([path.read_text() for path in renamed], ["account version"])
        self.assertEqual((shared / "same.md").read_text(), "shared version")
        self.assertEqual((shared / "a.md").read_text(), "only in the account")
        self.assertEqual((shared / "MEMORY.md").read_text(),
                         "- [Same](same.md) — both\n- [B](b.md) — shared\n- [A](a.md) — account\n"
                         f"- [Same]({renamed[0].name}) — both\n")
        # Both originals are kept in the backups.
        backups = self.home / ".local/share/cage/claude-shared/backups"
        self.assertEqual([(path / "same.md").read_text() for path in (backups / FIRST / "projects" / key).glob("memory-*")], ["account version"])
        self.assertEqual([path.read_text() for path in (backups / "common/projects" / key / "memory").glob("MEMORY.md-*")],
                         ["- [Same](same.md) — both\n- [B](b.md) — shared"])
        listed = helper.read_notices(FIRST)
        self.assertEqual([entry["kind"] for entry in listed], ["memory_merged"])

    def test_unmergeable_project_memory_launches_with_the_accounts_own(self):
        first, project, key, own, shared = self.memory_fixture()
        (own / "topic").write_text("a note named like the shared folder")
        (shared / "topic").mkdir()
        (shared / "topic/x.md").write_text("shared note")
        settings, stderr = self.launch_in(first, project)
        self.assertIn("项目记忆冲突", stderr)
        self.assertNotIn("autoMemoryDirectory", settings)
        self.assertFalse(own.is_symlink())
        self.assertEqual((own / "topic").read_text(), "a note named like the shared folder")
        self.assertEqual((shared / "topic/x.md").read_text(), "shared note")
        self.assertEqual([entry["kind"] for entry in helper.read_notices(FIRST)], ["memory_conflict"])
        # Not repeated on the next launch; Desktop reports it too, and the
        # notice is cleared once the user resolves it.
        self.assertNotIn("项目记忆冲突", self.launch_in(first, project)[1])
        self.share(FIRST)
        self.assertEqual([entry["kind"] for entry in helper.read_notices(FIRST)], ["memory_conflict"])
        (own / "topic").unlink()
        settings, _ = self.launch_in(first, project)
        self.assertIn("autoMemoryDirectory", settings)
        self.assertTrue(own.is_symlink())
        self.assertEqual(helper.read_notices(FIRST), [])

    def test_memory_links_inside_are_skipped_and_links_outside_refuse(self):
        first, project, key, own, shared = self.memory_fixture()
        (own / "note.md").write_text("note")
        (own / "alias.md").symlink_to(own / "note.md")
        settings, stderr = self.launch_in(first, project)
        self.assertNotIn("autoMemoryDirectory", settings)
        self.assertTrue((own / "alias.md").is_symlink())
        (own / "alias.md").unlink()
        (own / "alias.md").symlink_to(self.home / "outside.md")
        with patch.object(helper.Path, "cwd", return_value=project), self.assertRaisesRegex(helper.SharedResourceConflict, "异常链接"):
            helper.shared_launch_arguments(first, [])

    def test_interrupted_settings_intent_claims_only_the_file_it_was_about_to_write(self):
        self.write(self.common / "settings.json", {"theme": "dark", "language": "zh"})
        first, config = self.profile(FIRST)
        self.write(config / "settings.json", {"theme": "light"})
        real = helper.replace_json
        def killed(path, *rest):
            if path == config / "settings.json":
                raise KeyboardInterrupt("killed after recording the intent")
            return real(path, *rest)
        with patch.object(helper, "replace_json", killed), self.assertRaises(KeyboardInterrupt):
            helper.shared_profile_resources(first, desktop=True)
        self.assertEqual(helper.read_record(first)["pending"]["keys"], {"language": "zh"})
        # The write never happened; the user then sets the same value by hand.
        self.write(config / "settings.json", {"theme": "light", "language": "zh"})
        self.write(self.common / "settings.json", {"theme": "dark"})
        self.share(FIRST)
        self.assertEqual(self.read(config / "settings.json"), {"theme": "light", "language": "zh"})
        self.assertNotIn("pending", helper.read_record(first))
        self.assertEqual(helper.read_record(first).get("keys"), {})
        # The same for an MCP server addition.
        self.write(self.home / ".claude.json", {"mcpServers": {"notes": {"command": "srv"}}})
        def killed_mcp(path, *rest):
            if path == config / ".claude.json":
                raise KeyboardInterrupt("killed after recording the intent")
            return real(path, *rest)
        with patch.object(helper, "replace_json", killed_mcp), self.assertRaises(KeyboardInterrupt):
            helper.shared_profile_resources(first, desktop=True)
        self.assertEqual(helper.read_record(first)["pending_mcp"]["servers"], {"notes": {"command": "srv"}})
        self.write(config / ".claude.json", {"mcpServers": {"notes": {"command": "srv"}}})
        self.write(self.home / ".claude.json", {"mcpServers": {}})
        self.share(FIRST)
        self.assertEqual(self.read(config / ".claude.json")["mcpServers"], {"notes": {"command": "srv"}})

    def test_a_value_the_account_already_had_is_never_withdrawn(self):
        first, config = self.profile(FIRST)
        self.write(config / "settings.json", {"model": "opus"})
        self.write(self.common / "settings.json", {"model": "opus", "theme": "dark"})
        self.share(FIRST)
        self.assertEqual(helper.read_record(first)["keys"], {"theme": "dark"})
        self.write(self.common / "settings.json", {})
        self.share(FIRST)
        self.assertEqual(self.read(config / "settings.json"), {"model": "opus"})

    def test_unknown_or_emptied_shared_configuration_keeps_earlier_copies(self):
        first, config = self.profile(FIRST)
        servers = {"notes": {"command": "notes-srv"}, "search": {"command": "search-srv"}}
        cli_fields = {"firstStartTime": "x", "machineID": "x"}
        self.write(self.home / ".claude.json", {**cli_fields, "mcpServers": {"notes": servers["notes"]}})
        self.write(self.common / "settings.json", {"theme": "dark"})
        self.share(FIRST)
        self.assertEqual(self.read(config / ".claude.json")["mcpServers"], {"notes": servers["notes"]})
        # Missing, unreadable, or without a server list: nothing is withdrawn.
        for state in (None, "{ broken", {"oauthAccount": {}}):
            if state is None:
                (self.home / ".claude.json").unlink()
            else:
                self.write(self.home / ".claude.json", state)
            self.share(FIRST)
            self.assertEqual(self.read(config / ".claude.json")["mcpServers"], {"notes": servers["notes"]})
        self.write(self.home / ".claude.json", {**cli_fields, "mcpServers": servers})
        self.share(FIRST)
        self.assertEqual(self.read(config / ".claude.json")["mcpServers"], servers)
        # A file holding nothing but an empty server list was not written by
        # `claude mcp remove`: the copies stay, and the user is told.
        self.write(self.home / ".claude.json", {"mcpServers": {}})
        self.assertIn("用户级 MCP", self.share(FIRST).stderr)
        self.assertEqual(self.read(config / ".claude.json")["mcpServers"], servers)
        # Still the same on the next launch: taken as the user's choice.
        self.share(FIRST)
        self.assertEqual(self.read(config / ".claude.json")["mcpServers"], {})
        # `claude mcp remove` of every server keeps the CLI's other fields: withdrawn at once.
        servers = {"alpha": {"command": "alpha-srv"}, "beta": {"command": "beta-srv"}}
        self.write(self.home / ".claude.json", {**cli_fields, "mcpServers": servers})
        self.share(FIRST)
        self.assertEqual(self.read(config / ".claude.json")["mcpServers"], servers)
        self.write(self.home / ".claude.json", {**cli_fields, "mcpServers": {}})
        self.assertNotIn("用户级 MCP", self.share(FIRST).stderr)
        self.assertEqual(self.read(config / ".claude.json")["mcpServers"], {})
        # A missing shared settings file keeps the copied settings too.
        (self.common / "settings.json").unlink()
        self.share(FIRST)
        self.assertEqual(self.read(config / "settings.json"), {"theme": "dark"})
        self.write(self.common / "settings.json", {})
        self.share(FIRST)
        self.assertEqual(self.read(config / "settings.json"), {})

def numpy_base36(value):
    digits = ""
    while True:
        value, rest = divmod(value, 36)
        digits = "0123456789abcdefghijklmnopqrstuvwxyz"[rest] + digits
        if not value:
            return digits


if __name__ == "__main__":
    unittest.main()
