#!/bin/sh
set -eu
login="${CAGE_CLAUDE_LOGIN:-}"
if [ "${1:-}" = --login-id ]; then
  [ "$#" -ge 3 ] || exit 2
  login="$2"
  case "$login" in ''|*[!a-f0-9]*) exit 2 ;; esac
  [ "${#login}" = 24 ] || exit 2
  shift 2
fi
case "${1:-}" in
  --finish-login)
    [ -n "$login" ] || exit 2
    set -- /login-finish "" "$login"
    ;;
  tailscale:*)
    if [ "$(id -un)" = cage ]; then exec /usr/local/bin/cage-tailnet --browser-return; fi
    exec sudo -n -H -u cage /usr/local/bin/cage-tailnet --browser-return
    ;;
  claude://*)
    sudo -u cage /usr/local/bin/cage-claude-open "$1"
    # Publish focus only after the running application accepted the handoff.
    # The browser service never receives the login URL or its credentials.
    set -- /desktop-focus
    ;;
  http://*|https://*|about:blank)
    if [ -n "$login" ]; then
      set -- /login-open "$1" "$login"
    else
      set -- /new "$1"
    fi
    ;;
  *)
    # File operations belong to the desktop session, never the browser control
    # token. The host resolves them only within Cage project directories.
    exec /usr/local/bin/cage-desktop-files --open "${1:-}"
    ;;
esac
# sudo records its arguments in the system journal. Send the URL on stdin.
browser_request() {
  # Chromium already runs as cage-browser, whose sudo rules intentionally do
  # not allow running arbitrary commands (even as itself).
  if [ "$(id -un)" = cage-browser ]; then
    /usr/bin/node "$@"
  else
    sudo -u cage-browser /usr/bin/node "$@"
  fi
}
printf '%s' "${2:-}" | browser_request --input-type=module -e '
import {readFileSync} from "node:fs";
const {token}=JSON.parse(readFileSync("/etc/claude-cage/browser-control.json","utf8"));
const response=await fetch("http://127.0.0.1:18500"+process.argv[1],{method:"POST",headers:{Authorization:"Bearer "+token,"Content-Type":"application/json"},body:JSON.stringify({url:readFileSync(0,"utf8"),login:process.argv[2]})});
if(!response.ok)process.exit(1);
' "$1" "${3:-}"
