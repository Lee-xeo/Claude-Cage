"""Unit tests for mobile remote Claude Desktop touch scrolling in view.py (#822)."""
import importlib.util
from pathlib import Path
import sys
import types
import unittest
from unittest.mock import Mock, patch


def load_view_module():
    pil = types.ModuleType('PIL')
    pil.Image = types.SimpleNamespace(Resampling=types.SimpleNamespace(LANCZOS=1))

    xlib = types.ModuleType('Xlib')
    xlib.X = types.SimpleNamespace(
        AnyPropertyType=0,
        IsViewable=2,
        ZPixmap=2,
        KeyPress=2,
        KeyRelease=3,
        ButtonPress=4,
        ButtonRelease=5,
        MotionNotify=6,
    )
    xlib.XK = types.SimpleNamespace(string_to_keysym=lambda name: 1)
    xlib.Xutil = types.SimpleNamespace(PMinSize=1)
    xlib.display = types.SimpleNamespace(Display=Mock)
    xlib.error = types.SimpleNamespace(XError=Exception)

    xlib_ext = types.ModuleType('Xlib.ext')
    xlib_ext.composite = types.SimpleNamespace(RedirectAutomatic=0)
    xlib_ext.xtest = types.SimpleNamespace(fake_input=Mock())

    stubs = {
        'PIL': pil,
        'Xlib': xlib,
        'Xlib.ext': xlib_ext,
    }
    path = Path(__file__).resolve().with_name('view.py')
    spec = importlib.util.spec_from_file_location('phone_desktop_view_test', path)
    module = importlib.util.module_from_spec(spec)
    with patch.dict(sys.modules, stubs):
        spec.loader.exec_module(module)
    return module, xlib, xlib_ext.xtest


class PhoneDesktopScrollTests(unittest.TestCase):
    def setUp(self):
        self.view_mod, self.xlib, self.xtest = load_view_module()

    def test_scroll_buttons_suppress_minor_cross_axis_jitter(self):
        # Vertical swipe with small horizontal thumb drift emits only vertical wheel notches.
        self.assertEqual(self.view_mod.scroll_buttons(14, 200), [5, 5, 5, 5])
        self.assertEqual(self.view_mod.scroll_buttons(-18, -150), [4, 4, 4])
        # Horizontal swipe with tiny vertical drift emits only horizontal wheel notches.
        self.assertEqual(self.view_mod.scroll_buttons(150, 10), [7, 7, 7])
        # Deliberate diagonal scroll keeps both axes, vertical first.
        self.assertEqual(self.view_mod.scroll_buttons(100, 100), [5, 5, 7, 7])
        # Short single-axis flick still sends one notch.
        self.assertEqual(self.view_mod.scroll_buttons(0, 20), [5])
        self.assertEqual(self.view_mod.scroll_buttons(0, 0), [])

    def test_scroll_buttons_reject_invalid_deltas(self):
        for dx, dy in [(float('nan'), 0), (0, 3201), ('10', 0), (None, 10)]:
            with self.assertRaises(self.view_mod.Failure) as ctx:
                self.view_mod.scroll_buttons(dx, dy)
            self.assertEqual(ctx.exception.status, 400)

    def test_action_spaces_scroll_notches_and_rechecks_window_each_step(self):
        view = self.view_mod.View.__new__(self.view_mod.View)
        view.size = (390, 640)
        events = []

        win = Mock(id=42)
        geometry = types.SimpleNamespace(width=780, height=1280)
        view.active = Mock(return_value=(win, 'desktop', geometry, 'doc-1'))

        root = Mock(id=1)
        root.translate_coords.return_value = types.SimpleNamespace(x=200, y=300)
        root.query_pointer.return_value = types.SimpleNamespace(child=win)
        view.root = root

        conn = Mock()
        conn.grab_server.side_effect = lambda: events.append('grab')
        conn.ungrab_server.side_effect = lambda: events.append('ungrab')
        conn.sync.side_effect = lambda: events.append('sync')
        view.conn = conn

        self.xtest.fake_input.side_effect = lambda _conn, kind, detail=0, x=0, y=0: events.append((kind, detail, x, y))
        sleeps = []
        with patch.object(self.view_mod.time, 'sleep', side_effect=lambda s: (sleeps.append(s), events.append('sleep'))):
            view.action({'type': 'scroll', 'document': 'doc-1', 'x': 100, 'y': 150, 'dx': 8, 'dy': 150})

        # 150px / 50 = 3 vertical notches, 2 inter-step sleeps strictly outside grab_server.
        self.assertEqual(len(sleeps), 2)
        self.assertEqual(events.count('grab'), 3)
        self.assertEqual(events.count('ungrab'), 3)
        self.assertEqual(events.count('sync'), 3)
        presses = [e[1] for e in events if isinstance(e, tuple) and e[0] == self.xlib.X.ButtonPress]
        self.assertEqual(presses, [5, 5, 5])
        # Verify sleep never occurs while server is grabbed.
        grabbed = False
        for ev in events:
            if ev == 'grab':
                grabbed = True
            elif ev == 'ungrab':
                grabbed = False
            elif ev == 'sleep':
                self.assertFalse(grabbed, 'time.sleep must not run while X server is grabbed')


if __name__ == '__main__':
    unittest.main()
