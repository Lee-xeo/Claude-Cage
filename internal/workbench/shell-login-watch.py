"""Signal verified-default-login refreshes without forwarding files, names or tokens."""
import ctypes
import json
import os
import select
import struct
import sys
from pathlib import Path

home = Path.home()
config = home / ".claude"
lib = ctypes.CDLL(None, use_errno=True)
fd = lib.inotify_init1(os.O_CLOEXEC | os.O_NONBLOCK)
if fd < 0:
    raise OSError(ctypes.get_errno(), "inotify_init1")

# Watch directories because the CLI publishes new credentials by replacement.
events = 0x0008 | 0x0080 | 0x0040 | 0x0100 | 0x0200 | 0x0400 | 0x0800
home_wd = lib.inotify_add_watch(fd, os.fsencode(home), events)
if home_wd < 0:
    raise OSError(ctypes.get_errno(), "cannot watch home")


def watch_config():
    return lib.inotify_add_watch(fd, os.fsencode(config), events)


def signature():
    try:
        st = (config / ".credentials.json").stat()
        credential = (st.st_ino, st.st_size, st.st_mtime_ns)
    except FileNotFoundError:
        credential = None
    # Ordinary CLI activity can also rewrite .claude.json. Only its public
    # account identity is relevant; the official CLI confirms every change.
    try:
        with (home / ".claude.json").open() as file:
            account = json.load(file).get("oauthAccount") or {}
        subject = (account.get("accountUuid"), account.get("emailAddress"),
                   account.get("organizationUuid"))
    except (OSError, ValueError, AttributeError):
        subject = ()
    return credential, subject if any(subject) else ()


def refresh():
    global previous
    current = signature()
    if previous != current:
        previous = current
        print("changed", flush=True)


config_wd = watch_config()
previous = (None, ())
refresh()
wake = select.poll()
wake.register(fd, select.POLLIN)
wake.register(sys.stdin, select.POLLIN | select.POLLHUP)
while True:
    ready = dict(wake.poll())
    if sys.stdin.fileno() in ready and not os.read(sys.stdin.fileno(), 64):
        # Capture a login followed immediately by shell exit.
        refresh()
        break
    if fd not in ready:
        continue
    data = os.read(fd, 65536)
    offset = 0
    relevant = False
    while offset < len(data):
        wd, mask, _, length = struct.unpack_from("iIII", data, offset)
        name = os.fsdecode(data[offset + 16:offset + 16 + length].split(b"\0", 1)[0])
        offset += 16 + length
        if wd < 0 or (wd == home_wd and name == ".claude") or (wd == config_wd and not name):
            config_wd = watch_config()
            relevant = True
        elif wd == config_wd and name == ".credentials.json" or wd == home_wd and name == ".claude.json":
            relevant = True
    if relevant:
        refresh()
