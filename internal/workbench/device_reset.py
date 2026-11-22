"""Fixed-capability guest device reset and recoverable profile cleanup.

Only main() chooses production roots on the guest. Requests select opaque target
IDs and validated public identifiers, never arbitrary paths or shell commands.
"""
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import time
import uuid

BATCH_RE = re.compile(r"^[0-9a-f]{32}\Z")
HOST_RE = re.compile(r"^[A-Za-z](?:[A-Za-z0-9-]{0,13}[A-Za-z0-9])?\Z")
MACHINE_ID_RE = re.compile(r"^[0-9a-f]{32}\Z")
MAC_RE = re.compile(r"^[0-9a-fA-F]{2}(?::[0-9a-fA-F]{2}){5}\Z")
EXT_SUFFIX_RE = re.compile(r"^[a-f0-9]{16}\Z")
ACCOUNT_DESKTOP_UNIT_RE = re.compile(r"^desktop-[0-9a-f]{24}\.service\Z")
SCAN_LIMIT = 50000

TARGETS = [
    {
        "id": "browser_profile",
        "title": "浏览器完整资料与登录态",
        "description": "Cookie、LocalStorage、IndexedDB、浏览器设备 ID（Local State）、历史与登录配置",
        "affects_identity": True,
        "default_selected": True,
    },
    {
        "id": "claude_code",
        "title": "Claude Code 登录与设备状态",
        "description": "CLI 认证凭据、用户/设备标识与本地缓存",
        "affects_identity": True,
        "default_selected": True,
    },
    {
        "id": "claude_desktop",
        "title": "Claude Desktop 登录与应用数据",
        "description": "桌面端 Cookie、存储、GPU 缓存与本地设备状态",
        "affects_identity": True,
        "default_selected": True,
    },
    {
        "id": "editor_state",
        "title": "内置编辑器状态与缓存",
        "description": "工作区界面布局、最近打开记录与编辑器缓存（不影响项目文件）",
        "affects_identity": False,
        "default_selected": True,
    },
    {
        "id": "system_traces",
        "title": "终端历史与系统临时痕迹",
        "description": "Shell 命令历史、临时状态与旧控制扩展目录",
        "affects_identity": False,
        "default_selected": True,
    },
]
TARGET_IDS = {t["id"] for t in TARGETS}


class Refused(Exception):
    pass


def check_safe_path(path: Path, root: Path):
    if not path.is_absolute() or ".." in path.parts:
        raise Refused("unsafe_path")
    try:
        path.relative_to(root)
    except ValueError:
        raise Refused("unsafe_path")
    cur = root
    for part in path.relative_to(root).parts:
        cur = cur / part
        try:
            info = cur.lstat()
        except FileNotFoundError:
            continue
        if stat.S_ISLNK(info.st_mode):
            raise Refused("unsafe_path")


def measure_path(path: Path, root: Path):
    check_safe_path(path, root)
    try:
        info = path.lstat()
    except FileNotFoundError:
        return 0, 0, False
    if stat.S_ISLNK(info.st_mode):
        raise Refused("unsafe_path")
    if stat.S_ISREG(info.st_mode):
        return info.st_size, 1, True
    if not stat.S_ISDIR(info.st_mode):
        return 0, 0, True
    total_bytes = 0
    total_files = 0
    stack = [path]
    seen = 0
    while stack:
        current = stack.pop()
        try:
            with os.scandir(current) as it:
                for entry in it:
                    seen += 1
                    if seen > SCAN_LIMIT:
                        return total_bytes, total_files, True
                    try:
                        st = entry.stat(follow_symlinks=False)
                    except OSError:
                        continue
                    if stat.S_ISLNK(st.st_mode):
                        continue
                    if stat.S_ISDIR(st.st_mode):
                        stack.append(Path(entry.path))
                    elif stat.S_ISREG(st.st_mode):
                        total_bytes += st.st_size
                        total_files += 1
        except OSError:
            continue
    return total_bytes, total_files, True


