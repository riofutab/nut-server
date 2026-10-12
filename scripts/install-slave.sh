#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

NODE_ID=""
MASTER_ADDR=""
TOKEN_FILE=""
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
usage: install-slave.sh --node-id <node-id> --master-addr <host:port> --token-file <path> [--tags <comma-separated-tags>] [--disable-tls] [--tls-ca-file <path>] [--tls-server-name <name>] [--tls-cert-file <path> --tls-key-file <path>] [--tls-insecure-skip-verify]

  --token-file <path>                file holding the token (kept off the command line)

/etc/nut-server/slave.yaml is always generated from these options. An existing
slave.yaml is first backed up to slave.yaml.bak-<UTC timestamp>; only its
dry_run value is carried over, so merge any other custom settings (tags,
shutdown_command, metrics_listen_addr, ...) back by hand.

TLS:
  --disable-tls                      force plain TCP and ignore all TLS certificate settings
  --tls-ca-file <path>               CA bundle used to verify the master certificate; copied
                                     to /etc/nut-server/ca.crt
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
    --token-file)
      TOKEN_FILE="$2"
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

if [ -z "$NODE_ID" ] || [ -z "$MASTER_ADDR" ] || [ -z "$TOKEN_FILE" ]; then
  echo "--node-id, --master-addr and --token-file are required"
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
if [ -n "$TLS_CA_FILE" ] && [ ! -r "$TLS_CA_FILE" ]; then
  echo "--tls-ca-file $TLS_CA_FILE is not readable" >&2
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

# master_addr is host:port or [ipv6]:port. Brackets are only accepted around
# an IPv6 literal, so the value stays safe inside the double-quoted YAML.
check_master_addr() {
  case "$MASTER_ADDR" in
    \[*\]:*)
      host=${MASTER_ADDR#\[}
      host=${host%%\]*}
      port=${MASTER_ADDR##*\]:}
      if [ "[$host]:$port" != "$MASTER_ADDR" ]; then
        echo "--master-addr must be host:port or [ipv6]:port" >&2
        exit 1
      fi
      reject_unsafe "--master-addr IPv6 host" "$host" 'a-fA-F0-9:.'
      ;;
    *:*)
      host=${MASTER_ADDR%:*}
      port=${MASTER_ADDR##*:}
      reject_unsafe "--master-addr host" "$host" 'a-zA-Z0-9_.\-'
      ;;
    *)
      host=""
      port=""
      ;;
  esac
  if [ -z "$host" ] || [ -z "$port" ]; then
    echo "--master-addr must be host:port or [ipv6]:port" >&2
    exit 1
  fi
  reject_unsafe "--master-addr port" "$port" '0-9'
}

reject_unsafe "--node-id" "$NODE_ID" 'a-zA-Z0-9_.\-'
check_master_addr
reject_unsafe "--token-file contents" "$TOKEN" 'a-zA-Z0-9_.\-'
if [ -n "$TAGS" ]; then
  reject_unsafe "--tags" "$TAGS" 'a-zA-Z0-9_.,\-'
fi
if [ -n "$TLS_SERVER_NAME" ]; then
  # ':' admits IPv6 literals.
  reject_unsafe "--tls-server-name" "$TLS_SERVER_NAME" 'a-zA-Z0-9_.\-:'
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
SERVICE_SOURCE=$(find_file "packaging/systemd/nut-slave.service" || true)
SUDOERS_SOURCE=$(find_file "packaging/sudoers/nut-server-slave" || true)

if [ -z "$BIN_SOURCE" ]; then
  echo "nut-slave binary not found near script"
  exit 1
fi
if [ -z "$SERVICE_SOURCE" ]; then
  echo "required service file not found near script"
  exit 1
fi
if [ -z "$SUDOERS_SOURCE" ]; then
  echo "required sudoers file not found near script"
  exit 1
fi

if ! getent passwd nut-server >/dev/null; then
  useradd --system --home-dir /var/lib/nut-server --shell /usr/sbin/nologin nut-server
fi

install -d /usr/local/bin /usr/local/lib/nut-server
install -d -o root -g nut-server -m 0750 /etc/nut-server
install -d -o nut-server -g nut-server -m 0750 /var/lib/nut-server

CONFIG_PATH="/etc/nut-server/slave.yaml"
if [ -L "$CONFIG_PATH" ]; then
  echo "$CONFIG_PATH is a symlink; refusing to replace it" >&2
  exit 1
fi

# -check-config below runs the sudo self-check as nut-server, so the sudoers
# rules must be in place first.
install -o root -g root -m 0440 "$SUDOERS_SOURCE" /etc/sudoers.d/nut-server-slave
if command -v visudo >/dev/null 2>&1; then
  visudo -c -f /etc/sudoers.d/nut-server-slave >/dev/null
fi

