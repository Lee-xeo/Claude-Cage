import importlib.util
import os
from pathlib import Path
import shutil
import socket
import struct
import subprocess
import sys
import tempfile
import time
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location('display_governor', Path(__file__).with_name('display-governor.py'))
governor = importlib.util.module_from_spec(spec)
spec.loader.exec_module(governor)

try:
    from Xlib import X, display
    from Xlib.ext import xtest
except ImportError:
    display = None


def stop_process(process):
    process.terminate()
    try:
        process.wait(timeout=3)
    except subprocess.TimeoutExpired:
        process.kill()
        process.wait(timeout=3)
        raise AssertionError('fixture process did not stop gracefully')


class PolicyTests(unittest.TestCase):
    def test_rate_follows_time_since_input(self):
        self.assertEqual(governor.target_rate(0), 60)
        self.assertEqual(governor.target_rate(2.9), 60)
        self.assertEqual(governor.target_rate(3), 30)
        self.assertEqual(governor.target_rate(179), 30)
        self.assertEqual(governor.target_rate(180), 20)

    def test_wakes_exactly_at_the_next_change(self):
        self.assertAlmostEqual(governor.next_change(1), 2)
        self.assertAlmostEqual(governor.next_change(10), 170)
        self.assertIsNone(governor.next_change(500))

    def test_only_its_own_modes_are_governed(self):
        self.assertTrue(governor.governed('fps30-2339x1368'))
        # Size owners keep, match and delete these names themselves.
        for name in ('cage-2339x1368', 'cage-phone-800x600', '1600x1000', 'fps-1x2', 'fps30-1x', 'fps30', 'fpsx-1x2'):
            self.assertFalse(governor.governed(name), name)

    def test_mode_has_the_requested_refresh_rate(self):
        for rate in (20, 30, 60):
            mode = governor.mode_timing(rate, 2339, 1368)
            self.assertAlmostEqual(mode['dot_clock'] / (mode['h_total'] * mode['v_total']), rate)
            self.assertEqual((mode['width'], mode['height']), (2339, 1368))
            self.assertEqual(mode['name_length'], len(governor.mode_name(rate, 2339, 1368)))

    def test_full_rate_returns_to_the_size_owners_mode(self):
        base = ('cage-800x600', 800, 600)
        passive = ('fps30-800x600', 800, 600)
        self.assertEqual(governor.choose_mode(30, base, None, {base}), ('fps30-800x600', base))
        self.assertEqual(governor.choose_mode(60, passive, base, {base, passive}), ('cage-800x600', base))
        self.assertEqual(governor.choose_mode(60, base, base, {base}), ('cage-800x600', base))

    def test_full_rate_never_restores_another_size(self):
        stale = ('cage-800x600', 800, 600)
        current = ('fps30-1024x768', 1024, 768)
        self.assertEqual(governor.choose_mode(60, current, stale, {stale, current})[0], 'fps60-1024x768')
        # The owner removed its mode while the display was passive.
        self.assertEqual(governor.choose_mode(60, ('fps30-800x600', 800, 600), stale, set())[0], 'fps60-800x600')

    def test_activity_without_a_path_listens_nowhere(self):
        activity = governor.Activity()
        self.assertEqual(activity.files(), [])
        self.assertFalse(activity.acted())

    def test_activity_replaces_a_stale_socket_and_drops_closed_clients(self):
        with tempfile.TemporaryDirectory(dir='/tmp') as temp:
            path = temp + '/run/a.sock'
            os.makedirs(os.path.dirname(path))
            open(path, 'w').close()
            activity = governor.Activity(path)
            client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            client.connect(path)
            self.assertFalse(activity.acted())
            self.assertEqual(len(activity.files()), 2)
            client.send(b'\n')
            self.assertTrue(activity.acted())
            self.assertFalse(activity.acted())
            client.close()
            self.assertFalse(activity.acted())
            self.assertEqual(len(activity.files()), 1)
            activity.listener.close()

    def test_raw_key_reads_the_keycode(self):
        self.assertEqual(governor.raw_key(struct.pack('=HIIHHII', 3, 1234, 250, 3, 0, 0, 0)), 250)
        self.assertIsNone(governor.raw_key(b'short'))
        self.assertIsNone(governor.raw_key(None))

    @unittest.skipUnless(display, 'needs python3-xlib')
    def test_requires_xi21_for_grab_safe_events(self):
        from Xlib.ext import xinput
        connection = mock.Mock()
        connection.display.get_extension_major.return_value = 131
        for minor, supported in [(0, False), (1, True), (4, True)]:
            with self.subTest(minor=minor), mock.patch.object(xinput, 'XIQueryVersion') as query:
                query.return_value.major_version = 2
                query.return_value.minor_version = minor
                self.assertEqual(governor.negotiate_input(connection), supported)
                query.assert_called_once_with(display=connection.display, opcode=131,
                                              major_version=2, minor_version=1)


