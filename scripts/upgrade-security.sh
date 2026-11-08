#!/usr/bin/env bash
# Offline binary-only upgrade of the standard systemd installation.
set -Eeuo pipefail
umask 077
archive='' checksum=''
usage() { echo 'Usage: bash upgrade-security.sh --archive FILE --sha256 HEX'; }
while (($#)); do
    case "$1" in
        --archive|--sha256)
            (($# >= 2)) || { usage >&2; exit 2; }
            if [[ $1 == --archive ]]; then archive=$2; else checksum=$2; fi
            shift 2 ;;
        -h|--help) usage; exit 0 ;;
        *) usage >&2; exit 2 ;;
    esac
done
[[ -f "$archive" && "$checksum" =~ ^[0-9a-fA-F]{64}$ ]] || { usage >&2; exit 2; }
[[ $(uname -s) == Linux ]] || { echo 'Linux/systemd required.' >&2; exit 1; }
prefix=${V2BX_SECURITY_TEST_ROOT:-}
if [[ -n "$prefix" ]]; then
    [[ "$prefix" == /* && "$prefix" != / && -f "$prefix/.v2bx-test-root" ]] || exit 2
else
    ((EUID == 0)) || { echo 'Run as root.' >&2; exit 1; }
fi
for tool in unzip sha256sum systemctl install flock mktemp; do
    command -v "$tool" >/dev/null || { echo "Missing dependency: $tool" >&2; exit 1; }
done
binary="$prefix/usr/local/V2bX/V2bX"
state="$prefix/var/lib/v2bx-security-upgrade"
[[ -f "$binary" && ! -L "$binary" ]] || { echo 'Standard V2bX installation required.' >&2; exit 1; }
systemctl is-active --quiet V2bX.service || { echo 'Existing V2bX.service must be active.' >&2; exit 1; }
[[ $(systemctl show -p ExecStart --value V2bX.service) == *"$binary "* ]] || {
    echo 'Service ExecStart does not match the standard installation.' >&2; exit 1;
}
install -d -m 0700 "$state"
exec 9>"$state/upgrade.lock"
flock -n 9 || { echo 'Another security upgrade is running.' >&2; exit 1; }
temp=$(mktemp -d "$state/.staging.XXXXXX")
backup='' restore=0
cleanup() {
    local code=$?
    trap - EXIT
    if ((restore)); then
        echo 'Upgrade failed/interrupted; restoring previous executable.' >&2
        systemctl stop V2bX.service || true
        rollback=$(mktemp "${binary}.rollback.XXXXXX")
        if install -m 0755 "$backup/V2bX" "$rollback" && mv -f -- "$rollback" "$binary"; then
            systemctl reset-failed V2bX.service || true
            systemctl start V2bX.service || echo 'ERROR: restart after rollback failed; inspect journalctl -u V2bX.' >&2
        else
            rm -f -- "$rollback"
            echo "ERROR: rollback failed. Backup: $backup/V2bX" >&2
        fi
    fi
    rm -rf -- "$temp"
    exit "$code"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
actual=$(sha256sum "$archive"); actual=${actual%% *}
[[ ${actual,,} == "${checksum,,}" ]] || { echo 'Checksum mismatch; service unchanged.' >&2; exit 1; }
# Extract one known member; never unpack arbitrary paths from an archive.
unzip -p "$archive" V2bX > "$temp/V2bX"
chmod 0700 "$temp/V2bX"
"$temp/V2bX" version > "$temp/version.txt"
grep -qF 'V2bX v0.1.3-security.7 (' "$temp/version.txt" || {
    echo 'Unexpected executable/version; service unchanged.' >&2; exit 1;
}
backup="$state/backup-$(date -u +%Y%m%dT%H%M%SZ)-$$"
install -d -m 0700 "$backup"
cp -p -- "$binary" "$backup/V2bX"
echo 'Restarting V2bX; current proxy connections will be interrupted.'
restore=1
systemctl stop V2bX.service
# Stage beside the destination so replacement is atomic on its filesystem.
staged=$(mktemp "${binary}.security.XXXXXX")
if ! install -m 0755 "$temp/V2bX" "$staged"; then rm -f -- "$staged"; exit 1; fi
if ! mv -f -- "$staged" "$binary"; then rm -f -- "$staged"; exit 1; fi
systemctl reset-failed V2bX.service || true
systemctl start V2bX.service
for ((i=0;i<10;i++)); do
    sleep 1
    systemctl is-active --quiet V2bX.service || exit 1
done
restore=0
echo "Upgrade completed; executable backup: $backup/V2bX"
echo 'Config, certificates, geodata and access reporting settings were preserved.'
echo 'A 10-second service check is not a full panel/client connectivity test.'
echo 'Install and boot the vendor kernel fix for CVE-2026-98432; this binary is not a kernel patch.'
