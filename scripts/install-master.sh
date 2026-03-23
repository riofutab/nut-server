#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

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
CONFIG_SOURCE=$(find_file "configs/master.example.yaml" || true)
SERVICE_SOURCE=$(find_file "packaging/systemd/nut-master.service" || true)

if [ -z "$BIN_SOURCE" ]; then
  echo "nut-master binary not found near script"
  exit 1
fi
if [ -z "$CONFIG_SOURCE" ] || [ -z "$SERVICE_SOURCE" ]; then
  echo "required config or service file not found near script"
  exit 1
fi

install -d /usr/local/bin /usr/local/lib/nut-server /etc/nut-server
install -m 0755 "$BIN_SOURCE" /usr/local/bin/nut-master

if [ ! -f /etc/nut-server/master.yaml ]; then
  install -m 0644 "$CONFIG_SOURCE" /etc/nut-server/master.yaml
fi

install -m 0644 "$SERVICE_SOURCE" /etc/systemd/system/nut-master.service
systemctl daemon-reload

echo "installed nut-master"
echo "edit /etc/nut-server/master.yaml then run: systemctl enable --now nut-master"
