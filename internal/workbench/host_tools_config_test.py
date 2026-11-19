"""Temporary fixtures only; never inspect the actual user's home."""
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("host_tools_config", Path(__file__).with_name("host_tools_config.py"))
config = importlib.util.module_from_spec(spec)
spec.loader.exec_module(config)

ENTRY = {"type": "http", "url": "http://127.0.0.1:18962/", "headers": {"Authorization": "Bearer t"}}


def ours(current):
    return isinstance(current, dict) and current.get("url") == ENTRY["url"]


class HostToolsConfigTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.home = Path(temporary.name)

    def test_every_profile_is_found(self):
        account = self.home / ".local/share/cage/claude-accounts/work/config"
        account.mkdir(parents=True)
        self.assertEqual(config.profile_files(self.home), [self.home / ".claude.json", account / ".claude.json"])

    def test_entry_is_added_beside_existing_settings(self):
        path = self.home / ".claude.json"
        path.write_text(json.dumps({"theme": "dark", "mcpServers": {"other": {"command": "x"}}}))
        path.chmod(0o640)
        self.assertTrue(config.add_server(path, "computer", ENTRY, ours))
        data = json.loads(path.read_text())
        self.assertEqual(data["theme"], "dark")
        self.assertEqual(data["mcpServers"], {"other": {"command": "x"}, "computer": ENTRY})
        self.assertEqual(path.stat().st_mode & 0o777, 0o640)

    def test_missing_file_is_created_private(self):
        path = self.home / ".claude.json"
        self.assertTrue(config.add_server(path, "computer", ENTRY, ours))
        self.assertEqual(json.loads(path.read_text()), {"mcpServers": {"computer": ENTRY}})
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)

    def test_unchanged_entry_is_not_rewritten(self):
        path = self.home / ".claude.json"
        config.add_server(path, "computer", ENTRY, ours)
        before = path.stat().st_ino
        self.assertFalse(config.add_server(path, "computer", ENTRY, ours))
        self.assertEqual(path.stat().st_ino, before)

    def test_new_token_replaces_the_old_entry(self):
        path = self.home / ".claude.json"
        config.add_server(path, "computer", ENTRY, ours)
        newer = dict(ENTRY, headers={"Authorization": "Bearer u"})
        self.assertTrue(config.add_server(path, "computer", newer, ours))
        self.assertEqual(json.loads(path.read_text())["mcpServers"]["computer"], newer)

    def test_the_users_own_server_of_that_name_is_kept(self):
        path = self.home / ".claude.json"
        own = {"type": "http", "url": "https://example.com/mcp"}
        path.write_text(json.dumps({"mcpServers": {"computer": own}}))
        self.assertFalse(config.add_server(path, "computer", ENTRY, ours))
        self.assertEqual(json.loads(path.read_text())["mcpServers"]["computer"], own)

    def test_unreadable_file_is_left_alone(self):
        path = self.home / ".claude.json"
        path.write_text("{not json")
        self.assertFalse(config.add_server(path, "computer", ENTRY, ours))
        self.assertEqual(path.read_text(), "{not json")
        self.assertEqual([p.name for p in self.home.iterdir()], [".claude.json"])

    def test_desktop_app_gets_a_relay_and_keeps_its_preferences(self):
        desktop = self.home / ".config/Claude"
        desktop.mkdir(parents=True)
        (desktop / "claude_desktop_config.json").write_text(json.dumps({"preferences": {"sidebarMode": "x"}}))
        config.configure(self.home, "computer", ENTRY, "# relay\n")
        config.configure(self.home, "computer", ENTRY, "# relay\n")
        program = self.home / ".local/share/mcp-relay/relay.py"
        endpoint = self.home / ".config/mcp-relay/computer.json"
        self.assertEqual(program.read_text(), "# relay\n")
        self.assertEqual(json.loads(endpoint.read_text()), {"url": ENTRY["url"], "headers": ENTRY["headers"]})
        self.assertEqual(endpoint.stat().st_mode & 0o777, 0o600)
        data = json.loads((desktop / "claude_desktop_config.json").read_text())
        self.assertEqual(data["preferences"], {"sidebarMode": "x"})
        self.assertEqual(data["mcpServers"]["computer"], {"command": "/usr/bin/python3", "args": [str(program), str(endpoint)]})
        self.assertNotIn("Bearer", (desktop / "claude_desktop_config.json").read_text())

    def test_without_the_desktop_app_nothing_is_installed_for_it(self):
        config.configure(self.home, "computer", ENTRY, "# relay\n")
        self.assertFalse((self.home / ".config").exists())
        self.assertFalse((self.home / ".local/share/mcp-relay").exists())
        self.assertIn("computer", json.loads((self.home / ".claude.json").read_text())["mcpServers"])

    def test_removal_takes_back_only_what_was_added(self):
        desktop = self.home / ".config/Claude"
        desktop.mkdir(parents=True)
        (desktop / "claude_desktop_config.json").write_text(json.dumps({"preferences": {"a": 1}}))
        code = self.home / ".claude.json"
        code.write_text(json.dumps({"theme": "dark", "mcpServers": {"other": {"command": "x"}}}))
        config.configure(self.home, "computer", ENTRY, "# relay\n")
        config.unconfigure(self.home, "computer", ENTRY)
        self.assertEqual(json.loads(code.read_text()), {"theme": "dark", "mcpServers": {"other": {"command": "x"}}})
        self.assertEqual(json.loads((desktop / "claude_desktop_config.json").read_text()), {"preferences": {"a": 1}})
        self.assertFalse((self.home / ".config/mcp-relay").exists())
        self.assertFalse((self.home / ".local/share/mcp-relay").exists())

    def test_removal_keeps_the_users_own_server_of_that_name(self):
        code = self.home / ".claude.json"
        own = {"type": "http", "url": "https://example.com/mcp"}
        code.write_text(json.dumps({"mcpServers": {"computer": own}}))
        config.unconfigure(self.home, "computer", ENTRY)
        self.assertEqual(json.loads(code.read_text())["mcpServers"]["computer"], own)


if __name__ == "__main__":
    unittest.main()
