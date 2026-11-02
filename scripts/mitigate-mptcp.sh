#!/usr/bin/env bash
# CVE-2026-98432 temporary mitigation. This is NOT a kernel patch.
set -euo pipefail

mode=${1:-check}
restart=${2:-}
if [[ "$mode" != check && "$mode" != apply ]] ||
   [[ -n "$restart" && "$restart" != --restart-v2bx ]] ||
   [[ "$mode" == check && -n "$restart" ]] || (( $# > 2 )); then
    echo 'Usage: bash mitigate-mptcp.sh [check | apply [--restart-v2bx]]' >&2
    exit 2
fi
[[ $(uname -s) == Linux ]] || { echo 'Linux only' >&2; exit 2; }

echo 'CVE-2026-98432: mitigation status, NOT a vulnerability verdict'
uname -r
if ! current=$(sysctl -n net.mptcp.enabled 2>/dev/null); then
    echo 'MPTCP sysctl unavailable. Check host kernel/configuration; no change made.'
    exit 2
fi
printf 'net.mptcp.enabled=%s\n' "$current"
echo 'Even when this value is 0, the underlying TCP kernel bug may remain.'
if [[ "$mode" == check ]]; then
    echo 'Apply a vendor kernel fix and boot it. Existing MPTCP sockets must be closed.'
    exit 0
fi
(( EUID == 0 )) || { echo 'apply requires root' >&2; exit 1; }

target=/etc/sysctl.d/99-v2bx-disable-mptcp.conf
content='# CVE-2026-98432 mitigation only; install a fixed host kernel.
net.mptcp.enabled = 0'
if [[ -L "$target" || ( -e "$target" && ! -f "$target" ) ]]; then
    echo "Refusing unexpected file type: $target" >&2
    exit 1
fi
if [[ -f "$target" && $(cat "$target") != "$content" ]]; then
    echo "Refusing to overwrite different existing content: $target" >&2
    exit 1
fi
if [[ "$restart" == --restart-v2bx ]]; then
    command -v systemctl >/dev/null || { echo 'systemd required for --restart-v2bx' >&2; exit 1; }
    systemctl is-active --quiet V2bX.service || { echo 'V2bX.service must be active before restart' >&2; exit 1; }
fi
mkdir -p /etc/sysctl.d
temporary=$(mktemp /etc/sysctl.d/.v2bx-mptcp.XXXXXX)
trap 'rm -f -- "$temporary"' EXIT
printf '%s\n' "$content" > "$temporary"
chmod 0644 "$temporary"
sysctl -w net.mptcp.enabled=0
mv -f -- "$temporary" "$target"
[[ $(sysctl -n net.mptcp.enabled) == 0 ]] || { echo 'MPTCP disable verification failed' >&2; exit 1; }
echo "Persisted to $target. This changes MPTCP for this network namespace."
echo 'Check other sysctl files for overrides; containers need host/admin action.'
if [[ "$restart" == --restart-v2bx ]]; then
    echo 'Restarting V2bX: current proxy connections will be interrupted.'
    systemctl restart V2bX.service
    systemctl is-active --quiet V2bX.service
else
    echo 'Restart/recreate ALL services with existing MPTCP listeners (including containers).'
    echo 'For V2bX on systemd: systemctl restart V2bX.service'
fi
echo 'Required next step: install the vendor kernel fix and reboot into that kernel.'
echo 'Removing the sysctl file does not restore the runtime value; review before re-enabling.'
