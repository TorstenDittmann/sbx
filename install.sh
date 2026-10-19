#!/bin/sh
# Install sbx from a GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/sbx/main/install.sh | sh
#
# Linux installs sbx and sbx-agent. macOS installs the darwin sbx binary and
# the linux sbx-agent of the same architecture, because the agent runs inside
# the sandbox container rather than on the host.
set -eu

REPO="${SBX_REPO:-TorstenDittmann/sbx}"

die() {
  printf '%s\n' "$*" >&2
  exit 1
}

usage() {
  cat <<'EOF'
Usage: install.sh [--version vX.Y.Z] [--print-target]

  curl -fsSL https://raw.githubusercontent.com/TorstenDittmann/sbx/main/install.sh | sh

Environment:
  SBX_VERSION       Release tag, v0.2.0 or 0.2.0. Defaults to the latest release.
  SBX_INSTALL_DIR   Directory for the binaries. Defaults to /usr/local/bin when
                    that directory is writable, otherwise ~/.local/bin.
  SBX_OS            linux or darwin. Defaults to uname -s.
  SBX_ARCH          amd64 or arm64. Defaults to uname -m.
  SBX_REPO          GitHub owner/name. Defaults to TorstenDittmann/sbx.
  SBX_SKIP_AGENT    Set to 1 to install sbx without sbx-agent.
EOF
}

detect_os() {
  if [ -n "${SBX_OS:-}" ]; then
    printf '%s\n' "$SBX_OS"
    return
  fi
  case "$(uname -s)" in
    Linux) printf '%s\n' linux ;;
    Darwin) printf '%s\n' darwin ;;
    *) die "unsupported OS: $(uname -s)" ;;
  esac
}

detect_arch() {
  if [ -n "${SBX_ARCH:-}" ]; then
    printf '%s\n' "$SBX_ARCH"
    return
  fi
  case "$(uname -m)" in
    x86_64 | amd64) printf '%s\n' amd64 ;;
    arm64 | aarch64) printf '%s\n' arm64 ;;
    *) die "unsupported architecture: $(uname -m)" ;;
  esac
}

validate_target() {
  case "$1" in
    linux | darwin) ;;
    *) die "unsupported OS: $1" ;;
  esac
  case "$2" in
    amd64 | arm64) ;;
    *) die "unsupported architecture: $2" ;;
  esac
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    die "sha256sum or shasum is required"
  fi
}

fetch() {
  url=$1
  out=$2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 3 --retry-delay 2 -o "$out" "$url" || die "download failed: $url"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$out" "$url" || die "download failed: $url"
  else
    die "curl or wget is required"
  fi
}

check_sum() {
  file=$1
  name=$2
  sums=$3
  want=$(awk -v name="$name" '$NF == name { print $1; exit }' "$sums")
  if [ -z "$want" ]; then
    die "checksums.txt has no entry for $name"
  fi
  got=$(sha256_file "$file")
  if [ "$got" != "$want" ]; then
    die "checksum mismatch for $name"
  fi
}

install_bin() {
  src=$1
  dest_dir=$2
  name=$(basename "$src")
  mkdir -p "$dest_dir" 2>/dev/null || true
  if [ -w "$dest_dir" ]; then
    cp "$src" "$dest_dir/$name"
    chmod 755 "$dest_dir/$name"
    return
  fi
  if command -v sudo >/dev/null 2>&1; then
    sudo mkdir -p "$dest_dir"
    sudo cp "$src" "$dest_dir/$name"
    sudo chmod 755 "$dest_dir/$name"
    return
  fi
  die "cannot write to $dest_dir (set SBX_INSTALL_DIR)"
}

default_dest() {
  if [ -n "${SBX_INSTALL_DIR:-}" ]; then
    printf '%s\n' "$SBX_INSTALL_DIR"
    return
  fi
  if [ -d /usr/local/bin ] && [ -w /usr/local/bin ]; then
    printf '%s\n' /usr/local/bin
    return
  fi
  if [ "$(id -u)" -eq 0 ]; then
    printf '%s\n' /usr/local/bin
    return
  fi
  if [ -z "${HOME:-}" ]; then
    die "HOME is not set; pass SBX_INSTALL_DIR"
  fi
  printf '%s\n' "$HOME/.local/bin"
}

print_target() {
  os_name=$(detect_os)
  arch_name=$(detect_arch)
  validate_target "$os_name" "$arch_name"
  printf 'os=%s\n' "$os_name"
  printf 'arch=%s\n' "$arch_name"
  printf 'agent_os=linux\n'
  printf 'agent_arch=%s\n' "$arch_name"
}

