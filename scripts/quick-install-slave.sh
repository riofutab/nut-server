#!/usr/bin/env sh
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)

usage() {
  cat <<'EOF'
usage: quick-install-slave.sh --node-id <node-id> --master-addr <host:port> --token <token> [install-slave options]

Defaults:
  - TLS is disabled unless you pass any --tls-* option or --disable-tls yourself.
  - All remaining flags are forwarded to install-slave.sh unchanged.

Example:
  sudo ./scripts/quick-install-slave.sh --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
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

exec "$SCRIPT_DIR/install-slave.sh" "$@"
