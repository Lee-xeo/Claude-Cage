"""SaveFile destinations and completed Desktop exports (runs as cage, never root)."""
import ctypes as C
from datetime import datetime, timezone
import errno
try:
    import fcntl
except ImportError:
    fcntl = None
from importlib.machinery import SourceFileLoader
import json
import os
from pathlib import Path
import re
import select
import signal
import stat
import struct
import threading
import uuid


def valid_name(name):
    return (isinstance(name, str) and bool(name) and len(name.encode()) <= 255
            and name not in ('.', '..') and not re.search(r'[/\\\x00-\x1f]', name))


def _ignore_sigio():
    sigio = getattr(signal, 'SIGIO', None)
    if sigio is None or threading.current_thread() is not threading.main_thread():
        return
    try:
        if signal.getsignal(sigio) == signal.SIG_DFL:
            signal.signal(sigio, signal.SIG_IGN)
    except (OSError, ValueError):
        pass


def _proc_has_open_writer(device, inode):
    """Fallback when kernel leases are unavailable."""
    try:
        entries = os.listdir('/proc')
    except OSError:
        return False
    for pid in entries:
        if not pid.isdigit():
            continue
        fd_dir = f'/proc/{pid}/fd'
        try:
            fds = os.listdir(fd_dir)
        except OSError:
            continue
        for num in fds:
            try:
                st = os.stat(f'{fd_dir}/{num}')
                if st.st_dev != device or st.st_ino != inode:
                    continue
                with open(f'/proc/{pid}/fdinfo/{num}', 'r') as info:
                    for line in info:
                        if line.startswith('flags:'):
                            flags = int(line.split()[1], 8)
                            if (flags & 3) in (os.O_WRONLY, os.O_RDWR):
                                return True
                            break
            except (OSError, ValueError, IndexError):
                continue
    return False


def target_writer_state(path):
    """Return 'missing', 'open', 'closed', or 'invalid' for a download target."""
    try:
        fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
    except FileNotFoundError:
        return 'missing'
    except OSError:
        # Symlinks (ELOOP) or unreadable entries are handed to save() to fail cleanly.
        return 'invalid'
    try:
        info = os.fstat(fd)
        if not stat.S_ISREG(info.st_mode):
            return 'invalid'
        sigio = getattr(signal, 'SIGIO', None)
        can_lease = (fcntl is not None and sigio is not None
                     and signal.getsignal(sigio) != signal.SIG_DFL)
        if can_lease:
            setlease = getattr(fcntl, 'F_SETLEASE', 1024)
            try:
                fcntl.fcntl(fd, setlease, fcntl.F_RDLCK)
                try:
                    fcntl.fcntl(fd, setlease, fcntl.F_UNLCK)
                except OSError:
                    pass
                return 'closed'
            except OSError as error:
                if error.errno == errno.EAGAIN:
                    return 'open'
                if error.errno not in (errno.EINVAL, errno.ENOSYS, errno.EOPNOTSUPP, errno.EPERM, errno.EACCES):
                    raise
        return 'open' if _proc_has_open_writer(info.st_dev, info.st_ino) else 'closed'
    finally:
        os.close(fd)