def target_paths(work_home: Path, browser_home: Path):
    return {
        "browser_profile": [
            (browser_home, browser_home / "profile"),
            (browser_home, browser_home / "LoginProfiles"),
            (browser_home, browser_home / "phone-profiles"),
            (browser_home, browser_home / "workbench-state.json"),
            (browser_home, browser_home / ".cache" / "chromium"),
        ],
        "claude_code": [
            (work_home, work_home / ".claude"),
            (work_home, work_home / ".claude.json"),
            (work_home, work_home / ".claude-accounts"),
            (work_home, work_home / ".local" / "share" / "cage" / "claude-accounts"),
            (work_home, work_home / ".cache" / "claude-cli-nodejs"),
            (work_home, work_home / ".cache" / "claude"),
        ],
        "claude_desktop": [
            (work_home, work_home / ".config" / "Claude"),
            (work_home, work_home / ".cache" / "Claude"),
            (work_home, work_home / ".local" / "share" / "Claude"),
            (work_home, work_home / ".local" / "share" / "desktop-profiles"),
            (work_home, work_home / ".local" / "share" / "keyrings"),
        ],
        "editor_state": [
            (work_home, work_home / ".local" / "share" / "code-server"),
            (work_home, work_home / ".config" / "code-server"),
        ],
        "system_traces": [
            (work_home, work_home / ".bash_history"),
            (work_home, work_home / ".zsh_history"),
            (work_home, work_home / ".local" / "state"),
            (browser_home, browser_home / ".local" / "state"),
        ],
    }


def _rmtree_readonly(path: Path):
    def _handle_error(func, target, _exc):
        try:
            parent = os.path.dirname(target)
            if parent:
                os.chmod(parent, os.stat(parent).st_mode | stat.S_IWUSR | stat.S_IXUSR)
            if not os.path.islink(target):
                mode = os.lstat(target).st_mode
                extra = stat.S_IWUSR | (stat.S_IXUSR if stat.S_ISDIR(mode) else 0)
                os.chmod(target, mode | extra)
        except OSError:
            pass
        func(target)

    shutil.rmtree(path, onerror=_handle_error)


def read_text_safe(path: Path) -> str:
    try:
        return path.read_text(encoding="utf-8", errors="replace").strip()
    except OSError:
        return ""


