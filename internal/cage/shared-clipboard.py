#!/usr/bin/python3
"""Share CLIPBOARD selections over a private stdin/stdout channel.

No clipboard payload is logged. File bytes use a separate streaming archive.
"""
import base64
from contextlib import contextmanager
import fcntl
import hashlib
import json
import os
from pathlib import Path
import queue
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import threading
import time
import uuid
from urllib.parse import unquote, urlsplit

TEXT_LIMIT = 2 * 1024 * 1024
IMAGE_LIMIT = 20 * 1024 * 1024
FILE_LIMIT = 2 * 1024 * 1024 * 1024
WIRE_LIMIT = 30 * 1024 * 1024


class CacheRegistry:
    def __init__(self, directory=None):
        self.directory = Path(directory or Path(tempfile.gettempdir()) / ('cage-clipboard-ownership-' + str(os.getuid())))

    def ensure_directory(self):
        self.directory.mkdir(mode=0o700, parents=True, exist_ok=True)
        info = self.directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid != os.geteuid() or info.st_mode & 0o077:
            raise ValueError('Clipboard ownership directory is not private')

    def session_path(self, session):
        if not isinstance(session, str) or not 1 <= len(session) <= 256 or any(c not in 'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-' for c in session):
            raise ValueError('Invalid clipboard session')
        return self.directory / ('session-' + session)

    def begin_session(self, session):
        self.ensure_directory()
        path = self.session_path(session)
        with path.open('x') as output:
            os.chmod(path, 0o600)
            json.dump({'owner': os.getpid()}, output)

    def require_session(self, session):
        try:
            value = json.loads(self.session_path(session).read_text())
            owner = value['owner']
            if not isinstance(owner, int) or owner < 1:
                raise ValueError('Invalid clipboard session owner')
            os.kill(owner, 0)
        except (OSError, ValueError, KeyError, TypeError) as error:
            raise ValueError('Clipboard session changed') from error

    def end_session(self, session):
        self.session_path(session).unlink(missing_ok=True)

    def records(self):
        if not self.directory.exists():
            return []
        self.ensure_directory()
        records = []
        for path in self.directory.iterdir():
            if path.suffix != '.json' or not path.is_file():
                continue
            record = json.loads(path.read_text())
            identity = record.get('id') if isinstance(record, dict) else None
            directory = record.get('directory') if isinstance(record, dict) else None
            paths = record.get('paths') if isinstance(record, dict) else None
            marker = record.get('marker') if isinstance(record, dict) else None
            if (not isinstance(identity, str) or len(identity) != 32 or any(c not in '0123456789abcdef' for c in identity)
                    or identity + '.json' != path.name or not isinstance(directory, str)
                    or not Path(directory).is_absolute() or os.path.normpath(directory) != directory
                    or not isinstance(paths, list) or not 1 <= len(paths) <= 100
                    or any(not isinstance(item, str) or Path(item).parent != Path(directory) or os.path.normpath(item) != item for item in paths)
                    or not isinstance(marker, str) or marker in ('', '.', '..') or Path(marker).name != marker
                    or not isinstance(record.get('session'), str) or not isinstance(record.get('retired'), bool)
                    or not isinstance(record.get('pending', False), bool)
                    or record.get('pending') and (not isinstance(record.get('producer'), int) or record['producer'] < 1)):
                raise ValueError('Invalid clipboard ownership record')
            records.append(record)
        return records

    def save(self, record):
        self.ensure_directory()
        fd, temporary = tempfile.mkstemp(dir=self.directory, prefix='.record-')
        try:
            with os.fdopen(fd, 'w') as output:
                json.dump(record, output)
            os.replace(temporary, self.directory / (record['id'] + '.json'))
        finally:
            Path(temporary).unlink(missing_ok=True)

    def register(self, paths, session, pending=False):
        root = Path(paths[0]).parent
        identity = uuid.uuid4().hex
        fd, marker = tempfile.mkstemp(dir=root, prefix='.cage-clipboard-owner-')
        with os.fdopen(fd, 'w') as output:
            output.write(identity)
        self.ensure_directory()
        lock = self.directory / (identity + '.lock')
        lock.touch(mode=0o600, exist_ok=False)
        try:
            record = {'id': identity, 'directory': str(root), 'marker': Path(marker).name,
                      'paths': paths, 'session': session, 'retired': False, 'pending': pending, 'producer': os.getpid()}
            self.save(record)
            return record
        except BaseException:
            lock.unlink(missing_ok=True)
            raise

    def retire(self, paths):
        selected = set(paths)
        for record in self.records():
            if selected.intersection(record['paths']):
                record['retired'] = True
                self.save(record)

    def adopt(self, paths, session):
        selected = set(paths)
        for record in self.records():
            if selected.intersection(record['paths']):
                record['session'] = session
                self.save(record)

    @contextmanager
    def reading(self, paths):
        leases = []
        try:
            for record in self.records():
                root = Path(record['directory'])
                if any(Path(path).is_relative_to(root) for path in paths):
                    lock = (self.directory / (record['id'] + '.lock')).open('rb')
                    fcntl.flock(lock, fcntl.LOCK_SH)
                    leases.append(lock)
            yield
        finally:
            for lease in leases:
                lease.close()

    def collect(self, selected, session):
        remaining = False
        for record in self.records():
            root = Path(record['directory'])
            if root.exists() and record.get('pending') and not record['retired']:
                try:
                    os.kill(record['producer'], 0)
                    remaining = True
                    continue
                except ProcessLookupError:
                    pass
            if root.exists() and not record.get('pending') and not record['retired'] and (not record['session'] or record['session'] == session):
                continue
            if any(Path(path).is_relative_to(root) or root.is_relative_to(Path(path)) for path in selected):
                remaining = True
                continue
            lock_path = self.directory / (record['id'] + '.lock')
            with lock_path.open('rb') as lease:
                try:
                    fcntl.flock(lease, fcntl.LOCK_EX | fcntl.LOCK_NB)
                except BlockingIOError:
                    remaining = True
                    continue
                if root.exists():
                    marker = root / record['marker']
                    if root.is_symlink() or marker.is_symlink() or marker.read_text() != record['id']:
                        raise ValueError('Clipboard ownership changed')
                    for item in root.iterdir():
                        if item == marker:
                            continue
                        if item.is_dir() and not item.is_symlink():
                            shutil.rmtree(item)
                        else:
                            item.unlink()
                    marker.unlink()
                    try:
                        root.rmdir()
                    except OSError:
                        marker.write_text(record['id'])
                        marker.chmod(0o600)
                        raise
                (self.directory / (record['id'] + '.json')).unlink()
            lock_path.unlink(missing_ok=True)
        return remaining