class Downloads:
    def __init__(self, directory, workspace, copier):
        _ignore_sigio()
        self.directory, self.workspace, self.copier = Path(directory), workspace, copier
        self.directory.mkdir(parents=True, exist_ok=True, mode=0o700)
        self.manifest = self.directory / 'state.json'
        self.lock = threading.RLock()
        self.copy_lock = threading.Lock()
        self.records = {}
        try:
            if self.manifest.stat().st_size > 1024 * 1024:
                raise ValueError('下载记录过大')
            records = json.loads(self.manifest.read_text())
            for item in records[-100:]:
                if (not re.fullmatch(r'[a-f0-9-]{36}', item['id']) or not valid_name(item['name'])
                        or not valid_name(item['sourceName'])):
                    continue
                if item['state'] in ('downloading', 'saving'):
                    item.update(state='failed', error='下载或保存被中断；已完整写入的文件可重试保存。')
                item['retryable'] = bool(item.get('complete') and self.source(item).is_file()
                                         and item['state'] == 'failed')
                self.records[item['id']] = item
        except (OSError, ValueError, TypeError, KeyError):
            self.records = {}

    def source(self, item):
        return self.directory / item['id'] / item['sourceName']

    def persist(self):
        finished = [key for key, item in self.records.items() if item['state'] in ('saved', 'failed', 'canceled')]
        for key in finished[:-50]:
            del self.records[key]
        temporary = self.manifest.with_suffix('.tmp')
        with temporary.open('w') as output:
            os.chmod(temporary, 0o600)
            json.dump(list(self.records.values()), output, ensure_ascii=False)
            output.flush()
            os.fsync(output.fileno())
        temporary.replace(self.manifest)

    def snapshot(self):
        with self.lock:
            return [{key: value for key, value in item.items() if key not in ('sourceName', 'complete')}
                    for item in reversed(list(self.records.values()))]

    def host_visible_path(self, path):
        """Translate retained SaveFile staging locations to the published file."""
        with self.lock:
            for item in self.records.values():
                source = self.source(item)
                if path in (str(source), str(source.parent)):
                    if item['state'] != 'saved':
                        raise ValueError('文件尚未保存完成，请稍后重试')
                    return item['path'] if path == str(source) else str(Path(item['path']).parent)
        return path

    def begin(self, name):
        if not valid_name(name):
            raise ValueError('下载文件名无效')
        with self.lock:
            if sum(item['state'] in ('downloading', 'saving') for item in self.records.values()) >= 50:
                raise ValueError('同时下载的文件过多，请等待下载完成')
            key = str(uuid.uuid4())
            folder = self.directory / key
            folder.mkdir(mode=0o700)
            # Watch before handing the URI to Chromium, including very small exports.
            libc = C.CDLL(None, use_errno=True)
            fd = libc.inotify_init1(os.O_CLOEXEC | os.O_NONBLOCK)
            if fd < 0:
                raise OSError(C.get_errno(), '无法监听下载完成')
            watch = libc.inotify_add_watch(fd, os.fsencode(folder), 0x2 | 0x8 | 0x80 | 0x200)
            if watch < 0:
                os.close(fd)
                raise OSError(C.get_errno(), '无法监听下载目录')
            item = dict(id=key, name=name, sourceName=name, state='downloading', target='desktop',
                        startedAt=datetime.now(timezone.utc).isoformat(), complete=False, retryable=False)
            self.records[key] = item
            try:
                self.persist()
            except Exception:
                del self.records[key]
                os.close(fd)
                raise
            threading.Thread(target=self.watch, args=(item, fd), daemon=True).start()
            return str(self.source(item))

    def watch(self, item, fd):
        wrote_target = False
        moved_target = False
        try:
            while True:
                if not select.select([fd], [], [], 1800)[0]:
                    raise ValueError('长时间未收到完整文件，请从原应用重新下载')
                data = os.read(fd, 65536)
                offset = 0
                while offset + 16 <= len(data):
                    _, mask, _, length = struct.unpack_from('iIII', data, offset)
                    name = os.fsdecode(data[offset + 16:offset + 16 + length].split(b'\0')[0])
                    offset += 16 + length
                    if mask & 0x4000:  # IN_Q_OVERFLOW: never infer completion from size stability.
                        raise ValueError('下载完成通知丢失，请重新下载')
                    if mask & 0x200 and (name == item['sourceName'] + '.crdownload'
                                         or (name == item['sourceName'] and (wrote_target or moved_target))):
                        with self.lock:
                            item.update(state='canceled', retryable=False)
                            self.persist()
                        return
                    if name != item['sourceName']:
                        continue
                    if mask & 0x2:
                        wrote_target = True
                    if mask & 0x80:
                        moved_target = True
                    # Chromium may reserve the final name with an empty file
                    # before writing .crdownload, or rename an active download
                    # into place while still holding the file descriptor open
                    # for writing. Publish only after all writers have closed.
                    if (mask & 0x80) or ((mask & 0x8) and (wrote_target or moved_target)):
                        state = target_writer_state(self.source(item))
                        if state in ('open', 'missing'):
                            continue
                        with self.lock:
                            item.update(complete=True, state='saving')
                            self.persist()
                        self.save(item)
                        return
        except Exception as error:
            self.failed(item, error)
        finally:
            os.close(fd)

    def failed(self, item, error):
        with self.lock:
            item.update(state='failed', error=str(error)[:300],
                        retryable=bool(item['complete'] and self.source(item).is_file()))
            try:
                self.persist()
            except OSError:
                item['error'] = '无法保存下载记录，请检查客机可用空间'

    def save(self, item):
        try:
            with self.copy_lock:
                source = os.open(self.source(item), os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
                try:
                    info = os.fstat(source)
                    if not stat.S_ISREG(info.st_mode):
                        raise ValueError('下载源文件无效')
                    result = self.copier(source, info.st_size, self.workspace, item['sourceName'])
                finally:
                    os.close(source)
            with self.lock:
                item.update(state='saved', path=result['path'], name=result['name'],
                            receivedBytes=result['bytes'], totalBytes=result['bytes'], retryable=False)
                item.pop('error', None)
                self.persist()
            # Retain the private source: applications may reuse a SaveFile URI.
        except Exception as error:
            self.failed(item, error)

    def retry(self, key):
        with self.lock:
            item = self.records.get(key)
            if not item or item['state'] != 'failed' or not item['retryable']:
                raise ValueError('完整下载源文件不可用，请重新下载')
            item.update(state='saving', retryable=False)
            self.persist()
            threading.Thread(target=self.save, args=(item,), daemon=True).start()
        return {'ok': True}


def downloads_directory(runtime=None):
    base = Path.home() / '.local/share/cage/desktop-downloads'
    if runtime:
        name = Path(runtime).name
        if re.fullmatch(r'desktop-[a-f0-9]{24}', name):
            return base / name
    return base


def create_downloads(runtime=None):
    helper = SourceFileLoader('cage_download_copy', '/usr/local/bin/cage-save-download').load_module()
    with open('/etc/claude-cage/desktop-downloads.json') as config:
        workspace = json.load(config)['workspace']
    return Downloads(downloads_directory(runtime), workspace, helper.copy_to_workspace)
