import assert from "node:assert/strict";
import test from "node:test";
import { AudioInput } from "./audio-input-controller.mjs";

const flush = () => new Promise(resolve => setImmediate(resolve));

function createHarness({ getUserMedia } = {}) {
  const sockets = [];
  const tracks = [];
  let mediaRequests = 0;
  let lastWorkletNode = null;

  class FakeWebSocket {
    constructor(url) {
      this.url = url;
      this.readyState = 1;
      this.bufferedAmount = 0;
      this.sent = [];
      this.closed = false;
      sockets.push(this);
    }
    send(data) { this.sent.push(data); }
    close() {
      if (this.closed) return;
      this.closed = true;
      this.readyState = 3;
      this.onclose?.();
    }
  }

  class FakeAudioContext {
    constructor() {
      this.closed = false;
      this.destination = { kind: "destination" };
      this.audioWorklet = { addModule: async () => {} };
    }
    async resume() {}
    createMediaStreamSource(stream) {
      return { stream, connect() {}, disconnect() {} };
    }
    createGain() {
      return { gain: { value: 1 }, connect() {}, disconnect() {} };
    }
    async close() { this.closed = true; }
  }

  class FakeAudioWorkletNode {
    constructor() {
      this.port = { onmessage: null };
      lastWorkletNode = this;
    }
    connect() {}
    disconnect() {}
  }

  const states = [];
  const errors = [];
  const input = new AudioInput(
    value => states.push(value),
    message => errors.push(message),
    {
      api: async () => ({ format: "s16le", sample_rate: 48000, channels: 1 }),
      socketURL: path => "ws://localhost:17777/api" + path,
      workletURL: "/assets/audio-input-worklet.js",
      WebSocket: FakeWebSocket,
      AudioContext: FakeAudioContext,
      AudioWorkletNode: FakeAudioWorkletNode,
      getMediaDevices: () => ({
        getUserMedia: async constraints => {
          mediaRequests++;
          if (getUserMedia) return getUserMedia(constraints);
          const track = {
            stopped: false,
            onended: null,
            stop() { this.stopped = true; },
          };
          tracks.push(track);
          return { getTracks: () => [track] };
        },
      }),
    },
  );

  return {
    input,
    states,
    errors,
    sockets,
    tracks,
    get mediaRequests() { return mediaRequests; },
    get lastWorkletNode() { return lastWorkletNode; },
  };
}

test("AudioInput stays idle without requesting microphone until guest demands recording", async () => {
  const h = createHarness();
  try {
    h.input.setActive(true);
    await flush();
    assert.equal(h.sockets.length, 1);
    const ws = h.sockets[0];

    ws.onmessage({ data: JSON.stringify({ format: "s16le", sample_rate: 48000, channels: 1, demand: false }) });
    await flush();
    assert.equal(h.mediaRequests, 0, "must not touch host microphone while idle");

    // Guest opens microphone -> host starts capture and forwards PCM.
    ws.onmessage({ data: JSON.stringify({ demand: true }) });
    await flush();
    assert.equal(h.mediaRequests, 1);
    assert.equal(h.tracks.length, 1);
    assert.equal(h.tracks[0].stopped, false);
    assert.deepEqual(h.states.at(-1), { demand: true, state: "capturing" });

    const frame = new ArrayBuffer(1920);
    h.lastWorkletNode.port.onmessage({ data: frame });
    assert.equal(ws.sent.length, 1);
    assert.equal(ws.sent[0], frame);

    // Guest closes microphone -> host stops track immediately.
    ws.onmessage({ data: JSON.stringify({ demand: false }) });
    await flush();
    assert.equal(h.tracks[0].stopped, true);
    assert.deepEqual(h.states.at(-1), { demand: false, state: "idle" });
  } finally {
    h.input.close();
  }
});

test("AudioInput reports permission denial and allows manual retry while demand remains active", async () => {
  let attempt = 0;
  const h = createHarness({
    getUserMedia: async () => {
      attempt++;
      if (attempt === 1) {
        const err = new Error("denied");
        err.name = "NotAllowedError";
        throw err;
      }
      const track = { stopped: false, onended: null, stop() { this.stopped = true; } };
      return { getTracks: () => [track] };
    },
  });
  try {
    h.input.setActive(true);
    await flush();
    const ws = h.sockets[0];
    ws.onmessage({ data: JSON.stringify({ format: "s16le", sample_rate: 48000, channels: 1, demand: true }) });
    await flush();
    assert.deepEqual(h.states.at(-1), { demand: true, state: "denied" });
    assert.match(h.errors.at(-1), /麦克风权限/);

    h.input.retry();
    await flush();
    assert.deepEqual(h.states.at(-1), { demand: true, state: "capturing" });
    assert.equal(h.errors.at(-1), "");
  } finally {
    h.input.close();
  }
});
