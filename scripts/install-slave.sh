#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

NODE_ID=""
MASTER_ADDR=""
TOKEN=""
TAGS=""
TLS_CERT_FILE=""
TLS_KEY_FILE=""
TLS_CA_FILE=""
TLS_SERVER_NAME=""
TLS_INSECURE_SKIP_VERIFY="false"
TLS_ENABLED="false"
TLS_DISABLED="false"

usage() {
  cat <<'EOF'
usage: install-slave.sh --node-id <node-id> --master-addr <host:port> --token <token> [--tags <comma-separated-tags>] [--disable-tls] [--tls-ca-file <path>] [--tls-server-name <name>] [--tls-cert-file <path> --tls-key-file <path>] [--tls-insecure-skip-verify]

TLS:
  --disable-tls                      force plain TCP and ignore all TLS certificate settings
  --tls-ca-file <path>               CA bundle used to verify the master certificate
  --tls-server-name <name>           expected server name during certificate verification
  --tls-cert-file <path>             client certificate presented to master in mTLS mode
  --tls-key-file <path>              client private key presented to master in mTLS mode
  --tls-insecure-skip-verify         skip master certificate verification, test only
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --node-id)
      NODE_ID="$2"
      shift 2
      ;;
    --master-addr)
      MASTER_ADDR="$2"
      shift 2
      ;;
    --token)
      TOKEN="$2"
      shift 2
      ;;
    --tags)
      TAGS="$2"
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
    --tls-server-name)
      TLS_SERVER_NAME="$2"
      TLS_ENABLED="true"
      shift 2
      ;;
    --tls-insecure-skip-verify)
      TLS_INSECURE_SKIP_VERIFY="true"
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

if [ -z "$NODE_ID" ] || [ -z "$MASTER_ADDR" ] || [ -z "$TOKEN" ]; then
  echo "--node-id, --master-addr and --token are required"
  usage
  exit 1
fi

if [ "$TLS_DISABLED" = "true" ]; then
  TLS_ENABLED="false"
  TLS_CERT_FILE=""
  TLS_KEY_FILE=""
  TLS_CA_FILE=""
  TLS_SERVER_NAME=""
  TLS_INSECURE_SKIP_VERIFY="false"
else
  if [ -n "$TLS_CERT_FILE" ] && [ -z "$TLS_KEY_FILE" ]; then
    echo "--tls-key-file is required when --tls-cert-file is set"
    exit 1
  fi
  if [ -n "$TLS_KEY_FILE" ] && [ -z "$TLS_CERT_FILE" ]; then
    echo "--tls-cert-file is required when --tls-key-file is set"
    exit 1
  fi
  if [ "$TLS_INSECURE_SKIP_VERIFY" = "true" ] && [ -n "$TLS_CA_FILE" ]; then
    echo "--tls-ca-file cannot be combined with --tls-insecure-skip-verify"
    exit 1
  fi
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "please run as root"
  exit 1
fi

find_binary() {
  for candidate in \
    "$ROOT_DIR/nut-slave" \
    "$ROOT_DIR/bin/nut-slave" \
    "$ROOT_DIR/dist/linux-amd64/nut-slave" \
    "$ROOT_DIR/dist/linux-arm64/nut-slave"
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
SERVICE_SOURCE=$(find_file "packaging/systemd/nut-slave.service" || true)

if [ -z "$BIN_SOURCE" ]; then
  echo "nut-slave binary not found near script"
  exit 1
fi
if [ -z "$SERVICE_SOURCE" ]; then
  echo "required service file not found near script"
  exit 1
fi

install -d /usr/local/bin /usr/local/lib/nut-server /etc/nut-server
install -m 0755 "$BIN_SOURCE" /usr/local/bin/nut-slave

cat > /etc/nut-server/slave.yaml <<EOF
node_id: "$NODE_ID"
master_addr: "$MASTER_ADDR"
token: "$TOKEN"
state_file: "/var/lib/nut-server/slave-state.json"
reconnect_interval: "5s"
dry_run: true
tls:
  enabled: $TLS_ENABLED
  disabled: $TLS_DISABLED
  cert_file: "$TLS_CERT_FILE"
  key_file: "$TLS_KEY_FILE"
  ca_file: "$TLS_CA_FILE"
  server_name: "$TLS_SERVER_NAME"
  insecure_skip_verify: $TLS_INSECURE_SKIP_VERIFY
shutdown_command:
  - "/sbin/shutdown"
  - "-h"
  - "now"
EOF

if [ -n "$TAGS" ]; then
  printf 'tags:\n' >> /etc/nut-server/slave.yaml
  OLD_IFS=$IFS
  IFS=,
  set -- $TAGS
  IFS=$OLD_IFS
  for tag in "$@"; do
    printf '  - "%s"\n' "$tag" >> /etc/nut-server/slave.yaml
  done
fi

install -m 0644 "$SERVICE_SOURCE" /etc/systemd/system/nut-slave.service
systemctl daemon-reload

echo "installed nut-slave"
echo "generated /etc/nut-server/slave.yaml for node_id=$NODE_ID master_addr=$MASTER_ADDR"
echo "run: systemctl enable --now nut-slave"
