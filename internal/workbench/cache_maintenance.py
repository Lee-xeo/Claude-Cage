"""Fixed local cache inventory and recoverable moves. No network or account edits.

Workflow inspired by claude-post-ban-cleanup and Claude-Guardian; independently
implemented for the workbench. Only main() chooses production roots. Requests
select opaque item IDs, never paths, commands, credentials or account contents.
"""
import hashlib
import ctypes
import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import time
import uuid

LIMIT = 50000
ID = re.compile(r"[a-z0-9-]{1,80}\Z")
BATCH = re.compile(r"[0-9a-f]{32}\Z")
REASONS = {
    "active": "相关客户端正在运行，缓存已保留；正常退出后可重新盘点。",
    "unknown_processes": "无法完整核对运行进程，缓存已保留。",
    "unsafe_path": "目录包含链接、挂载点或特殊文件，已保留。",
    "unavailable": "目录权限或归属无法确认，已保留。",
    "too_large": "文件数量超过本次盘点上限，已保留。",
    "changed": "缓存已变化，请重新盘点。",
    "destination_exists": "原位置已生成新数据，恢复不会覆盖它；原恢复副本已保留。",
    "move_failed": "移动未完成，原文件和恢复批次均已保留，请重新盘点。",
    "protected_data": "目录内发现登录、会话、项目或持久磁盘标记，已保留。",
}
PROTECTED_NAMES = frozenset({".git", ".claude.json", ".credentials.json", "settings.json", "settings.local.json",
                             "history.jsonl", "cookies", "login data", "sessions", "session storage", "local storage",
                             "indexeddb", "projects", "file-history", "skills", "plugins", "backups"})


class Refused(Exception):
    pass


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def safe(path):
    """lstat every existing component; never accept links or Windows junctions."""
    if not path.is_absolute() or ".." in path.parts:
        raise Refused("unsafe_path")
    for part in (path, *path.parents):
        try:
            info = part.lstat()
        except FileNotFoundError:
            continue
        if stat.S_ISLNK(info.st_mode) or getattr(info, "st_file_attributes", 0) & 0x400:
            raise Refused("unsafe_path")


def snapshot(path):
    safe(path)
    rows = []
    total = 0

    def visit(current, relative, depth):
        nonlocal total
        if len(rows) >= LIMIT or depth > 32:
            raise Refused("too_large")
        if relative != "." and (current.name.lower() in PROTECTED_NAMES or current.suffix.lower() in {".img", ".vhdx", ".vmdk", ".qcow2"}):
            raise Refused("protected_data")
        info = current.lstat()
        if stat.S_ISLNK(info.st_mode) or getattr(info, "st_file_attributes", 0) & 0x400:
            raise Refused("unsafe_path")
        directory = stat.S_ISDIR(info.st_mode)
        if not directory and (not stat.S_ISREG(info.st_mode) or info.st_nlink != 1):
            raise Refused("unsafe_path")
        if info.st_dev != device or directory and os.path.ismount(current):
            raise Refused("unsafe_path")
        if hasattr(os, "geteuid") and os.geteuid() != 0 and info.st_uid != os.geteuid():
            raise Refused("unavailable")
        # A rename changes root ctime, but does not change its content metadata.
        rows.append([relative, info.st_dev, info.st_ino, info.st_mode,
                     info.st_size, info.st_mtime_ns, info.st_ctime_ns if relative != "." else 0])
        if directory:
            with os.scandir(current) as children:
                names = []
                for entry in children:
                    names.append(entry.name)
                    if len(rows) + len(names) > LIMIT:
                        raise Refused("too_large")
                names.sort()
            for name in names:
                visit(current / name, name if relative == "." else relative + "/" + name, depth + 1)
        else:
            total += info.st_size

    try:
        root_info = path.lstat()
        device = root_info.st_dev
    except FileNotFoundError:
        return None
    if not stat.S_ISDIR(root_info.st_mode):
        raise Refused("unsafe_path")
    visit(path, ".", 0)
    return {"fingerprint": digest(rows), "bytes": total, "files": sum(stat.S_ISREG(row[3]) for row in rows)}


