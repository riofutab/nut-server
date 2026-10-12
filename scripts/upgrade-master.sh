#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
. "$SCRIPT_DIR/upgrade-common.sh"

require_root
secure_install_layout /etc/nut-server/master.yaml
install_sudoers_from_package nut-server-master
install_binary_from_package nut-master /etc/nut-server/master.yaml
install_service_from_package nut-master.service
reload_systemd
restart_if_active nut-master

echo "upgraded nut-master"
echo "preserved /etc/nut-server/master.yaml and existing state data"
