#!/bin/sh
set -eu

if ! getent passwd nut-server >/dev/null; then
  useradd --system --home-dir /var/lib/nut-server --shell /usr/sbin/nologin nut-server
fi

chown -R nut-server:nut-server /etc/nut-server /var/lib/nut-server
chmod 0750 /etc/nut-server /var/lib/nut-server

random_hex() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 24
  else
    od -An -tx1 -N24 /dev/urandom | tr -d ' \n'
  fi
}

if [ -f /etc/sudoers.d/nut-server-master ]; then
  chown root:root /etc/sudoers.d/nut-server-master
  chmod 0440 /etc/sudoers.d/nut-server-master
  if command -v visudo >/dev/null 2>&1; then
    visudo -c -f /etc/sudoers.d/nut-server-master >/dev/null
  fi
fi

if [ ! -f /etc/nut-server/master.yaml ]; then
  cp /etc/nut-server/master.example.yaml /etc/nut-server/master.yaml
  # Generate a strong admin_token (the example placeholder is rejected at load).
  # auth_tokens is deliberately left as the placeholder so the operator must set
  # a shared fleet secret — the master refuses to start until they do.
  admin_token=$(random_hex)
  sed -i "s|replace-with-strong-admin-token|${admin_token}|" /etc/nut-server/master.yaml
  # Use an absolute, sandbox-writable state path (the relative example default is
  # read-only under the shipped systemd ProtectSystem=strict).
  sed -i 's|^state_file:.*|state_file: "/var/lib/nut-server/master-state.json"|' /etc/nut-server/master.yaml
  chown nut-server:nut-server /etc/nut-server/master.yaml
  chmod 0640 /etc/nut-server/master.yaml
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi

cat <<'EOF'

nut-master installed.

  Edit /etc/nut-server/master.yaml (admin_token, auth_tokens, snmp.target).
  Then: sudo systemctl enable --now nut-master

EOF
