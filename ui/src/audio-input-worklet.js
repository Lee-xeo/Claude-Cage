// Standalone AudioWorklet module: Vite emits this file without dependencies.
// Resamples host microphone input from any native sample rate into 48 kHz mono
// signed 16-bit little-endian PCM in 20 ms (960-sample, 1920-byte) frames.
export class PCMCaptureResampler {
  constructor(targetRate = 48000, frameSamples = 960) {
    this.targetRate = targetRate;
    this.frameSamples = frameSamples;
    this.reset();
  }
  reset() {
    this.frame = new Int16Array(this.frameSamples);
    this.offset = 0;
    this.phase = 0;
    this.hasLast = false;
    this.lastSample = 0;
  }
  push(input, inputRate) {
    if (!input || !input.length || !Number.isFinite(inputRate) || inputRate <= 0) return [];
    const step = inputRate / this.targetRate;
    const packets = [];
    const count = input.length;
    if (!this.hasLast) {
      this.lastSample = input[0];
      this.hasLast = true;
    }
    let pos = this.phase;
    while (pos < count) {
      const idx = Math.floor(pos);
      const frac = pos - idx;
      const a = idx < 0 ? this.lastSample : input[idx];
      const b = idx + 1 < count ? input[idx + 1] : input[count - 1];
      let sample = a + (b - a) * frac;
      if (sample > 1) sample = 1;
      else if (sample < -1) sample = -1;
      this.frame[this.offset++] = sample < 0 ? Math.round(sample * 32768) : Math.round(sample * 32767);
      if (this.offset === this.frameSamples) {
        packets.push(this._emitFrame());
        this.offset = 0;
      }
      pos += step;
    }
    this.lastSample = input[count - 1];
    this.phase = pos - count;
    return packets;
  }
  _emitFrame() {
    const buffer = new ArrayBuffer(this.frameSamples * 2);
    const view = new DataView(buffer);
    for (let i = 0; i < this.frameSamples; i++) {
      view.setInt16(i * 2, this.frame[i], true);
    }
    return buffer;
  }
}

if (typeof globalThis.registerProcessor === "function") {
  class CageAudioInputProcessor extends globalThis.AudioWorkletProcessor {
    constructor() {
      super();
      this.resampler = new PCMCaptureResampler();
      this.port.onmessage = ({ data }) => {
        if (data === "reset") this.resampler.reset();
      };
    }
    process(inputs) {
      const channel = inputs[0]?.[0];
      if (channel && channel.length) {
        const packets = this.resampler.push(channel, globalThis.sampleRate);
        for (const packet of packets) {
          this.port.postMessage(packet, [packet]);
        }
      }
      return true;
    }
  }
  globalThis.registerProcessor("cage-audio-input", CageAudioInputProcessor);
}
