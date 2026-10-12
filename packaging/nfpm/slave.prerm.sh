#!/bin/sh
set -eu

# Stop and disable only when the package is removed (deb passes "remove", rpm
# passes 0). On upgrade the service keeps running and the new package's postinst
# (deb) or %posttrans (rpm) restarts it onto the new binary once the existing
# config passes its checks.
case "${1:-}" in
  remove | 0)
    if command -v systemctl >/dev/null 2>&1; then
      systemctl stop nut-slave 2>/dev/null || true
      systemctl disable nut-slave 2>/dev/null || true
    fi
    ;;
esac
