#!/usr/bin/env python3
"""Demand-driven PulseAudio microphone feeder for the guest audio service.

Watches PulseAudio source-outputs targeting the configured virtual microphone
source. Emits JSON line state transitions on stdout ({"demand": true/false})
so the host only captures its physical microphone while a guest application is
actively recording. Framed PCM (uint16 big-endian length + 48 kHz mono s16le)
arriving on stdin is paced into the module-pipe-source FIFO; silence fills
brief startup gaps while active so recording clients never stall.
"""

import errno
import json
import os
import signal
import struct
import subprocess
import sys
import threading
import time

DEFAULT_FIFO = "/run/cage-audio/input.fifo"
DEFAULT_SERVER = "unix:/run/cage-audio/native"
FRAME_BYTES = 48000 * 1 * 2 // 50  # 20 ms @ 48 kHz mono s16le (1920 bytes)
FRAME_INTERVAL = 0.02
MAX_FRAME_BYTES = 16384
MAX_BUFFER_BYTES = FRAME_BYTES * 10  # 200 ms low-latency cap
DEBOUNCE_SECONDS = 0.4
ACTIVE_RECHECK_SECONDS = 1.0
SILENCE_FRAME = b"\x00" * FRAME_BYTES


def c_locale_env():
    env = os.environ.copy()
    env["LC_ALL"] = "C"
    env["LANG"] = "C"
    return env


def parse_source_index(short_sources_text, source_name):
    for line in short_sources_text.splitlines():
        parts = line.split("\t")
        if len(parts) >= 2 and parts[1].strip() == source_name:
            return parts[0].strip()
    return None


def has_active_recorder(source_outputs_text, target_source_index):
    if not target_source_index:
        return False
    in_block = False
    current_source = None
    current_corked = False

    def block_active():
        return in_block and current_source == target_source_index and not current_corked

    for raw in source_outputs_text.splitlines():
        line = raw.strip()
        if line.startswith("Source Output #"):
            if block_active():
                return True
            in_block = True
            current_source = None
            current_corked = False
            continue
        if not in_block:
            continue
        if line.startswith("Source:"):
            current_source = line.split(":", 1)[1].strip()
        elif line.startswith("Corked:"):
            current_corked = line.split(":", 1)[1].strip().lower() == "yes"
    return block_active()


def query_pulse_active(server, source_name):
    env = c_locale_env()
    try:
        sources = subprocess.run(
            ["/usr/bin/pactl", f"--server={server}", "list", "short", "sources"],
            capture_output=True,
            text=True,
            timeout=3,
            check=True,
            env=env,
        ).stdout
        idx = parse_source_index(sources, source_name)
        if not idx:
            return False
        outputs = subprocess.run(
            ["/usr/bin/pactl", f"--server={server}", "list", "source-outputs"],
            capture_output=True,
            text=True,
            timeout=3,
            check=True,
            env=env,
        ).stdout
        return has_active_recorder(outputs, idx)
    except Exception:
        return False


class PCMRingBuffer:
    def __init__(self, max_bytes=MAX_BUFFER_BYTES):
        self.max_bytes = max_bytes
        self._buf = bytearray()
        self._lock = threading.Lock()

    def push(self, chunk):
        if not chunk:
            return
        with self._lock:
            if len(chunk) >= self.max_bytes:
                keep = self.max_bytes - (self.max_bytes % 2)
                self._buf = bytearray(chunk[-keep:])
                return
            overflow = len(self._buf) + len(chunk) - self.max_bytes
            if overflow > 0:
                overflow += overflow % 2
                del self._buf[:overflow]
            self._buf.extend(chunk)

    def pop_frame(self, size=FRAME_BYTES):
        with self._lock:
            avail = len(self._buf)
            if avail == 0:
                return SILENCE_FRAME if size == FRAME_BYTES else b"\x00" * size
            if avail >= size:
                out = bytes(self._buf[:size])
                del self._buf[:size]
                return out
            out = bytes(self._buf) + b"\x00" * (size - avail)
            self._buf.clear()
            return out

    def clear(self):
        with self._lock:
            self._buf.clear()


