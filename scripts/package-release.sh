#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DIST_DIR="$ROOT_DIR/dist"
RELEASE_DIR="$DIST_DIR/release"
VERSION=${VERSION:-dev}

"$ROOT_DIR/scripts/build-linux.sh"
rm -rf "$RELEASE_DIR"
mkdir -p "$RELEASE_DIR"

reset_stage() {
  stage_dir="$1"
  rm -rf "$stage_dir"
  mkdir -p "$stage_dir"
}

copy_common_files() {
  stage_dir="$1"
  readme_source="$2"
  cp "$readme_source" "$stage_dir/README.md"
  cp "$ROOT_DIR/LICENSE" "$stage_dir/LICENSE"
}

copy_master_files() {
  src_dir="$1"
  stage_dir="$2"
  mkdir -p "$stage_dir/bin" "$stage_dir/configs" "$stage_dir/systemd" "$stage_dir/scripts"
  cp "$src_dir/nut-master" "$stage_dir/bin/nut-master"
  cp "$src_dir/configs/master.example.yaml" "$stage_dir/configs/master.example.yaml"
  cp "$src_dir/packaging/systemd/nut-master.service" "$stage_dir/systemd/nut-master.service"
  cp "$src_dir/scripts/install-master.sh" "$stage_dir/scripts/install-master.sh"
  cp "$src_dir/scripts/quick-install-master.sh" "$stage_dir/scripts/quick-install-master.sh"
  cp "$src_dir/scripts/install-online.sh" "$stage_dir/scripts/install-online.sh"
  cp "$src_dir/scripts/upgrade-common.sh" "$stage_dir/scripts/upgrade-common.sh"
  cp "$src_dir/scripts/upgrade-master.sh" "$stage_dir/scripts/upgrade-master.sh"
}

copy_slave_files() {
  src_dir="$1"
  stage_dir="$2"
  mkdir -p "$stage_dir/bin" "$stage_dir/configs" "$stage_dir/systemd" "$stage_dir/scripts"
  cp "$src_dir/nut-slave" "$stage_dir/bin/nut-slave"
  cp "$src_dir/configs/slave.example.yaml" "$stage_dir/configs/slave.example.yaml"
  cp "$src_dir/packaging/systemd/nut-slave.service" "$stage_dir/systemd/nut-slave.service"
  cp "$src_dir/scripts/install-slave.sh" "$stage_dir/scripts/install-slave.sh"
  cp "$src_dir/scripts/quick-install-slave.sh" "$stage_dir/scripts/quick-install-slave.sh"
  cp "$src_dir/scripts/install-online.sh" "$stage_dir/scripts/install-online.sh"
  cp "$src_dir/scripts/upgrade-common.sh" "$stage_dir/scripts/upgrade-common.sh"
  cp "$src_dir/scripts/upgrade-slave.sh" "$stage_dir/scripts/upgrade-slave.sh"
}

write_archive() {
  archive="$1"
  stage_dir="$2"
  tar -C "$RELEASE_DIR" -czf "$archive" "$(basename "$stage_dir")"
  rm -rf "$stage_dir"
}

package_full() {
  arch="$1"
  src_dir="$DIST_DIR/linux-$arch"
  stage_dir="$RELEASE_DIR/nut-server_linux_${arch}"
  archive="$RELEASE_DIR/nut-server_${VERSION}_linux_${arch}.tar.gz"

  reset_stage "$stage_dir"
  copy_master_files "$src_dir" "$stage_dir"
  copy_slave_files "$src_dir" "$stage_dir"
  cp "$ROOT_DIR/README.md" "$stage_dir/README.md"
  cp "$ROOT_DIR/LICENSE" "$stage_dir/LICENSE"
  write_archive "$archive" "$stage_dir"
}

package_master() {
  arch="$1"
  src_dir="$DIST_DIR/linux-$arch"
  stage_dir="$RELEASE_DIR/nut-master_linux_${arch}"
  archive="$RELEASE_DIR/nut-master_${VERSION}_linux_${arch}.tar.gz"

  reset_stage "$stage_dir"
  copy_master_files "$src_dir" "$stage_dir"
  copy_common_files "$stage_dir" "$ROOT_DIR/README-master.md"
  write_archive "$archive" "$stage_dir"
}

package_slave() {
  arch="$1"
  src_dir="$DIST_DIR/linux-$arch"
  stage_dir="$RELEASE_DIR/nut-slave_linux_${arch}"
  archive="$RELEASE_DIR/nut-slave_${VERSION}_linux_${arch}.tar.gz"

  reset_stage "$stage_dir"
  copy_slave_files "$src_dir" "$stage_dir"
  copy_common_files "$stage_dir" "$ROOT_DIR/README-slave.md"
  write_archive "$archive" "$stage_dir"
}

package_upgrade() {
  arch="$1"
  src_dir="$DIST_DIR/linux-$arch"
  stage_dir="$RELEASE_DIR/nut-server-upgrade_linux_${arch}"
  archive="$RELEASE_DIR/nut-server-upgrade_${VERSION}_linux_${arch}.tar.gz"

  reset_stage "$stage_dir"
  mkdir -p "$stage_dir/bin" "$stage_dir/systemd" "$stage_dir/scripts"
  cp "$src_dir/nut-master" "$stage_dir/bin/nut-master"
  cp "$src_dir/nut-slave" "$stage_dir/bin/nut-slave"
  cp "$src_dir/packaging/systemd/nut-master.service" "$stage_dir/systemd/nut-master.service"
  cp "$src_dir/packaging/systemd/nut-slave.service" "$stage_dir/systemd/nut-slave.service"
  cp "$src_dir/scripts/upgrade-common.sh" "$stage_dir/scripts/upgrade-common.sh"
  cp "$src_dir/scripts/upgrade-master.sh" "$stage_dir/scripts/upgrade-master.sh"
  cp "$src_dir/scripts/upgrade-slave.sh" "$stage_dir/scripts/upgrade-slave.sh"
  copy_common_files "$stage_dir" "$ROOT_DIR/README-upgrade.md"
  write_archive "$archive" "$stage_dir"
}

for arch in amd64 arm64
do
  package_full "$arch"
  package_master "$arch"
  package_slave "$arch"
  package_upgrade "$arch"
done

echo "release archives generated under $RELEASE_DIR"
