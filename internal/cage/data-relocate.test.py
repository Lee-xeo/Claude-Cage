import hashlib
import os
import runpy
import signal
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

SOURCE = Path(__file__).with_name("data-relocate.py")
ACCOUNT = "3b604337ab587cb90026d295"


def snapshot(root):
    """Every entry with its bytes, mode, times and file identity."""
    result = {}
    for directory, subdirectories, files in os.walk(root):
        subdirectories.sort()
        for name in sorted(subdirectories + files):
            path = os.path.join(directory, name)
            relative = os.path.relpath(path, root)
            info = os.lstat(path)
            entry = {"mode": info.st_mode, "uid": info.st_uid, "mtime": info.st_mtime_ns, "inode": (info.st_dev, info.st_ino)}
            if os.path.isdir(path) and not os.path.islink(path):
                # Adding the marker or following a link changes the time of
                # the directory that holds it, nothing else.
                del entry["mtime"]
            if os.path.islink(path):
                entry["link"] = os.readlink(path)
            elif os.path.isfile(path):
                entry["sha256"] = hashlib.sha256(Path(path).read_bytes()).hexdigest()
                entry["size"] = info.st_size
            result[relative] = entry
    return result


class Relocation(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.home = os.path.realpath(self.temporary.name)
        # Every path the tests touch lies inside this temporary home.
        self.assertTrue(self.home.startswith(os.path.realpath(tempfile.gettempdir()) + os.sep))
        self.share = os.path.join(self.home, ".local", "share")
        self.former = os.path.join(self.share, "cage")
        self.current = os.path.join(self.share, "workbench")
        self.module = runpy.run_path(str(SOURCE))

    def tearDown(self):
        self.temporary.cleanup()

    def inside(self, path):
        self.assertTrue(os.path.realpath(path).startswith(self.home + os.sep), path)

    def former_layout(self):
        def write(relative, data, mode=0o600):
            path = os.path.join(self.former, relative)
            os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
            with open(path, "wb") as stream:
                stream.write(data)
            os.chmod(path, mode)
            os.utime(path, ns=(1_700_000_000_123_456_789, 1_700_000_000_123_456_789))
        config = os.path.join("claude-accounts", ACCOUNT, "config")
        write(os.path.join(config, ".credentials.json"), b'{"claudeAiOauth":{"refreshToken":"r"}}')
        write(os.path.join(config, ".claude.json"), b'{"oauthAccount":{"emailAddress":"a@example.com"}}')
        write(os.path.join(config, "projects", "-home-x-p", "s.jsonl"), os.urandom(200_000))
        write(os.path.join("claude-accounts", ACCOUNT, "quota.json"), b"{}")
        write(os.path.join("claude-shared", "backups", "runtime-memory", "m.md"), "记忆\n".encode())
        write(os.path.join("attachments", "files", "a.bin"), os.urandom(4096), 0o640)
        write(os.path.join("desktop-downloads", "report.pdf"), os.urandom(1024))
        write(os.path.join("mobile-cli-identities", "sid"), b"{}")
        write(os.path.join("cli-channels", "c", "state.json"), b"{}")
        os.makedirs(os.path.join(self.home, ".claude", "skills"), exist_ok=True)
        os.symlink(os.path.join(self.home, ".claude", "skills"), os.path.join(self.former, config, "skills"))
        os.symlink("../quota.json", os.path.join(self.former, config, "quota-link"))
        os.symlink(os.path.join(self.former, "attachments", "files"), os.path.join(self.former, "attachments", "latest"))
        os.chmod(self.former, 0o700)
        return snapshot(self.former)

    def assert_moved(self, before):
        self.assertTrue(os.path.islink(self.former))
        self.assertEqual(os.readlink(self.former), "workbench")
        self.assertTrue(os.path.isdir(self.current) and not os.path.islink(self.current))
        after = snapshot(self.current)
        marker = after.pop(".relocated", None)
        self.assertIsNotNone(marker)
        before = dict(before)
        before.pop(".relocated", None)
        # The link that named the former absolute path follows the data;
        # it is a new link with the same meaning.
        moved_link = os.path.join("attachments", "latest")
        self.assertEqual(before[moved_link]["link"], os.path.join(self.former, "attachments", "files"))
        self.assertEqual(after[moved_link]["link"], os.path.join(self.current, "attachments", "files"))
        before.pop(moved_link)
        after.pop(moved_link)
        self.assertEqual(before, after)
        # Readable under both names, byte for byte.
        config = os.path.join("claude-accounts", ACCOUNT, "config", ".credentials.json")
        self.assertEqual(Path(self.former, config).read_bytes(), Path(self.current, config).read_bytes())
        self.inside(self.current)

    def run_script(self, *extra):
        return subprocess.run([sys.executable, "-I", str(SOURCE), self.home, "20261120T000000Z", *extra],
                              capture_output=True, text=True, env={"PATH": os.environ.get("PATH", ""), "HOME": self.home})

    def test_new_environment_is_left_alone(self):
        self.assertEqual(self.module["relocate"](self.home, "s"), "nothing")
        self.assertFalse(os.path.lexists(self.share))

    def test_move_keeps_every_byte_and_is_repeatable(self):
        before = self.former_layout()
        result = self.run_script()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assert_moved(before)
        moved = snapshot(self.current)
        for _ in range(2):
            self.assertEqual(self.module["relocate"](self.home, "again"), "linked")
            self.assertEqual(snapshot(self.current), moved)
        self.assertEqual(sorted(os.listdir(self.share)), ["cage", "workbench"])

    def test_killed_after_the_rename_finishes_on_the_next_run(self):
        before = self.former_layout()
        program = ("import os,runpy,signal,sys\n"
                   "m=runpy.run_path(sys.argv[1])\n"
                   "m['relocate'].__globals__['link_former']=lambda share: os.kill(os.getpid(), signal.SIGKILL)\n"
                   "m['relocate'](sys.argv[2],'s')\n")
        killed = subprocess.run([sys.executable, "-I", "-c", program, str(SOURCE), self.home])
        self.assertEqual(killed.returncode, -signal.SIGKILL)
        self.assertFalse(os.path.lexists(self.former))
        self.assertTrue(os.path.isdir(self.current))
        self.assertEqual(self.module["relocate"](self.home, "s"), "linked")
        self.assert_moved(before)

    def test_killed_before_the_rename_starts_again(self):
        before = self.former_layout()
        program = ("import os,runpy,signal,sys\n"
                   "m=runpy.run_path(sys.argv[1])\n"
                   "g=m['relocate'].__globals__\n"
                   "real=g['os'].rename\n"
                   "class Os: pass\n"
                   "def rename(a,b):\n"
                   "  if a.endswith('/cage'): os.kill(os.getpid(), signal.SIGKILL)\n"
                   "  real(a,b)\n"
                   "import types\n"
                   "g['os']=types.SimpleNamespace(**{k:getattr(os,k) for k in dir(os) if not k.startswith('__')})\n"
                   "g['os'].rename=rename\n"
                   "m['relocate'](sys.argv[2],'s')\n")
        killed = subprocess.run([sys.executable, "-I", "-c", program, str(SOURCE), self.home])
        self.assertEqual(killed.returncode, -signal.SIGKILL)
        self.assertTrue(os.path.isdir(self.former) and not os.path.islink(self.former))
        self.assertFalse(os.path.lexists(self.current))
        self.assertEqual(self.module["relocate"](self.home, "s"), "moved")
        self.assert_moved(before)

    def test_data_already_at_the_current_name_is_set_aside_unchanged(self):
        before = self.former_layout()
        written = os.path.join(self.current, "claude-accounts", ACCOUNT, "config")
        os.makedirs(written)
        Path(written, ".claude.json").write_text("{}")
        early = snapshot(self.current)
        result = self.module["relocate"](self.home, "20261120T000000Z")
        aside = os.path.join(self.share, "workbench.before-relocation-20261120T000000Z")
        self.assertEqual(result, "moved, earlier data kept at " + aside)
        self.assertEqual(snapshot(aside), early)
        self.assert_moved(before)

    def test_mismatch_restores_the_former_names(self):
        before = self.former_layout()
        Path(self.share, "workbench").mkdir()
        Path(self.share, "workbench", "early").write_text("kept")
        early = snapshot(self.current)
        calls = []
        real = self.module["directories"]
        def changed(root):
            calls.append(root)
            found = real(root)
            if len(calls) == 2:
                found["claude-accounts"] = (0, 0)
            return found
        self.module["relocate"].__globals__["directories"] = changed
        with self.assertRaises(self.module["RelocationError"]):
            self.module["relocate"](self.home, "s")
        self.assertTrue(os.path.isdir(self.former) and not os.path.islink(self.former))
        restored = snapshot(self.former)
        restored.pop(".relocated")
        before.pop(".relocated", None)
        self.assertEqual(restored, before)
        self.assertEqual(snapshot(self.current), early)
        self.assertFalse(any(name.startswith("workbench.before") for name in os.listdir(self.share)))
        # Once the cause is gone, the next run moves the data.
        self.module["relocate"].__globals__["directories"] = real
        self.assertTrue(self.module["relocate"](self.home, "t").startswith("moved"))

    def test_data_that_cannot_be_renamed_stays_and_is_referenced(self):
        before = self.former_layout()
        g = self.module["relocate"].__globals__
        real = os.rename
        def rename(a, b):
            if a == self.former:
                raise OSError(18, "Invalid cross-device link")
            real(a, b)
        import types
        g["os"] = types.SimpleNamespace(**{k: getattr(os, k) for k in dir(os) if not k.startswith("__")})
        g["os"].rename = rename
        try:
            self.assertEqual(self.module["relocate"](self.home, "s"), "referenced")
        finally:
            g["os"] = os
        self.assertEqual(os.readlink(self.current), self.former)
        after = snapshot(self.former)
        after.pop(".relocated")
        before.pop(".relocated", None)
        self.assertEqual(after, before)
        self.assertEqual(self.module["relocate"](self.home, "s"), "referenced")

    def test_former_link_to_another_place_moves_as_a_link(self):
        elsewhere = os.path.join(self.home, "data")
        os.makedirs(os.path.join(elsewhere, "claude-accounts"))
        Path(elsewhere, "claude-accounts", "x").write_text("x")
        os.makedirs(self.share)
        os.symlink(elsewhere, self.former)
        self.assertEqual(self.module["relocate"](self.home, "s"), "moved")
        self.assertEqual(os.readlink(self.current), elsewhere)
        self.assertEqual(os.readlink(self.former), "workbench")
        self.assertEqual(Path(self.former, "claude-accounts", "x").read_text(), "x")

    def test_unrelated_file_at_the_former_name_is_left_alone(self):
        os.makedirs(self.share)
        Path(self.former).write_text("not ours")
        self.assertEqual(self.module["relocate"](self.home, "s"), "nothing")
        self.assertEqual(Path(self.former).read_text(), "not ours")
        self.assertFalse(os.path.lexists(self.current))

    def test_rollback_after_completion_returns_the_former_layout(self):
        before = self.former_layout()
        self.assertEqual(self.module["relocate"](self.home, "s"), "moved")
        self.module["rollback"](self.share)
        self.assertTrue(os.path.isdir(self.former) and not os.path.islink(self.former))
        self.assertFalse(os.path.lexists(self.current))
        after = snapshot(self.former)
        after.pop(".relocated")
        before.pop(".relocated", None)
        # The absolute link names the former path again; it is a new link.
        moved_link = os.path.join("attachments", "latest")
        self.assertEqual(after.pop(moved_link)["link"], before.pop(moved_link)["link"])
        self.assertEqual(after, before)


if __name__ == "__main__":
    unittest.main()
