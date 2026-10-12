#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

LISTEN_ADDR=":9000"
ADMIN_LISTEN_ADDR="127.0.0.1:9001"
TOKEN_FILE=""
SNMP_TARGET=""
SNMP_COMMUNITY="public"
TLS_CERT_FILE=""
TLS_KEY_FILE=""
TLS_CA_FILE=""
TLS_REQUIRE_CLIENT_CERT="false"
TLS_ENABLED="false"
TLS_DISABLED="false"

usage() {
  cat <<'EOF'
usage: install-master.sh --token-file <path> --snmp-target <host> [--listen-addr <addr>] [--admin-listen-addr <addr>] [--snmp-community <community>] [--disable-tls] [--tls-cert-file <path> --tls-key-file <path>] [--tls-ca-file <path>] [--tls-require-client-cert]

  --token-file <path>            file holding the enrollment token new slaves first register
                                 with (kept off the command line); each node is then issued
                                 its own token. Create per-node install scripts with
                                 POST /nodes/<node_id>/install-script.

  admin_token is generated on first install and kept in master.yaml.

TLS:
  --disable-tls                  force plain TCP and ignore all TLS certificate settings
  --tls-cert-file <path>         server certificate for master listener
  --tls-key-file <path>          server private key for master listener
  --tls-ca-file <path>           CA bundle used to verify slave client certificates in mTLS mode
  --tls-require-client-cert      require and verify slave client certificates
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --listen-addr)
      LISTEN_ADDR="$2"
      shift 2
      ;;
    --admin-listen-addr)
      ADMIN_LISTEN_ADDR="$2"
      shift 2
      ;;
    --token-file)
      TOKEN_FILE="$2"
      shift 2
      ;;
    --snmp-target)
      SNMP_TARGET="$2"
      shift 2
      ;;
    --snmp-community)
      SNMP_COMMUNITY="$2"
      shift 2
      ;;
    --disable-tls)
      TLS_DISABLED="true"
      TLS_ENABLED="false"
      shift 1
      ;;
    --tls-cert-file)
      TLS_CERT_FILE="$2"
      TLS_ENABLED="true"
      shift 2
      ;;
    --tls-key-file)
      TLS_KEY_FILE="$2"
      TLS_ENABLED="true"
      shift 2
      ;;
    --tls-ca-file)
      TLS_CA_FILE="$2"
      TLS_ENABLED="true"
      shift 2
      ;;
    --tls-require-client-cert)
      TLS_REQUIRE_CLIENT_CERT="true"
      TLS_ENABLED="true"
      shift 1
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1"
      usage
      exit 1
      ;;
  esac
done

if [ -z "$TOKEN_FILE" ] || [ -z "$SNMP_TARGET" ]; then
  echo "--token-file and --snmp-target are required"
  usage
  exit 1
fi
if [ ! -r "$TOKEN_FILE" ]; then
  echo "--token-file $TOKEN_FILE is not readable" >&2
  exit 1
fi
TOKEN=$(cat "$TOKEN_FILE")
if [ -z "$TOKEN" ]; then
  echo "--token-file $TOKEN_FILE is empty" >&2
  exit 1
fi

reject_unsafe() {
  name="$1"
  value="$2"
  allowed="$3"
  case "$value" in
    *[!${allowed}]*)
      echo "$name contains characters outside [$allowed]" >&2
      exit 1
      ;;
  esac
}

reject_unsafe "--token-file contents" "$TOKEN" 'a-zA-Z0-9_.\-'
reject_unsafe "--snmp-target" "$SNMP_TARGET" 'a-zA-Z0-9_.\-:'
reject_unsafe "--snmp-community" "$SNMP_COMMUNITY" 'a-zA-Z0-9_.\-'
reject_unsafe "--listen-addr" "$LISTEN_ADDR" 'a-zA-Z0-9_.\-:'
reject_unsafe "--admin-listen-addr" "$ADMIN_LISTEN_ADDR" 'a-zA-Z0-9_.\-:'

