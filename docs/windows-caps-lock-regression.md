# Windows Caps Lock synchronization regression

## Failure and fix

The Windows native keyboard bridge previously forwarded Shift/Ctrl/Alt but omitted
`modifierCapsLock`. The shared remote-input bridge constructs a `KeyboardEvent`
from that payload, so `getModifierState("CapsLock")` defaults to false. When noVNC
receives a remote LED-state update reporting Caps Lock on, it sends a compensating
Caps Lock press to turn it off. This explains why Shift can produce uppercase
letters while Caps Lock does not stay enabled.

`NativeInput.Key` now includes `IsKeyLocked(Keys.CapsLock)` on both keydown and
keyup. It preserves the existing scan-code mapping and does not change noVNC,
guest services, user configuration, or the clipboard. The macOS native bridge
already supplies this field; Linux receives native WebKit keyboard events and
does not use the Windows synthetic-event path.

## Native regression

On Windows with the .NET SDK required by the project:

```powershell
dotnet run --project desktop/windows.tests/Cage.Windows.Tests.csproj
```

The input test invokes the production control callbacks in a dedicated STA thread
with a hidden form. It checks Caps Lock on/off, Shift combinations, printable
and command keys, and keydown/keyup payloads. It uses `SetKeyboardState` only for
that test thread, restores the original state, and does not use `SendInput`, attach
to another input queue, change the physical keyboard LED, or touch a real session.

Other platforms continue to use the existing non-WinForms target. That path can
also be checked on Windows:

```powershell
dotnet run --project desktop/windows.tests/Cage.Windows.Tests.csproj -p:WindowsNativeInputTests=false
```

## Browser/protocol regression

The smoke test needs Node.js, `uv`, Playwright and a Chromium executable. Playwright
is not a dependency in `ui/package.json`: supply an existing installation through
`CAGE_PLAYWRIGHT_MODULE` (module name or absolute package directory), or install it
in a separate test environment. Set `CAGE_TEST_BROWSER_PATH` to the desired
Chromium/Edge executable; otherwise Playwright uses its installed Chromium.

From the repository root, prepare the production noVNC modules and run:

```powershell
npm run --prefix ui prepare-vnc
node ui/scripts/caps-lock-smoke.mjs
```

The fixture uses real Chromium `KeyboardEvent`s, production remote-input/noVNC,
and final RFB/QEMU key encoding with an in-memory transport. It covers the missing
field as a negative control, on/off state preservation, stale remote state,
Caps Lock toggles followed by LED replies, Shift and Caps Lock+Shift combinations,
and the existing macOS payload shape. All page requests are served from local
source modules or rejected. Clipboard revision is a constant stub; no real
clipboard, user session, VM, or external WebSocket is accessed.

## Runtime acceptance boundary

These tests do not replace testing a physical keyboard against a real remote
input field. Check Caps Lock off/on/off (`abc` / `ABC` / `abc`), Shift uppercase,
and Caps Lock+Shift lowercase after reconnecting. The macOS browser case simulates
the native payload in Chromium; it is not a macOS/WebKit or Linux native-client
runtime acceptance test.