def file_paths(uris):
    paths = []
    for uri in uris:
        parsed = urlsplit(uri)
        if parsed.scheme != 'file' or parsed.netloc not in ('', 'localhost'):
            raise ValueError('Only local clipboard files can be shared')
        path = Path(unquote(parsed.path))
        if not path.is_absolute() or path.is_symlink() or not path.exists():
            raise ValueError('Clipboard file unavailable')
        paths.append(str(path))
    if not 1 <= len(paths) <= 100:
        raise ValueError('Clipboard supports 1 to 100 selected files or folders')
    return paths


def export_files(paths, output):
    paths = file_paths([Path(path).as_uri() for path in paths])
    names = set()
    with tarfile.open(fileobj=output, mode='w|') as archive:
        def check(info):
            if not (info.isfile() or info.isdir()) or info.size > FILE_LIMIT:
                raise ValueError('Clipboard supports ordinary files up to 2 GB')
            return info
        for path in paths:
            name = Path(path).name
            if not name or name in names:
                raise ValueError('Clipboard file names must be distinct')
            names.add(name)
            archive.add(path, arcname=name, filter=check)


def import_files(source, session=''):
    registry = CacheRegistry()
    if session:
        registry.require_session(session)
    root = Path(tempfile.mkdtemp(prefix='cage-clipboard-'))
    record = None
    try:
        record = registry.register([str(root / '.pending')], session, pending=True)
        paths = extract_files(source, root)
        record['paths'] = paths
        record['pending'] = False
        registry.save(record)
        if session:
            registry.require_session(session)
        return paths
    except BaseException:
        shutil.rmtree(root)
        if record:
            (registry.directory / (record['id'] + '.json')).unlink(missing_ok=True)
            (registry.directory / (record['id'] + '.lock')).unlink(missing_ok=True)
        raise


