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

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  # Upgrade of a running service: restart onto the new binary only when the
  # existing config passes its checks (as the service user, since sudo checks
  # always pass for root). Otherwise the old process keeps running.
  if systemctl is-active --quiet nut-slave; then
    if runuser -u nut-server -- /usr/local/bin/nut-slave -check-config -config /etc/nut-server/slave.yaml; then
      systemctl restart nut-slave
      echo "nut-slave restarted on the new version."
    else
      cat >&2 <<'EOF'

WARNING: /etc/nut-server/slave.yaml does not pass the new version's checks.
nut-slave was NOT restarted and is still running the previous version from memory;
it will fail to start after a reboot. Fix the config (see docs/upgrade.md), then:
  sudo -u nut-server /usr/local/bin/nut-slave -check-config -config /etc/nut-server/slave.yaml
  sudo systemctl restart nut-slave

EOF
    fi
    exit 0
  fi
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
