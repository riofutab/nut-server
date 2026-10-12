#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/upgrade-common.sh"

require_root
secure_install_layout /etc/nut-server/slave.yaml
install_sudoers_from_package nut-server-slave
install_binary_from_package nut-slave /etc/nut-server/slave.yaml
install_service_from_package nut-slave.service
reload_systemd
restart_if_active nut-slave

echo "upgraded nut-slave"
echo "preserved /etc/nut-server/slave.yaml and existing state data"
