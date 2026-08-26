#!/bin/sh

set -eu

repository="patriksimms/dkwws"
version="${DKWWS_VERSION:-latest}"
install_dir="${DKWWS_INSTALL_DIR:-${HOME:?HOME is not set}/.local/bin}"

fail() {
	printf 'dkwws installer: %s\n' "$1" >&2
	exit 1
}

case "$(uname -s)" in
	Linux) os=linux ;;
	Darwin) os=darwin ;;
	*) fail "unsupported operating system: $(uname -s)" ;;
esac

machine=$(uname -m)
if [ "$os" = "darwin" ] && [ "$machine" = "x86_64" ]; then
	if [ "$(sysctl -in sysctl.proc_translated 2>/dev/null || true)" = "1" ]; then
		machine=arm64
	fi
fi

case "$machine" in
	x86_64 | amd64) arch=amd64 ;;
	aarch64 | arm64) arch=arm64 ;;
	*) fail "unsupported architecture: $machine" ;;
esac

if [ "$os/$arch" = "darwin/amd64" ]; then
	fail "macOS amd64 releases are not available"
fi

asset="dkwws_${os}_${arch}.tar.gz"
if [ "$version" = "latest" ]; then
	download_url="https://github.com/${repository}/releases/latest/download"
else
	case "$version" in
		v*) ;;
		*) version="v${version}" ;;
	esac
	download_url="https://github.com/${repository}/releases/download/${version}"
fi

command -v curl >/dev/null 2>&1 || fail "curl is required"
command -v tar >/dev/null 2>&1 || fail "tar is required"
command -v install >/dev/null 2>&1 || fail "install is required"

temp_dir=$(mktemp -d 2>/dev/null || mktemp -d -t dkwws)
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

archive="$temp_dir/$asset"
checksums="$temp_dir/SHA256SUMS"

printf 'Downloading dkwws %s for %s/%s...\n' "$version" "$os" "$arch"
curl -fsSL "$download_url/$asset" -o "$archive" || fail "could not download $asset"
curl -fsSL "$download_url/SHA256SUMS" -o "$checksums" || fail "could not download SHA256SUMS"

expected_checksum=$(awk -v asset="$asset" '
	{
		name = $2
		sub(/^\*/, "", name)
		sub(/^\.\//, "", name)
		if (name == asset) {
			print $1
			exit
		}
	}
' "$checksums")
[ -n "$expected_checksum" ] || fail "SHA256SUMS does not contain $asset"

if command -v sha256sum >/dev/null 2>&1; then
	actual_checksum=$(sha256sum "$archive" | awk '{print $1}')
elif command -v shasum >/dev/null 2>&1; then
	actual_checksum=$(shasum -a 256 "$archive" | awk '{print $1}')
else
	fail "sha256sum or shasum is required"
fi

[ "$actual_checksum" = "$expected_checksum" ] || fail "checksum verification failed"

extract_dir="$temp_dir/extract"
mkdir -p "$extract_dir"
tar -xzf "$archive" -C "$extract_dir"

binary=$(find "$extract_dir" -type f -name dkwws -print | head -n 1)
[ -n "$binary" ] || fail "release archive does not contain dkwws"

mkdir -p "$install_dir"
install -m 0755 "$binary" "$install_dir/dkwws"

printf 'Installed dkwws to %s/dkwws\n' "$install_dir"
case ":${PATH:-}:" in
	*":$install_dir:"*) ;;
	*) printf 'Add %s to PATH to run dkwws from your shell.\n' "$install_dir" ;;
esac
