"""Offline upgrade regression tests; all service and installation paths are mocked."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
import zipfile


BASH = shutil.which("bash") or r"C:\Program Files\Git\usr\bin\bash.exe"
SCRIPT = Path(__file__).resolve().parents[1] / "upgrade-security.sh"


def posix(path):
    if os.name == "nt":
        return subprocess.check_output([BASH, "-lc", 'cygpath -u "$1"', "test", str(path)], encoding="utf-8").strip()
    return str(path)


class UpgradeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.binary = self.root / "usr/local/V2bX/V2bX"
        self.binary.parent.mkdir(parents=True)
        self.binary.write_text("#!/bin/bash\necho OLD\n", newline="\n")
        self.binary.chmod(0o755)
        (self.root / ".v2bx-test-root").touch()
        (self.root / "status").write_text("active")
        self.config = self.root / "etc/V2bX/config.json"
        self.config.parent.mkdir(parents=True)
        self.config.write_text('{"keep":"existing config"}')
        mockbin = self.root / "mockbin"
        mockbin.mkdir()
        mocks = {
            "uname": "echo Linux",
            "sleep": ":",
            "flock": ":",
            "systemctl": '''
echo "$*" >> "$V2BX_SECURITY_TEST_ROOT/actions"
case "$1" in
  is-active)
    if grep -q BAD_HEALTH "$V2BX_SECURITY_TEST_ROOT/usr/local/V2bX/V2bX"; then exit 1; fi
    [[ $(cat "$V2BX_SECURITY_TEST_ROOT/status") == active ]] ;;
  show) echo "$V2BX_SECURITY_TEST_ROOT/usr/local/V2bX/V2bX server" ;;
  stop) echo stopped > "$V2BX_SECURITY_TEST_ROOT/status" ;;
  reset-failed) : ;;
  start)
    if grep -q BAD_START "$V2BX_SECURITY_TEST_ROOT/usr/local/V2bX/V2bX"; then exit 1; fi
    echo active > "$V2BX_SECURITY_TEST_ROOT/status" ;;
  *) exit 2 ;;
esac
''',
        }
        if os.name == "nt":
            # Git Bash cannot apply Unix ownership/modes to this Windows test disk.
            mocks["install"] = '''
directory=0
if [[ $1 == -d ]]; then directory=1; shift; fi
if [[ $1 == -m ]]; then shift 2; fi
if ((directory)); then mkdir -p -- "$@"; else cp -- "$1" "$2"; fi
'''
            mocks["chmod"] = ":"
        for name, text in mocks.items():
            p = mockbin / name
            p.write_text("#!/bin/bash\n" + text + "\n", newline="\n")
            p.chmod(0o755)
        self.env = dict(os.environ, V2BX_SECURITY_TEST_ROOT=posix(self.root),
                        V2BX_TEST_BIN=posix(mockbin), V2BX_TEST_SCRIPT=posix(SCRIPT))

    def run_upgrade(self, *, bad_hash=False, bad_start=False, wrong_version=False, bad_health=False):
        archive = self.root / "release.zip"
        binary = "#!/bin/bash\n"
        if bad_start:
            binary += "# BAD_START\n"
        if bad_health:
            binary += "# BAD_HEALTH\n"
        binary += "echo 'V2bX " + ("WRONG" if wrong_version else "v0.1.3-security.4") + " (test)'\n"
        with zipfile.ZipFile(archive, "w") as z:
            z.writestr("V2bX", binary)
            z.writestr("../../escape", "must not extract")
        checksum = "0" * 64 if bad_hash else hashlib.sha256(archive.read_bytes()).hexdigest()
        self.env.update(V2BX_TEST_ARCHIVE=posix(archive), V2BX_TEST_SHA=checksum)
        result = subprocess.run([BASH, "-c", '''
export PATH="$V2BX_TEST_BIN:/usr/bin:/bin:$PATH"
bash "$V2BX_TEST_SCRIPT" --archive "$V2BX_TEST_ARCHIVE" --sha256 "$V2BX_TEST_SHA"
'''], env=self.env, encoding="utf-8", errors="replace", stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        self.assertEqual(self.config.read_text(), '{"keep":"existing config"}')
        self.assertFalse((self.root.parent / "escape").exists())
        return result

    def test_success_and_backup(self):
        r = self.run_upgrade()
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertIn("v0.1.3-security.4", self.binary.read_text())
        backups = list(self.root.glob("var/lib/v2bx-security-upgrade/backup-*/V2bX"))
        self.assertEqual(len(backups), 1)
        self.assertIn("OLD", backups[0].read_text())

    def test_checksum_failure_does_not_stop_service(self):
        r = self.run_upgrade(bad_hash=True)
        self.assertNotEqual(r.returncode, 0, r.stdout)
        self.assertIn("OLD", self.binary.read_text())
        self.assertNotIn("stop", (self.root / "actions").read_text())

    def test_wrong_version_does_not_stop_service(self):
        r = self.run_upgrade(wrong_version=True)
        self.assertNotEqual(r.returncode, 0, r.stdout)
        self.assertIn("OLD", self.binary.read_text())
        self.assertNotIn("stop", (self.root / "actions").read_text())

    def test_restart_failure_restores_old_binary_and_service(self):
        r = self.run_upgrade(bad_start=True)
        self.assertNotEqual(r.returncode, 0, r.stdout)
        self.assertIn("OLD", self.binary.read_text())
        self.assertEqual((self.root / "status").read_text().strip(), "active")
        self.assertIn("restoring previous executable", r.stdout)

    def test_unhealthy_service_restores_old_binary_and_service(self):
        r = self.run_upgrade(bad_health=True)
        self.assertNotEqual(r.returncode, 0, r.stdout)
        self.assertIn("OLD", self.binary.read_text())
        self.assertEqual((self.root / "status").read_text().strip(), "active")


if __name__ == "__main__":
    unittest.main()
