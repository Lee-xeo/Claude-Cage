#!/bin/sh
set -eu
login="${LOGIN_ID:-}"
route=""
account=""
while [ "$#" -ge 3 ]; do
  case "$1" in
    --login-id|--account-id|--route-id) ;;
    *) exit 2 ;;
  esac
  case "$2" in ''|*[!a-f0-9]*) exit 2 ;; esac
  [ "${#2}" = 24 ] || exit 2
  case "$1" in
    --login-id) login="$2";;
    --account-id) account="$2";;
    --route-id) route="$2";;
  esac
  shift 2
done
if [ "${1:-}" = --stdin ]; then set -- "$(cat)"; fi
case "${1:-}" in
  --finish-login)
    [ -n "$login" ] || exit 2
    set -- /login-finish "" "$login"
    ;;
  tailscale:*)
    if [ "$(id -un)" = cage ]; then exec /usr/local/bin/tailnet --browser-return; fi
    exec sudo -n -H -u cage /usr/local/bin/tailnet --browser-return
    ;;
  claude://*)
    sudo -u cage /usr/local/libexec/claude-desktop-open "$1"
    # Publish focus only after the running application accepted the handoff.
    # The browser service never receives the login URL or its credentials.
    set -- /desktop-focus
    ;;
  http://*|https://*|about:blank)
    if [ -z "$route$account$login" ] && [ -s "${XDG_RUNTIME_DIR:-/nonexistent}/account-scope" ]; then
      read -r owner mode _ < "$XDG_RUNTIME_DIR/account-scope"
      case "$owner" in ''|*[!a-f0-9]*) exit 2 ;; esac
      [ "${#owner}" = 24 ] || exit 2
      account=$owner
      if [ "$mode" = node ]; then
        printf '%s' "$1" | "/usr/local/lib/account-routes/$owner/open" --account-id "$owner" --stdin
        exit $?
      fi
    fi
    if [ -n "$login" ]; then
      set -- /login-open "$1" "$login"
    else
      set -- /new "$1"
    fi
    ;;
  *)
    # Links of a scheme a plugin handles go to the workbench, which hands them
    # to the plugin. The workbench keeps the list of those schemes.
    scheme=$(printf '%s' "${1:-}" | sed -n 's/^\([A-Za-z][A-Za-z0-9+.-]*\):.*/\1/p' | tr 'A-Z' 'a-z')
    if [ -n "$scheme" ] && grep -qxF "$scheme" /usr/local/lib/plugin-links 2>/dev/null; then
      set -- /plugin-link "$1"
    else
      # File operations belong to the desktop session, never the browser control
      # token. The host resolves them only within Cage project directories.
      exec /usr/local/libexec/desktop-files --open "${1:-}"
    fi
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
const {token}=JSON.parse(readFileSync("/etc/workbench/browser-control.json","utf8"));
const response=await fetch("http://127.0.0.1:18500"+process.argv[1],{method:"POST",headers:{Authorization:"Bearer "+token,"Content-Type":"application/json"},body:JSON.stringify({url:readFileSync(0,"utf8"),login:process.argv[2],route:process.argv[3] || undefined,account:process.argv[4] || undefined})});
if(!response.ok)process.exit(1);
' "$1" "${3:-}" "$route" "$account"
