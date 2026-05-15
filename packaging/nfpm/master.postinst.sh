#!/bin/sh
set -eu

if ! getent passwd nut-server >/dev/null; then
  useradd --system --home-dir /var/lib/nut-server --shell /usr/sbin/nologin nut-server
fi

chown -R nut-server:nut-server /etc/nut-server /var/lib/nut-server
chmod 0750 /etc/nut-server /var/lib/nut-server

if [ ! -f /etc/nut-server/master.yaml ]; then
  cp /etc/nut-server/master.example.yaml /etc/nut-server/master.yaml
  if command -v openssl >/dev/null 2>&1; then
    token=$(openssl rand -hex 24)
    sed -i "s|replace-with-strong-admin-token|${token}|" /etc/nut-server/master.yaml
  fi
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
