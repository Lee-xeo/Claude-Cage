"""Give the user's data directory of earlier versions its current name.

It runs once per update from the session that applies the components, as the
work account, before any current program is installed. It is never written
into the environment. The data is moved, never copied: a rename within one
file system keeps every file, its contents, owner, mode and times exactly as
they were, and processes that already hold a file open keep using it.

The former name stays as a relative link to the current one, so programs that
were started before the update, or an earlier version restored after a failed
update, find the same files. Nothing is ever deleted here.

States after an interruption at any point, and what the next run does:
- nothing changed yet: start again;
- data previously at the current name set aside: kept as it is, start again;
- directory renamed but the link not yet in place: the marker inside the
  directory says so, and the link is added;
- link in place: nothing to do.
"""

import os
import sys
from pathlib import Path

CURRENT_NAME = "workbench"
FORMER_NAME = "cage"
# Written inside the directory before it is renamed. It records that the
# directory came from the former name, so an interrupted run can finish.
MARKER = ".relocated"


class RelocationError(Exception):
    pass


def identity(path):
    info = os.lstat(path)
    return info.st_dev, info.st_ino


def directories(root):
    """The directories that hold accounts, sign-ins and conversations.

    A rename keeps every one of them. Files are not compared: a running
    program may replace one (Claude saves its settings and credentials by
    writing a new file in their place) while the move happens.
    """
    found = {}
    for name in sorted(os.listdir(root)):
        path = os.path.join(root, name)
        if os.path.isdir(path) and not os.path.islink(path):
            found[name] = identity(path)
    accounts = os.path.join(root, "claude-accounts")
    if os.path.isdir(accounts) and not os.path.islink(accounts):
        for account in sorted(os.listdir(accounts)):
            for relative in ("", "config", os.path.join("config", "projects")):
                path = os.path.join(accounts, account, relative)
                if os.path.isdir(path) and not os.path.islink(path):
                    found[os.path.join("claude-accounts", account, relative)] = identity(path)
    return found


def fsync_directory(path):
    descriptor = os.open(path, os.O_RDONLY | os.O_DIRECTORY)
    try:
        os.fsync(descriptor)
    finally:
        os.close(descriptor)


def write_marker(directory):
    path = os.path.join(directory, MARKER)
    if os.path.exists(path):
        return
    with open(path, "w") as stream:
        stream.flush()
        os.fsync(stream.fileno())
    fsync_directory(directory)


def link_former(share):
    """Put the relative link at the former name; replace nothing."""
    former = os.path.join(share, FORMER_NAME)
    if os.path.lexists(former):
        return
    staged = os.path.join(share, "." + FORMER_NAME + ".link")
    if os.path.lexists(staged):
        os.unlink(staged)
    os.symlink(CURRENT_NAME, staged)
    os.rename(staged, former)
    fsync_directory(share)


def rewrite_absolute_links(current, former_path, current_path):
    """Links inside the data that name the former absolute path follow it."""
    prefix = former_path + os.sep
    for directory, subdirectories, files in os.walk(current):
        for name in subdirectories + files:
            path = os.path.join(directory, name)
            if not os.path.islink(path):
                continue
            target = os.readlink(path)
            if target == former_path or target.startswith(prefix):
                staged = os.path.join(directory, "." + name + ".relink")
                if os.path.lexists(staged):
                    os.unlink(staged)
                os.symlink(current_path + target[len(former_path):], staged)
                os.replace(staged, path)


def set_aside(share, current, stamp):
    """Data found at the current name before the move is kept, not merged."""
    aside = os.path.join(share, CURRENT_NAME + ".before-relocation-" + stamp)
    os.rename(current, aside)
    fsync_directory(share)
    return aside


def relocate(home, stamp):
    share = os.path.join(home, ".local", "share")
    former = os.path.join(share, FORMER_NAME)
    current = os.path.join(share, CURRENT_NAME)
    former_path = os.path.abspath(former)
    current_path = os.path.abspath(current)

    if os.path.islink(former) and os.readlink(former) == CURRENT_NAME:
        if os.path.isdir(current) and os.path.exists(os.path.join(current, MARKER)):
            rewrite_absolute_links(current, former_path, current_path)
        return "linked"
    if not os.path.lexists(former):
        # Renamed by an earlier run that was stopped before the link.
        if os.path.isdir(current) and not os.path.islink(current) and os.path.exists(os.path.join(current, MARKER)):
            link_former(share)
            rewrite_absolute_links(current, former_path, current_path)
            return "linked"
        return "nothing"
    if not os.path.isdir(former):
        # Not something earlier versions created; leave it alone.
        return "nothing"
    if os.path.islink(current) and os.path.realpath(current) == os.path.realpath(former):
        # The data could not be moved earlier; the current name refers to it.
        return "referenced"

    aside = None
    if os.path.lexists(current):
        aside = set_aside(share, current, stamp)
    write_marker(former)
    before_root = identity(former)
    before = directories(former)
    try:
        os.rename(former, current)
    except OSError:
        # Another file system or a mount point: the data stays where it is and
        # the current name refers to it.
        if aside is None:
            os.symlink(former_path, current)
            fsync_directory(share)
            return "referenced"
        os.rename(aside, current)
        raise RelocationError("The data directory could not be renamed; nothing was changed.")
    link_former(share)
    after = directories(current) if os.path.isdir(current) else {}
    if identity(current) != before_root or any(after.get(name) != value for name, value in before.items()):
        rollback(share, aside)
        raise RelocationError("The renamed data directory did not match; the previous names were restored.")
    rewrite_absolute_links(current, former_path, current_path)
    return "moved" if aside is None else "moved, earlier data kept at " + aside


def rollback(share, aside=None):
    """Restore the former name, and what was at the current name, if any."""
    former = os.path.join(share, FORMER_NAME)
    current = os.path.join(share, CURRENT_NAME)
    if os.path.islink(former) and os.readlink(former) == CURRENT_NAME:
        os.unlink(former)
    if not os.path.lexists(former) and os.path.isdir(current) and not os.path.islink(current):
        os.rename(current, former)
        rewrite_absolute_links(former, os.path.abspath(current), os.path.abspath(former))
    if aside and os.path.lexists(aside) and not os.path.lexists(current):
        os.rename(aside, current)
    fsync_directory(share)


def main():
    home = sys.argv[1] if len(sys.argv) > 1 else str(Path.home())
    stamp = sys.argv[2] if len(sys.argv) > 2 else str(os.getpid())
    try:
        result = relocate(home, stamp)
    except (OSError, RelocationError) as error:
        sys.exit("Data directory: " + str(error))
    if result.startswith("moved"):
        print("Data directory: " + result)


if __name__ == "__main__":
    main()