self_test() {
  linux_out=$(SBX_OS=linux SBX_ARCH=amd64 sh "$0" --print-target)
  printf '%s\n' "$linux_out" | grep -qx 'os=linux'
  printf '%s\n' "$linux_out" | grep -qx 'arch=amd64'
  printf '%s\n' "$linux_out" | grep -qx 'agent_os=linux'
  printf '%s\n' "$linux_out" | grep -qx 'agent_arch=amd64'

  mac_out=$(SBX_OS=darwin SBX_ARCH=arm64 sh "$0" --print-target)
  printf '%s\n' "$mac_out" | grep -qx 'os=darwin'
  printf '%s\n' "$mac_out" | grep -qx 'arch=arm64'
  printf '%s\n' "$mac_out" | grep -qx 'agent_os=linux'
  printf '%s\n' "$mac_out" | grep -qx 'agent_arch=arm64'

  if SBX_OS=freebsd SBX_ARCH=amd64 sh "$0" --print-target >/dev/null 2>&1; then
    die "freebsd was accepted"
  fi
  if SBX_OS=linux SBX_ARCH=ppc64 sh "$0" --print-target >/dev/null 2>&1; then
    die "ppc64 was accepted"
  fi

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT
  printf 'abc' >"$tmp/f"
  got=$(sha256_file "$tmp/f")
  want=ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad
  if [ "$got" != "$want" ]; then
    die "sha256 self-test failed: $got"
  fi
  printf '%s  f\n' "$want" >"$tmp/checksums.txt"
  check_sum "$tmp/f" f "$tmp/checksums.txt"
  printf '0000000000000000000000000000000000000000000000000000000000000000  f\n' >"$tmp/checksums.txt"
  # die exits the shell, so the rejection has to run in a subshell.
  if (check_sum "$tmp/f" f "$tmp/checksums.txt") >/dev/null 2>&1; then
    die "bad checksum was accepted"
  fi
  printf 'install.sh self-test ok\n'
}

PRINT_TARGET=0
SELF_TEST=0
while [ $# -gt 0 ]; do
  case "$1" in
    -h | --help)
      usage
      exit 0
      ;;
    --print-target)
      PRINT_TARGET=1
      shift
      ;;
    --self-test)
      SELF_TEST=1
      shift
      ;;
    --version)
      if [ $# -lt 2 ]; then
        die "--version needs a value"
      fi
      SBX_VERSION=$2
      shift 2
      ;;
    --version=*)
      SBX_VERSION=${1#--version=}
      shift
      ;;
    *)
      die "unknown argument: $1"
      ;;
  esac
done

if [ "$PRINT_TARGET" -eq 1 ]; then
  print_target
  exit 0
fi

if [ "$SELF_TEST" -eq 1 ]; then
  self_test
  exit 0
fi

os_name=$(detect_os)
arch_name=$(detect_arch)
validate_target "$os_name" "$arch_name"

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

if [ -n "${SBX_VERSION:-}" ]; then
  tag=$SBX_VERSION
else
  printf 'looking up the latest release\n' >&2
  fetch "https://api.github.com/repos/${REPO}/releases/latest" "$tmpdir/release.json"
  tag=$(sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' "$tmpdir/release.json" | head -n 1)
  if [ -z "$tag" ]; then
    die "could not find the latest release for ${REPO}"
  fi
fi
case "$tag" in
  v*) ;;
  *) tag="v${tag}" ;;
esac
ver=${tag#v}

base="https://github.com/${REPO}/releases/download/${tag}"
sbx_name="sbx_${ver}_${os_name}_${arch_name}.tar.gz"
agent_name="sbx-agent_${ver}_linux_${arch_name}.tar.gz"

printf 'downloading %s\n' "$sbx_name" >&2
fetch "$base/checksums.txt" "$tmpdir/checksums.txt"
fetch "$base/$sbx_name" "$tmpdir/$sbx_name"
check_sum "$tmpdir/$sbx_name" "$sbx_name" "$tmpdir/checksums.txt"
tar -xzf "$tmpdir/$sbx_name" -C "$tmpdir" sbx

if [ "${SBX_SKIP_AGENT:-}" != "1" ]; then
  printf 'downloading %s\n' "$agent_name" >&2
  fetch "$base/$agent_name" "$tmpdir/$agent_name"
  check_sum "$tmpdir/$agent_name" "$agent_name" "$tmpdir/checksums.txt"
  tar -xzf "$tmpdir/$agent_name" -C "$tmpdir" sbx-agent
fi

dest=$(default_dest)
install_bin "$tmpdir/sbx" "$dest"
if [ "${SBX_SKIP_AGENT:-}" != "1" ]; then
  install_bin "$tmpdir/sbx-agent" "$dest"
fi

printf 'installed sbx to %s\n' "$dest" >&2
case ":${PATH}:" in
  *":${dest}:"*) ;;
  *) printf '%s is not on PATH\n' "$dest" >&2 ;;
esac