@unittest.skipUnless(display and shutil.which('Xtigervnc') and shutil.which('xrandr'), 'needs Xtigervnc, xrandr and python3-xlib')
class DisplayTests(unittest.TestCase):
    """The governor against a private virtual display."""

    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(dir='/tmp')
        self.addCleanup(self.temp.cleanup)
        number = next(n for n in range(120, 200) if not os.path.exists(f'/tmp/.X11-unix/X{n}'))
        self.activity = self.temp.name + '/activity/a.sock'
        self.env = dict(os.environ, DISPLAY=f':{number}', CAGE_DISPLAY_ACTIVITY=self.activity)
        self.env.pop('XAUTHORITY', None)
        server = subprocess.Popen(['Xtigervnc', f':{number}', '-geometry', '800x600', '-depth', '24', '-rfbport', '-1',
                                   '-rfbunixpath', self.temp.name + '/d.sock', '-nolisten', 'tcp', '-SecurityTypes', 'None'],
                                  stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        self.addCleanup(stop_process, server)
        for attempt in range(100):
            if os.path.exists(f'/tmp/.X11-unix/X{number}'):
                break
            time.sleep(.05)
        else:
            self.fail('virtual display did not start')
        self.conn = display.Display(f':{number}')
        self.addCleanup(self.conn.close)
        self.root = self.conn.screen().root
        script = (f'import importlib.util, sys\n'
                  f'spec = importlib.util.spec_from_file_location("g", {str(Path(__file__).with_name("display-governor.py"))!r})\n'
                  f'g = importlib.util.module_from_spec(spec); spec.loader.exec_module(g)\n'
                  f'g.ACTIVE_HOLD, g.IDLE_AFTER = .5, 1.5\nsys.exit(g.main())\n')
        process = subprocess.Popen([sys.executable, '-c', script], env=self.env)
        self.addCleanup(stop_process, process)
        self.process = process

    def current(self):
        # The governor may create/switch/remove a mode between these replies.
        # Read one atomic snapshot, like the production apply() path does.
        self.conn.grab_server()
        try:
            resources = self.root.xrandr_get_screen_resources()
            output = self.conn.xrandr_get_output_info(resources.outputs[0], resources.config_timestamp)
            crtc = self.conn.xrandr_get_crtc_info(output.crtc, resources.config_timestamp)
            position = 0
            for mode in resources.modes:
                name = resources.mode_names[position:position + mode.name_length]
                position += mode.name_length
                if mode.id == crtc.mode:
                    return name, round(mode.dot_clock / (mode.h_total * mode.v_total)), (mode.width, mode.height)
        finally:
            self.conn.ungrab_server()
            self.conn.sync()

    def modes(self):
        return subprocess.check_output(['xrandr', '--query'], env=self.env, text=True)

    def wait(self, rate, size=(800, 600), timeout=4):
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            current = self.current()
            if current[1:] == (rate, size):
                return current[0]
            time.sleep(.01)
        self.fail(f'expected {rate} Hz at {size}, display shows {self.current()}')

    def resize(self, width, height):
        # The sequence of the browser and Desktop resize paths.
        name = f'cage-{width}x{height}'
        if f' {name} ' not in self.modes():
            subprocess.check_call(['xrandr', '--newmode', name, f'{(width + 32) * (height + 8) * 60 / 1000000:.3f}',
                                   *map(str, (width, width + 8, width + 16, width + 32, height, height + 1, height + 2, height + 8)),
                                   '+HSync', '-VSync'], env=self.env)
            subprocess.check_call(['xrandr', '--addmode', 'VNC-0', name], env=self.env)
        subprocess.check_call(['xrandr', '--output', 'VNC-0', '--mode', name], env=self.env)

    def release_pointer(self):
        self.conn.ungrab_pointer(X.CurrentTime)
        self.conn.sync()

    def test_unattended_display_slows_and_input_restores_it(self):
        original = self.current()[0]
        self.assertEqual(self.wait(30), 'fps30-800x600')
        self.assertEqual(self.wait(20), 'fps20-800x600')
        started = time.monotonic()
        xtest.fake_input(self.conn, X.MotionNotify, x=40, y=40)
        self.conn.sync()
        self.assertEqual(self.wait(60), original)
        self.assertLess(time.monotonic() - started, .5)
        self.wait(30)
        code = self.conn.keysym_to_keycode(0x61)
        xtest.fake_input(self.conn, X.KeyPress, code)
        xtest.fake_input(self.conn, X.KeyRelease, code)
        self.conn.sync()
        self.assertEqual(self.wait(60), original)

    def test_a_write_to_the_activity_socket_is_viewer_input(self):
        original = self.current()[0]
        self.wait(20)
        self.assertEqual(os.stat(self.activity).st_mode & 0o777, 0o600)
        client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.addCleanup(client.close)
        client.connect(self.activity)
        # Connecting alone is not input.
        time.sleep(.2)
        self.assertEqual(self.current()[1], 20)
        started = time.monotonic()
        client.send(b'\n')
        self.assertEqual(self.wait(60), original)
        self.assertLess(time.monotonic() - started, .5)
        # Scrolling keeps the full rate; the display slows once it ends.
        for _ in range(6):
            time.sleep(.2)
            client.send(b'\n')
            self.assertEqual(self.current()[1], 60)
        self.wait(30)
        client.close()
        other = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.addCleanup(other.close)
        other.connect(self.activity)
        other.send(b'\n')
        self.assertEqual(self.wait(60), original)
        self.assertIsNone(self.process.poll())

    def test_caret_probe_key_is_not_viewer_input(self):
        subprocess.check_call(['xmodmap', '-e', 'keycode 250 = F24'], env=self.env)
        self.wait(20)
        for _ in range(3):
            xtest.fake_input(self.conn, X.KeyPress, 250)
            xtest.fake_input(self.conn, X.KeyRelease, 250)
            self.conn.sync()
            time.sleep(.2)
            self.assertEqual(self.current()[1], 20)

    def test_pointer_grab_does_not_hide_scroll_activity(self):
        # Menus, scrollbar drags and other clients may own an active grab.
        result = self.root.grab_pointer(False, X.ButtonPressMask | X.ButtonReleaseMask | X.PointerMotionMask,
                                        X.GrabModeAsync, X.GrabModeAsync, X.NONE, X.NONE, X.CurrentTime)
        self.assertEqual(result, X.GrabSuccess)
        self.addCleanup(self.release_pointer)
        self.conn.sync()
        self.wait(20)
        xtest.fake_input(self.conn, X.ButtonPress, 4)
        xtest.fake_input(self.conn, X.ButtonRelease, 4)
        self.conn.sync()
        self.wait(60, timeout=1)

    def test_real_remote_scroll_restores_full_rate(self):
        self.remote_scroll()

    def test_real_remote_scroll_during_pointer_grab(self):
        self.remote_scroll(grab=True)

    def remote_scroll(self, grab=False):
        # The display's own viewer socket, as the workbench connects to it.
        path = self.temp.name + '/d.sock'
        client = socket.socket(socket.AF_UNIX)
        self.addCleanup(client.close)
        client.settimeout(2)
        client.connect(path)
        def receive(size):
            data = b''
            while len(data) < size:
                block = client.recv(size - len(data))
                self.assertTrue(block)
                data += block
            return data
        self.assertEqual(receive(12), b'RFB 003.008\n')
        client.sendall(b'RFB 003.008\n')
        types = receive(receive(1)[0])
        self.assertIn(1, types)
        client.sendall(b'\x01')
        self.assertEqual(receive(4), b'\0' * 4)
        client.sendall(b'\x01')
        header = receive(24)
        receive(struct.unpack('>I', header[20:24])[0])
        # RandR refresh changes notify viewers like a resize. A bare RFB
        # client without DesktopSize support is disconnected by TigerVNC.
        client.sendall(struct.pack('>BBHi', 2, 0, 1, -223))
        client.sendall(struct.pack('>BBHH', 5, 0, 40, 40))
        if grab:
            result = self.root.grab_pointer(False, X.ButtonPressMask | X.ButtonReleaseMask | X.PointerMotionMask,
                                            X.GrabModeAsync, X.GrabModeAsync, X.NONE, X.NONE, X.CurrentTime)
            self.assertEqual(result, X.GrabSuccess)
            self.addCleanup(self.release_pointer)
            self.conn.sync()
        self.wait(20)
        # Exact RFB wheel input used by noVNC, not an XTest stand-in.
        # Keep the coordinates unchanged so motion cannot mask a lost wheel.
        for _ in range(4):
            client.sendall(struct.pack('>BBHH', 5, 8, 40, 40) + struct.pack('>BBHH', 5, 0, 40, 40))
            self.wait(60, timeout=.4)
            time.sleep(.3)
            self.assertEqual(self.current()[1], 60)

    def test_resize_keeps_its_size_and_removes_earlier_modes(self):
        self.wait(30)
        self.resize(1024, 700)
        self.assertEqual(self.wait(60, (1024, 700)), 'cage-1024x700')
        self.assertEqual(self.wait(30, (1024, 700)), 'fps30-1024x700')
        self.assertNotIn('fps30-800x600', self.modes())
        self.assertNotIn('fps20-800x600', self.modes())
        # A viewer attaching at the same size restores the full rate as well.
        self.resize(1024, 700)
        self.assertEqual(self.wait(60, (1024, 700)), 'cage-1024x700')
        # Each step of a drag stays at the size its owner selected.
        self.wait(30, (1024, 700))
        for width in range(1030, 1100, 10):
            self.resize(width, 700)
            time.sleep(.05)
            self.assertEqual(self.current()[1:], (60, (width, 700)))
        self.assertEqual(self.wait(30, (1090, 700)), 'fps30-1090x700')
        self.assertIsNone(self.process.poll())

    def test_stopping_restores_the_full_rate(self):
        original = self.current()[0]
        self.wait(30)
        self.process.terminate()
        self.assertEqual(self.process.wait(timeout=3), 0)
        self.assertEqual(self.current()[:2], (original, 60))

    def test_stopping_idle_governor_wakes_and_restores(self):
        original = self.current()[0]
        self.wait(20)
        self.process.terminate()
        self.assertEqual(self.process.wait(timeout=3), 0)
        self.assertEqual(self.current()[:2], (original, 60))

    def test_stopping_during_input_bursts_restores_full_rate(self):
        original = self.current()[0]
        self.wait(20)
        for x in range(30, 80):
            xtest.fake_input(self.conn, X.MotionNotify, x=x, y=40)
        self.conn.sync()
        self.process.terminate()
        self.assertEqual(self.process.wait(timeout=3), 0)
        self.assertEqual(self.current()[:2], (original, 60))

    def test_stopping_after_resize_preserves_new_size(self):
        self.wait(30)
        self.resize(1024, 700)
        self.process.terminate()
        self.assertEqual(self.process.wait(timeout=3), 0)
        self.assertEqual(self.current()[1:], (60, (1024, 700)))


if __name__ == '__main__':
    unittest.main()
