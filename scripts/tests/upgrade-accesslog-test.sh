#!/usr/bin/env bash
set -Eeuo pipefail
[[ $EUID == 0 ]] || { echo 'Run this isolated fixture test as root.' >&2; exit 1; }
repo=$(cd "$(dirname "$0")/../.." && pwd)
fixture=$(mktemp -d)
trap 'rm -rf -- "$fixture"' EXIT
export V2BX_UPGRADE_TEST=1 V2BX_UPGRADE_TEST_ROOT="$fixture/root"
export FIXTURE_ACTIVE="$fixture/active" FIXTURE_CALLS="$fixture/calls"
export FIXTURE_BINARY="$fixture/root/usr/local/V2bX/V2bX"
mkdir -p "$fixture/bin" "$fixture/archive" "$(dirname "$FIXTURE_BINARY")" "$fixture/root/etc/V2bX"
export PATH="$fixture/bin:$PATH"
cat > "$fixture/bin/systemctl" <<'SH'
#!/usr/bin/env bash
set -eu
echo "$*" >> "$FIXTURE_CALLS"
case "$1" in
 is-active) test -f "$FIXTURE_ACTIVE" ;;
 stop) rm -f "$FIXTURE_ACTIVE" ;;
 start)
   if [[ ${FAIL_NEW:-0} == 1 ]] && grep -q NEW_EXECUTABLE "$FIXTURE_BINARY"; then exit 1; fi
   touch "$FIXTURE_ACTIVE" ;;
 *) exit 2 ;;
esac
SH
printf '#!/usr/bin/env bash\nexit 0\n' > "$fixture/bin/sleep"
cat > "$fixture/archive/V2bX" <<'SH'
#!/usr/bin/env bash
# NEW_EXECUTABLE
set -eu
if [[ "$*" == *--help* ]]; then echo --password-stdin; exit 0; fi
config=''
while (($#)); do
 if [[ "$1" == --config ]]; then config=$2; shift 2; else shift; fi
done
read -r secret
[[ "$secret" == fixture-password ]] || exit 1
printf '{"Enabled":true}\n' > "$(dirname "$config")/accesslog.json"
SH
chmod +x "$fixture/bin/"* "$fixture/archive/V2bX"
(cd "$fixture/archive" && zip -q "$fixture/release.zip" V2bX)
checksum=$(sha256sum "$fixture/release.zip"); checksum=${checksum%% *}
printf 'fixture-password\n' > "$fixture/password"
printf '{"Nodes":[{"keep":"byte-for-byte"}]}\n' > "$fixture/original-config"
printf '#!/usr/bin/env bash\n# ORIGINAL\nexit 0\n' > "$fixture/original-binary"
printf '{"Enabled":false,"Password":"old-fixture"}\n' > "$fixture/original-sidecar"
reset_fixture() {
 cp "$fixture/original-binary" "$FIXTURE_BINARY"
 cp "$fixture/original-config" "$fixture/root/etc/V2bX/config.json"
 cp "$fixture/original-sidecar" "$fixture/root/etc/V2bX/accesslog.json"
 chmod 0755 "$FIXTURE_BINARY"
 : > "$FIXTURE_CALLS"
 touch "$FIXTURE_ACTIVE"
}
run_upgrade() {
 bash "$repo/scripts/upgrade-accesslog.sh" --archive "$fixture/release.zip" --sha256 "$1" \
   --endpoint https://example.com/ingest --username writer --password-file "$fixture/password" > "$fixture/output" 2>&1
}
assert_preserved() { cmp "$fixture/original-config" "$fixture/root/etc/V2bX/config.json"; }
reset_fixture
run_upgrade "$checksum"
cmp "$fixture/archive/V2bX" "$FIXTURE_BINARY"
test -f "$FIXTURE_ACTIVE"
assert_preserved
test "$(stat -c %a "$fixture/root/etc/V2bX/accesslog.json")" == 600
! grep -q fixture-password "$fixture/output"
echo 'PASS: upgrade, config preservation and secret handling'

reset_fixture
if run_upgrade "$(printf '%064d' 0)"; then echo 'bad checksum unexpectedly succeeded'; exit 1; fi
cmp "$fixture/original-binary" "$FIXTURE_BINARY"
cmp "$fixture/original-sidecar" "$fixture/root/etc/V2bX/accesslog.json"
test ! -s "$FIXTURE_CALLS"
assert_preserved
echo 'PASS: bad checksum leaves service and files untouched'

reset_fixture
export FAIL_NEW=1
if run_upgrade "$checksum"; then echo 'broken startup unexpectedly succeeded'; exit 1; fi
unset FAIL_NEW
cmp "$fixture/original-binary" "$FIXTURE_BINARY"
cmp "$fixture/original-sidecar" "$fixture/root/etc/V2bX/accesslog.json"
test -f "$FIXTURE_ACTIVE"
assert_preserved
echo 'PASS: failed startup rolls back executable, sidecar and service'

reset_fixture
rm -f "$FIXTURE_ACTIVE"
run_upgrade "$checksum"
test ! -f "$FIXTURE_ACTIVE"
assert_preserved
echo 'PASS: stopped service remains stopped'