def running_roles():
    """Inspect command lines privately; return only affected categories."""
    roles = set()
    try:
        if os.name == "nt":
            executable = Path(os.environ.get("SystemRoot", r"C:\Windows")) / "System32/WindowsPowerShell/v1.0/powershell.exe"
            result = subprocess.run([str(executable), "-NoProfile", "-NonInteractive", "-Command",
                                     "$ErrorActionPreference='Stop'; [Console]::OutputEncoding=[Text.UTF8Encoding]::new(); ConvertTo-Json -InputObject @(Get-CimInstance Win32_Process | Select-Object ProcessId,Name,CommandLine) -Compress"],
                                    capture_output=True, text=True, encoding="utf-8", timeout=15, check=True)
            rows = json.loads(result.stdout.lstrip("\ufeff"))
            if not isinstance(rows, list) or not rows:
                return None
            processes = [(row["ProcessId"], row["Name"], row.get("CommandLine")) for row in rows]
        else:
            output = subprocess.check_output(["/bin/ps", "-axo", "pid=,comm=,args="], text=True, timeout=10)
            processes = []
            # comm can contain spaces on macOS; use a separate PID/comm query.
            commands = subprocess.check_output(["/bin/ps", "-axo", "pid=,comm="], text=True, timeout=10)
            names = {int(row.split(None, 1)[0]): row.split(None, 1)[1] for row in commands.splitlines() if len(row.split(None, 1)) == 2}
            for row in output.splitlines():
                fields = row.strip().split(None, 1)
                if len(fields) == 2:
                    processes.append((int(fields[0]), names.get(int(fields[0]), ""), fields[1]))
            if not processes:
                return None
        for pid, command, args in processes:
            if pid == os.getpid():
                continue
            name = command.replace("\\", "/").rsplit("/", 1)[-1].lower().removesuffix(".exe")
            if name in {"claude", "claude-code", "claude-desktop", "claude desktop"} or name.startswith("claude helper"):
                # Both native CLI and Desktop can be named claude.
                roles.update(("cli", "desktop"))
            if any(part in name for part in ("chromium", "google chrome", "chrome", "msedge")):
                roles.add("browser")
            if name == "electron":
                roles.add("desktop")
            if name in {"node", "bun"}:
                if args is None:
                    return None
                if "@anthropic-ai/claude-code" in args.replace("\\", "/") or re.search(r"(?:^|[/\s])claude(?:-code)?(?:\.exe)?(?:\s|$)", args):
                    roles.add("cli")
        return roles
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError):
        return None


def private_directory(path):
    safe(path)
    if path.exists():
        info = path.stat()
        if not path.is_dir() or os.name != "nt" and (info.st_uid != os.geteuid() or info.st_mode & 0o077):
            raise Refused("unavailable")
        return
    path.mkdir(mode=0o700)
    if os.name == "nt":
        executable = Path(os.environ.get("SystemRoot", r"C:\Windows")) / "System32/WindowsPowerShell/v1.0/powershell.exe"
        script = "$ErrorActionPreference='Stop'; $sid=[Security.Principal.WindowsIdentity]::GetCurrent().User; $acl=New-Object Security.AccessControl.DirectorySecurity; $acl.SetAccessRuleProtection($true,$false); $acl.SetOwner($sid); foreach($id in @($sid,[Security.Principal.SecurityIdentifier]::new('S-1-5-18'))){$acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new($id,'FullControl','ContainerInherit,ObjectInherit','None','Allow'))}; Set-Acl -LiteralPath $env:MAINTENANCE_DIRECTORY -AclObject $acl"
        subprocess.run([str(executable), "-NoProfile", "-NonInteractive", "-Command", script], env={**os.environ, "MAINTENANCE_DIRECTORY": str(path)},
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, timeout=15, check=True)


