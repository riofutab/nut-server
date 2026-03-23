#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

LISTEN_ADDR=":9000"
TOKEN=""
SNMP_TARGET=""
SNMP_COMMUNITY="public"

usage() {
  cat <<'EOF'
usage: install-master.sh --token <token> --snmp-target <host> [--listen-addr <addr>] [--snmp-community <community>]
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --listen-addr)
      LISTEN_ADDR="$2"
      shift 2
      ;;
    --token)
      TOKEN="$2"
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

if [ -z "$TOKEN" ] || [ -z "$SNMP_TARGET" ]; then
  echo "--token and --snmp-target are required"
  usage
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "please run as root"
  exit 1
fi

find_binary() {
  for candidate in \
    "$ROOT_DIR/nut-master" \
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

if [ -z "$BIN_SOURCE" ]; then
  echo "nut-master binary not found near script"
  exit 1
fi
if [ -z "$SERVICE_SOURCE" ]; then
  echo "required service file not found near script"
  exit 1
fi

install -d /usr/local/bin /usr/local/lib/nut-server /etc/nut-server
install -m 0755 "$BIN_SOURCE" /usr/local/bin/nut-master

cat > /etc/nut-server/master.yaml <<EOF
listen_addr: "$LISTEN_ADDR"
auth_tokens:
  - "$TOKEN"
poll_interval: "10s"
dry_run: true
shutdown_policy:
  require_on_battery: true
  min_battery_charge: 30
  min_runtime_minutes: 15
  shutdown_reason: "UPS battery threshold reached"
snmp:
  target: "$SNMP_TARGET"
  port: 161
  community: "$SNMP_COMMUNITY"
  version: "2c"
  timeout_seconds: 2
  output_source_oid: ".1.3.6.1.2.1.33.1.4.1.0"
  charge_oid: ".1.3.6.1.2.1.33.1.2.4.0"
  runtime_minutes_oid: ".1.3.6.1.2.1.33.1.2.3.0"
EOF

install -m 0644 "$SERVICE_SOURCE" /etc/systemd/system/nut-master.service
systemctl daemon-reload

echo "installed nut-master"
echo "generated /etc/nut-server/master.yaml for listen_addr=$LISTEN_ADDR snmp_target=$SNMP_TARGET"
echo "run: systemctl enable --now nut-master"
