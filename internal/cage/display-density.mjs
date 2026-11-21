import { spawn } from "node:child_process";
import { existsSync, mkdirSync, renameSync, writeFileSync } from "node:fs";
import path from "node:path";

// Chromium applies an XSETTINGS density in steps of 1/64.
export function displayDensity(value) {
  return typeof value === "number" && value >= 0.75 && value <= 4 ? Math.round(value * 64) / 64 : null;
}

export function densitySettings(scale, theme = "") {
  const dpi = Math.round(scale * 96 * 1024);
  let output = `Xft/DPI ${dpi}\nGdk/WindowScalingFactor 1\nGdk/UnscaledDPI ${dpi}\n`;
  if (theme === "dark") output += 'Net/ThemeName "Adwaita-dark"\nGtk/ApplicationPreferDarkTheme 1\n';
  else if (theme === "light") output += 'Net/ThemeName "Adwaita"\nGtk/ApplicationPreferDarkTheme 0\n';
  return output;
}

// A display's pixel density is an XSETTINGS value, and Chromium follows a
// change without restarting. Each viewer can then have the density of its own
// screen. Without xsettingsd the display keeps the density it started with.
export function createDisplaySettings({
  file, initial, initialTheme = "", available = existsSync("/usr/bin/xsettingsd"),
  start = () => spawn("/usr/bin/xsettingsd", ["--config=" + file], { stdio: "ignore" }), restart = 1000,
}) {
  let scale = initial, theme = initialTheme === "light" || initialTheme === "dark" ? initialTheme : "", child, timer, closed = false;
  const write = () => {
    writeFileSync(file + ".new", densitySettings(scale, theme));
    renameSync(file + ".new", file);
  };
  const run = () => {
    const current = child = start();
    // Applications fall back to 96 DPI while no manager owns the settings.
    const ended = () => {
      if (child !== current) return;
      child = undefined;
      if (!closed) timer = setTimeout(run, restart);
    };
    current.on("error", ended);
    current.on("exit", ended);
  };
  if (available) {
    mkdirSync(path.dirname(file), { recursive: true });
    write();
    run();
  }
  return {
    available,
    get scale() { return scale; },
    get theme() { return theme; },
    set(value) {
      if (!available || value === scale) return scale;
      scale = value;
      write();
      // A manager that is being restarted reads the new file when it starts.
      child?.kill("SIGHUP");
      return scale;
    },
    setTheme(value) {
      const next = value === "light" || value === "dark" ? value : "";
      if (!available || next === theme) return theme;
      theme = next;
      write();
      child?.kill("SIGHUP");
      return theme;
    },
    close() {
      closed = true;
      clearTimeout(timer);
      child?.kill();
    },
  };
}