if [ "$TLS_DISABLED" = "true" ]; then
  TLS_ENABLED="false"
  TLS_CERT_FILE=""
  TLS_KEY_FILE=""
  TLS_CA_FILE=""
  TLS_REQUIRE_CLIENT_CERT="false"
else
  if [ -n "$TLS_CERT_FILE" ] && [ -z "$TLS_KEY_FILE" ]; then
    echo "--tls-key-file is required when --tls-cert-file is set"
    exit 1
  fi
  if [ -n "$TLS_KEY_FILE" ] && [ -z "$TLS_CERT_FILE" ]; then
    echo "--tls-cert-file is required when --tls-key-file is set"
    exit 1
  fi
  if [ "$TLS_ENABLED" = "true" ] && { [ -z "$TLS_CERT_FILE" ] || [ -z "$TLS_KEY_FILE" ]; }; then
    echo "--tls-cert-file and --tls-key-file are required when TLS is enabled"
    exit 1
  fi
  if [ "$TLS_REQUIRE_CLIENT_CERT" = "true" ] && [ -z "$TLS_CA_FILE" ]; then
    echo "--tls-ca-file is required when --tls-require-client-cert is set"
    exit 1
  fi
  if [ -n "$TLS_CA_FILE" ] && [ "$TLS_REQUIRE_CLIENT_CERT" != "true" ]; then
    echo "--tls-ca-file only applies with --tls-require-client-cert (master verifies slave certificates with it)"
    exit 1
  fi
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "please run as root"
  exit 1
fi

