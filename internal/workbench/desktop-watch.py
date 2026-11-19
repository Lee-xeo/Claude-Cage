"""Wake the controller on native identity transitions. No periodic guest polling."""
import ctypes
import os
import re
import select
import struct
import time
from pathlib import Path

lib = ctypes.CDLL(None, use_errno=True)
fd = lib.inotify_init1(os.O_CLOEXEC | os.O_NONBLOCK)
if fd < 0:
    raise OSError(ctypes.get_errno(), "inotify_init1")
root = Path("/run")
name = re.compile(r"desktop-[0-9a-f]{24}|cage-desktop")
wake = select.poll()
wake.register(fd, select.POLLIN)
watched = {}


def watch(path, mask):
    wd = lib.inotify_add_watch(fd, os.fsencode(path), mask)
    if wd >= 0:
        watched[wd] = path


def refresh():
    folders = [p for p in root.iterdir() if name.fullmatch(p.name) and p.is_dir()]
    for folder in folders:
        if folder not in watched.values():
            # Identity file creation and modification; runtime directory removal.
            watch(folder, 0x100 | 0x80 | 0x400)
    return bool(folders)


watch(root, 0x100 | 0x200 | 0x80)
while refresh():
    print("changed", flush=True)
    while wake.poll():
        time.sleep(.15)  # coalesce the Desktop provider's multiline event
        events = os.read(fd, 65536)
        changed = False
        while len(events) >= 16:
            wd, mask, _, length = struct.unpack_from("iIII", events)
            item = os.fsdecode(events[16:16 + length].split(b"\0", 1)[0])
            events = events[16 + length:]
            if mask & 0x8000:
                watched.pop(wd, None)
            changed |= (wd < 0 or
                        (watched.get(wd) == root and bool(name.fullmatch(item))) or
                        (watched.get(wd) != root and (not item or item == "account.json")))
        if changed:
            break

# Publish the final stop, even when no instance directory remains.
print("changed", flush=True)
