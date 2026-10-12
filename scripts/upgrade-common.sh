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

# A .deb/.rpm install keeps its unit in /lib/systemd/system; the copy this
# script writes to /etc/systemd/system would shadow every later packaged unit.
refuse_package_managed() {
  name="$1"
  path="/usr/local/bin/$name"
  if { command -v dpkg >/dev/null 2>&1 && dpkg -S "$path" >/dev/null 2>&1; } ||
    { command -v rpm >/dev/null 2>&1 && rpm -qf "$path" >/dev/null 2>&1; }; then
    echo "$path was installed by the $name package; upgrade it with apt, dnf or yum instead of this script" >&2
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

# The new binary checks the existing config before it replaces the installed
# one, so a config the new release rejects leaves the running version alone.
# The check runs as the service user so its sudo self-check sees the same
# sudoers rules as the service; install the sudoers file first.
install_binary_from_package() {
  name="$1"
  config_file="$2"
  source_file=$(find_binary_source "$name" || true)
  if [ -z "$source_file" ]; then
    echo "$name binary not found near script"
    exit 1
  fi
  install -d /usr/local/bin
  staged="/usr/local/bin/.$name.new"
  install -m 0755 "$source_file" "$staged"
  if ! runuser -u nut-server -- "$staged" -check-config -config "$config_file"; then
    rm -f "$staged"
    echo "$config_file failed the $name config check; binary not replaced and $name not restarted"
    echo "update the config (see docs/upgrade.md) and run this script again"
    exit 1
  fi
  mv -f "$staged" "/usr/local/bin/$name"
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

find_sudoers_source() {
  name="$1"
  find_existing_file \
    "$ROOT_DIR/sudoers/$name" \
    "$ROOT_DIR/packaging/sudoers/$name" \
    "$ROOT_DIR/dist/linux-amd64/packaging/sudoers/$name" \
    "$ROOT_DIR/dist/linux-arm64/packaging/sudoers/$name"
}

# Older releases shipped sudoers rules that never matched, so the shutdown
# command could not run; an upgrade must replace them too. The new file is
# validated before it replaces the installed one.
install_sudoers_from_package() {
  name="$1"
  source_file=$(find_sudoers_source "$name" || true)
  if [ -z "$source_file" ]; then
    echo "$name sudoers file not found near script"
    exit 1
  fi
  install -d -m 0750 /etc/sudoers.d
  staged="/etc/sudoers.d/.$name.new"
  install -o root -g root -m 0440 "$source_file" "$staged"
  if command -v visudo >/dev/null 2>&1 && ! visudo -c -f "$staged" >/dev/null; then
    rm -f "$staged"
    echo "$name sudoers file failed validation; installed rules left unchanged"
    exit 1
  fi
  mv -f "$staged" "/etc/sudoers.d/$name"
}

# Directories and config are root-owned and group-readable so the service can
# read its tokens but not rewrite them; only the state directory is writable.
secure_install_layout() {
  config_file="$1"
  install -d /usr/local/lib/nut-server
  install -d -o root -g nut-server -m 0750 /etc/nut-server
  install -d -o nut-server -g nut-server -m 0750 /var/lib/nut-server
  if [ -L "$config_file" ]; then
    echo "$config_file is a symlink; refusing to change its ownership"
    exit 1
  fi
  if [ -f "$config_file" ]; then
    chown root:nut-server "$config_file"
    chmod 0640 "$config_file"
  fi
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
