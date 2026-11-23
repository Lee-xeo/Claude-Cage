const maxSocketBackpressure = 32768;

export function classifyMicrophoneError(error) {
  const name = error?.name || "";
  if (name === "NotAllowedError" || name === "PermissionDeniedError" || name === "SecurityError") {
    return {
      state: "denied",
      message: "麦克风权限未授予，请在系统设置中允许 Cage 使用麦克风后点击重试",
    };
  }
  if (name === "NotFoundError" || name === "DevicesNotFoundError") {
    return {
      state: "no-device",
      message: "未检测到可用的本机麦克风",
    };
  }
  return {
    state: "error",
    message: "麦克风采集未能启动，请检查本机音频设备后重试",
  };
}

export class AudioInput {
  constructor(onState = () => {}, onError = () => {}, deps = {}) {
    this.onState = onState;
    this.onError = onError;
    this.deps = {
      api: deps.api,
      socketURL: deps.socketURL,
      workletURL: deps.workletURL,
      WebSocket: deps.WebSocket ?? globalThis.WebSocket,
      AudioContext: deps.AudioContext ?? globalThis.AudioContext,
      AudioWorkletNode: deps.AudioWorkletNode ?? globalThis.AudioWorkletNode,
      getMediaDevices: deps.getMediaDevices ?? (() => globalThis.navigator?.mediaDevices),
    };
    this.active = false;
    this.disposed = false;
    this.demand = false;
    this.state = "idle";
    this.connectionGen = 0;
    this.captureGen = 0;
    this.opening = false;
    this.socket = null;
    this.retryTimer = null;
    this.stream = null;
    this.context = null;
    this.source = null;
    this.node = null;
    this.silence = null;
  }

  _publish(state, error = "") {
    this.state = state;
    this.onState({ demand: this.demand, state });
    this.onError(error);
  }

  setActive(active) {
    if (this.disposed || this.active === active) return;
    this.active = active;
    if (active) this.connect();
    else this.disconnect();
  }

  disconnect() {
    this.connectionGen++;
    clearTimeout(this.retryTimer);
    this.retryTimer = null;
    this.opening = false;
    const socket = this.socket;
    this.socket = null;
    if (socket) {
      socket.onclose = null;
      socket.onerror = null;
      socket.onmessage = null;
      socket.close();
    }
    this.demand = false;
    this.stopCapture();
    this._publish("idle", "");
  }

  async connect() {
    if (!this.active || this.disposed || this.socket || this.opening) return;
    clearTimeout(this.retryTimer);
    this.retryTimer = null;
    this.opening = true;
    const generation = this.connectionGen;
    const live = () => this.active && !this.disposed && generation === this.connectionGen;

    const scheduleReconnect = (delayMs = 2500) => {
      if (!live()) return;
      this.opening = false;
      this.socket = null;
      this.demand = false;
      this.stopCapture();
      this._publish("idle", "");
      this.retryTimer = setTimeout(() => this.connect(), delayMs);
    };

    try {
      const format = await this.deps.api("/audio/input");
      if (!live()) return;
      if (format.format !== "s16le" || format.sample_rate !== 48000 || format.channels !== 1) {
        this.opening = false;
        return;
      }
      const socket = new this.deps.WebSocket(this.deps.socketURL("/audio/input"));
      socket.binaryType = "arraybuffer";
      this.socket = socket;
      let confirmed = false;

      socket.onmessage = ({ data }) => {
        if (!live() || typeof data !== "string") return;
        let payload;
        try {
          payload = JSON.parse(data);
        } catch {
          socket.close();
          return;
        }
        if (!confirmed) {
          confirmed = payload.format === "s16le" && payload.sample_rate === 48000 && payload.channels === 1;
          if (!confirmed) {
            socket.close();
            return;
          }
          this._applyDemand(payload.demand === true);
          return;
        }
        if (typeof payload.demand === "boolean") {
          this._applyDemand(payload.demand);
        }
      };

      socket.onclose = () => scheduleReconnect(2500);
      socket.onerror = () => socket.close();
      this.opening = false;
    } catch (error) {
      if (!live()) return;
      // Older remote servers without /api/audio/input return 404: stay quiet.
      if (error?.status === 404) {
        this.opening = false;
        return;
      }
      scheduleReconnect(error?.status === 409 ? 10000 : 2500);
    }
  }

