"""Offline release installer checks: no network, service, or user config writes."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[2]


class Installer(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.tools = self.root / 'tools'
        self.tools.mkdir()
        self.downloads = self.root / 'downloads'
        self.downloads.mkdir()
        self.home = self.root / 'home with spaces'
        self.home.mkdir()
        self.log = self.root / 'requests.jsonl'
        self.env = {k: v for k, v in os.environ.items() if not k.startswith('MCP_RUNTIME_')}
        self.env.update(HOME=str(self.home), PATH=f'{self.tools}:{os.environ["PATH"]}',
                        DOWNLOADS=str(self.downloads), REQUEST_LOG=str(self.log),
                        FAKE_OS='Darwin', FAKE_ARCH='arm64', FAKE_MACOS='13.7')
        self.tool('uname', 'import os,sys\nprint(os.environ["FAKE_OS" if sys.argv[1] == "-s" else "FAKE_ARCH"])')
        self.tool('sw_vers', 'import os\nprint(os.environ["FAKE_MACOS"])')
        self.tool('curl', '''import json,os,pathlib,shutil,sys
args=sys.argv[1:]; url=args[args.index('-o')-1]; dest=args[args.index('-o')+1]
with open(os.environ['REQUEST_LOG'],'a') as f:f.write(json.dumps(url)+'\\n')
source=pathlib.Path(os.environ['DOWNLOADS'])/url.rsplit('/',1)[-1]
if source.name == os.environ.get('FAIL_DOWNLOAD') or not source.exists(): sys.exit(22)
shutil.copyfile(source,dest)
''')
        self.tool('ditto', '''import pathlib,shutil,sys,zipfile
args=sys.argv[1:]
if args[0]=='-x':
 with zipfile.ZipFile(args[2]) as z:z.extractall(args[3])
 for p in pathlib.Path(args[3]).rglob('*'):
  if p.is_file():p.chmod(0o755)
else:shutil.copytree(args[0],args[1])
''')
        for system in ('darwin', 'linux'):
            for arch in ('amd64', 'arm64'):
                (self.downloads / f'mcp-runtime-{system}-{arch}').write_bytes(b'CLI fixture\n')
        self.archive()
        self.checksums()

    def tool(self, name, source):
        target = self.tools / name
        target.write_text('#!/usr/bin/env python3\n' + source + '\n')
        target.chmod(0o755)

    def archive(self, helper=True):
        with zipfile.ZipFile(self.downloads / 'MCPRuntimeAdapter-unsigned-preview.zip', 'w') as archive:
            archive.writestr('MCPRuntimeAdapter.app/Contents/MacOS/MCPRuntimeAdapter', 'app fixture')
            if helper:
                archive.writestr('MCPRuntimeAdapter.app/Contents/Helpers/mcp-runtime', 'CLI fixture')

    def checksums(self):
        (self.downloads / 'SHA256SUMS').write_text(''.join(
            f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n'
            for p in sorted(self.downloads.iterdir()) if p.name != 'SHA256SUMS'))

    def run_installer(self, *args):
        return subprocess.run(['sh', str(ROOT / 'install.sh'), *args], env=self.env,
                              capture_output=True, text=True, timeout=20)

    def assert_ok(self, result):
        self.assertEqual(result.returncode, 0, result.stderr)

    def cli(self):
        return self.home / '.local/bin/mcp-runtime'

    def app(self):
        return self.home / 'Applications/MCPRuntimeAdapter.app'

    def requests(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def test_cli_only_default(self):
        self.assert_ok(self.run_installer())
        self.assertEqual(self.cli().read_bytes(), b'CLI fixture\n')
        self.assertFalse(self.app().exists())
        self.assertTrue(self.requests()[-1].endswith('/latest/download/mcp-runtime-darwin-arm64'))

    def test_desktop_with_cli_and_spaces(self):
        result = self.run_installer('--with-desktop')
        self.assert_ok(result)
        self.assertTrue(self.cli().exists())
        self.assertTrue((self.app() / 'Contents/Helpers/mcp-runtime').is_file())
        self.assertIn('Unsigned preview', result.stderr)
        self.assertIn('Update service from this app', result.stderr)
        self.assertEqual(list(self.app().parent.glob('.mcp-runtime-adapter.*')), [])

    def test_custom_directories_and_version(self):
        self.env.update(MCP_RUNTIME_VERSION='v0.12.0',
                        MCP_RUNTIME_INSTALL_DIR=str(self.root / 'custom bin'),
                        MCP_RUNTIME_APP_DIR=str(self.root / 'custom apps'))
        self.assert_ok(self.run_installer('--with-desktop'))
        self.assertTrue((self.root / 'custom apps/MCPRuntimeAdapter.app').exists())
        self.assertTrue((self.root / 'custom bin/mcp-runtime').exists())
        self.assertTrue(all('/download/v0.12.0/' in url for url in self.requests()))

    def test_existing_app_preserved_before_download(self):
        self.app().mkdir(parents=True)
        marker = self.app() / 'keep'
        marker.write_text('original')
        self.assertNotEqual(self.run_installer('--with-desktop').returncode, 0)
        self.assertEqual(marker.read_text(), 'original')
        self.assertEqual(self.requests(), [])

    def test_dangling_app_symlink_preserved(self):
        self.app().parent.mkdir(parents=True)
        self.app().symlink_to(self.root / 'missing')
        self.assertNotEqual(self.run_installer('--with-desktop').returncode, 0)
        self.assertTrue(self.app().is_symlink())
        self.assertEqual(self.requests(), [])

    def test_cli_checksum_failure_preserves_installed_cli(self):
        self.cli().parent.mkdir(parents=True)
        self.cli().write_bytes(b'existing CLI')
        (self.downloads / 'mcp-runtime-darwin-arm64').write_bytes(b'tampered')
        self.assertNotEqual(self.run_installer('--with-desktop').returncode, 0)
        self.assertEqual(self.cli().read_bytes(), b'existing CLI')
        self.assertFalse(self.app().exists())

    def test_desktop_checksum_failure_installs_nothing(self):
        with (self.downloads / 'MCPRuntimeAdapter-unsigned-preview.zip').open('ab') as archive:
            archive.write(b'tampered')
        result = self.run_installer('--with-desktop')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('Checksum mismatch', result.stderr)
        self.assertFalse(self.cli().exists())
        self.assertFalse(self.app().exists())

    def test_missing_checksum_fails_closed(self):
        (self.downloads / 'SHA256SUMS').write_text('')
        self.assertNotEqual(self.run_installer().returncode, 0)
        self.assertFalse(self.cli().exists())

    def test_missing_helper_installs_nothing(self):
        self.archive(helper=False)
        self.checksums()
        self.assertNotEqual(self.run_installer('--with-desktop').returncode, 0)
        self.assertFalse(self.cli().exists())
        self.assertFalse(self.app().exists())

    def test_desktop_download_failure(self):
        self.env['FAIL_DOWNLOAD'] = 'MCPRuntimeAdapter-unsigned-preview.zip'
        self.assertNotEqual(self.run_installer('--with-desktop').returncode, 0)
        self.assertFalse(self.cli().exists())
        self.assertFalse(self.app().exists())

    def test_linux_cli_and_desktop_rejection(self):
        self.env.update(FAKE_OS='Linux', FAKE_ARCH='x86_64')
        result = self.run_installer('--with-desktop')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('requires macOS', result.stderr)
        self.assertEqual(self.requests(), [])
        self.assert_ok(self.run_installer())
        self.assertTrue(self.requests()[-1].endswith('/mcp-runtime-linux-amd64'))

    def test_old_macos_rejected_before_download(self):
        self.env['FAKE_MACOS'] = '12.7.6'
        self.assertNotEqual(self.run_installer('--with-desktop').returncode, 0)
        self.assertEqual(self.requests(), [])

    def test_help_and_unknown_options(self):
        result = self.run_installer('--help')
        self.assert_ok(result)
        self.assertIn('--with-desktop', result.stdout)
        self.assertEqual(self.requests(), [])
        self.assertNotEqual(self.run_installer('--unknown').returncode, 0)


if __name__ == '__main__':
    unittest.main()
