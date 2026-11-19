import hashlib
import contextlib
import io
from datetime import datetime, timezone
import importlib.util
import json
import os
import runpy
import shlex
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
                    "CAGE_CLAUDE_SUBJECT": self.subject, "PYTHONDONTWRITEBYTECODE": "1", "TMPDIR": str(self.home)}

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

    def test_memory_alias_conflict_preserves_both_versions_and_explains_failure(self):
        project, alias, target = self.replaced_memory_alias()
        (alias / "existing.md").write_text("changed memory")
        result = self.run_helper("run", FIRST, cwd=project, check=False)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("记忆", result.stderr)
        self.assertIn("冲突", result.stderr)
        self.assertNotIn("登录状态", result.stderr)
        self.assertNotIn(str(self.home), result.stderr)
        self.assertEqual((target / "existing.md").read_text(), "original memory")
        self.assertEqual((alias / "existing.md").read_text(), "changed memory")
        self.assertFalse(alias.is_symlink())

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
            with self.assertRaises(helper.SharedResourceConflict):
                helper.shared_launch_arguments(helper.profile(FIRST), [])
        self.assertEqual((target / "new.md").read_text(), "concurrent memory")
        self.assertEqual((alias / "new.md").read_text(), "new memory")
        self.assertFalse(alias.is_symlink())

    def test_launch_errors_are_specific_and_do_not_expose_private_details(self):
        private = "private/path credential=secret"
        cases = [
            (helper.SubscriptionStatusTimeout(private), "读取 Claude 订阅状态超时"),
            (helper.SubscriptionStatusError(private), "无法读取 Claude 订阅状态"),
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

    def test_conflicting_shared_resource_preserves_both_versions(self):
        with patch.dict(os.environ,self.env,clear=True):
            base=helper.profile(FIRST);config=helper.config_directory(base);config.mkdir(parents=True)
            common=self.home/".claude";common.mkdir()
            (common/"CLAUDE.md").write_text("global")
            (config/"CLAUDE.md").write_text("account")
            (config/"commands").mkdir()
            (config/"commands/keep.md").write_text("retain the original layout on conflict")
            with self.assertRaises(ValueError):helper.shared_profile_resources(base)
            self.assertEqual((common/"CLAUDE.md").read_text(),"global")
            self.assertEqual((config/"CLAUDE.md").read_text(),"account")
            self.assertFalse((config/"commands").is_symlink())
            self.assertEqual((config/"commands/keep.md").read_text(),"retain the original layout on conflict")
            failure=self.run_helper("run",FIRST,check=False)
            self.assertIn("同名冲突",failure.stderr)

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
            backup=self.home/".local/share/cage/claude-shared/backups"/FIRST/"projects"/key/"memory/MEMORY.md"
            self.assertEqual(backup.read_text(),"account learning")
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


if __name__ == "__main__":
    unittest.main()
