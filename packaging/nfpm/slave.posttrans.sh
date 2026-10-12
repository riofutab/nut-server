#!/bin/sh
set -eu

# rpm %posttrans: runs after the old package's %preun, which in packages before
# v0.6.0 stopped and disabled the service on every upgrade. Restores the state
# the new package's %pre recorded.
STATE_FILE=/run/nut-slave.upgrade-state

# Restores the service after an upgrade: enables it if it was enabled, and
# restarts it onto the new binary if it was running and the existing config
# passes its checks (as the service user, since sudo checks always pass for root).
restore_service() {
  was_enabled="$1"
  was_active="$2"
  if [ "$was_enabled" = "true" ]; then
    systemctl enable nut-slave
  fi
  if [ "$was_active" != "true" ]; then
    return 0
  fi
  if runuser -u nut-server -- /usr/local/bin/nut-slave -check-config -config /etc/nut-server/slave.yaml; then
    systemctl restart nut-slave
    echo "nut-slave restarted on the new version."
    return 0
  fi
  echo "WARNING: /etc/nut-server/slave.yaml does not pass the new version's checks." >&2
  if systemctl is-active --quiet nut-slave; then
    echo "nut-slave was NOT restarted and is still running the previous version from memory;" >&2
    echo "it will fail to start after a reboot. Fix the config (see docs/upgrade.md), then:" >&2
  else
    echo "nut-slave is stopped. Fix the config (see docs/upgrade.md), then:" >&2
  fi
  echo "  sudo -u nut-server /usr/local/bin/nut-slave -check-config -config /etc/nut-server/slave.yaml" >&2
  echo "  sudo systemctl restart nut-slave" >&2
}

[ -f "$STATE_FILE" ] || exit 0
was_enabled=false
was_active=false
grep -qx enabled "$STATE_FILE" && was_enabled=true
grep -qx active "$STATE_FILE" && was_active=true
rm -f "$STATE_FILE"
command -v systemctl >/dev/null 2>&1 || exit 0
systemctl daemon-reload || true
restore_service "$was_enabled" "$was_active"