CA_PATH="/etc/nut-server/ca.crt"
CA_BACKUP=""
CA_REPLACED="false"
STAGED_BIN="/usr/local/bin/.nut-slave.new"
TMP_CONFIG=""
# Any exit before the commit point puts the previous ca.crt back, so a failed
# run leaves an installed slave as it was.
cleanup() {
  rm -f "$TMP_CONFIG" "$STAGED_BIN"
  if [ "$CA_REPLACED" = "true" ]; then
    if [ -n "$CA_BACKUP" ]; then
      mv -f "$CA_BACKUP" "$CA_PATH"
    else
      rm -f "$CA_PATH"
    fi
  fi
}
trap cleanup EXIT

if [ -n "$TLS_CA_FILE" ]; then
  if [ -f "$CA_PATH" ]; then
    CA_BACKUP=$(mktemp "$CA_PATH.XXXXXX")
    cp -p "$CA_PATH" "$CA_BACKUP"
  fi
  CA_REPLACED="true"
  install -o root -g root -m 0644 "$TLS_CA_FILE" "$CA_PATH"
  TLS_CA_FILE="$CA_PATH"
fi

# Keep dry_run from an existing config so re-running the installer (for
# example with a rotated token) never flips a production slave back to dry-run.
DRY_RUN="true"
if [ -f "$CONFIG_PATH" ]; then
  OLD_DRY_RUN=$(awk '/^dry_run:/ { gsub(/"/, "", $2); print $2; exit }' "$CONFIG_PATH")
  case "$OLD_DRY_RUN" in
    true|false) DRY_RUN="$OLD_DRY_RUN" ;;
  esac
fi

# The token reaches the file only through the heredoc, never through argv;
# mktemp creates the file 0600 so it is not readable before the chmod.
TMP_CONFIG=$(mktemp "$CONFIG_PATH.XXXXXX")
cat > "$TMP_CONFIG" <<EOF
node_id: "$NODE_ID"
master_addr: "$MASTER_ADDR"
token: "$TOKEN"
state_file: "/var/lib/nut-server/slave-state.json"
reconnect_interval: "5s"
dry_run: $DRY_RUN
tls:
  enabled: $TLS_ENABLED
  disabled: $TLS_DISABLED
  cert_file: "$TLS_CERT_FILE"
  key_file: "$TLS_KEY_FILE"
  ca_file: "$TLS_CA_FILE"
  server_name: "$TLS_SERVER_NAME"
  insecure_skip_verify: $TLS_INSECURE_SKIP_VERIFY
shutdown_command:
  - "/usr/bin/sudo"
  - "-n"
  - "/sbin/shutdown"
  - "-h"
  - "now"
EOF

if [ -n "$TAGS" ]; then
  printf 'tags:\n' >> "$TMP_CONFIG"
  OLD_IFS=$IFS
  IFS=,
  set -- $TAGS
  IFS=$OLD_IFS
  for tag in "$@"; do
    printf '  - "%s"\n' "$tag" >> "$TMP_CONFIG"
  done
fi
chown root:nut-server "$TMP_CONFIG"
chmod 0640 "$TMP_CONFIG"

# Check the new binary against the new config before replacing either, so a
# failed check leaves an installed slave as it was.
install -m 0755 "$BIN_SOURCE" "$STAGED_BIN"
if ! runuser -u nut-server -- "$STAGED_BIN" -check-config -config "$TMP_CONFIG"; then
  echo "config check failed; $CONFIG_PATH and /usr/local/bin/nut-slave were not changed" >&2
  exit 1
fi

if [ -f "$CONFIG_PATH" ]; then
  BACKUP_PATH="$CONFIG_PATH.bak-$(date -u +%Y%m%dT%H%M%SZ)"
  cp -p "$CONFIG_PATH" "$BACKUP_PATH"
  echo "backed up existing $CONFIG_PATH to $BACKUP_PATH; regenerated it with dry_run=$DRY_RUN (merge other custom settings back by hand)"
fi
mv -f "$TMP_CONFIG" "$CONFIG_PATH"
mv -f "$STAGED_BIN" /usr/local/bin/nut-slave
CA_REPLACED="false"
if [ -n "$CA_BACKUP" ]; then
  rm -f "$CA_BACKUP"
fi
echo "generated $CONFIG_PATH for node_id=$NODE_ID master_addr=$MASTER_ADDR"

install -m 0644 "$SERVICE_SOURCE" /etc/systemd/system/nut-slave.service
systemctl daemon-reload

echo "installed nut-slave"
# A re-run normally carries a newly issued token that invalidates the old
# one, so a running slave must restart to pick up the new config.
if systemctl is-active --quiet nut-slave; then
  systemctl restart nut-slave
  echo "restarted nut-slave"
else
  echo "run: systemctl enable --now nut-slave"
fi
