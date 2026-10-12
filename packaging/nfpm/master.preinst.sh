#!/bin/sh
set -eu

# rpm upgrade ($1 = 2): %pre runs before the old package's %preun, so record
# whether nut-master is enabled and running; %posttrans restores that state after
# the old package's scripts have run.
STATE_FILE=/run/nut-master.upgrade-state

command -v systemctl >/dev/null 2>&1 || exit 0
rm -f "$STATE_FILE"

if [ "${1:-}" = "2" ]; then
  : > "$STATE_FILE"
  if systemctl is-enabled --quiet nut-master; then
    echo enabled >> "$STATE_FILE"
  fi
  if systemctl is-active --quiet nut-master; then
    echo active >> "$STATE_FILE"
  fi
fi