def extract_files(source, root):
    root.chmod(0o755)
    names = []
    with tarfile.open(fileobj=source, mode='r|') as archive:
        for item in archive:
            parts = item.name.split('/')
            if (not parts or any(part in ('', '.', '..') or '\\' in part for part in parts)
                    or not (item.isfile() or item.isdir()) or item.size > FILE_LIMIT):
                raise ValueError('Invalid clipboard archive')
            if parts[0] not in names:
                names.append(parts[0])
                if len(names) > 100:
                    raise ValueError('Too many clipboard files')
            destination = root.joinpath(*parts)
            destination.parent.mkdir(parents=True, exist_ok=True, mode=0o755)
            if item.isdir():
                destination.mkdir(exist_ok=True, mode=0o755)
            else:
                with archive.extractfile(item) as source_file, destination.open('xb') as target:
                    while chunk := source_file.read(1024 * 1024):
                        target.write(chunk)
                destination.chmod(0o644)
    if not names:
        raise ValueError('Empty clipboard archive')
    return [str(root / name) for name in names]


def materialize_guest_files(paths, user, session=''):
    # The two graphical sessions have separate UIDs. Read only with the
    # copying application's permissions, then provide a shared temporary copy.
    import multiprocessing
    import pwd
    account = pwd.getpwnam(user)
    read_fd, write_fd = os.pipe()
    def export():
        os.close(read_fd)
        os.initgroups(account.pw_name, account.pw_gid)
        os.setgid(account.pw_gid)
        os.setuid(account.pw_uid)
        try:
            with os.fdopen(write_fd, 'wb') as output:
                export_files(paths, output)
        except BaseException:
            os._exit(1)
    process = multiprocessing.get_context('fork').Process(target=export)
    process.start()
    os.close(write_fd)
    copied = None
    try:
        with os.fdopen(read_fd, 'rb') as source:
            copied = import_files(source, session)
        process.join(timeout=3)
        if process.exitcode != 0:
            raise ValueError('Clipboard source files cannot be read')
        return copied
    except BaseException:
        if copied:
            shutil.rmtree(Path(copied[0]).parent)
        raise
    finally:
        if process.is_alive():
            process.terminate(); process.join(timeout=2)


RICH_TEXT_TARGETS = {'text/rtf', 'application/rtf', 'text/richtext'}
PLAIN_TEXT_TARGETS = {'UTF8_STRING', 'STRING', 'TEXT', 'COMPOUND_TEXT', 'text/plain', 'text/plain;charset=utf-8'}


def copies_rich_text(targets):
    """Word processors and spreadsheets publish a rendered picture next to the
    text they copied. Such a copy is text, never the picture."""
    names = set(targets)
    return bool(names & RICH_TEXT_TARGETS and names & PLAIN_TEXT_TARGETS)


def pixel_signature(width, height, rowstride, channels, pixels):
    """Identify decoded pixels, so a picture written here is recognised when
    the clipboard hands it back re-encoded as different PNG bytes."""
    digest = hashlib.sha256(('%d:%d:%d:' % (width, height, channels)).encode())
    view = memoryview(pixels)
    for row in range(height):
        digest.update(view[row * rowstride:row * rowstride + width * channels])
    return digest.digest()


def valid_desktop_instance(instance):
    if not isinstance(instance, str) or len(instance) != 24 or any(c not in '0123456789abcdef' for c in instance):
        raise ValueError('Invalid desktop instance')
    return instance


def desktop_display(instance, runtime_root=Path('/run')):
    valid_desktop_instance(instance)
    directory = runtime_root / ('desktop-' + instance)
    display_file, authority = directory / 'display', directory / 'Xauthority'
    if directory.is_symlink() or display_file.is_symlink() or authority.is_symlink():
        raise ValueError('Invalid desktop runtime')
    number = display_file.read_text().strip()
    if not number.isascii() or not number.isdigit() or len(number) > 5 or not authority.is_file():
        raise ValueError('Invalid desktop display')
    return ':' + number, str(authority)


