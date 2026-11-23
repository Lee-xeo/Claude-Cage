import importlib.util
import io
import json
import os
import struct
import tempfile
import threading
import time
import unittest
from pathlib import Path

MODULE_PATH = Path(__file__).with_name("guest-audio-input.py")
SPEC = importlib.util.spec_from_file_location("guest_audio_input", MODULE_PATH)
MOD = importlib.util.module_from_spec(SPEC)
assert SPEC and SPEC.loader
SPEC.loader.exec_module(MOD)


class GuestAudioInputTests(unittest.TestCase):
    def test_parse_source_index_and_ignore_monitor(self):
        short_sources = "\n".join([
            "0\talsa_output.usb-Focusrite_Scarlett_Solo_1234ABCD-00.analog-stereo.monitor\tmodule-null-sink.c\ts16le 2ch 48000Hz\tRUNNING",
            "1\talsa_input.usb-Focusrite_Scarlett_Solo_1234ABCD-00.analog-stereo\tmodule-pipe-source.c\ts16le 1ch 48000Hz\tIDLE",
        ])
        idx = MOD.parse_source_index(
            short_sources,
            "alsa_input.usb-Focusrite_Scarlett_Solo_1234ABCD-00.analog-stereo",
        )
        self.assertEqual(idx, "1")
        self.assertIsNone(MOD.parse_source_index(short_sources, "missing_source"))

    def test_has_active_recorder_ignores_monitor_and_corked(self):
        # Source 0 is the output monitor (used by parec for playback streaming).
        # Source 1 is the microphone input source.
        monitor_only = """
Source Output #10
\tDriver: protocol-native.c
\tSource: 0
\tCorked: no
\tProperties:
\t\tapplication.name = "parec"
"""
        self.assertFalse(MOD.has_active_recorder(monitor_only, "1"))

        corked_mic = """
Source Output #11
\tDriver: protocol-native.c
\tSource: 1
\tCorked: yes
"""
        self.assertFalse(MOD.has_active_recorder(corked_mic, "1"))

        active_mic = """
Source Output #10
\tSource: 0
\tCorked: no
Source Output #12
\tSource: 1
\tCorked: no
"""
        self.assertTrue(MOD.has_active_recorder(active_mic, "1"))

    def test_debounce_transitions(self):
        stdout = io.StringIO()
        bridge = MOD.AudioInputBridge(
            source_name="alsa_input.test",
            query_fn=lambda: False,
            stdin=io.BytesIO(b""),
            stdout=stdout,
            debounce_seconds=0.4,
        )
        # Immediate activation.
        bridge.apply_observed_state(True, now=10.0)
        self.assertTrue(bridge.is_active())
        self.assertEqual(stdout.getvalue(), '{"demand":true}\n')

        # Brief drop below debounce stays active.
        bridge.apply_observed_state(False, now=10.1)
        bridge.apply_observed_state(False, now=10.35)
        self.assertTrue(bridge.is_active())
        self.assertEqual(stdout.getvalue(), '{"demand":true}\n')

        # Re-activation cancels pending deactivation.
        bridge.apply_observed_state(True, now=10.39)
        bridge.apply_observed_state(False, now=10.5)
        bridge.apply_observed_state(False, now=10.89)
        self.assertTrue(bridge.is_active())

        # Past debounce transitions to inactive and clears buffer.
        bridge.buffer.push(b"\x01\x02" * 100)
        bridge.apply_observed_state(False, now=10.91)
        self.assertFalse(bridge.is_active())
        self.assertEqual(bridge.buffer.pop_frame(4), b"\x00\x00\x00\x00")
        lines = [json.loads(line) for line in stdout.getvalue().splitlines()]
        self.assertEqual(lines, [{"demand": True}, {"demand": False}])

    def test_ring_buffer_bounds_and_alignment(self):
        ring = MOD.PCMRingBuffer(max_bytes=10)
        ring.push(b"\x01\x00\x02\x00\x03\x00")
        ring.push(b"\x04\x00\x05\x00\x06\x00")
        # Kept latest 10 bytes (samples 2..6).
        self.assertEqual(ring.pop_frame(10), b"\x02\x00\x03\x00\x04\x00\x05\x00\x06\x00")
        # Empty buffer produces silence.
        self.assertEqual(ring.pop_frame(4), b"\x00\x00\x00\x00")

    def test_end_to_end_fifo_streaming_and_silence_fill(self):
        with tempfile.TemporaryDirectory() as tmp:
            fifo_path = os.path.join(tmp, "input.fifo")
            os.mkfifo(fifo_path, 0o600)
            read_fd = os.open(fifo_path, os.O_RDWR | os.O_NONBLOCK)
            stdin_r, stdin_w = os.pipe()
            stdout = io.StringIO()
            state = {"active": False}

            stdin_file = os.fdopen(stdin_r, "rb", buffering=0)
            bridge = MOD.AudioInputBridge(
                source_name="alsa_input.test",
                fifo_path=fifo_path,
                query_fn=lambda: state["active"],
                stdin=stdin_file,
                stdout=stdout,
                debounce_seconds=0.06,
                frame_interval=0.01,
            )

            runner = threading.Thread(target=bridge.run, kwargs={"start_subscriber": False}, daemon=True)
            runner.start()
            try:
                # Idle: nothing emitted, nothing written to FIFO.
                time.sleep(0.05)
                self.assertEqual(stdout.getvalue(), "")
                with self.assertRaises(BlockingIOError):
                    os.read(read_fd, 64)

                # Guest starts recording: demand=true emitted and silence fills FIFO.
                state["active"] = True
                bridge.check_event.set()
                deadline = time.monotonic() + 1.0
                while '{"demand":true}' not in stdout.getvalue() and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertIn('{"demand":true}', stdout.getvalue())

                # Send a real PCM frame from host.
                pcm = struct.pack("<h", 12345) * (MOD.FRAME_BYTES // 2)
                os.write(stdin_w, struct.pack(">H", len(pcm)) + pcm)

                received = bytearray()
                deadline = time.monotonic() + 1.0
                while pcm not in received and time.monotonic() < deadline:
                    try:
                        chunk = os.read(read_fd, 8192)
                        if chunk:
                            received.extend(chunk)
                    except BlockingIOError:
                        time.sleep(0.01)
                self.assertIn(pcm, received)

                # Guest stops recording: demand=false emitted after debounce.
                state["active"] = False
                bridge.check_event.set()
                deadline = time.monotonic() + 1.0
                while '{"demand":false}' not in stdout.getvalue() and time.monotonic() < deadline:
                    time.sleep(0.01)
                self.assertIn('{"demand":false}', stdout.getvalue())
            finally:
                os.close(stdin_w)
                runner.join(timeout=2.0)
                stdin_file.close()
                os.close(read_fd)
            self.assertFalse(runner.is_alive())


if __name__ == "__main__":
    unittest.main()
