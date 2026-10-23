"""Offline checks of the hosted workflow's actual shell before any update."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[2]
WORKFLOW = ROOT / '.github/workflows/hosted-platform-release-deploy.yaml'


class HostedReleaseWorkflow(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.bin = self.root / 'bin'
        self.bin.mkdir()
        self.log = self.root / 'commands.jsonl'
        self.tool('kubectl', "import os\nprint(os.environ['FAKE_CLUSTER_UID'])\n")
        self.tool('sshpass', "import os,json,sys\nwith open(os.environ['COMMAND_LOG'],'a') as f: f.write(json.dumps(sys.argv[1:])+'\\n')\n")
        self.tool('bash', """import os,json,pathlib,sys
assert sys.argv[1:] == ['hack/deploy/reference/backup.sh', '--setup']
if os.environ.get('FAKE_BACKUP_FAIL'): sys.exit(1)
profile=pathlib.Path(os.environ['MCP_DEPLOY_ENV'])
assert profile.stat().st_mode & 0o077 == 0
assert 'MCP_EXPECTED_CLUSTER_UID' in profile.read_text()
root=pathlib.Path(os.environ['RUNNER_TEMP'])/'pre-deploy-snapshot'
snapshot=root/'snapshot'
snapshot.mkdir(parents=True)
(snapshot/'BACKUP_COMPLETE').write_text('complete')
(snapshot/'SHA256SUMS').write_text('')
(root/'latest').symlink_to(snapshot, target_is_directory=True)
with open(os.environ['COMMAND_LOG'],'a') as f: f.write(json.dumps(['backup', '--setup'])+'\\n')
""")
        # An empty checksum fixture is enough here: the canonical snapshot
        # contents/checksums have their own behavioral tests.
        self.tool('sha256sum', "import sys\nassert sys.argv[1:] == ['-c', 'SHA256SUMS']\n")

    def tool(self, name, body):
        path = self.bin / name
        path.write_text('#!/usr/bin/env python3\n' + body)
        path.chmod(0o755)

    def run_snapshot(self, uid='cluster-a', verify=False, fail_backup=False):
        source = WORKFLOW.read_text()
        start = source.index('          actual_cluster_uid=')
        end = source.index('\n          sshpass -e ssh', source.index('Verified protected pre-update snapshot', start))
        fragment = '\n'.join(line[10:] for line in source[start:end].splitlines())
        script = 'set -euo pipefail\numask 077\nkubeconfig=/private/config\nssh_opts=(-o StrictHostKeyChecking=yes)\n' + fragment
        env = {**os.environ, 'PATH': f'{self.bin}:{os.environ["PATH"]}',
               'RUNNER_TEMP': str(self.root), 'COMMAND_LOG': str(self.log),
               'FAKE_CLUSTER_UID': uid, 'MCP_EXPECTED_CLUSTER_UID': 'cluster-a',
               'VERIFY_ONLY': 'true' if verify else 'false', 'RELEASE_TAG': 'v0.12.1',
               'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '1'}
        if fail_backup:
            env['FAKE_BACKUP_FAIL'] = '1'
        result = subprocess.run(['/bin/bash', '-c', script], env=env, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        calls = [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []
        return result, calls

    def test_wrong_cluster_stops_before_backup_or_ssh(self):
        result, calls = self.run_snapshot(uid='wrong-cluster')
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_verify_only_does_not_create_backup_or_write_remote_state(self):
        result, calls = self.run_snapshot(verify=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls, [])
        self.assertFalse((self.root / 'pre-deploy-backup.env').exists())

    def test_snapshot_precedes_remote_copy_and_verification(self):
        result, calls = self.run_snapshot()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(calls[0], ['backup', '--setup'])
        self.assertEqual(len(calls), 4)
        self.assertIn('scp', calls[2])
        self.assertIn('sha256sum -c SHA256SUMS', calls[3][-1])
        self.assertIn('/root/.mcpruntime/backups/releases/v0.12.1-123-1', result.stdout)

    def test_failed_backup_stops_before_remote_copy(self):
        result, calls = self.run_snapshot(fail_backup=True)
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(calls, [])

    def test_every_update_is_bound_to_expected_cluster(self):
        source = WORKFLOW.read_text()
        commands = re.findall(r'\./bin/mcp-runtime update[^\n]*', source.replace('\\\n', ' '))
        self.assertEqual(len(commands), 4)
        for command in commands:
            self.assertIn('--expect-cluster-id "$MCP_EXPECTED_CLUSTER_UID"', command)
        self.assertLess(source.index('actual_cluster_uid='), source.index('get secret mcp-ui-credentials'))
        self.assertLess(source.index('Verified protected pre-update snapshot'), source.index('./bin/mcp-runtime update'))
        self.assertLess(source.index('./bin/mcp-runtime cluster doctor'), source.index('./bin/mcp-runtime update'))

    def test_deployment_shell_syntax_and_lint(self):
        source = WORKFLOW.read_text().split('      - name: Deploy and verify Runtime', 1)[1]
        source = source.split('        run: |\n', 1)[1].split('\n  publish-live-version:', 1)[0]
        script = self.root / 'deploy-step.sh'
        script.write_text('\n'.join(line[10:] for line in source.splitlines()) + '\n')
        subprocess.run(['/bin/bash', '-n', str(script)], check=True)
        subprocess.run(['shellcheck', '-s', 'bash', str(script)], check=True)


if __name__ == '__main__':
    unittest.main()