class AudioInputBridge:
    def __init__(
        self,
        source_name,
        fifo_path=DEFAULT_FIFO,
        server=DEFAULT_SERVER,
        query_fn=None,
        stdin=None,
        stdout=None,
        debounce_seconds=DEBOUNCE_SECONDS,
        frame_interval=FRAME_INTERVAL,
    ):
        self.source_name = source_name
        self.fifo_path = fifo_path
        self.server = server
        self.query_fn = query_fn or (lambda: query_pulse_active(self.server, self.source_name))
        self.stdin = stdin if stdin is not None else sys.stdin.buffer
        self.stdout = stdout if stdout is not None else sys.stdout
        self.debounce_seconds = debounce_seconds
        self.frame_interval = frame_interval

        self.buffer = PCMRingBuffer()
        self.stop_event = threading.Event()
        self.check_event = threading.Event()
        self.active_event = threading.Event()
        self._state_lock = threading.Lock()
        self._stdout_lock = threading.Lock()
        self._active = False
        self._inactive_since = None
        self._sub_proc = None
        self._sub_lock = threading.Lock()

    def is_active(self):
        with self._state_lock:
            return self._active

    def _emit_demand(self, active):
        line = json.dumps({"demand": bool(active)}, separators=(",", ":")) + "\n"
        with self._stdout_lock:
            try:
                self.stdout.write(line)
                self.stdout.flush()
            except OSError:
                self.stop_event.set()

    def apply_observed_state(self, observed, now=None):
        if now is None:
            now = time.monotonic()
        emit = None
        with self._state_lock:
            if observed:
                self._inactive_since = None
                if not self._active:
                    self._active = True
                    self.active_event.set()
                    emit = True
            else:
                if self._active:
                    if self._inactive_since is None:
                        self._inactive_since = now
                    elif now - self._inactive_since >= self.debounce_seconds:
                        self._active = False
                        self._inactive_since = None
                        self.active_event.clear()
                        self.buffer.clear()
                        emit = False
        if emit is not None:
            self._emit_demand(emit)

    def _next_wait_timeout(self):
        with self._state_lock:
            if not self._active:
                return None
            if self._inactive_since is not None:
                remaining = self.debounce_seconds - (time.monotonic() - self._inactive_since)
                return max(0.01, remaining)
            return ACTIVE_RECHECK_SECONDS

    def _subscribe_loop(self):
        env = c_locale_env()
        while not self.stop_event.is_set():
            proc = None
            try:
                proc = subprocess.Popen(
                    ["/usr/bin/pactl", f"--server={self.server}", "subscribe"],
                    stdout=subprocess.PIPE,
                    stderr=subprocess.DEVNULL,
                    text=True,
                    bufsize=1,
                    env=env,
                )
                with self._sub_lock:
                    self._sub_proc = proc
                self.check_event.set()
                assert proc.stdout is not None
                for line in proc.stdout:
                    if self.stop_event.is_set():
                        break
                    if "source-output" in line or " on source " in line:
                        self.check_event.set()
            except OSError:
                pass
            finally:
                with self._sub_lock:
                    if self._sub_proc is proc:
                        self._sub_proc = None
                if proc is not None:
                    try:
                        proc.terminate()
                        proc.wait(timeout=1)
                    except Exception:
                        try:
                            proc.kill()
                        except Exception:
                            pass
            if not self.stop_event.is_set():
                self.check_event.set()
                self.stop_event.wait(0.5)

    def _monitor_loop(self):
        self.check_event.set()
        while not self.stop_event.is_set():
            timeout = self._next_wait_timeout()
            self.check_event.wait(timeout=timeout)
            if self.stop_event.is_set():
                break
            self.check_event.clear()
            observed = self.query_fn()
            self.apply_observed_state(observed)

    def _read_exact(self, count):
        chunks = []
        remaining = count
        while remaining > 0 and not self.stop_event.is_set():
            data = self.stdin.read(remaining)
            if not data:
                return None
            chunks.append(data)
            remaining -= len(data)
        if remaining > 0:
            return None
        return b"".join(chunks)

    def _stdin_loop(self):
        try:
            while not self.stop_event.is_set():
                header = self._read_exact(2)
                if header is None:
                    break
                (length,) = struct.unpack(">H", header)
                if length == 0:
                    continue
                if length > MAX_FRAME_BYTES or (length % 2) != 0:
                    break
                payload = self._read_exact(length)
                if payload is None:
                    break
                if self.is_active():
                    self.buffer.push(payload)
        except OSError:
            pass
        finally:
            self.stop_event.set()
            self.check_event.set()
            self.active_event.set()

    def _fifo_loop(self):
        fd = None
        next_tick = None
        try:
            while not self.stop_event.is_set():
                if not self.is_active():
                    if fd is not None:
                        try:
                            os.close(fd)
                        except OSError:
                            pass
                        fd = None
                    next_tick = None
                    self.active_event.wait(timeout=0.5)
                    continue

                now = time.monotonic()
                if next_tick is None:
                    next_tick = now
                elif now < next_tick:
                    self.stop_event.wait(next_tick - now)
                    continue

                if fd is None:
                    try:
                        fd = os.open(self.fifo_path, os.O_WRONLY | os.O_NONBLOCK)
                    except OSError:
                        next_tick = time.monotonic() + self.frame_interval
                        continue

                frame = self.buffer.pop_frame(FRAME_BYTES)
                try:
                    os.write(fd, frame)
                except BlockingIOError:
                    pass
                except OSError as err:
                    if err.errno not in (errno.EAGAIN, errno.EWOULDBLOCK):
                        try:
                            os.close(fd)
                        except OSError:
                            pass
                        fd = None

                next_tick += self.frame_interval
                # Resynchronize if stalled so we never burst stale audio.
                now = time.monotonic()
                if now - next_tick > self.frame_interval * 4:
                    next_tick = now
        finally:
            if fd is not None:
                try:
                    os.close(fd)
                except OSError:
                    pass

    def stop(self):
        self.stop_event.set()
        self.check_event.set()
        self.active_event.set()
        with self._sub_lock:
            proc = self._sub_proc
        if proc is not None:
            try:
                proc.terminate()
            except OSError:
                pass

    def run(self, start_subscriber=True):
        threads = [
            threading.Thread(target=self._monitor_loop, name="monitor", daemon=True),
            threading.Thread(target=self._fifo_loop, name="fifo", daemon=True),
        ]
        if start_subscriber:
            threads.append(threading.Thread(target=self._subscribe_loop, name="subscribe", daemon=True))
        for t in threads:
            t.start()
        try:
            self._stdin_loop()
        finally:
            self.stop()
            for t in threads:
                t.join(timeout=1.5)


def main(argv):
    if len(argv) < 2 or not argv[1].strip():
        sys.stderr.write("usage: cage-audio-input <source_name> [fifo_path] [pulse_server]\n")
        return 2
    source_name = argv[1].strip()
    fifo_path = argv[2] if len(argv) >= 3 and argv[2] else DEFAULT_FIFO
    server = argv[3] if len(argv) >= 4 and argv[3] else DEFAULT_SERVER
    bridge = AudioInputBridge(source_name=source_name, fifo_path=fifo_path, server=server)

    def handle_signal(_signum, _frame):
        bridge.stop()

    signal.signal(signal.SIGTERM, handle_signal)
    signal.signal(signal.SIGINT, handle_signal)
    bridge.run()
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv))
