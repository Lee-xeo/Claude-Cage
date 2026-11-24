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

function createReconnectHarness({ api } = {}) {
  const sockets = [];
  const delays = [];
  let pending = null;
  let clock = 0;
  class FakeWebSocket {
    constructor() { sockets.push(this); }
    close() { this.onclose?.(); }
  }
  const input = new AudioInput(() => {}, () => {}, {
    api: api ?? (async () => ({ format: "s16le", sample_rate: 48000, channels: 1 })),
    socketURL: path => "ws://localhost:17777/api" + path,
    WebSocket: FakeWebSocket,
    now: () => clock,
    setTimeout: (callback, delay) => { delays.push(delay); pending = callback; return delays.length; },
    clearTimeout: () => { pending = null; },
  });
  return {
    input,
    sockets,
    delays,
    advance(ms) { clock += ms; },
    // Runs the scheduled reconnect, as the timer would.
    async reconnect() { const callback = pending; pending = null; callback(); await flush(); },
  };
}

test("AudioInput waits longer after each connection the environment refuses", async () => {
  const h = createReconnectHarness();
  try {
    h.input.setActive(true);
    await flush();
    for (let attempt = 0; attempt < 7; attempt++) {
      h.sockets.at(-1).close();
      await h.reconnect();
    }
    assert.deepEqual(h.delays, [2500, 5000, 10000, 20000, 40000, 60000, 60000]);
    assert.equal(h.sockets.length, 8);
  } finally {
    h.input.close();
  }
});

test("AudioInput backs off while another window holds the microphone connection", async () => {
  const h = createReconnectHarness({ api: async () => { throw Object.assign(new Error("occupied"), { status: 409 }); } });
  try {
    h.input.setActive(true);
    await flush();
    for (let attempt = 0; attempt < 3; attempt++) await h.reconnect();
    assert.deepEqual(h.delays, [10000, 20000, 40000, 60000]);
    assert.equal(h.sockets.length, 0, "must not open a socket the server would refuse");
  } finally {
    h.input.close();
  }
});

test("AudioInput reconnects promptly after a working connection drops", async () => {
  const h = createReconnectHarness();
  try {
    h.input.setActive(true);
    await flush();
    h.sockets[0].close();
    await h.reconnect();
    h.sockets[1].onmessage({ data: JSON.stringify({ format: "s16le", sample_rate: 48000, channels: 1, demand: false }) });
    h.advance(60000);
    h.sockets[1].close();
    await h.reconnect();
    h.sockets[2].close();
    assert.deepEqual(h.delays, [2500, 2500, 2500], "a working connection clears earlier failures");

    // Leaving and returning to the environment starts over as well.
    h.input.setActive(false);
    h.input.setActive(true);
    await flush();
    h.sockets.at(-1).close();
    assert.equal(h.delays.at(-1), 2500);
  } finally {
    h.input.close();
  }
});
