#!/bin/bash
# Runs as the unit's user before systemd tears down the keyring and display.
set -eu
runtime=$1
unit=$2
if [[ -r "$runtime/app-pid" ]]; then
  read -r app < "$runtime/app-pid"
  if [[ $app =~ ^[1-9][0-9]*$ ]] && grep -Fqx "0::/system.slice/$unit" "/proc/$app/cgroup" 2>/dev/null; then
    kill -TERM "$app" 2>/dev/null || :
    for _ in {1..160}; do
      [[ -d /proc/$app ]] || break
      sleep .05
    done
  fi
fi
