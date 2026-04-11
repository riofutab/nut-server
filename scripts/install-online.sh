#!/usr/bin/env sh
set -eu

REPO="riofutab/nut-server"
ROLE=""
VERSION="latest"
ARCH=""
WORK_DIR=""

cleanup() {
  if [ -n "$WORK_DIR" ] && [ -d "$WORK_DIR" ]; then
    rm -rf "$WORK_DIR"
  fi
}

trap cleanup EXIT INT TERM

usage() {
  cat <<'EOF'
usage: install-online.sh --role <master|slave|upgrade-master|upgrade-slave> [--version <tag>|latest] [--repo <owner/repo>] [--arch <amd64|arm64>] -- [role-script options]

Examples:
  ./scripts/install-online.sh --role master --version v0.1.3 -- --token your-token --snmp-target 10.0.0.31
  ./scripts/install-online.sh --role slave --version latest -- --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
  ./scripts/install-online.sh --role upgrade-master --version latest
EOF
}

detect_arch() {
  machine=$(uname -m)
  case "$machine" in
    x86_64|amd64)
      printf 'amd64\n'
      ;;
    aarch64|arm64)
      printf 'arm64\n'
      ;;
    *)
      echo "unsupported architecture: $machine"
      exit 1
      ;;
  esac
}

resolve_latest_version() {
  repo="$1"
  tag=$(
    curl -fsSL "https://api.github.com/repos/$repo/releases/latest" |
      sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' |
      head -n 1
  )
  if [ -z "$tag" ]; then
    echo "failed to resolve latest release tag for $repo"
    exit 1
  fi
  printf '%s\n' "$tag"
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --role)
      ROLE="$2"
      shift 2
      ;;
    --version)
      VERSION="$2"
      shift 2
      ;;
    --repo)
      REPO="$2"
      shift 2
      ;;
    --arch)
      ARCH="$2"
      shift 2
      ;;
    --)
      shift
      break
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "unknown argument: $1"
      usage
      exit 1
      ;;
  esac
done

if [ -z "$ROLE" ]; then
  echo "--role is required"
  usage
  exit 1
fi

if [ -z "$ARCH" ]; then
  ARCH=$(detect_arch)
fi

if [ "$VERSION" = "latest" ]; then
  VERSION=$(resolve_latest_version "$REPO")
fi

case "$ROLE" in
  master)
    package_name="nut-master"
    script_name="quick-install-master.sh"
    ;;
  slave)
    package_name="nut-slave"
    script_name="quick-install-slave.sh"
    ;;
  upgrade-master)
    package_name="nut-server-upgrade"
    script_name="upgrade-master.sh"
    ;;
  upgrade-slave)
    package_name="nut-server-upgrade"
    script_name="upgrade-slave.sh"
    ;;
  *)
    echo "unsupported role: $ROLE"
    usage
    exit 1
    ;;
esac

asset_name="${package_name}_${VERSION}_linux_${ARCH}.tar.gz"
download_url="https://github.com/${REPO}/releases/download/${VERSION}/${asset_name}"
WORK_DIR=$(mktemp -d)
archive_path="$WORK_DIR/$asset_name"

echo "downloading $download_url"
curl -fsSL "$download_url" -o "$archive_path"

root_name=$(tar -tzf "$archive_path" | head -n 1 | cut -d/ -f1)
if [ -z "$root_name" ]; then
  echo "failed to inspect archive layout"
  exit 1
fi

tar -C "$WORK_DIR" -xzf "$archive_path"
"$WORK_DIR/$root_name/scripts/$script_name" "$@"
