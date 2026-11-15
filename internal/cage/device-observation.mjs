// Self-contained: run only in an isolated local diagnostic document. No page
// URLs, account data, device IDs, serials, cookies or permissions are collected.
export async function collectDeviceObservation() {
  const result = {
    user_agent: navigator.userAgent,
    platform: navigator.platform,
    client_hints: navigator.userAgentData ? {
      brands: navigator.userAgentData.brands.map(item => ({ brand: item.brand, version: item.version })),
      platform: navigator.userAgentData.platform,
      mobile: navigator.userAgentData.mobile,
    } : null,
    screen: { width: screen.width, height: screen.height, color_depth: screen.colorDepth, pixel_ratio: devicePixelRatio },
    viewport: { width: innerWidth, height: innerHeight },
    canvas: { state: "unavailable" },
    webgl: { state: "unavailable", vendor: "", renderer: "", version: "" },
    audio: { state: "unavailable", labels: [] },
    webgpu: Boolean(navigator.gpu),
  };
  try {
    const canvas = document.createElement("canvas"); canvas.width = 96; canvas.height = 32;
    const ctx = canvas.getContext("2d", { willReadFrequently: true });
    if (ctx) {
      ctx.fillStyle = "rgb(17,93,201)"; ctx.fillRect(0, 0, 96, 32);
      const sample = ctx.getImageData(0, 0, 1, 1).data;
      const correct = [17, 93, 201, 255].every((value, index) => sample[index] === value);
      ctx.font = "14px sans-serif"; ctx.fillStyle = "#fff"; ctx.fillText("Render check 0123", 3, 21);
      const pixels = ctx.getImageData(0, 0, 96, 32).data;
      let checksum = 2166136261;
      for (const value of pixels) checksum = Math.imul(checksum ^ value, 16777619) >>> 0;
      result.canvas = { state: correct ? "passed" : "failed", checksum: checksum.toString(16).padStart(8, "0") };
    }
  } catch { result.canvas = { state: "blocked" }; }
  for (const kind of ["webgl2", "webgl"]) {
    let gl;
    try {
      const canvas = document.createElement("canvas"); canvas.width = 4; canvas.height = 4;
      gl = canvas.getContext(kind, { antialias: false, preserveDrawingBuffer: true });
      if (!gl) continue;
      const debug = gl.getExtension("WEBGL_debug_renderer_info");
      result.webgl = {
        state: "failed", api: kind,
        vendor: String(gl.getParameter(debug ? debug.UNMASKED_VENDOR_WEBGL : gl.VENDOR)).slice(0, 256),
        renderer: String(gl.getParameter(debug ? debug.UNMASKED_RENDERER_WEBGL : gl.RENDERER)).slice(0, 512),
        version: String(gl.getParameter(gl.VERSION)).slice(0, 256),
      };
      const program = gl.createProgram(), shaders = [];
      const vertex = kind === "webgl2" ? "#version 300 es\nin vec2 position;void main(){gl_Position=vec4(position,0.0,1.0);}" : "attribute vec2 position;void main(){gl_Position=vec4(position,0.0,1.0);}";
      const fragment = kind === "webgl2" ? "#version 300 es\nprecision mediump float;out vec4 color;void main(){color=vec4(17.0/255.0,93.0/255.0,201.0/255.0,1.0);}" : "precision mediump float;void main(){gl_FragColor=vec4(17.0/255.0,93.0/255.0,201.0/255.0,1.0);}";
      for (const [type, code] of [[gl.VERTEX_SHADER, vertex], [gl.FRAGMENT_SHADER, fragment]]) {
        const shader = gl.createShader(type); shaders.push(shader);
        gl.shaderSource(shader, code); gl.compileShader(shader);
        if (!gl.getShaderParameter(shader, gl.COMPILE_STATUS)) throw Error("shader compile failed");
        gl.attachShader(program, shader);
      }
      gl.linkProgram(program);
      if (!gl.getProgramParameter(program, gl.LINK_STATUS)) throw Error("shader link failed");
      gl.useProgram(program);
      const buffer = gl.createBuffer(); gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
      gl.bufferData(gl.ARRAY_BUFFER, new Float32Array([-1, -1, 1, -1, -1, 1, 1, 1]), gl.STATIC_DRAW);
      const location = gl.getAttribLocation(program, "position");
      gl.enableVertexAttribArray(location); gl.vertexAttribPointer(location, 2, gl.FLOAT, false, 0, 0);
      gl.viewport(0, 0, 4, 4); gl.disable(gl.DITHER); gl.drawArrays(gl.TRIANGLE_STRIP, 0, 4);
      const pixels = new Uint8Array(64); gl.readPixels(0, 0, 4, 4, gl.RGBA, gl.UNSIGNED_BYTE, pixels);
      const expected = [17, 93, 201, 255];
      result.webgl.state = gl.getError() === gl.NO_ERROR && pixels.every((value, index) => Math.abs(value - expected[index % 4]) <= 2) ? "passed" : "failed";
      if (result.webgl.state === "failed" && result.canvas.state === "blocked") result.webgl.state = "readback_blocked";
      gl.deleteBuffer(buffer); for (const shader of shaders) gl.deleteShader(shader); gl.deleteProgram(program);
    } catch { if (gl) result.webgl.state = "failed"; }
    finally { try { gl?.getExtension("WEBGL_lose_context")?.loseContext(); } catch {} }
    if (gl) break;
  }
  try {
    if (navigator.mediaDevices?.enumerateDevices) {
      const devices = await Promise.race([navigator.mediaDevices.enumerateDevices(), new Promise(resolve => setTimeout(() => resolve([]), 1000))]);
      result.audio.labels = devices.filter(item => item.kind === "audiooutput" && item.label).slice(0, 8).map(item => item.label.slice(0, 128));
      result.audio.state = result.audio.labels.length ? "observed" : "permission_required";
    }
  } catch { result.audio.state = "unavailable"; }
  return result;
}