  _applyDemand(demand) {
    if (this.demand === demand) return;
    this.demand = demand;
    if (demand) {
      void this.startCapture();
    } else {
      this.stopCapture();
      this._publish("idle", "");
    }
  }

  retry() {
    if (this.disposed || !this.active || !this.demand) return;
    if (this.state === "capturing") return;
    void this.startCapture();
  }

  async startCapture() {
    this.stopCapture();
    if (!this.active || !this.demand || this.disposed) return;
    const generation = ++this.captureGen;
    const live = () => this.active && this.demand && !this.disposed && generation === this.captureGen;

    const mediaDevices = this.deps.getMediaDevices();
    if (!mediaDevices || typeof mediaDevices.getUserMedia !== "function" || typeof this.deps.AudioContext !== "function") {
      this._publish("unsupported", "当前窗口不支持麦克风采集");
      return;
    }

    this._publish("starting", "");
    let stream = null;
    try {
      stream = await mediaDevices.getUserMedia({
        audio: {
          echoCancellation: true,
          noiseSuppression: true,
          autoGainControl: true,
          channelCount: 1,
        },
        video: false,
      });
      if (!live()) {
        for (const track of stream.getTracks()) track.stop();
        return;
      }
      this.stream = stream;
      for (const track of stream.getTracks()) {
        track.onended = () => {
          if (!live()) return;
          this.stopCapture();
          this._publish("disconnected", "本机麦克风已断开，请检查设备后点击重试");
        };
      }

      const context = new this.deps.AudioContext({ latencyHint: "interactive" });
      this.context = context;
      if (typeof context.resume === "function") {
        await context.resume().catch(() => {});
      }
      await context.audioWorklet.addModule(this.deps.workletURL);
      if (!live()) {
        this.stopCapture();
        return;
      }

      const source = context.createMediaStreamSource(stream);
      const node = new this.deps.AudioWorkletNode(context, "cage-audio-input", {
        numberOfInputs: 1,
        numberOfOutputs: 1,
        channelCount: 1,
      });
      const silence = context.createGain();
      silence.gain.value = 0;

      node.port.onmessage = ({ data }) => {
        if (!live() || !(data instanceof ArrayBuffer)) return;
        const socket = this.socket;
        if (!socket || socket.readyState !== 1) return;
        if (typeof socket.bufferedAmount === "number" && socket.bufferedAmount >= maxSocketBackpressure) return;
        try {
          socket.send(data);
        } catch {
          /* Reconnect is handled by socket onclose. */
        }
      };

      source.connect(node);
      node.connect(silence);
      silence.connect(context.destination);

      this.source = source;
      this.node = node;
      this.silence = silence;
      this._publish("capturing", "");
    } catch (error) {
      const stillLive = live();
      if (stream) {
        for (const track of stream.getTracks()) track.stop();
      }
      this.stopCapture();
      if (!stillLive) return;
      const classified = classifyMicrophoneError(error);
      this._publish(classified.state, classified.message);
    }
  }

  stopCapture() {
    this.captureGen++;
    if (this.node?.port) {
      this.node.port.onmessage = null;
    }
    try { this.source?.disconnect(); } catch { /* ignore */ }
    try { this.node?.disconnect(); } catch { /* ignore */ }
    try { this.silence?.disconnect(); } catch { /* ignore */ }
    this.source = null;
    this.node = null;
    this.silence = null;
    if (this.stream) {
      for (const track of this.stream.getTracks()) {
        track.onended = null;
        try { track.stop(); } catch { /* ignore */ }
      }
      this.stream = null;
    }
    if (this.context) {
      const ctx = this.context;
      this.context = null;
      ctx.close?.().catch?.(() => {});
    }
  }

  close() {
    this.disposed = true;
    this.active = false;
    this.disconnect();
  }
}
