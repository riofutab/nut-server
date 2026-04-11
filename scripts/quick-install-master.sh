#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

usage() {
  cat <<'EOF'
usage: quick-install-master.sh --token <token> --snmp-target <host> [install-master options]

Defaults:
  - TLS is disabled unless you pass any --tls-* option or --disable-tls yourself.
  - All remaining flags are forwarded to install-master.sh unchanged.

Example:
  sudo ./scripts/quick-install-master.sh --token your-token --snmp-target 10.0.0.31
EOF
}

if [ "$#" -eq 0 ]; then
  usage
  exit 1
fi

tls_configured="false"
for arg in "$@"
do
  case "$arg" in
    -h|--help)
      usage
      exit 0
      ;;
    --disable-tls|--tls-*)
      tls_configured="true"
      ;;
  esac
done

if [ "$tls_configured" = "false" ]; then
  set -- --disable-tls "$@"
fi

exec "$SCRIPT_DIR/install-master.sh" "$@"
