import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from uuid import UUID


SOURCE = Path(__file__).resolve().parent
ACCOUNT, OTHER, ORGANIZATION, OTHER_ORG = (str(UUID(int=n)) for n in range(1, 5))
OLD, CURRENT, FOREIGN, AMBIGUOUS, UNKNOWN, FOREIGN_ORG = (str(UUID(int=n)) for n in range(10, 16))
EMAIL = "test@example.com"


class DesktopHistoryTest(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.home = Path(temporary.name)
        self.config = self.home / "account/config"
        self.config.mkdir(parents=True)
        self.project = Path("projects/-work")
        self.legacy = self.home / ".claude"
        self.index = self.home / ".config/Claude/claude-code-sessions"

    def put(self, path, content):
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(content)

    def transcript(self, root, session):
        return root / self.project / (session + ".jsonl")

    def record(self, account, session, organization=ORGANIZATION):
        self.put(self.index / account / organization / f"local_{session}.json",
                 json.dumps({"cliSessionId": session}))

    def launch(self, expected_email=EMAIL):
        subject = hashlib.sha256((expected_email + "\n" + ORGANIZATION).encode()).hexdigest()
        result = subprocess.run(
            [sys.executable, "-I", SOURCE / "desktop-identity.py", self.home / "account.json",
             self.config, subject],
            env={**os.environ, "HOME": str(self.home)},
            text=True, capture_output=True, check=True, input=(
                '[MAIN_LOG] 2026-01-01 00:00:00 [debug] [DATAPROVIDER] Setting "current-account" to: {\n'
                f"  isLoggedOut: false,\n  accountUuid: '{ACCOUNT}',\n"
                f"  organizationUuid: '{ORGANIZATION}',\n  emailAddress: '{EMAIL}',\n" "}\n"
            ),
        )
        self.assertEqual(json.loads((self.home / "account.json").read_text()),
                         {"logged_in": True, "email": EMAIL, "organization": ORGANIZATION})
        return result.stderr

    def test_upgrade_restores_only_sessions_owned_by_the_active_account(self):
        for session in (OLD, CURRENT, FOREIGN, AMBIGUOUS, UNKNOWN, FOREIGN_ORG):
            self.put(self.transcript(self.legacy, session), "old history\n")
        for session in (OLD, CURRENT, AMBIGUOUS):
            self.record(ACCOUNT, session)
        self.record(OTHER, FOREIGN)
        self.record(OTHER, AMBIGUOUS)
        self.record(ACCOUNT, FOREIGN_ORG, OTHER_ORG)
        self.put(self.transcript(self.config, CURRENT), "new history\n")
        self.put(self.legacy / self.project / OLD / "tool-results" / "output.txt", "tool output")
        self.put(self.legacy / "file-history" / OLD / "snapshot", "file snapshot")
        self.put(self.legacy / ".credentials.json", "unrelated private data")
        (self.legacy / self.project / OLD / "link").symlink_to(self.legacy / ".credentials.json")
        self.assertEqual(self.launch(), "")
        self.assertEqual(self.transcript(self.config, OLD).read_text(), "old history\n")
        self.assertEqual(self.transcript(self.config, CURRENT).read_text(), "new history\n")
        self.assertEqual(self.transcript(self.legacy, OLD).read_text(), "old history\n")
        self.assertEqual((self.config / self.project / OLD / "tool-results/output.txt").read_text(), "tool output")
        self.assertEqual((self.config / "file-history" / OLD / "snapshot").read_text(), "file snapshot")
        self.assertFalse((self.config / self.project / OLD / "link").is_symlink())
        self.assertFalse((self.config / ".credentials.json").exists())
        for session in (FOREIGN, AMBIGUOUS, UNKNOWN, FOREIGN_ORG):
            self.assertFalse(self.transcript(self.config, session).exists())
        self.transcript(self.config, OLD).unlink()
        self.assertEqual(self.launch(), "")
        self.assertFalse(self.transcript(self.config, OLD).exists())

    def test_live_identity_must_match_the_service_owner_and_failures_retry(self):
        self.record(ACCOUNT, OLD)
        self.put(self.transcript(self.legacy, OLD), "recover me\n")
        self.assertEqual(self.launch("another@example.com"), "")
        self.assertFalse(self.transcript(self.config, OLD).exists())

        # Simulate a write error. The original and its association remain usable.
        self.put(self.config / "projects", "blocks directory creation")
        self.assertIn("will retry", self.launch())
        (self.config / "projects").unlink()
        self.assertEqual(self.launch(), "")
        self.assertEqual(self.transcript(self.config, OLD).read_text(), "recover me\n")


if __name__ == "__main__":
    unittest.main()
