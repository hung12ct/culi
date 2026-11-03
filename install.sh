#!/bin/sh
# Install the culi binary from GitHub Releases.
#
#   curl -fsSL https://raw.githubusercontent.com/hung12ct/culi/main/install.sh | sh
#
# Environment:
#   CULI_VERSION      release tag to install (default: latest)
#   CULI_INSTALL_DIR  destination directory (default: $HOME/.local/bin)
#   CULI_BASE_URL     download base URL override (mirrors, testing)
#   CULI_NO_INIT      set to skip running `culi init` after installing
set -eu

repo=hung12ct/culi
version=${CULI_VERSION:-latest}
install_dir=${CULI_INSTALL_DIR:-$HOME/.local/bin}

fail() {
	echo "culi install: $*" >&2
	exit 1
}

case "$(uname -s)" in
Darwin) os=darwin ;;
Linux) os=linux ;;
*) fail "unsupported OS $(uname -s); on Windows download culi_windows_amd64.zip from https://github.com/$repo/releases" ;;
esac

case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=arm64 ;;
*) fail "unsupported architecture $(uname -m)" ;;
esac

if [ -n "${CULI_BASE_URL:-}" ]; then
	base=$CULI_BASE_URL
elif [ "$version" = latest ]; then
	base="https://github.com/$repo/releases/latest/download"
else
	base="https://github.com/$repo/releases/download/$version"
fi

asset="culi_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

fetch() {
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		fail "need curl or wget"
	fi
}

echo "culi install: downloading $asset ($version)"
fetch "$base/$asset" "$tmp/$asset" || fail "download failed: $base/$asset"
fetch "$base/checksums.txt" "$tmp/checksums.txt" || fail "download failed: $base/checksums.txt"

want=$(awk -v f="$asset" '$2 == f || $2 == "*"f { print $1 }' "$tmp/checksums.txt")
[ -n "$want" ] || fail "no checksum listed for $asset"
if command -v sha256sum >/dev/null 2>&1; then
	got=$(sha256sum "$tmp/$asset" | awk '{ print $1 }')
else
	got=$(shasum -a 256 "$tmp/$asset" | awk '{ print $1 }')
fi
[ "$want" = "$got" ] || fail "checksum mismatch for $asset"

tar -xzf "$tmp/$asset" -C "$tmp" culi
mkdir -p "$install_dir"
mv "$tmp/culi" "$install_dir/culi"
chmod 0755 "$install_dir/culi"

echo "culi install: installed $("$install_dir/culi" version 2>/dev/null | head -n 1) to $install_dir/culi"
case ":$PATH:" in
*":$install_dir:"*) ;;
*) echo "culi install: add $install_dir to your PATH, e.g. export PATH=\"$install_dir:\$PATH\"" ;;
esac
if [ -n "${CULI_NO_INIT:-}" ]; then
	echo "culi install: next, run: culi init"
	exit 0
fi
# One command, like any agent plugin: register hooks + MCP for the Claude Code
# and Codex installs found on this machine. Re-running is safe (idempotent).
echo "culi install: running culi init (set CULI_NO_INIT=1 to skip)"
"$install_dir/culi" init
