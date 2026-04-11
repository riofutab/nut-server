#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "please run as root"
    exit 1
  fi
}

find_existing_file() {
  for candidate in "$@"
  do
    if [ -f "$candidate" ]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done
  return 1
}

find_binary_source() {
  name="$1"
  find_existing_file \
    "$ROOT_DIR/$name" \
    "$ROOT_DIR/bin/$name" \
    "$ROOT_DIR/dist/linux-amd64/$name" \
    "$ROOT_DIR/dist/linux-arm64/$name"
}

find_service_source() {
  name="$1"
  find_existing_file \
    "$ROOT_DIR/$name" \
    "$ROOT_DIR/systemd/$name" \
    "$ROOT_DIR/packaging/systemd/$name" \
    "$ROOT_DIR/dist/linux-amd64/packaging/systemd/$name" \
    "$ROOT_DIR/dist/linux-arm64/packaging/systemd/$name"
}

install_binary_from_package() {
  name="$1"
  source_file=$(find_binary_source "$name" || true)
  if [ -z "$source_file" ]; then
    echo "$name binary not found near script"
    exit 1
  fi
  install -d /usr/local/bin
  install -m 0755 "$source_file" "/usr/local/bin/$name"
}

install_service_from_package() {
  name="$1"
  source_file=$(find_service_source "$name" || true)
  if [ -z "$source_file" ]; then
    echo "$name service file not found near script"
    exit 1
  fi
  install -d /etc/systemd/system
  install -m 0644 "$source_file" "/etc/systemd/system/$name"
}

reload_systemd() {
  systemctl daemon-reload
}

restart_if_active() {
  service_name="$1"
  if systemctl is-active --quiet "$service_name"; then
    systemctl restart "$service_name"
    echo "restarted $service_name"
    return 0
  fi
  echo "files updated for $service_name"
  echo "run: systemctl restart $service_name"
}
