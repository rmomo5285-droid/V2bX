#!/usr/bin/env bash
# Download a fixed release, verify both downloads, then use the rollback helper.
set -Eeuo pipefail
umask 077
release='v0.1.3-security.6'
repository='rmomo5285-droid/V2bX'
[[ $(uname -s) == Linux ]] || { echo 'Linux/systemd required.' >&2; exit 1; }
case "$(uname -m)" in
    x86_64|amd64) platform=linux-64 ;;
    aarch64|arm64) platform=linux-arm64-v8a ;;
    i386|i486|i586|i686) platform=linux-32 ;;
    armv7l) platform=linux-arm32-v7a ;;
    armv6l) platform=linux-arm32-v6 ;;
    armv5l|armv5tel) platform=linux-arm32-v5 ;;
    riscv64|ppc64|ppc64le|s390x) platform="linux-$(uname -m)" ;;
    *) echo 'Unsupported architecture; use the verified offline upgrade helper.' >&2; exit 1 ;;
esac
if [[ -z ${V2BX_SECURITY_TEST_ROOT:-} ]]; then
    ((EUID == 0)) || { echo 'Run as root.' >&2; exit 1; }
fi
for tool in curl sha256sum mktemp bash; do
    command -v "$tool" >/dev/null || { echo "Missing dependency: $tool" >&2; exit 1; }
done
temp=$(mktemp -d)
trap 'rm -rf -- "$temp"' EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
asset="V2bX-$platform.zip"
base="https://github.com/$repository/releases/download/$release"
for file in "$asset" "$asset.sha256" upgrade-security.sh upgrade-security.sh.sha256; do
    curl --fail --location --retry 3 --proto '=https' --proto-redir '=https' \
        --output "$temp/$file" "$base/$file"
done
verify() {
    local file=$1 hash name extra
    local -a lines
    mapfile -t lines < "$temp/$file.sha256"
    ((${#lines[@]} == 1)) || { echo "Invalid checksum file: $file" >&2; return 1; }
    read -r hash name extra <<< "${lines[0]}"
    [[ "$hash" =~ ^[0-9a-fA-F]{64}$ && "$name" == "$file" && -z "$extra" ]] || {
        echo "Invalid checksum entry: $file" >&2; return 1;
    }
    (cd "$temp" && printf '%s  %s\n' "$hash" "$file" | sha256sum -c -)
}
verify "$asset"
verify upgrade-security.sh
read -r checksum _ < "$temp/$asset.sha256"
bash "$temp/upgrade-security.sh" --archive "$temp/$asset" --sha256 "$checksum"
