// Public creation preferences only. No arbitrary command-line flags, paths,
// executables or network options cross this boundary.
export function checkedBrowserProfile(input) {
  if (!input || typeof input !== "object" || Array.isArray(input) ||
      Object.keys(input).some(key => !["browser_width", "browser_height", "webgl", "canvas"].includes(key)) ||
      !Number.isInteger(input.browser_width) || input.browser_width < 800 || input.browser_width > 3840 ||
      !Number.isInteger(input.browser_height) || input.browser_height < 600 || input.browser_height > 2160 ||
      !["native", "disabled"].includes(input.webgl) || !["native", "blocked"].includes(input.canvas))
    throw Object.assign(new Error("客机浏览器创建配置无效"), { code: "invalid_configuration", safe: true });
  return { browser_width: input.browser_width, browser_height: input.browser_height, webgl: input.webgl, canvas: input.canvas };
}

export function browserProfileFlags(input) {
  const value = checkedBrowserProfile(input);
  return [`--window-size=${value.browser_width},${value.browser_height}`,
    ...(value.webgl === "disabled" ? ["--disable-3d-apis", "--disable-webgl"] : []),
    ...(value.canvas === "blocked" ? ["--disable-reading-from-canvas"] : [])];
}

export async function* lines(input, limit = 128 << 10) {
  let buffer = Buffer.alloc(0);
  for await (const chunk of input) {
    buffer = Buffer.concat([buffer, chunk]);
    let end;
    while ((end = buffer.indexOf(10)) >= 0) {
      if (end > limit) throw Object.assign(new Error("浏览器请求过大"), { code: "message_limit", safe: true });
      yield buffer.subarray(0, end).toString("utf8");
      buffer = buffer.subarray(end + 1);
    }
    if (buffer.length > limit) throw Object.assign(new Error("浏览器请求过大"), { code: "message_limit", safe: true });
  }
  if (buffer.length) throw Object.assign(new Error("浏览器请求没有完整收到"), { code: "message_invalid", safe: true });
}