def desktop_pid(instance):
    valid_desktop_instance(instance)
    result = subprocess.run(['systemctl', 'show', 'desktop-' + instance + '.service',
                             '--property=MainPID', '--value'],
                            check=True, capture_output=True, text=True, timeout=5)
    pid = int(result.stdout.strip())
    if pid <= 1:
        raise ValueError('Desktop instance is not running')
    return pid


def enter_desktop_mount_namespace(instance):
    pid = desktop_pid(instance)
    descriptor = os.open('/proc/' + str(pid) + '/ns/mnt', os.O_RDONLY)
    try:
        os.setns(descriptor, 0)
    finally:
        os.close(descriptor)
    # Re-evaluate /tmp after entering the service's PrivateTmp mount namespace.
    tempfile.tempdir = None
    return pid


def bridge_desktop(instance):
    display, authority = desktop_display(instance)
    # Join the instance's PrivateTmp mount namespace in the parent so file
    # imports, exports, and CacheRegistry receipts stay isolated per instance
    # while remaining visible to the X11 worker and Claude Desktop.
    pid = enter_desktop_mount_namespace(instance)
    net_fd = os.open('/proc/' + str(pid) + '/ns/net', os.O_RDONLY)
    try:
        bridge(displays=((display, 'cage'),), authorities={display: authority},
               namespace_fds=(net_fd,),
               display_exists=lambda _name: Path('/proc/' + str(pid)).is_dir())
    finally:
        os.close(net_fd)


