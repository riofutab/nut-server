#!/bin/sh
set -eu

if ! getent passwd nut-server >/dev/null; then
  useradd --system --home-dir /var/lib/nut-server --shell /usr/sbin/nologin nut-server
fi

chown -R nut-server:nut-server /etc/nut-server /var/lib/nut-server
chmod 0750 /etc/nut-server /var/lib/nut-server

if [ -f /etc/sudoers.d/nut-server-slave ]; then
  chown root:root /etc/sudoers.d/nut-server-slave
  chmod 0440 /etc/sudoers.d/nut-server-slave
  if command -v visudo >/dev/null 2>&1; then
    visudo -c -f /etc/sudoers.d/nut-server-slave >/dev/null
  fi
fi

if [ ! -f /etc/nut-server/slave.yaml ]; then
  cp /etc/nut-server/slave.example.yaml /etc/nut-server/slave.yaml
  # Use an absolute, sandbox-writable state path (the relative example default is
  # read-only under the shipped systemd ProtectSystem=strict).
  sed -i 's|^state_file:.*|state_file: "/var/lib/nut-server/slave-state.json"|' /etc/nut-server/slave.yaml
  chown nut-server:nut-server /etc/nut-server/slave.yaml
  chmod 0640 /etc/nut-server/slave.yaml
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi

cat <<'EOF'

nut-slave installed.

  Slave runs as the nut-server user. /etc/sudoers.d/nut-server-slave
  grants it NOPASSWD access to /sbin/shutdown so the shutdown command
  works without root.

  Edit /etc/nut-server/slave.yaml (master_addr, node_id, token).
  Then: sudo systemctl enable --now nut-slave

EOF
