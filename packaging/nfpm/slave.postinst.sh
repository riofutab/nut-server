#!/bin/sh
set -eu

if ! getent passwd nut-server >/dev/null; then
  useradd --system --home-dir /var/lib/nut-server --shell /usr/sbin/nologin nut-server
fi

# The service may read its config (tokens) but not modify it; only the state
# directory is writable by nut-server.
chown root:nut-server /etc/nut-server
chmod 0750 /etc/nut-server
chown -R nut-server:nut-server /var/lib/nut-server
chmod 0750 /var/lib/nut-server

if [ -f /etc/sudoers.d/nut-server-slave ]; then
  chown root:root /etc/sudoers.d/nut-server-slave
  chmod 0440 /etc/sudoers.d/nut-server-slave
  if command -v visudo >/dev/null 2>&1; then
    visudo -c -f /etc/sudoers.d/nut-server-slave >/dev/null
  fi
fi

# Older releases let nut-server own /etc/nut-server, so it could have swapped the
# config for a symlink; refuse rather than write/chown/chmod the link target as root.
if [ -L /etc/nut-server/slave.yaml ]; then
  echo "/etc/nut-server/slave.yaml must not be a symbolic link" >&2
  exit 1
fi

if [ ! -f /etc/nut-server/slave.yaml ]; then
  cp /etc/nut-server/slave.example.yaml /etc/nut-server/slave.yaml
  # Use an absolute, sandbox-writable state path (the relative example default is
  # read-only under the shipped systemd ProtectSystem=strict).
  sed -i 's|^state_file:.*|state_file: "/var/lib/nut-server/slave-state.json"|' /etc/nut-server/slave.yaml
fi
chown root:nut-server /etc/nut-server/slave.yaml
chmod 0640 /etc/nut-server/slave.yaml

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

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  case "${1:-}" in
    2)
      # rpm upgrade: the old package's %preun has not run yet; %posttrans
      # restores the state %pre recorded.
      exit 0
      ;;
    configure)
      if systemctl is-active --quiet nut-slave; then
        restore_service false true
        exit 0
      fi
      # The prerm of packages before v0.6.0 stops and disables the service on
      # upgrade, and runs before any script of this package.
      if [ -n "${2:-}" ] && dpkg --compare-versions "$2" lt 0.6.0~; then
        cat >&2 <<EOF

The nut-slave $2 package stopped and disabled nut-slave during this upgrade.
If it was running, check the config and start it again:
  sudo -u nut-server /usr/local/bin/nut-slave -check-config -config /etc/nut-server/slave.yaml
  sudo systemctl enable --now nut-slave

EOF
        exit 0
      fi
      ;;
  esac
fi

cat <<'EOF'

nut-slave installed.

  Slave runs as the nut-server user. /etc/sudoers.d/nut-server-slave
  grants it NOPASSWD access to /sbin/shutdown so the shutdown command
  works without root.

  Edit /etc/nut-server/slave.yaml (master_addr, node_id, token).
  Check it as the service user, then start:
    sudo -u nut-server /usr/local/bin/nut-slave -check-config -config /etc/nut-server/slave.yaml
    sudo systemctl enable --now nut-slave

EOF