def write_manifest(path, value):
    safe(path)
    temporary = path.with_name(".receipt-" + uuid.uuid4().hex)
    descriptor = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
            json.dump(value, stream, ensure_ascii=False)
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def move_cache(source, destination):
    """Same-filesystem rename without replacement; pin POSIX parent directories."""
    safe(source)
    safe(destination)
    if os.name == "nt":
        # Windows rename refuses an existing destination, including directories.
        os.rename(source, destination)
        return

    def directory_fd(path):
        descriptor = os.open("/", os.O_RDONLY | os.O_DIRECTORY)
        try:
            for part in path.parts[1:]:
                next_descriptor = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=descriptor)
                os.close(descriptor)
                descriptor = next_descriptor
            return descriptor
        except BaseException:
            os.close(descriptor)
            raise

    source_fd = directory_fd(source.parent)
    try:
        destination_fd = directory_fd(destination.parent)
        try:
            native = ctypes.CDLL(None, use_errno=True)
            name = "renameatx_np" if sys.platform == "darwin" else "renameat2"
            rename = getattr(native, name)
            rename.argtypes = [ctypes.c_int, ctypes.c_char_p, ctypes.c_int, ctypes.c_char_p, ctypes.c_uint]
            rename.restype = ctypes.c_int
            flag = 4 if sys.platform == "darwin" else 1  # RENAME_EXCL / RENAME_NOREPLACE
            if rename(source_fd, os.fsencode(source.name), destination_fd, os.fsencode(destination.name), flag) != 0:
                raise OSError(ctypes.get_errno(), "Cache move refused")
        finally:
            os.close(destination_fd)
    finally:
        os.close(source_fd)