def clipboard_worker(name, connection, authority=None):
    os.environ['NO_AT_BRIDGE'] = '1'
    os.environ['DISPLAY'] = name
    if authority:
        os.environ['XAUTHORITY'] = authority
    elif name == ':78':
        os.environ['XAUTHORITY'] = '/run/cage-desktop/Xauthority'
    else:
        os.environ.pop('XAUTHORITY', None)
    import gi
    gi.require_version('Gtk', '3.0')
    gi.require_version('Gdk', '3.0')
    from gi.repository import Gdk, GdkPixbuf, GLib, Gtk

    Gtk.init_check([])
    display = Gdk.Display.get_default()
    if not display:
        return
    clipboard = Gtk.Clipboard.get(Gdk.SELECTION_CLIPBOARD)
    displays = {name: (display, clipboard)}
    last = None
    initialized = False
    commands = queue.Queue()
    changes = {}
    written = None
    def signature(image):
        return pixel_signature(image.get_width(), image.get_height(), image.get_rowstride(), image.get_n_channels(), image.get_pixels())
    # Bytes are owned by Gtk and retained until another application copies.
    def write(clipboard, payload):
        if 'text' in payload:
            clipboard.set_text(payload['text'], -1)
        elif 'png' in payload:
            loader = GdkPixbuf.PixbufLoader.new_with_type('png')
            loader.write(base64.b64decode(payload['png'], validate=True))
            loader.close()
            image = loader.get_pixbuf()
            if image.get_width() * image.get_height() > 40_000_000:
                raise ValueError('Clipboard image too large')
            nonlocal written
            written = signature(image)
            clipboard.set_image(image)
        else:
            uris = [Path(path).as_uri() for path in payload['files']]
            # PyGObject does not expose gtk_clipboard_set_with_data. Supply the
            # URI selection through its C API while keeping callbacks alive.
            set_files(clipboard, uris)

    import ctypes as C
    gtk = C.CDLL('libgtk-3.so.0')
    gdk = C.CDLL('libgdk-3.so.0')
    p = C.c_void_p
    def bind(lib, name, result, *args):
        function = getattr(lib, name)
        function.restype, function.argtypes = result, list(args)
        return function
    atom = bind(gdk, 'gdk_atom_intern_static_string', p, C.c_char_p)
    set_data = bind(gtk, 'gtk_selection_data_set', None, p, p, C.c_int, p, C.c_int)
    get_clipboard = bind(gtk, 'gtk_clipboard_get_for_display', p, p, p)
    class Target(C.Structure):
        _fields_ = [('target', C.c_char_p), ('flags', C.c_uint), ('info', C.c_uint)]
    provide_type = C.CFUNCTYPE(None, p, p, C.c_uint, p)
    clear_type = C.CFUNCTYPE(None, p, p)
    own = bind(gtk, 'gtk_clipboard_set_with_data', C.c_int, p, C.POINTER(Target), C.c_uint, provide_type, clear_type, p)
    owners = {}
    def set_files(clipboard, uris):
        names = [b'text/uri-list', b'x-special/gnome-copied-files']
        data = [('\r\n'.join(uris) + '\r\n').encode(), ('copy\n' + '\n'.join(uris)).encode()]
        buffers = [C.create_string_buffer(value) for value in data]
        @provide_type
        def provide(_clipboard, selection, index, _user):
            set_data(selection, atom(names[index]), 8, buffers[index], len(data[index]))
        @clear_type
        def clear(_clipboard, _user):
            pass
        display = clipboard.get_display()
        native_clipboard = get_clipboard(hash(display), atom(b'CLIPBOARD'))
        targets = (Target * 2)(*(Target(name, 0, index) for index, name in enumerate(names)))
        # Keep old INCR transfers alive briefly when replacing the owner.
        previous = owners.get(hash(display))
        if previous:
            GLib.timeout_add_seconds(10, lambda retained=previous: False)
        owners[hash(display)] = (provide, clear, buffers, targets)
        if not own(native_clipboard, targets, 2, provide, clear, None):
            raise ValueError('Cannot own clipboard files')

    def publish(payload, source):
        nonlocal last
        if not initialized or payload == last:
            return
        last = payload
        for name, (_, clipboard) in displays.items():
            if name != source:
                write(clipboard, payload)
        message = dict(payload)
        if 'files' in payload:
            message['source'] = 'cage-browser' if source == ':77' else 'cage'
        connection.send(message)

    def changed(clipboard, _event, name):
        # Read the actual OS CLIPBOARD, never X11's drag-selection PRIMARY.
        # Requests stay asynchronous: reading one X server must not block this
        # process from answering selection requests on the other X server.
        changes[name] = changes.get(name, 0) + 1
        sequence = changes[name]
        def valid():
            return sequence == changes.get(name)
        def failure():
            connection.send({'error': '剪贴板内容无法共享，请检查文件是否仍存在及大小限制。'})
        def text_ready(_clipboard, text, _data):
            if not valid() or text is None:
                return
            if len(text.encode()) > TEXT_LIMIT:
                failure()
            else:
                publish({'text': text}, name)
        def image_ready(_clipboard, image, _data):
            if not valid():
                return
            if not image:
                clipboard.request_text(text_ready, None)
                return
            try:
                if image.get_width() * image.get_height() > 40_000_000:
                    raise ValueError('Clipboard image too large')
                # Reading back the picture this process just wrote is not a
                # new copy; publishing it would overwrite its source.
                if last and 'png' in last and signature(image) == written:
                    return
                ok, data = image.save_to_bufferv('png', [], [])
                if not ok or len(data) > IMAGE_LIMIT:
                    raise ValueError('Clipboard image too large')
                publish({'png': base64.b64encode(data).decode()}, name)
            except (ValueError, OSError, GLib.Error):
                failure()
        def rich_text_ready(_clipboard, text, _data):
            if not valid():
                return
            if text:
                text_ready(clipboard, text, None)
            else:
                clipboard.request_image(image_ready, None)
        def targets_ready(_clipboard, targets, _count, _data=None):
            if not valid():
                return
            if copies_rich_text(target.name() for target in targets or []):
                clipboard.request_text(rich_text_ready, None)
            else:
                clipboard.request_image(image_ready, None)
        def selected_files(uris):
            if not valid():
                return
            if uris:
                try:
                    paths = file_paths(uris)
                    connection.send({'clipboard_files': paths})
                    publish({'files': paths}, name)
                except (ValueError, OSError):
                    failure()
            else:
                connection.send({'clipboard_files': []})
                clipboard.request_targets(targets_ready, None)
        def gnome_ready(_clipboard, selection, _data):
            data = selection.get_data() or b''
            if len(data) > TEXT_LIMIT:
                failure()
                return
            try:
                lines = data.decode('utf-8', errors='strict').splitlines()
            except UnicodeError:
                failure()
                return
            selected_files(lines[1:] if lines and lines[0] in ('copy', 'cut') else [])
        def uris_ready(_clipboard, uris, _data):
            if uris:
                selected_files(uris)
            else:
                clipboard.request_contents(Gdk.Atom.intern('x-special/gnome-copied-files', False), gnome_ready, None)
        clipboard.request_uris(uris_ready, None)

    def tick():
        nonlocal last, initialized
        while not commands.empty():
            payload = commands.get()
            if payload is None:
                Gtk.main_quit()
                return False
            try:
                serial = payload.pop('serial', 0)
                if 'text' in payload and isinstance(payload['text'], str) and len(payload['text'].encode()) <= TEXT_LIMIT:
                    pass
                elif 'png' in payload and len(base64.b64decode(payload['png'], validate=True)) <= IMAGE_LIMIT:
                    pass
                elif 'files' in payload:
                    payload = {'files': file_paths([Path(path).as_uri() for path in payload['files']])}
                else:
                    raise ValueError('Invalid clipboard payload')
                last = payload
                initialized = True
                for _, clipboard in displays.values():
                    write(clipboard, payload)
                connection.send({'ack': serial})
            except (TypeError, ValueError, KeyError, OSError, GLib.Error):
                connection.send({'error': '剪贴板内容无法共享。'})
        return False

    def read():
        try:
            while True:
                commands.put(connection.recv())
                GLib.idle_add(tick)
        except EOFError:
            pass
        commands.put(None)
        GLib.idle_add(tick)
    threading.Thread(target=read, daemon=True).start()
    clipboard.connect('owner-change', changed, name)
    changed(clipboard, None, name)
    Gtk.main()


