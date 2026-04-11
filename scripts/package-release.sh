#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DIST_DIR="$ROOT_DIR/dist"
RELEASE_DIR="$DIST_DIR/release"
VERSION=${VERSION:-dev}

"$ROOT_DIR/scripts/build-linux.sh"
mkdir -p "$RELEASE_DIR"

package_target() {
  arch="$1"
  src_dir="$DIST_DIR/linux-$arch"
  stage_dir="$RELEASE_DIR/nut-server_linux_${arch}"
  archive="$RELEASE_DIR/nut-server_${VERSION}_linux_${arch}.tar.gz"

  rm -rf "$stage_dir"
  mkdir -p "$stage_dir/bin" "$stage_dir/configs" "$stage_dir/systemd" "$stage_dir/scripts"

  cp "$src_dir/nut-master" "$stage_dir/bin/nut-master"
  cp "$src_dir/nut-slave" "$stage_dir/bin/nut-slave"
  cp "$src_dir/configs/master.example.yaml" "$stage_dir/configs/master.example.yaml"
  cp "$src_dir/configs/slave.example.yaml" "$stage_dir/configs/slave.example.yaml"
  cp "$src_dir/packaging/systemd/nut-master.service" "$stage_dir/systemd/nut-master.service"
  cp "$src_dir/packaging/systemd/nut-slave.service" "$stage_dir/systemd/nut-slave.service"
  cp "$src_dir/scripts/install-master.sh" "$stage_dir/scripts/install-master.sh"
  cp "$src_dir/scripts/install-slave.sh" "$stage_dir/scripts/install-slave.sh"
  cp "$ROOT_DIR/README.md" "$stage_dir/README.md"
  cp "$ROOT_DIR/LICENSE" "$stage_dir/LICENSE"

  tar -C "$RELEASE_DIR" -czf "$archive" "$(basename "$stage_dir")"
}

package_target amd64
package_target arm64

echo "release archives generated under $RELEASE_DIR"
