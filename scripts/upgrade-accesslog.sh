#!/usr/bin/env bash
# Upgrade only the executable and the optional accesslog sidecar. Main config,
# included nodes, certificates, geodata and the installed manager are preserved.
set -Eeuo pipefail
umask 077

version='' endpoint='' username='' archive='' checksum='' password_file=''
usage() {
    echo 'Usage: upgrade-accesslog.sh (--version vX.Y.Z | --archive FILE --sha256 HEX) --endpoint HTTPS_URL --username WRITER [--password-file FILE]'
}
while (($#)); do
    case "$1" in
        --version|--endpoint|--username|--archive|--sha256|--password-file)
            (($# >= 2)) || { usage >&2; exit 2; }
            case "$1" in
                --version) version=$2 ;; --endpoint) endpoint=$2 ;; --username) username=$2 ;;
                --archive) archive=$2 ;; --sha256) checksum=$2 ;; --password-file) password_file=$2 ;;
            esac
            shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
done
[[ -n "$endpoint" && -n "$username" ]] || { usage >&2; exit 2; }
[[ -n "$version" || ( -n "$archive" && "$checksum" =~ ^[0-9a-fA-F]{64}$ ) ]] || { usage >&2; exit 2; }
[[ -z "$version" || ( "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ && -z "$archive" ) ]] || { usage >&2; exit 2; }
[[ $EUID == 0 ]] || { echo 'Run as root.' >&2; exit 1; }
for tool in curl unzip sha256sum systemctl install flock; do command -v "$tool" >/dev/null || { echo "Missing dependency: $tool" >&2; exit 1; }; done

# An explicit test-only root permits service/rollback tests without touching
# the host installation. Production always uses the established system paths.
prefix=''
if [[ ${V2BX_UPGRADE_TEST:-0} == 1 ]]; then
    prefix=${V2BX_UPGRADE_TEST_ROOT:?}
    [[ "$prefix" == /* && "$prefix" != / ]] || exit 2
fi
binary="$prefix/usr/local/V2bX/V2bX"
config="$prefix/etc/V2bX/config.json"
sidecar="$prefix/etc/V2bX/accesslog.json"
state="$prefix/var/lib/v2bx-accesslog-upgrade"
[[ -f "$binary" && -f "$config" && ! -L "$binary" && ! -L "$sidecar" ]] || { echo 'Existing regular V2bX installation required.' >&2; exit 1; }
install -d -m 0700 "$state"
exec 9>"$state/upgrade.lock"
flock -n 9 || { echo 'Another accesslog upgrade is running.' >&2; exit 1; }
temp=$(mktemp -d "$state/.staging.XXXXXX")
switched=0 was_active=0 backup=''
cleanup() {
    local code=$?
    trap - EXIT
    if ((code != 0 && switched)); then
        echo 'Upgrade failed; restoring the previous executable and accesslog config.' >&2
        systemctl stop V2bX.service || true
        install -m 0755 "$backup/V2bX" "$binary" || echo 'ERROR: executable rollback failed.' >&2
        if [[ -f "$backup/accesslog.json" ]]; then install -m 0600 "$backup/accesslog.json" "$sidecar" || echo 'ERROR: config rollback failed.' >&2; else rm -f -- "$sidecar"; fi
        if ((was_active)); then systemctl start V2bX.service || echo 'ERROR: restart after rollback failed; inspect journalctl -u V2bX.' >&2; fi
    fi
    rm -rf -- "$temp"
    exit "$code"
}
trap cleanup EXIT

if [[ -n "$version" ]]; then
    case $(uname -m) in
        x86_64) asset=V2bX-linux-64.zip ;; aarch64|arm64) asset=V2bX-linux-arm64-v8a.zip ;;
        *) echo 'This helper supports Linux amd64/arm64; use the normal installer on other platforms.' >&2; exit 1 ;;
    esac
    base="https://github.com/rmomo5285-droid/V2bX/releases/download/$version"
    curl --proto '=https' --tlsv1.2 -fLsS --retry 3 "$base/$asset" -o "$temp/release.zip"
    curl --proto '=https' --tlsv1.2 -fLsS --retry 3 "$base/$asset.sha256" -o "$temp/release.sha256"
    checksum=$(awk -v asset="$asset" '$2 == asset || $2 == "*"asset {print $1}' "$temp/release.sha256")
    [[ "$checksum" =~ ^[0-9a-fA-F]{64}$ ]] || { echo 'Invalid checksum sidecar.' >&2; exit 1; }
    archive="$temp/release.zip"
fi
actual=$(sha256sum "$archive"); actual=${actual%% *}
[[ "${actual,,}" == "${checksum,,}" ]] || { echo 'Archive checksum mismatch; service unchanged.' >&2; exit 1; }
unzip -p "$archive" V2bX > "$temp/V2bX"
chmod 0700 "$temp/V2bX"
"$temp/V2bX" accesslog configure --help | grep -q -- '--password-stdin' || { echo 'Archive does not support access reporting.' >&2; exit 1; }
args=(accesslog configure --config "$temp/config.json" --endpoint "$endpoint" --username "$username")
if [[ -n "$password_file" ]]; then
    "$temp/V2bX" "${args[@]}" --password-stdin < "$password_file"
else
    "$temp/V2bX" "${args[@]}"
fi
[[ -s "$temp/accesslog.json" ]] || { echo 'No valid access log config produced.' >&2; exit 1; }
backup="$state/backup-$(date -u +%Y%m%dT%H%M%SZ)-$$"
install -d -m 0700 "$backup"
cp -p -- "$binary" "$backup/V2bX"
if [[ -f "$sidecar" ]]; then install -m 0600 "$sidecar" "$backup/accesslog.json"; fi
if systemctl is-active --quiet V2bX.service; then was_active=1; fi
systemctl stop V2bX.service
switched=1
staged_binary=$(mktemp "${binary}.upgrade.XXXXXX")
install -m 0755 "$temp/V2bX" "$staged_binary"
mv -f -- "$staged_binary" "$binary"
staged_sidecar=$(mktemp "${sidecar}.upgrade.XXXXXX")
install -m 0600 "$temp/accesslog.json" "$staged_sidecar"
mv -f -- "$staged_sidecar" "$sidecar"
if ((was_active)); then
    systemctl start V2bX.service
    for ((i=0;i<10;i++)); do sleep 1; systemctl is-active --quiet V2bX.service || exit 1; done
fi
switched=0
echo "Upgrade completed. Existing node settings preserved. Rollback files: $backup"
echo "Reporting status: $binary accesslog status"
