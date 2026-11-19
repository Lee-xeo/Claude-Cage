"""Restore legacy Linux Desktop Code transcripts for a verified account."""
import fcntl
import json
from pathlib import Path
import re
import shutil
import tempfile


UUID = re.compile(r"[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}")


def directory(path):
    return not path.is_symlink() and path.is_dir()


def regular(path):
    return not path.is_symlink() and path.is_file()


def ensure_directory(path):
    path.mkdir(mode=0o700, exist_ok=True)
    if not directory(path):
        raise OSError("History directory is unavailable")


def session_ids(index, account, organization):
    owned, others = set(), set()
    if not directory(index):
        return owned
    for user in index.iterdir():
        if not UUID.fullmatch(user.name) or not directory(user):
            continue
        for org in user.iterdir():
            if not UUID.fullmatch(org.name) or not directory(org):
                continue
            target = owned if (user.name, org.name) == (account, organization) else others
            for path in org.glob("local_*.json"):
                if not regular(path):
                    continue
                try:
                    row = json.loads(path.read_bytes())
                except (UnicodeError, ValueError):
                    continue
                session_id = row.get("cliSessionId") if isinstance(row, dict) else None
                if isinstance(session_id, str) and UUID.fullmatch(session_id):
                    target.add(session_id)
    return owned - others


def copy_file(source, target):
    if not regular(source) or target.exists() or target.is_symlink():
        return False
    # Install a complete private file without replacing a concurrently created one.
    with tempfile.NamedTemporaryFile(dir=target.parent) as temporary:
        with source.open("rb") as stream:
            shutil.copyfileobj(stream, temporary)
        temporary.flush()
        try:
            target.hardlink_to(temporary.name)
        except FileExistsError:
            return False
    return True


def copy_tree(source, target):
    if not directory(source):
        return
    ensure_directory(target)
    for item in source.iterdir():
        if directory(item):
            copy_tree(item, target / item.name)
        else:
            copy_file(item, target / item.name)


def recover(home, config, account, organization):
    if not UUID.fullmatch(account) or not UUID.fullmatch(organization):
        raise ValueError("Invalid Desktop history identity")
    # Keep the receipt outside the application's configuration directory so an
    # intentional deletion after migration remains effective.
    receipt = config.parent / f".desktop-history-{account}-{organization}"
    with (config.parent / ".desktop-history.lock").open("a") as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        if receipt.is_file():
            return 0
        source = home / ".claude"
        projects = source / "projects"
        profile = home / ".config" / "Claude"
        if not all(directory(path) for path in (source, projects, profile.parent, profile)):
            return 0
        sessions = session_ids(profile / "claude-code-sessions", account, organization)
        count = 0
        for project in projects.iterdir():
            if not directory(project):
                continue
            for transcript in project.glob("*.jsonl"):
                session_id = transcript.stem
                if session_id not in sessions or not regular(transcript):
                    continue
                ensure_directory(config)
                ensure_directory(config / "projects")
                target = config / "projects" / project.name
                ensure_directory(target)
                destination = target / transcript.name
                if destination.exists() or destination.is_symlink():
                    continue
                copy_tree(project / session_id, target / session_id)
                if directory(source / "file-history") and directory(source / "file-history" / session_id):
                    ensure_directory(config / "file-history")
                    copy_tree(source / "file-history" / session_id, config / "file-history" / session_id)
                count += copy_file(transcript, destination)
        receipt.touch(mode=0o600)
        return count
