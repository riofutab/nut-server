#!/usr/bin/env sh
set -eu

REPO="riofutab/nut-server"
ROLE=""
VERSION="latest"
ARCH=""
PKG_FORMAT="auto"
WORK_DIR=""

cleanup() {
  if [ -n "$WORK_DIR" ] && [ -d "$WORK_DIR" ]; then
    rm -rf "$WORK_DIR"
  fi
}

trap cleanup EXIT INT TERM

usage() {
  cat <<'EOF'
usage: install-online.sh --role <master|slave|upgrade-master|upgrade-slave> [--version <tag>|latest] [--repo <owner/repo>] [--arch <amd64|arm64>] [--pkg <auto|deb|rpm|tar>] -- [role-script options]

  --pkg controls install format. Default "auto" prefers the host's
  package manager (deb on apt systems, rpm on dnf/yum systems) when
  the role is master|slave and no role-script options follow `--`.
  Otherwise it falls back to the tar.gz path so role-script flags
  (e.g. --token, --snmp-target) can prefill the config.

Examples:
  ./scripts/install-online.sh --role master --version v0.1.4 -- --token your-token --snmp-target 10.0.0.31
  ./scripts/install-online.sh --role slave --version latest -- --node-id slave-01 --master-addr 10.0.0.10:9000 --token your-token
  ./scripts/install-online.sh --role master --version latest --pkg deb
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

detect_pkg_manager() {
  if command -v apt-get >/dev/null 2>&1; then
    printf 'apt\n'
  elif command -v dnf >/dev/null 2>&1; then
    printf 'dnf\n'
  elif command -v yum >/dev/null 2>&1; then
    printf 'yum\n'
  else
    printf 'none\n'
  fi
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

verify_sha() {
  asset="$1"
  sha_file="$2"
  sum_line=$(grep " $asset\$" "$sha_file" || true)
  if [ -z "$sum_line" ]; then
    echo "checksum for $asset not found in SHA256SUMS" >&2
    exit 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s\n' "$sum_line" | sha256sum -c -
  elif command -v shasum >/dev/null 2>&1; then
    printf '%s\n' "$sum_line" | shasum -a 256 -c -
  else
    echo "neither sha256sum nor shasum available; cannot verify checksum" >&2
    exit 1
  fi
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    echo "package install requires root; re-run with sudo" >&2
    exit 1
  fi
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
    --pkg)
      PKG_FORMAT="$2"
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
  master|slave|upgrade-master|upgrade-slave) ;;
  *)
    echo "unsupported role: $ROLE"
    usage
    exit 1
    ;;
esac

resolve_format() {
  case "$PKG_FORMAT" in
    deb|rpm|tar) printf '%s\n' "$PKG_FORMAT"; return ;;
  esac
  if [ "$PKG_FORMAT" != "auto" ]; then
    echo "invalid --pkg value: $PKG_FORMAT" >&2
    exit 1
  fi
  # auto path
  case "$ROLE" in
    upgrade-master|upgrade-slave)
      printf 'tar\n'
      return
      ;;
  esac
  if [ "$#" -gt 0 ]; then
    # role-script options were passed; tar path supports prefill
    printf 'tar\n'
    return
  fi
  pkg_mgr=$(detect_pkg_manager)
  case "$pkg_mgr" in
    apt) printf 'deb\n' ;;
    dnf|yum) printf 'rpm\n' ;;
    *) printf 'tar\n' ;;
  esac
}

format=$(resolve_format "$@")
if [ "$format" != "tar" ] && [ "$#" -gt 0 ]; then
  echo "warning: role-script options after '--' are ignored when installing via $format package" >&2
fi

WORK_DIR=$(mktemp -d)
sha_url="https://github.com/${REPO}/releases/download/${VERSION}/SHA256SUMS"
sha_path="$WORK_DIR/SHA256SUMS"
echo "downloading $sha_url"
curl -fsSL "$sha_url" -o "$sha_path"

clean_version=$(printf '%s' "$VERSION" | sed 's/^v//')

case "$format" in
  tar)
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
    esac
    asset_name="${package_name}_${VERSION}_linux_${ARCH}.tar.gz"
    download_url="https://github.com/${REPO}/releases/download/${VERSION}/${asset_name}"
    archive_path="$WORK_DIR/$asset_name"

    echo "downloading $download_url"
    curl -fsSL "$download_url" -o "$archive_path"
    (cd "$WORK_DIR" && verify_sha "$asset_name" SHA256SUMS)

    root_name=$(tar -tzf "$archive_path" | head -n 1 | cut -d/ -f1)
    if [ -z "$root_name" ]; then
      echo "failed to inspect archive layout"
      exit 1
    fi
    tar -C "$WORK_DIR" -xzf "$archive_path"
    "$WORK_DIR/$root_name/scripts/$script_name" "$@"
    ;;
  deb)
    case "$ROLE" in
      master) pkg_base="nut-master" ;;
      slave) pkg_base="nut-slave" ;;
      *)
        echo "deb install only supports master|slave roles" >&2
        exit 1
        ;;
    esac
    asset_name="${pkg_base}_${clean_version}_linux_${ARCH}.deb"
    download_url="https://github.com/${REPO}/releases/download/${VERSION}/${asset_name}"
    pkg_path="$WORK_DIR/$asset_name"
    echo "downloading $download_url"
    curl -fsSL "$download_url" -o "$pkg_path"
    (cd "$WORK_DIR" && verify_sha "$asset_name" SHA256SUMS)
    require_root
    apt-get install -y "$pkg_path"
    ;;
  rpm)
    case "$ROLE" in
      master) pkg_base="nut-master" ;;
      slave) pkg_base="nut-slave" ;;
      *)
        echo "rpm install only supports master|slave roles" >&2
        exit 1
        ;;
    esac
    asset_name="${pkg_base}_${clean_version}_linux_${ARCH}.rpm"
    download_url="https://github.com/${REPO}/releases/download/${VERSION}/${asset_name}"
    pkg_path="$WORK_DIR/$asset_name"
    echo "downloading $download_url"
    curl -fsSL "$download_url" -o "$pkg_path"
    (cd "$WORK_DIR" && verify_sha "$asset_name" SHA256SUMS)
    require_root
    if command -v dnf >/dev/null 2>&1; then
      dnf install -y "$pkg_path"
    else
      yum install -y "$pkg_path"
    fi
    ;;
esac