def bridge_wait_timeout(worker_count, total_displays, cleanup_ready, cache_retry, next_cache_check, now):
    if cleanup_ready:
        return 0.0
    timeout = 10.0 if worker_count >= total_displays else 1.0
    if cache_retry:
        timeout = min(timeout, max(0.0, next_cache_check - now))
    return timeout


def bridge(displays=((':77', 'cage-browser'), (':78', 'cage')), stdin_fd=None, stdout=None,
           registry=None, spawn_worker=None, display_exists=None, materialize=materialize_guest_files,
           authorities=None, namespace_fds=()):
    # GTK's providers use a default display. Keep one small worker per X11
    # session, under that application's UID, and merge only clipboard events.
    import multiprocessing
    import pwd
    import selectors
    workers = {}
    commands = queue.Queue()
    pending = {}
    last = None
    initialized = False
    internal_serial = 0
    session = str(uuid.uuid4())
    announced = None
    registry = registry or CacheRegistry()
    registry.begin_session(session)
    stdin_fd = sys.stdin.fileno() if stdin_fd is None else stdin_fd
    stdout = sys.stdout if stdout is None else stdout
    display_exists = display_exists or (lambda name: Path('/tmp/.X11-unix/X' + name[1:]).exists())
    current_files = {}
    next_cache_check = 0.0
    cleanup_needed = True
    cache_retry = False
    def emit(value):
        print(json.dumps(value), file=stdout, flush=True)
    def referenced_files():
        selected = [path for paths in current_files.values() for path in paths]
        if last:
            selected.extend(last.get('files', []))
        for value in pending.values():
            selected.extend(value.get('files', []))
        return selected
    def run_worker(name, connection, uid, gid, home):
        for descriptor in namespace_fds:
            os.setns(descriptor, 0)
            os.close(descriptor)
        os.initgroups(pwd.getpwuid(uid).pw_name, gid)
        os.setgid(gid)
        os.setuid(uid)
        os.environ['HOME'] = home
        clipboard_worker(name, connection, (authorities or {}).get(name))
    context = None if spawn_worker is not None else multiprocessing.get_context('fork')
    if spawn_worker is None:
        def spawn_worker(name, user):
            account = pwd.getpwnam(user)
            parent, child = context.Pipe()
            process = context.Process(target=run_worker, args=(name, child, account.pw_uid, account.pw_gid, account.pw_dir))
            process.start(); child.close()
            return process, parent
    selector = selectors.DefaultSelector()
    selector.register(stdin_fd, selectors.EVENT_READ, ('stdin', None))
    incoming = b''
    def remove_worker(name):
        previous = workers.pop(name, None)
        if not previous:
            return
        process, connection = previous
        try:
            selector.unregister(connection.fileno())
        except (KeyError, ValueError, OSError):
            pass
        try:
            connection.close()
        except OSError:
            pass
        process.join(timeout=1)
        current_files.pop(name, None)
    def sync_workers():
        nonlocal announced
        for name, user in displays:
            previous = workers.get(name)
            if previous and (not previous[0].is_alive() or not display_exists(name)):
                if previous[0].is_alive():
                    previous[0].terminate()
                remove_worker(name)
            if name not in workers and display_exists(name):
                process, parent = spawn_worker(name, user)
                workers[name] = (process, parent)
                selector.register(parent.fileno(), selectors.EVENT_READ, ('worker', name))
                if initialized:
                    parent.send(last)
        available = bool(workers)
        if available != announced:
            emit({'hello': {'version': 1, 'os': 'linux', 'session': session,
                  'formats': ['text', 'png', 'files'], 'available': available, 'file_release': True}})
            announced = available
    try:
        running = True
        while running:
            sync_workers()
            cleanup_ready = cleanup_needed and bool(workers) and all(name in current_files for name in workers)
            timeout = bridge_wait_timeout(len(workers), len(displays), cleanup_ready, cache_retry, next_cache_check, time.monotonic())
            for key, _ in selector.select(timeout=timeout):
                if key.data[0] == 'stdin':
                    chunk = os.read(stdin_fd, 65536)
                    if not chunk:
                        running = False
                        break
                    incoming += chunk
                    if len(incoming) > WIRE_LIMIT:
                        running = False
                        break
                    while b'\n' in incoming:
                        line, incoming = incoming.split(b'\n', 1)
                        try:
                            commands.put(json.loads(line))
                        except ValueError:
                            running = False
                            break
            if not running:
                break
            sync_workers()
            while not commands.empty():
                message = commands.get()
                if message is None:
                    running = False
                    break
                if 'release' in message:
                    registry.retire(message['release'])
                    cleanup_needed = True
                    continue
                serial = message.get('serial', 0)
                last = {key: value for key, value in message.items() if key in ('text', 'png', 'files')}
                initialized = True
                registry.adopt(last.get('files', []), session)
                pending[serial] = {'targets': set(workers), 'message': None, 'files': last.get('files', [])}
                for _, connection in workers.values():
                    connection.send({**last, 'serial': serial})
                if not pending[serial]['targets']:
                    emit({'ack': serial})
                    del pending[serial]
            for name, (process, connection) in list(workers.items()):
                while connection.poll():
                    try:
                        message = connection.recv()
                    except (EOFError, OSError):
                        remove_worker(name)
                        break
                    if 'clipboard_files' in message:
                        current_files[name] = message['clipboard_files']
                        cleanup_needed = True
                        continue
                    if 'ack' in message:
                        serial = message['ack']
                        if serial in pending:
                            pending[serial]['targets'].discard(name)
                            if not pending[serial]['targets']:
                                outgoing = pending[serial]['message']
                                if outgoing is None:
                                    emit({'ack': serial})
                                elif {key: value for key, value in outgoing.items() if key in ('text','png','files')} == last:
                                    emit(outgoing)
                                else:
                                    registry.retire(outgoing.get('files', []))
                                    cleanup_needed = True
                                del pending[serial]
                        continue
                    if message.get('error'):
                        emit(message)
                        continue
                    payload = {key: value for key, value in message.items() if key in ('text', 'png', 'files')}
                    if not initialized or payload == last:
                        continue
                    if 'files' in payload:
                        try:
                            with registry.reading(payload['files']):
                                payload = {'files': materialize(payload['files'], 'cage-browser' if name == ':77' else 'cage', session)}
                            message = {**payload, 'source': 'cage'}
                        except (ValueError, OSError, tarfile.TarError):
                            emit({'error': '剪贴板文件无法共享，请检查文件是否仍存在及读取权限。'})
                            continue
                    last = payload
                    internal_serial -= 1
                    targets = set(workers) if 'files' in payload else set(workers) - {name}
                    pending[internal_serial] = {'targets': targets, 'message': message, 'files': payload.get('files', [])}
                    for other, (_, sibling) in workers.items():
                        if other in targets:
                            sibling.send({**payload, 'serial': internal_serial})
                    if not targets:
                        emit(message)
                        del pending[internal_serial]
            if workers and all(name in current_files for name in workers) and (cleanup_needed or (cache_retry and time.monotonic() >= next_cache_check)):
                try:
                    cache_retry = registry.collect(referenced_files(), session)
                except (ValueError, OSError):
                    print('Clipboard cache cleanup failed; creation records retained.', file=sys.stderr)
                    cache_retry = True
                cleanup_needed = False
                next_cache_check = time.monotonic() + 1.0
    finally:
        try:
            registry.end_session(session)
            for record in registry.records():
                if record['session'] == session:
                    registry.retire(record['paths'])
            if workers and all(name in current_files for name in workers):
                registry.collect(referenced_files(), session)
        except (ValueError, OSError):
            print('Clipboard cache cleanup failed; creation records retained.', file=sys.stderr)
        selector.close()
        for process, connection in workers.values():
            connection.close()
            process.terminate(); process.join(timeout=2)
            if process.is_alive():
                process.kill(); process.join()