class DeviceResetter:
    def __init__(
        self,
        work_home: Path,
        browser_home: Path,
        etc_dir: Path = Path("/etc"),
        dbus_dir: Path = Path("/var/lib/dbus"),
        ext_root: Path = Path("/usr/local/lib"),
        live_system: bool = True,
    ):
        self.work_home = work_home
        self.browser_home = browser_home
        self.etc_dir = etc_dir
        self.dbus_dir = dbus_dir
        self.ext_root = ext_root
        self.live_system = live_system
        self.recovery_root = work_home / ".device-reset-recovery"

    def list_batches(self):
        batches = []
        if not self.recovery_root.is_dir():
            return batches
        try:
            entries = sorted(self.recovery_root.iterdir(), key=lambda p: p.name, reverse=True)
        except OSError:
            return batches
        for entry in entries:
            if not BATCH_RE.match(entry.name) or not entry.is_dir():
                continue
            manifest_path = entry / "manifest.json"
            if not manifest_path.is_file():
                continue
            try:
                manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
            except (OSError, ValueError):
                continue
            items = manifest.get("entries") or []
            if not items:
                continue
            total_bytes, _, _ = measure_path(entry, self.recovery_root)
            batches.append({
                "id": entry.name,
                "created_at": int(manifest.get("created_at", 0)),
                "count": len(manifest.get("item_ids") or []),
                "item_ids": manifest.get("item_ids") or [],
                "bytes": total_bytes,
                "previous_hostname": manifest.get("previous_hostname", ""),
            })
        batches.sort(key=lambda b: b["created_at"], reverse=True)
        return batches

    def inspect(self):
        mapping = target_paths(self.work_home, self.browser_home)
        items = []
        for spec in TARGETS:
            total_bytes = 0
            total_files = 0
            present = False
            for root, path in mapping[spec["id"]]:
                try:
                    b, f, exists = measure_path(path, root)
                except Refused:
                    continue
                if exists:
                    present = True
                    total_bytes += b
                    total_files += f
            items.append({
                **spec,
                "present": present,
                "bytes": total_bytes,
                "files": total_files,
            })
        hostname = read_text_safe(self.etc_dir / "hostname")
        mid = read_text_safe(self.etc_dir / "machine-id")
        mid_prefix = mid[:8] if len(mid) >= 8 else ""
        return {
            "version": 1,
            "hostname": hostname,
            "machine_id_prefix": mid_prefix,
            "items": items,
            "batches": self.list_batches(),
        }

    def _list_account_desktop_services(self):
        try:
            proc = subprocess.run(
                [
                    "systemctl",
                    "list-units",
                    "--type=service",
                    "--state=running,activating,reloading",
                    "--no-legend",
                    "--plain",
                    "desktop-*.service",
                ],
                check=False,
                stdout=subprocess.PIPE,
                stderr=subprocess.DEVNULL,
                text=True,
            )
        except OSError:
            return []
        units = []
        for line in (proc.stdout or "").splitlines():
            parts = line.split()
            if parts and ACCOUNT_DESKTOP_UNIT_RE.match(parts[0]):
                units.append(parts[0])
        return units

    def stop_guest_services(self):
        if not self.live_system:
            return
        services = [
            "cage-claude-desktop",
            *self._list_account_desktop_services(),
            "cage-browser",
            "cage-window-manager",
            "cage-display",
            "cage-display-governor",
            "cage-webvnc",
            "cage-editor",
            "cage-audio",
        ]
        subprocess.run(["systemctl", "stop", *services], check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def rotate_os_identity(self, hostname: str, machine_id: str, old_mac: str, new_mac: str, old_ext_suffix: str):
        if hostname:
            if not HOST_RE.match(hostname):
                raise Refused("invalid_hostname")
            (self.etc_dir / "hostname").write_text(hostname + "\n", encoding="utf-8")
            hosts_path = self.etc_dir / "hosts"
            lines = []
            replaced = False
            if hosts_path.is_file():
                for line in hosts_path.read_text(encoding="utf-8", errors="replace").splitlines():
                    if re.match(r"^\s*127\.0\.1\.1\s+", line):
                        if not replaced:
                            lines.append(f"127.0.1.1\t{hostname}")
                            replaced = True
                    else:
                        lines.append(line)
            if not replaced:
                lines.append(f"127.0.1.1\t{hostname}")
            hosts_path.write_text("\n".join(lines) + "\n", encoding="utf-8")

        if machine_id:
            if not MACHINE_ID_RE.match(machine_id):
                raise Refused("invalid_machine_id")
            mid_path = self.etc_dir / "machine-id"
            mid_path.write_text(machine_id + "\n", encoding="utf-8")
            try:
                os.chmod(mid_path, 0o444)
            except OSError:
                pass
            if self.dbus_dir.is_dir():
                dbus_mid = self.dbus_dir / "machine-id"
                try:
                    if dbus_mid.is_symlink() or dbus_mid.exists():
                        dbus_mid.unlink()
                    dbus_mid.symlink_to(mid_path)
                except OSError:
                    try:
                        dbus_mid.write_text(machine_id + "\n", encoding="utf-8")
                    except OSError:
                        pass

        if old_mac and new_mac:
            if not MAC_RE.match(old_mac) or not MAC_RE.match(new_mac):
                raise Refused("invalid_mac")
            netplan = self.etc_dir / "netplan" / "50-cloud-init.yaml"
            if netplan.is_file() and not netplan.is_symlink():
                content = netplan.read_text(encoding="utf-8", errors="replace")
                updated = re.sub(re.escape(old_mac), new_mac, content, flags=re.IGNORECASE)
                if updated != content:
                    netplan.write_text(updated, encoding="utf-8")

        if old_ext_suffix:
            if not EXT_SUFFIX_RE.match(old_ext_suffix):
                raise Refused("invalid_extension_suffix")
            old_ext = self.ext_root / f"browser-panes-{old_ext_suffix}"
            if old_ext.exists() and not old_ext.is_symlink():
                shutil.rmtree(old_ext, ignore_errors=True)

        ssh_dir = self.etc_dir / "ssh"
        if ssh_dir.is_dir():
            for key_file in ssh_dir.glob("ssh_host_*"):
                if key_file.is_file() and not key_file.is_symlink():
                    try:
                        key_file.unlink()
                    except OSError:
                        pass
            if self.live_system and shutil.which("ssh-keygen"):
                subprocess.run(["ssh-keygen", "-A"], check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)

    def _write_manifest(self, batch_dir, previous_hostname, item_ids, entries):
        manifest = {
            "version": 1,
            "created_at": int(time.time()),
            "previous_hostname": previous_hostname,
            "item_ids": list(item_ids),
            "entries": list(entries),
        }
        manifest_path = batch_dir / "manifest.json"
        tmp = batch_dir / "manifest.json.tmp"
        tmp.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
        try:
            os.chmod(tmp, 0o600)
        except OSError:
            pass
        os.replace(tmp, manifest_path)

    def reset(self, request: dict):
        selected = request.get("items") or []
        if not isinstance(selected, list) or any(not isinstance(i, str) or i not in TARGET_IDS for i in selected):
            raise Refused("invalid_items")
        irreversible = bool(request.get("irreversible", False))
        rotate_os = bool(request.get("rotate_os", False))
        hostname = str(request.get("hostname") or "")
        machine_id = str(request.get("machine_id") or "")
        old_mac = str(request.get("old_mac") or "")
        new_mac = str(request.get("new_mac") or "")
        old_ext_suffix = str(request.get("old_extension_suffix") or "")

        previous_hostname = read_text_safe(self.etc_dir / "hostname")
        self.stop_guest_services()

        mapping = target_paths(self.work_home, self.browser_home)
        batch_id = ""
        batch_dir = None
        manifest_entries = []
        moved_item_ids = []
        outcomes = []
        had_failure = False

        if selected and not irreversible:
            batch_id = uuid.uuid4().hex
            self.recovery_root.mkdir(parents=True, exist_ok=True)
            try:
                os.chmod(self.recovery_root, 0o700)
            except OSError:
                pass
            batch_dir = self.recovery_root / batch_id
            batch_dir.mkdir(parents=True, exist_ok=True)
            try:
                os.chmod(batch_dir, 0o700)
            except OSError:
                pass

        for item_id in selected:
            item_changed = False
            item_error = ""
            for idx, (root, path) in enumerate(mapping[item_id]):
                try:
                    check_safe_path(path, root)
                except Refused:
                    item_error = "目录包含不安全链接，已跳过"
                    had_failure = True
                    continue
                if not path.exists():
                    continue
                try:
                    st = path.lstat()
                    if irreversible:
                        if stat.S_ISDIR(st.st_mode):
                            _rmtree_readonly(path)
                        else:
                            path.unlink()
                    else:
                        dest_rel = f"{item_id}/{idx}"
                        dest = batch_dir / dest_rel
                        dest.parent.mkdir(parents=True, exist_ok=True)
                        shutil.move(str(path), str(dest))
                        manifest_entries.append({
                            "item_id": item_id,
                            "stored": dest_rel,
                            "original": str(path),
                            "root": "browser" if root == self.browser_home else "work",
                            "uid": st.st_uid,
                            "gid": st.st_gid,
                        })
                        # Persist after every move so an interrupted reset (kill,
                        # power loss) still leaves a restorable batch.
                        if item_id not in moved_item_ids:
                            moved_item_ids.append(item_id)
                        self._write_manifest(batch_dir, previous_hostname, moved_item_ids, manifest_entries)
                    item_changed = True
                except OSError as exc:
                    item_error = f"清理未完成：{exc.strerror or exc}"
                    had_failure = True
            if item_changed and item_id not in moved_item_ids:
                moved_item_ids.append(item_id)
            outcomes.append({
                "id": item_id,
                "status": "error" if item_error else ("cleaned" if item_changed else "absent"),
                "reason": item_error or ("已清理" if item_changed else "未发现数据"),
            })

        if batch_dir is not None:
            if manifest_entries:
                self._write_manifest(batch_dir, previous_hostname, moved_item_ids, manifest_entries)
            else:
                shutil.rmtree(batch_dir, ignore_errors=True)
                batch_id = ""

        os_rotated = False
        if rotate_os:
            self.rotate_os_identity(hostname, machine_id, old_mac, new_mac, old_ext_suffix)
            os_rotated = True

        new_mid = read_text_safe(self.etc_dir / "machine-id")
        return {
            "status": "partial" if had_failure else "ok",
            "batch_id": batch_id,
            "irreversible": irreversible,
            "os_rotated": os_rotated,
            "hostname": read_text_safe(self.etc_dir / "hostname"),
            "machine_id_prefix": new_mid[:8] if len(new_mid) >= 8 else "",
            "outcomes": outcomes,
        }

    def restore(self, batch_id: str):
        if not isinstance(batch_id, str) or not BATCH_RE.match(batch_id):
            raise Refused("invalid_batch")
        batch_dir = self.recovery_root / batch_id
        check_safe_path(batch_dir, self.recovery_root)
        manifest_path = batch_dir / "manifest.json"
        if not manifest_path.is_file():
            raise Refused("batch_not_found")
        manifest = json.loads(manifest_path.read_text(encoding="utf-8"))
        entries = manifest.get("entries") or []
        remaining = []
        outcomes = []
        had_partial = False
        self.stop_guest_services()

        for entry in entries:
            orig = Path(entry["original"])
            root = self.browser_home if entry.get("root") == "browser" else self.work_home
            stored = batch_dir / entry["stored"]
            try:
                check_safe_path(orig, root)
                check_safe_path(stored, batch_dir)
            except Refused:
                had_partial = True
                remaining.append(entry)
                outcomes.append({"id": entry["item_id"], "status": "skipped", "reason": "目标路径不安全，已跳过"})
                continue
            if not stored.exists():
                continue
            if orig.exists():
                had_partial = True
                remaining.append(entry)
                outcomes.append({"id": entry["item_id"], "status": "skipped", "reason": "原位置已存在新数据，恢复不会覆盖；原恢复副本已保留"})
                continue
            try:
                orig.parent.mkdir(parents=True, exist_ok=True)
                shutil.move(str(stored), str(orig))
                if hasattr(os, "geteuid") and os.geteuid() == 0:
                    try:
                        os.chown(orig, int(entry["uid"]), int(entry["gid"]))
                    except OSError:
                        pass
                outcomes.append({"id": entry["item_id"], "status": "restored", "reason": "已恢复"})
            except OSError as exc:
                had_partial = True
                remaining.append(entry)
                outcomes.append({"id": entry["item_id"], "status": "error", "reason": f"恢复失败：{exc.strerror or exc}"})

        if remaining:
            manifest["entries"] = remaining
            manifest_path.write_text(json.dumps(manifest, ensure_ascii=False, indent=2), encoding="utf-8")
        else:
            shutil.rmtree(batch_dir, ignore_errors=True)
        return {
            "status": "partial" if had_partial else "ok",
            "batch_id": batch_id,
            "outcomes": outcomes,
        }

    def delete_batch(self, batch_id: str):
        if not isinstance(batch_id, str) or not BATCH_RE.match(batch_id):
            raise Refused("invalid_batch")
        batch_dir = self.recovery_root / batch_id
        check_safe_path(batch_dir, self.recovery_root)
        if batch_dir.exists():
            shutil.rmtree(batch_dir)
        return {"status": "ok", "batch_id": batch_id}


def dispatch(resetter: DeviceResetter, request: dict):
    action = request.get("action")
    if action == "inspect":
        return resetter.inspect()
    if action == "reset":
        return resetter.reset(request)
    if action == "restore":
        return resetter.restore(request.get("batch", ""))
    if action == "delete_batch":
        return resetter.delete_batch(request.get("batch", ""))
    raise Refused("invalid_action")


def main():
    if len(sys.argv) != 3:
        sys.stderr.write("usage: device_reset.py <work_home> <browser_home>\n")
        return 2
    work_home = Path(sys.argv[1])
    browser_home = Path(sys.argv[2])
    if not work_home.is_absolute() or not browser_home.is_absolute():
        sys.stderr.write("homes must be absolute paths\n")
        return 2
    try:
        request = json.loads(sys.stdin.read() or "{}")
        resetter = DeviceResetter(work_home, browser_home, live_system=True)
        result = dispatch(resetter, request)
        sys.stdout.write(json.dumps(result, ensure_ascii=False))
        return 0
    except Refused as exc:
        sys.stderr.write(str(exc) + "\n")
        return 1
    except Exception as exc:
        sys.stderr.write(f"error: {exc}\n")
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
