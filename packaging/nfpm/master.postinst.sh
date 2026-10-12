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

# Older releases let nut-server own /etc/nut-server, so it could have swapped the
# config for a symlink; refuse rather than write/chown/chmod the link target as root.
if [ -L /etc/nut-server/master.yaml ]; then
  echo "/etc/nut-server/master.yaml must not be a symbolic link" >&2
  exit 1
fi

if [ ! -f /etc/nut-server/master.yaml ]; then
  cp /etc/nut-server/master.example.yaml /etc/nut-server/master.yaml
  # Generate a strong admin_token (the example placeholder is rejected at load).
  # enrollment_tokens is deliberately left as the placeholder so the operator
  # must set a fleet secret — the master refuses to start until they do.
  admin_token=$(random_hex)
  sed -i "s|replace-with-strong-admin-token|${admin_token}|" /etc/nut-server/master.yaml
  # Use an absolute, sandbox-writable state path (the relative example default is
  # read-only under the shipped systemd ProtectSystem=strict).
  sed -i 's|^state_file:.*|state_file: "/var/lib/nut-server/master-state.json"|' /etc/nut-server/master.yaml
fi
chown root:nut-server /etc/nut-server/master.yaml
chmod 0640 /etc/nut-server/master.yaml

if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  # Upgrade of a running service: restart onto the new binary only when the
  # existing config passes its checks (as the service user, since sudo checks
  # always pass for root). Otherwise the old process keeps running.
  if systemctl is-active --quiet nut-master; then
    if runuser -u nut-server -- /usr/local/bin/nut-master -check-config -config /etc/nut-server/master.yaml; then
      systemctl restart nut-master
      echo "nut-master restarted on the new version."
    else
      cat >&2 <<'EOF'

WARNING: /etc/nut-server/master.yaml does not pass the new version's checks.
nut-master was NOT restarted and is still running the previous version from memory;
it will fail to start after a reboot. Fix the config (see docs/upgrade.md), then:
  sudo -u nut-server /usr/local/bin/nut-master -check-config -config /etc/nut-server/master.yaml
  sudo systemctl restart nut-master

EOF
    fi
    exit 0
  fi
fi

cat <<'EOF'

nut-master installed.

  Edit /etc/nut-server/master.yaml (enrollment_tokens, snmp.target,
  shutdown_policies). Check it as the service user, then start:
    sudo -u nut-server /usr/local/bin/nut-master -check-config -config /etc/nut-server/master.yaml
    sudo systemctl enable --now nut-master

EOF
