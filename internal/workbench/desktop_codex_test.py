import importlib.util
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
import uuid

spec = importlib.util.spec_from_file_location("adapter", Path(__file__).with_name("desktop_codex.py"))
adapter = importlib.util.module_from_spec(spec)
spec.loader.exec_module(adapter)


class DesktopConnectorTests(unittest.TestCase):
    def test_register_only_active_desktop_account_in_exact_project(self):
        with tempfile.TemporaryDirectory() as folder, patch.dict(os.environ, {"HOME": folder}):
            home = Path(folder).resolve()
            project = str(home / "project")
            grant = str(uuid.uuid4())
            session = str(uuid.uuid4())
            def live_record(config, entrypoint, cwd):
                directory = config / "sessions"
                directory.mkdir(parents=True, exist_ok=True)
                (directory / f"{os.getpid()}.json").write_text(json.dumps({
                    "pid": os.getpid(), "sessionId": session, "cwd": cwd,
                    "entrypoint": entrypoint, "kind": "interactive"}))
            account = "a" * 24
            config = home / ".local/share/cage/claude-accounts" / account / "config"
            live_record(home / ".claude", "claude-desktop", project)
            live_record(config, "cli", project)
            with patch.object(adapter, "register", return_value=True) as add:
                adapter.sync({account: {project: grant}})
                add.assert_not_called()
                live_record(config, "claude-desktop", project + "-other")
                adapter.sync({account: {project: grant}})
                add.assert_not_called()
                live_record(config, "claude-desktop", project)
                adapter.sync({account: {project: grant}})
                add.assert_called_once_with(config.resolve(), project, grant)

    def test_keep_user_connector(self):
        with tempfile.TemporaryDirectory() as folder:
            config = Path(folder)
            (config / ".claude.json").write_text(json.dumps({"projects": {"/project":{
                "mcpServers":{"codex":{"command":"user-owned"}}}}}))
            with patch.object(adapter.subprocess,"run") as run:
                self.assertFalse(adapter.register(config,"/project",str(uuid.uuid4())))
                run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