def main(argv=None):
    args = list(sys.argv[1:] if argv is None else argv)
    if not args:
        bridge()
        return
    if len(args) == 2 and args[0] == '--desktop':
        bridge_desktop(args[1])
        return
    if args[0] == '--import':
        session = ''
        desktop = ''
        index = 1
        while index < len(args):
            flag = args[index]
            if flag == '--session' and index + 1 < len(args) and not session:
                session = args[index + 1]
                index += 2
            elif flag == '--desktop' and index + 1 < len(args) and not desktop:
                desktop = args[index + 1]
                index += 2
            else:
                raise ValueError('Invalid clipboard import arguments')
        if desktop:
            enter_desktop_mount_namespace(desktop)
        print(json.dumps({'paths': import_files(sys.stdin.buffer, session)}))
        return
    if len(args) in (3, 5) and args[0] == '--export-user' and args[1] in ('cage', 'cage-browser') and args[2] == '--paths-stdin':
        desktop = ''
        if len(args) == 5:
            if args[3] != '--desktop':
                raise ValueError('Invalid clipboard export arguments')
            desktop = args[4]
        import pwd
        if desktop:
            enter_desktop_mount_namespace(desktop)
        selection = sys.stdin.buffer.read((1 << 20) + 1)
        if len(selection) > 1 << 20:
            raise ValueError('file selection exceeded its limit')
        paths = json.loads(selection)
        with CacheRegistry().reading(paths):
            # Keep this spelling: the guest profile rewrites it to map the
            # stable role name to the environment's own user name.
            user = args[1]
            account = pwd.getpwnam(user)
            os.initgroups(account.pw_name, account.pw_gid)
            os.setgid(account.pw_gid)
            os.setuid(account.pw_uid)
            export_files(paths, sys.stdout.buffer)
        return
    if len(args) == 2 and args[0] == '--export':
        selection = sys.stdin.buffer.read((1 << 20) + 1) if args[1] == '--paths-stdin' else args[1]
        if len(selection) > 1 << 20:
            raise ValueError('file selection exceeded its limit')
        export_files(json.loads(selection), sys.stdout.buffer)
        return
    raise ValueError('Invalid clipboard arguments')


if __name__ == '__main__':
    main()