class Maintenance:
    def __init__(self, home, system, browser, desktop, accounts, process_check=running_roles):
        self.home, self.system = home, system
        self.recovery = home / ".cache-maintenance-recovery"
        self.process_check = process_check
        self.targets = {}
        self.protected = []
        self.add_config("default", home / ".claude", "默认账户")
        safe(accounts)
        if accounts.is_dir():
            for number, entry in enumerate(sorted(accounts.iterdir()), 1):
                if re.fullmatch(r"[0-9a-f]{24}", entry.name):
                    self.add_config(entry.name, entry / "config", "独立账户 " + str(number))
                if number > 100:
                    raise Refused("too_large")
        self.protected += [("全局账户配置", home / ".claude.json")]
        for name, root in (("browser", browser), ("desktop", desktop)):
            for index, leaf in enumerate(("Cache", "Code Cache", "GPUCache")):
                self.targets[name + "-" + str(index)] = (root / leaf, name, ("浏览器" if name == "browser" else "Claude Desktop") + " · " + leaf)
            for leaf in ("Cookies", "Network", "Local Storage", "IndexedDB", "Sessions", "Session Storage"):
                self.protected.append(("网站登录与会话存储", root / leaf))
        safe(home)
        self.scope = digest([system, str(home), str(accounts), home.stat().st_dev, home.stat().st_ino])

    def add_config(self, account, root, label):
        self.targets["cli-" + account] = (root / "cache", "cli", "Claude Code · " + label + "缓存")
        for leaf in (".credentials.json", "settings.json", "projects", "history.jsonl", "skills", "plugins"):
            self.protected.append(("账户、会话与自定义设置", root / leaf))

    def inspect(self):
        roles = self.process_check()
        items = []
        for key, (path, role, title) in self.targets.items():
            row = {"id": key, "title": title, "location": self.display(path), "bytes": 0, "files": 0, "state": "absent", "reason": "未发现此缓存。"}
            try:
                value = snapshot(path)
                if value is not None:
                    row.update(value, state="ready", reason="可再生成缓存；清理会保留恢复副本。")
                    if roles is None or role in roles:
                        reason = "unknown_processes" if roles is None else "active"
                        row.update(state="blocked", reason=REASONS[reason])
            except (OSError, Refused) as error:
                row.update(state="blocked", reason=REASONS.get(str(error), REASONS["unavailable"]))
            items.append(row)
        counts = {}
        for category, path in self.protected:
            counts.setdefault(category, {"title": category, "present": 0, "unknown": 0})
            try:
                safe(path)
                counts[category]["present"] += int(path.exists())
            except (OSError, Refused):
                counts[category]["unknown"] += 1
        return {"version": 1, "scope": self.scope, "platform": self.system, "items": items,
                "protected": list(counts.values()), "batches": self.batches(),
                "notice": "仅查看缓存元数据与受保护位置的存在状态，不读取凭据、Cookie 值或会话正文。系统凭据库、项目内容和持久磁盘不参与扫描。"}

    def display(self, path):
        try:
            return "~/" + path.relative_to(self.home).as_posix()
        except ValueError:
            return "浏览器专用资料/" + path.name

    def load_batch(self, batch):
        if not isinstance(batch, str) or not BATCH.fullmatch(batch):
            raise Refused("invalid_batch")
        directory = self.recovery / batch
        safe(directory)
        receipt = directory / "receipt.json"
        safe(receipt)
        if receipt.stat().st_size > 65536:
            raise Refused("invalid_batch")
        data = json.loads(receipt.read_text(encoding="utf-8"))
        if data.get("version") != 1 or data.get("scope") != self.scope or not isinstance(data.get("items"), list) or len(data["items"]) > len(self.targets):
            raise Refused("invalid_batch")
        seen = set()
        for item in data["items"]:
            key = item.get("id")
            if key not in self.targets or key in seen or item.get("state") not in {"pending", "moved", "restored", "failed"}:
                raise Refused("invalid_batch")
            seen.add(key)
        return directory, data

    def batches(self):
        safe(self.recovery)
        if not self.recovery.exists():
            return []
        result = []
        for child in sorted(self.recovery.iterdir(), reverse=True):
            if not BATCH.fullmatch(child.name):
                continue
            try:
                directory, receipt = self.load_batch(child.name)
                count = sum((directory / item["id"]).exists() for item in receipt["items"] if item["state"] != "restored")
                if count:
                    result.append({"id": child.name, "created_at": receipt["created_at"], "count": count})
            except (OSError, ValueError, Refused, TypeError, KeyError):
                continue
        return sorted(result, key=lambda item: item["created_at"], reverse=True)[:20]

    def clean(self, request):
        if request.get("scope") != self.scope:
            raise Refused("scope_changed")
        selected = request.get("items")
        if not isinstance(selected, list) or not selected or len(selected) > len(self.targets):
            raise Refused("invalid_selection")
        current = {item["id"]: item for item in self.inspect()["items"]}
        seen = set()
        for item in selected:
            if not isinstance(item, dict) or set(item) != {"id", "fingerprint"}:
                raise Refused("invalid_selection")
            key = item["id"]
            row = current.get(key)
            if key in seen or row is None or row["state"] != "ready" or row["fingerprint"] != item["fingerprint"]:
                raise Refused("changed")
            seen.add(key)
        private_directory(self.recovery)
        batch = uuid.uuid4().hex
        directory = self.recovery / batch
        private_directory(directory)
        receipt = {"version": 1, "scope": self.scope, "created_at": int(time.time()),
                   "items": [{**item, "state": "pending"} for item in selected]}
        write_manifest(directory / "receipt.json", receipt)
        outcomes = []
        for item in receipt["items"]:
            key = item["id"]
            path, role, _ = self.targets[key]
            try:
                roles = self.process_check()
                if roles is None or role in roles:
                    raise Refused("active" if roles is not None else "unknown_processes")
                if snapshot(path)["fingerprint"] != item["fingerprint"]:
                    raise Refused("changed")
                safe(directory)
                if directory.stat().st_dev != path.stat().st_dev:
                    raise Refused("unsafe_path")
                move_cache(path, directory / key)
                item["state"] = "moved"
                write_manifest(directory / "receipt.json", receipt)
                if snapshot(directory / key)["fingerprint"] != item["fingerprint"]:
                    raise Refused("changed")
                outcomes.append({"id": key, "state": "moved", "reason": "已移入恢复批次，登录和工作资料保留。"})
            except (OSError, Refused, TypeError) as error:
                # The initial pending receipt also locates a move interrupted before
                # its completion record. Never remove either side on failure.
                outcomes.append({"id": key, "state": "failed", "reason": REASONS.get(str(error), REASONS["move_failed"])})
                break
        return {"version": 1, "batch": batch, "status": "complete" if len(outcomes) == len(selected) and all(item["state"] == "moved" for item in outcomes) else "partial", "outcomes": outcomes}

    def restore(self, batch):
        directory, receipt = self.load_batch(batch)
        outcomes = []
        for item in receipt["items"]:
            key = item["id"]
            source = directory / key
            if item["state"] == "restored" or not source.exists():
                continue
            target, role, _ = self.targets[key]
            try:
                roles = self.process_check()
                if roles is None or role in roles:
                    raise Refused("active" if roles is not None else "unknown_processes")
                safe(target)
                if target.exists():
                    raise Refused("destination_exists")
                if snapshot(source)["fingerprint"] != item["fingerprint"]:
                    raise Refused("changed")
                if not target.parent.is_dir():
                    raise Refused("unavailable")
                move_cache(source, target)
                item["state"] = "restored"
                write_manifest(directory / "receipt.json", receipt)
                outcomes.append({"id": key, "state": "restored", "reason": "已恢复缓存。"})
            except (OSError, Refused, TypeError) as error:
                outcomes.append({"id": key, "state": "failed", "reason": REASONS.get(str(error), REASONS["move_failed"])})
        return {"version": 1, "status": "partial" if any(row["state"] == "failed" for row in outcomes) else "complete", "outcomes": outcomes}


