#!/bin/sh
set -eu

if command -v systemctl >/dev/null 2>&1; then
  systemctl stop nut-master 2>/dev/null || true
  systemctl disable nut-master 2>/dev/null || true
fi
