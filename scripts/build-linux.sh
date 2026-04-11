#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
ROOT_DIR=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DIST_DIR="$ROOT_DIR/dist"

build_target() {
  arch="$1"
  out_dir="$DIST_DIR/linux-$arch"

  rm -rf "$out_dir"
  mkdir -p "$out_dir/configs" "$out_dir/packaging/systemd" "$out_dir/scripts"

  (
    cd "$ROOT_DIR"
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$out_dir/nut-master" ./cmd/nut-master
    CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -o "$out_dir/nut-slave" ./cmd/nut-slave
  )

  cp "$ROOT_DIR/configs/master.example.yaml" "$out_dir/configs/master.example.yaml"
  cp "$ROOT_DIR/configs/slave.example.yaml" "$out_dir/configs/slave.example.yaml"
  cp "$ROOT_DIR/packaging/systemd/nut-master.service" "$out_dir/packaging/systemd/nut-master.service"
  cp "$ROOT_DIR/packaging/systemd/nut-slave.service" "$out_dir/packaging/systemd/nut-slave.service"
  cp "$ROOT_DIR/scripts/install-master.sh" "$out_dir/scripts/install-master.sh"
  cp "$ROOT_DIR/scripts/install-slave.sh" "$out_dir/scripts/install-slave.sh"
  chmod +x "$out_dir/scripts/install-master.sh" "$out_dir/scripts/install-slave.sh"
}

mkdir -p "$DIST_DIR"
build_target amd64
build_target arm64

echo "linux builds generated under $DIST_DIR"
