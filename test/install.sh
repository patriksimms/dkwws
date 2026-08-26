#!/bin/sh

set -eu

repo_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

fixture_dir="$temp_dir/fixtures"
mock_bin="$temp_dir/mock-bin"
stage="$temp_dir/stage/dkwws_v0.1.0_linux_amd64"
install_dir="$temp_dir/install"
request_log="$temp_dir/requests"
mkdir -p "$fixture_dir" "$mock_bin" "$stage"

printf '#!/bin/sh\nprintf "fixture binary\\n"\n' > "$stage/dkwws"
chmod +x "$stage/dkwws"
tar -czf "$fixture_dir/dkwws_linux_amd64.tar.gz" -C "$temp_dir/stage" "$(basename "$stage")"
(
	cd "$fixture_dir"
	sha256sum ./dkwws_linux_amd64.tar.gz > SHA256SUMS
)

cat > "$mock_bin/uname" <<'EOF'
#!/bin/sh
case "$1" in
	-s) printf 'Linux\n' ;;
	-m) printf 'x86_64\n' ;;
	*) exit 1 ;;
esac
EOF

cat > "$mock_bin/curl" <<'EOF'
#!/bin/sh
while [ "$#" -gt 0 ]; do
	case "$1" in
		-o)
			destination=$2
			shift 2
			;;
		https://*)
			url=$1
			shift
			;;
		*) shift ;;
	esac
done
printf '%s\n' "$url" >> "$REQUEST_LOG"
cp "$FIXTURE_DIR/${url##*/}" "$destination"
EOF
chmod +x "$mock_bin/uname" "$mock_bin/curl"

export FIXTURE_DIR="$fixture_dir"
export REQUEST_LOG="$request_log"

run_installer() {
	PATH="$mock_bin:$PATH" \
		DKWWS_INSTALL_DIR="$install_dir" \
		sh "$repo_root/install.sh"
}

sh -n "$repo_root/install.sh"
run_installer

[ "$("$install_dir/dkwws")" = "fixture binary" ]
grep -Fq '/releases/latest/download/dkwws_linux_amd64.tar.gz' "$request_log"
grep -Fq '/releases/latest/download/SHA256SUMS' "$request_log"

: > "$request_log"
DKWWS_VERSION=1.2.3 run_installer
grep -Fq '/releases/download/v1.2.3/dkwws_linux_amd64.tar.gz' "$request_log"

printf '%064d  ./dkwws_linux_amd64.tar.gz\n' 0 > "$fixture_dir/SHA256SUMS"
if run_installer > "$temp_dir/checksum-output" 2>&1; then
	printf 'Installer accepted an invalid checksum.\n' >&2
	exit 1
fi
grep -Fq 'checksum verification failed' "$temp_dir/checksum-output"

printf 'Installer tests passed.\n'