def production(request):
    system = {"darwin": "macos", "win32": "windows"}.get(sys.platform, "linux")
    if system == "linux":
        if os.geteuid() != 0:
            raise Refused("requires_local_service")
        home = Path("/home/cage")
        browser = Path("/home/cage-browser/profile/Default")
        desktop = home / ".config/Claude"
        accounts = home / ".local/share/cage/claude-accounts"
    else:
        home = Path.home()
        if system == "macos":
            browser = home / "Library/Application Support/Cage/Browser/Default"
            desktop = home / "Library/Application Support/Claude"
            accounts = home / ".local/share/cage/claude-accounts"
        else:
            profile = request.get("profile", "")
            if not re.fullmatch(r"[0-9a-f]{16}", profile):
                raise Refused("invalid_profile")
            browser = Path(os.environ.get("LOCALAPPDATA", str(home / "AppData/Local"))) / "Cage/Browser/Profile/Default"
            desktop = Path(os.environ.get("APPDATA", str(home / "AppData/Roaming"))) / "Claude"
            accounts = home / "Cage/CLI" / profile / "accounts"
    return Maintenance(home, system, browser, desktop, accounts)


def main():
    try:
        raw = sys.stdin.buffer.read(32769)
        if len(raw) > 32768:
            raise Refused("invalid_request")
        request = json.loads(raw)
        if not isinstance(request, dict) or set(request) - {"action", "profile", "scope", "items", "batch"}:
            raise Refused("invalid_request")
        service = production(request)
        action = request.get("action")
        if action == "inspect":
            result = service.inspect()
        elif action in {"clean", "restore"}:
            private_directory(service.recovery)
            lock = service.recovery / ".operation-lock"
            safe(lock)
            # Kernel locks survive cancellation without leaving a stale busy flag.
            descriptor = os.open(lock, os.O_RDWR | os.O_CREAT | getattr(os, "O_NOFOLLOW", 0), 0o600)
            with os.fdopen(descriptor, "r+b") as stream:
                if os.name == "nt":
                    import msvcrt
                    stream.seek(0)
                    msvcrt.locking(stream.fileno(), msvcrt.LK_NBLCK, 1)
                else:
                    import fcntl
                    fcntl.flock(stream.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                result = service.clean(request) if action == "clean" else service.restore(request.get("batch"))
        else:
            raise Refused("invalid_request")
    except (OSError, ValueError, Refused, TypeError, KeyError, AttributeError, subprocess.SubprocessError):
        result = {"version": 1, "error": "operation_refused", "message": "未能完成缓存操作。请重新盘点；已有数据与恢复副本均保留。"}
    print(json.dumps(result, ensure_ascii=True))


if __name__ == "__main__":
    main()
