"""Exercise the download boundary without network access or service changes."""
import hashlib
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


BASH = shutil.which("bash") or r"C:\Program Files\Git\usr\bin\bash.exe"
SCRIPT = Path(__file__).resolve().parents[1] / "update-security.sh"


def posix(path):
    if os.name == "nt":
        return subprocess.check_output(
            [BASH, "-lc", 'cygpath -u "$1"', "test", str(path)], encoding="utf-8"
        ).strip()
    return str(path)


class DownloadTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.assets = self.root / "assets"
        self.assets.mkdir()
        self.mockbin = self.root / "mockbin"
        self.mockbin.mkdir()
        self.write_asset("V2bX-linux-64.zip", "archive contents")
        self.write_asset("V2bX-linux-arm64-v8a.zip", "arm archive contents")
        self.write_asset("upgrade-security.sh", '''#!/bin/bash
printf '%s\\n' "$@" > "$V2BX_SECURITY_TEST_ROOT/helper-args"
''')
        self.mock("uname", '''
if [[ $1 == -s ]]; then echo Linux; else echo "${TEST_ARCH:-x86_64}"; fi
''')
        self.mock("curl", '''
printf '%s\\n' "$*" >> "$V2BX_SECURITY_TEST_ROOT/downloads"
out='' url=''
while (($#)); do
  case "$1" in
    --output) out=$2; shift 2 ;;
    --retry|--proto|--proto-redir) shift 2 ;;
    --fail|--location) shift ;;
    https://*) url=$1; shift ;;
    *) exit 2 ;;
  esac
done
[[ "$url" == https://github.com/rmomo5285-droid/V2bX/releases/download/v0.1.3-security.4/* ]] || exit 3
file=${url##*/}
[[ "$file" != "${TEST_FAIL_DOWNLOAD:-}" ]] || exit 22
cp "$V2BX_SECURITY_TEST_ROOT/assets/$file" "$out"
''')
        self.env = dict(os.environ, V2BX_SECURITY_TEST_ROOT=posix(self.root),
                        V2BX_TEST_BIN=posix(self.mockbin), V2BX_TEST_SCRIPT=posix(SCRIPT))

    def mock(self, name, body):
        p = self.mockbin / name
        p.write_text("#!/bin/bash\n" + body, encoding="utf-8", newline="\n")
        p.chmod(0o755)

    def write_asset(self, name, contents):
        p = self.assets / name
        p.write_text(contents, encoding="utf-8", newline="\n")
        (self.assets / (name + ".sha256")).write_text(
            hashlib.sha256(p.read_bytes()).hexdigest() + "  " + name + "\n",
            encoding="utf-8", newline="\n")

    def run_update(self, **env):
        result = subprocess.run([BASH, "-c", '''
export PATH="$V2BX_TEST_BIN:/usr/bin:/bin:$PATH"
bash "$V2BX_TEST_SCRIPT"
'''], env=dict(self.env, **env), encoding="utf-8", errors="replace",
            stdout=subprocess.PIPE, stderr=subprocess.STDOUT)
        return result

    def assert_rejected(self, result):
        self.assertNotEqual(result.returncode, 0, result.stdout)
        self.assertFalse((self.root / "helper-args").exists(), result.stdout)

    def test_verified_amd64_download_runs_helper(self):
        r = self.run_update()
        self.assertEqual(r.returncode, 0, r.stdout)
        args = (self.root / "helper-args").read_text().splitlines()
        self.assertEqual(args[0], "--archive")
        self.assertTrue(args[1].endswith("/V2bX-linux-64.zip"))
        self.assertEqual(args[2], "--sha256")
        self.assertEqual(args[3], hashlib.sha256((self.assets / "V2bX-linux-64.zip").read_bytes()).hexdigest())
        self.assertFalse(Path(args[1]).exists(), "staging directory must be removed")

    def test_arm64_uses_matching_archive(self):
        r = self.run_update(TEST_ARCH="aarch64")
        self.assertEqual(r.returncode, 0, r.stdout)
        self.assertIn("V2bX-linux-arm64-v8a.zip", (self.root / "helper-args").read_text())

    def test_corrupt_archive_cannot_run_helper(self):
        (self.assets / "V2bX-linux-64.zip").write_text("modified")
        self.assert_rejected(self.run_update())

    def test_corrupt_helper_cannot_execute(self):
        (self.assets / "upgrade-security.sh").write_text("exit 0\n")
        self.assert_rejected(self.run_update())

    def test_checksum_cannot_reference_another_path(self):
        (self.assets / "V2bX-linux-64.zip.sha256").write_text("0" * 64 + "  ../other\n")
        self.assert_rejected(self.run_update())

    def test_multiline_checksum_rejected(self):
        p = self.assets / "V2bX-linux-64.zip.sha256"
        p.write_text(p.read_text() * 2)
        self.assert_rejected(self.run_update())

    def test_failed_download_cannot_run_helper(self):
        self.assert_rejected(self.run_update(TEST_FAIL_DOWNLOAD="upgrade-security.sh"))

    def test_unsupported_architecture_does_not_download(self):
        self.assert_rejected(self.run_update(TEST_ARCH="unknown"))
        self.assertFalse((self.root / "downloads").exists())

    def test_helper_failure_is_returned_to_caller(self):
        self.write_asset("upgrade-security.sh", "#!/bin/bash\nexit 7\n")
        self.assertEqual(self.run_update().returncode, 7)


if __name__ == "__main__":
    unittest.main()
