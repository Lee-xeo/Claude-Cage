// Key events for a Linux guest whose X server takes them straight from the
// viewer. Such a server follows each event literally: it repeats nothing by
// itself, presses again a key that is already down, and adds or drops Shift
// to produce exactly the symbol it was given. A physical keyboard on the
// guest behaves otherwise, and so did the screen-scraping server used before,
// so the three differences are settled here, before the events are sent.

// X11 defaults of the guest display.
const REPEAT_DELAY = 660;
const REPEAT_INTERVAL = 40;

const SHIFT = new Set([0xffe1, 0xffe2]);
// Control, Meta, Alt, Super and Hyper: a key held with one of them is a shortcut.
const CHORD = new Set([0xffe3, 0xffe4, 0xffe7, 0xffe8, 0xffe9, 0xffea, 0xffeb, 0xffec, 0xffed, 0xffee]);
// Keys that never repeat: every modifier and lock.
const quiet = keysym => (keysym >= 0xffe1 && keysym <= 0xffee) ||
  [0xff7f, 0xff14, 0xff7e, 0xfe03, 0xfe08, 0xfe11].includes(keysym);

// The two symbols of each key that carries no letter, on the guest's US layout.
const LEVELS = {
  Backquote: "`~", Digit1: "1!", Digit2: "2@", Digit3: "3#", Digit4: "4$", Digit5: "5%", Digit6: "6^",
  Digit7: "7&", Digit8: "8*", Digit9: "9(", Digit0: "0)", Minus: "-_", Equal: "=+", BracketLeft: "[{",
  BracketRight: "]}", Backslash: "\\|", Semicolon: ";:", Quote: "'\"", Comma: ",<", Period: ".>", Slash: "/?",
};

// In a shortcut the held Shift decides which symbol of the key is meant. Hosts
// disagree here: macOS reports a lowercase letter for Command+Shift+Z, and a
// host with Caps Lock on reports an uppercase one for Control+C.
function shortcutKeysym(keysym, code, shift) {
  if (keysym >= 0x61 && keysym <= 0x7a) return shift ? keysym - 0x20 : keysym;
  if (keysym >= 0x41 && keysym <= 0x5a) return shift ? keysym : keysym + 0x20;
  const levels = LEVELS[code];
  if (shift && levels && keysym === levels.charCodeAt(0)) return levels.charCodeAt(1);
  return keysym;
}

export function guestKeys(send, timers = globalThis) {
  // Sent keysym of every key that is down, by physical key where one is known.
  const down = new Map();
  let repeating = null, timer = null;
  const identity = (keysym, code) => code || `keysym:${keysym}`;
  const held = set => [...down.values()].some(keysym => set.has(keysym));
  const quietNow = () => {
    if (timer !== null) timers.clearTimeout(timer);
    timer = repeating = null;
  };
  const repeat = (id, keysym, code, delay) => {
    repeating = id;
    timer = timers.setTimeout(() => {
      if (repeating !== id || !down.has(id)) return;
      send(keysym, code, true);
      repeat(id, keysym, code, REPEAT_INTERVAL);
    }, delay);
  };
  const sendKey = (keysym, code, isDown) => {
    if (isDown === undefined) {
      sendKey(keysym, code, true);
      sendKey(keysym, code, false);
      return;
    }
    const id = identity(keysym, code);
    if (!isDown) {
      if (repeating === id) quietNow();
      // Release what was pressed, whatever Shift has done since.
      const pressed = down.get(id);
      down.delete(id);
      send(pressed ?? keysym, code, false);
      return;
    }
    // The host repeats a held key at its own pace, or not at all.
    if (down.has(id)) return;
    const sent = keysym && held(CHORD) ? shortcutKeysym(keysym, code, held(SHIFT)) : keysym;
    down.set(id, sent);
    send(sent, code, true);
    if (!keysym || quiet(keysym)) return;
    // As on a keyboard, only the key pressed last repeats.
    quietNow();
    repeat(id, sent, code, REPEAT_DELAY);
  };
  return {
    sendKey,
    stop() {
      quietNow();
      down.clear();
    },
  };
}
