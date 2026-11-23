import assert from "node:assert/strict";
import test from "node:test";
import { PCMCaptureResampler } from "./audio-input-worklet.js";

test("PCMCaptureResampler converts 48 kHz mono float samples to 20 ms s16le packets", () => {
  const resampler = new PCMCaptureResampler(48000, 960);
  const block = new Float32Array(960);
  block[0] = 0;
  block[1] = 1.5;   // clamped to +1 -> 32767
  block[2] = -1.5;  // clamped to -1 -> -32768
  block[3] = 0.5;   // ~16384

  const packets = resampler.push(block, 48000);
  assert.equal(packets.length, 1);
  assert.equal(packets[0].byteLength, 1920);
  const view = new DataView(packets[0]);
  assert.equal(view.getInt16(0, true), 0);
  assert.equal(view.getInt16(2, true), 32767);
  assert.equal(view.getInt16(4, true), -32768);
  assert.equal(view.getInt16(6, true), 16384);
});

test("PCMCaptureResampler resamples 44.1 kHz input cleanly across 128-sample blocks", () => {
  const resampler = new PCMCaptureResampler(48000, 960);
  // Push 4410 samples (0.1 s at 44.1 kHz) of constant 0.25 -> should yield 5 packets (4800 samples).
  const packets = [];
  for (let offset = 0; offset < 4410; offset += 128) {
    const len = Math.min(128, 4410 - offset);
    const chunk = new Float32Array(len).fill(0.25);
    packets.push(...resampler.push(chunk, 44100));
  }
  assert.equal(packets.length, 5);
  for (const packet of packets) {
    assert.equal(packet.byteLength, 1920);
    const view = new DataView(packet);
    assert.ok(Math.abs(view.getInt16(0, true) - 8192) <= 1);
    assert.ok(Math.abs(view.getInt16(1918, true) - 8192) <= 1);
  }
});
