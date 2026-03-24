#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

NODE_ID=""
MASTER_ADDR=""
TOKEN=""

usage() {
  cat <<'EOF'
usage: install-slave.sh --node-id <node-id> --master-addr <host:port> --token <token>
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
reconnect_interval: "5s"
dry_run: true
shutdown_command:
  - "/sbin/shutdown"
  - "-h"
  - "now"
EOF

install -m 0644 "$SERVICE_SOURCE" /etc/systemd/system/nut-slave.service
systemctl daemon-reload

echo "installed nut-slave"
echo "generated /etc/nut-server/slave.yaml for node_id=$NODE_ID master_addr=$MASTER_ADDR"
echo "run: systemctl enable --now nut-slave"