find_binary() {
  for candidate in \
    "$ROOT_DIR/nut-master" \
    "$ROOT_DIR/bin/nut-master" \
    "$ROOT_DIR/dist/linux-amd64/nut-master" \
    "$ROOT_DIR/dist/linux-arm64/nut-master"
  do
    if [ -f "$candidate" ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

find_file() {
  target="$1"
  for candidate in \
    "$ROOT_DIR/$target" \
    "$ROOT_DIR/systemd/$(basename "$target")" \
    "$ROOT_DIR/sudoers/$(basename "$target")" \
    "$ROOT_DIR/dist/linux-amd64/$target" \
    "$ROOT_DIR/dist/linux-arm64/$target"
  do
    if [ -f "$candidate" ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

BIN_SOURCE=$(find_binary || true)
SERVICE_SOURCE=$(find_file "packaging/systemd/nut-master.service" || true)
SUDOERS_SOURCE=$(find_file "packaging/sudoers/nut-server-master" || true)

if [ -z "$BIN_SOURCE" ]; then
  echo "nut-master binary not found near script"
  exit 1
fi
if [ -z "$SERVICE_SOURCE" ]; then
  echo "required service file not found near script"
  exit 1
fi

if ! getent passwd nut-server >/dev/null; then
  useradd --system --no-create-home --shell /usr/sbin/nologin nut-server
fi

install -d /usr/local/bin /usr/local/lib/nut-server
for dir in /etc/nut-server /var/lib/nut-server; do
  if [ -L "$dir" ]; then
    echo "$dir must not be a symbolic link" >&2
    exit 1
  fi
done
install -d -o root -g nut-server -m 0750 /etc/nut-server
install -d -o nut-server -g nut-server -m 0750 /var/lib/nut-server
chown -R nut-server:nut-server /var/lib/nut-server

# The script appends to, chowns and chmods the config as root; refuse a symlink
# rather than modify its target.
CONFIG_PATH="/etc/nut-server/master.yaml"
if [ -L "$CONFIG_PATH" ]; then
  echo "$CONFIG_PATH must not be a symbolic link" >&2
  exit 1
fi

# Install the master sudoers entry so an opt-in local_shutdown (master powers
# itself off last) can run /sbin/shutdown as the non-root nut-server user.
if [ -n "$SUDOERS_SOURCE" ]; then
  install -o root -g root -m 0440 "$SUDOERS_SOURCE" /etc/sudoers.d/nut-server-master
  if command -v visudo >/dev/null 2>&1; then
    visudo -c -f /etc/sudoers.d/nut-server-master >/dev/null
  fi
else
  echo "warning: master sudoers file not found; local_shutdown will need manual sudo setup" >&2
fi

random_token() {
  head -c 32 /dev/urandom | base64 | tr -d '/+=\n' | cut -c1-32
}

if [ -f "$CONFIG_PATH" ]; then
  echo "config $CONFIG_PATH already exists, skipping generation"
  if ! grep -q '^admin_token:' "$CONFIG_PATH"; then
    printf '\nadmin_token: "%s"\n' "$(random_token)" >> "$CONFIG_PATH"
    echo "added admin_token to existing master.yaml"
  fi
else
  ADMIN_TOKEN=$(random_token)
  echo "generated admin_token; see $CONFIG_PATH"
  cat > "$CONFIG_PATH" <<EOF
listen_addr: "$LISTEN_ADDR"
admin_listen_addr: "$ADMIN_LISTEN_ADDR"
admin_token: "$ADMIN_TOKEN"
state_file: "/var/lib/nut-server/master-state.json"
enrollment_tokens:
  - "$TOKEN"
enrollment: open
poll_interval: "10s"
command_timeout: "30s"
dry_run: true
log_ups_status: false
tls:
  enabled: $TLS_ENABLED
  disabled: $TLS_DISABLED
  cert_file: "$TLS_CERT_FILE"
  key_file: "$TLS_KEY_FILE"
  ca_file: "$TLS_CA_FILE"
  require_client_cert: $TLS_REQUIRE_CLIENT_CERT
shutdown_policies:
  - name: low-charge
    when:
      charge_below: 30
    target:
      all: true
    reason: "UPS battery charge low"
  - name: low-runtime
    when:
      runtime_below: 15
    target:
      all: true
    reason: "UPS runtime low"
snmp:
  target: "$SNMP_TARGET"
  port: 161
  community: "$SNMP_COMMUNITY"
  version: "2c"
  timeout_seconds: 2
EOF
  echo "generated $CONFIG_PATH for listen_addr=$LISTEN_ADDR snmp_target=$SNMP_TARGET"
fi
chown root:nut-server "$CONFIG_PATH"
chmod 0640 "$CONFIG_PATH"

# The new binary checks the config before it replaces the installed one, so a
# config it rejects leaves a running nut-master on its current version. The
# check runs as the service user so its sudo self-check sees the service's
# sudoers rules.
staged="/usr/local/bin/.nut-master.new"
install -m 0755 "$BIN_SOURCE" "$staged"
if ! runuser -u nut-server -- "$staged" -check-config -config "$CONFIG_PATH"; then
  rm -f "$staged"
  echo "config check failed; binary not replaced. Fix $CONFIG_PATH (see docs/upgrade.md) and run this script again" >&2
  exit 1
fi
mv -f "$staged" /usr/local/bin/nut-master

install -m 0644 "$SERVICE_SOURCE" /etc/systemd/system/nut-master.service
systemctl daemon-reload

echo "installed nut-master"
if systemctl is-active --quiet nut-master; then
  systemctl restart nut-master
  echo "restarted nut-master"
else
  echo "run: systemctl enable --now nut-master"
fi
cat <<EOF

To install a slave:
  1. Set public_addr in $CONFIG_PATH to the address slaves use to reach this
     master, then restart nut-master.
  2. On this master, generate the slave's install script:
       TOKEN=\$(sudo awk '/^admin_token:/ {print \$2}' $CONFIG_PATH | tr -d '"')
       curl -fsS -X POST -H "Authorization: Bearer \$TOKEN" \\
         http://$ADMIN_LISTEN_ADDR/nodes/<node_id>/install-script -o install-<node_id>.sh
  3. Copy install-<node_id>.sh to the slave, run it there with
     "bash install-<node_id>.sh" as a user with sudo rights, then delete it.
See docs/master.md.
EOF
