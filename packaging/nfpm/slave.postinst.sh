#!/bin/sh
set -eu

if [ ! -f /etc/nut-server/slave.yaml ]; then
  cp /etc/nut-server/slave.example.yaml /etc/nut-server/slave.yaml
  chmod 0640 /etc/nut-server/slave.yaml
fi

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi

cat <<'EOF'

nut-slave installed.

  Edit /etc/nut-server/slave.yaml (master_addr, node_id, token).
  Then: sudo systemctl enable --now nut-slave

EOF
