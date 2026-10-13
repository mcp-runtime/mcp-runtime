"""Behavioral checks for automation boundaries; all commands use isolated fake tools."""
import contextlib
import hashlib
import importlib.util
import json
import os
import socket
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]


def module(path, name):
    spec = importlib.util.spec_from_file_location(name, ROOT / path)
    result = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(result)
    return result


snapshot = module('hack/deploy/reference/lib/verify-snapshot.py', 'snapshot')
authz = module('hack/lib/authz-matrix.py', 'authz')
calls = module('hack/deploy/acceptance/check-calls.py', 'calls')


class Commands(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)
        self.bin = self.path / 'tools'
        self.bin.mkdir()
        self.log = self.path / 'commands.jsonl'
        self.profile = self.path / 'deployment.env'
        self.profile.write_text(f'export MCP_SETUP_KUBECONFIG={self.path}/explicit-kubeconfig\n'
                                'export MCP_KUBE_CONTEXT=prod-mcp-runtime\n'
                                'export MCP_EXPECTED_CLUSTER_UID=cluster-a\n'
                                'export MCP_DEPLOY_REF=v1.2.3\n')
        self.env = {**os.environ, 'PATH': f'{self.bin}:{os.environ["PATH"]}',
                    'MCP_DEPLOY_ENV': str(self.profile), 'MCPRUNTIME_ORG_ROOT': str(self.path),
                    'COMMAND_LOG': str(self.log), 'FAKE_CLUSTER_UID': 'cluster-a',
                    'KUBECONFIG': str(self.path / 'explicit-kubeconfig')}
        # No test inherits production profile/auth configuration.
        for key in ('MCP_SETUP_KUBECONFIG', 'MCP_SETUP_WITH_MCP_AUTH_SERVER', 'MCP_SETUP_MCP_AUTH_SERVER_IMAGE'):
            self.env.pop(key, None)
        self.tool('kubectl', '''import os,json,sys
args=sys.argv[1:]
with open(os.environ['COMMAND_LOG'],'a') as f:f.write(json.dumps(args)+'\\n')
if 'current-context' in args: print(os.environ.get('FAKE_CONTEXT','kind-test'))
elif 'nodes' in args: print(json.dumps({'items':[{'spec':{'providerID':os.environ.get('FAKE_PROVIDER','kind://docker/node')}}]}))
elif 'kube-system' in args: print(os.environ['FAKE_CLUSTER_UID'])
elif 'mcp-auth-server' in args:
 if os.environ.get('FAKE_AUTH_READ_ERROR'): sys.exit(2)
 if os.environ.get('FAKE_AUTH_IMAGE'): print(json.dumps({'spec':{'template':{'spec':{'containers':[{'image':os.environ['FAKE_AUTH_IMAGE']}]}}}}))
elif 'secret' in args: print(json.dumps({'type':'kubernetes.io/tls','data':{'tls.crt':'cert','tls.key':'key'}}))
elif 'ns' in args: print(os.environ.get('FAKE_NAMESPACE_INVENTORY','kube-system Active\\nmcp-platform Active\\nunrelated Active'))
''')
        self.tool('git', '''import os,sys
if 'status' in sys.argv: print(os.environ.get('FAKE_DIRTY',''),end='')
else: print('b'*40)
''')
        self.tool('make', '''import os,pathlib
root=pathlib.Path(os.environ['MCPRUNTIME_ORG_ROOT']);(root/'bin').mkdir(exist_ok=True)
cli=root/'bin/mcp-runtime'
cli.write_text('#!/usr/bin/env python3\\nimport os,json,sys\\nwith open(os.environ["COMMAND_LOG"],"a") as f:f.write(json.dumps(sys.argv[1:])+"\\\\n")\\n')
cli.chmod(0o755)
''')

    def tool(self, name, code):
        p = self.bin / name
        p.write_text('#!/usr/bin/env python3\n' + code)
        p.chmod(0o755)

    def run_script(self, path, *args):
        return subprocess.run(['bash', str(ROOT / path), *args], env=self.env,
                              text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)

    def commands(self):
        return [json.loads(line) for line in self.log.read_text().splitlines()] if self.log.exists() else []

    def test_missing_profile_does_not_use_example(self):
        self.env['MCP_DEPLOY_ENV'] = str(self.path / 'missing.env')
        self.assertNotEqual(self.run_script('hack/deploy/reference/reset.sh', '--dry-run').returncode, 0)
        self.assertEqual(self.commands(), [])

    def test_reset_yes_alone_never_deletes(self):
        result = self.run_script('hack/deploy/reference/reset.sh', '--yes')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any('delete' in c for c in self.commands()))

    def test_reset_wrong_confirmation_never_deletes(self):
        result = self.run_script('hack/deploy/reference/reset.sh', '--yes', '--confirm-cluster', 'other',
                                 '--recovery-snapshot', str(self.path))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any('delete' in c for c in self.commands()))

    def test_reset_incomplete_snapshot_never_deletes(self):
        result = self.run_script('hack/deploy/reference/reset.sh', '--yes', '--confirm-cluster', 'cluster-a',
                                 '--recovery-snapshot', str(self.path))
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(any('delete' in c for c in self.commands()))

    def test_reset_preserves_named_and_profile_ingress_namespaces(self):
        self.env['PLATFORM_TRAEFIK_NAMESPACE'] = 'custom-ingress'
        self.env['FAKE_NAMESPACE_INVENTORY'] = 'traefik Active\ncustom-ingress Active\nmcp-platform Active'
        result = self.run_script('hack/deploy/reference/reset.sh', '--dry-run')
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('  mcp-platform', result.stdout)
        self.assertNotIn('  traefik', result.stdout)
        self.assertNotIn('  custom-ingress', result.stdout)
        # Protection must take precedence even when the ingress namespace is labelled managed.
        for namespace in ('traefik', 'custom-ingress'):
            result = subprocess.run(['bash', '-c',
                f'source "{ROOT}/hack/deploy/reference/lib/reset.sh"; '
                'mcpruntime_org_namespace_has_mcp_runtime_label() { return 0; }; '
                f'if mcpruntime_org_should_delete_namespace {namespace}; then exit 1; fi'],
                env=self.env, capture_output=True)
            self.assertEqual(result.returncode, 0, result.stderr)


    def test_reset_preview_has_no_mutations(self):
        self.assertEqual(self.run_script('hack/deploy/reference/reset.sh', '--dry-run').returncode, 0)
        self.assertFalse(any(set(c) & {'delete', 'apply', 'patch', 'create'} for c in self.commands()))

    def test_live_guard_rejects_renamed_production(self):
        self.env['FAKE_CONTEXT'] = 'kind-fake-prod'
        self.env['FAKE_PROVIDER'] = 'k3s://prod'
        result = subprocess.run(['bash', '-c', f'set -euo pipefail; source "{ROOT}/hack/lib/test-cluster.sh"; require_test_cluster'],
                                env=self.env, capture_output=True)
        self.assertNotEqual(result.returncode, 0)

    def test_go_test_replaces_inherited_kubeconfig(self):
        self.tool('go', '''import os,pathlib,sys
if 'env' in sys.argv:
 print(pathlib.Path(os.environ['MCPRUNTIME_ORG_ROOT'])/sys.argv[-1]);sys.exit(0)
p=pathlib.Path(os.environ['KUBECONFIG']);assert p.exists() and p.read_bytes()==b''
assert 'explicit-kubeconfig' not in str(p)
''')
        result = self.run_script('hack/ci/go-test.sh', str(self.path), './...')
        if Path('/etc/rancher/k3s/k3s.yaml').exists():
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('system k3s kubeconfig', result.stderr)
        else:
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_multitenancy_refuses_production_before_commands(self):
        self.env.update(PLATFORM_URL='https://platform.mcpruntime.org', MCP_URL='https://mcp.mcpruntime.org',
                        REGISTRY_HOST='registry.mcpruntime.org', MCPRUNTIME_ORG_ROOT=str(ROOT))
        result = self.run_script('hack/qa/multitenancy-test.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('restricted to loopback/disposable', result.stderr)
        self.assertEqual(self.commands(), [])

    def test_demo_random_credentials_are_private_and_reusable(self):
        self.env.update(PLATFORM_URL='http://localhost:18080', MCP_URL='http://localhost:18080',
                        REGISTRY_HOST='localhost:5000', MCPRUNTIME_ORG_ROOT=str(ROOT), ADMIN_PASSWORD='disposable',
                        RUN_ID='test-run', MCP_RUNTIME_CONFIG_DIR=str(self.path / 'profile'),
                        WORK_DIR=str(self.path / 'demo'), BIN=str(self.path / 'missing-cli'))
        result = self.run_script('hack/qa/multitenancy-test.sh')
        self.assertIn('missing binary', result.stderr)
        file = self.path / 'profile/demo-credentials-test-run.json'
        original = file.read_bytes()
        passwords = json.loads(original)
        self.assertTrue(all(len(value) >= 24 for value in passwords.values()))
        self.assertEqual(file.stat().st_mode & 0o777, 0o600)
        self.run_script('hack/qa/multitenancy-test.sh')
        self.assertEqual(file.read_bytes(), original)


    def test_openapi_identical_base_fails(self):
        self.assertNotEqual(self.run_script('hack/ci/oasdiff-openapi.sh', 'HEAD').returncode, 0)

    def test_reset_valid_backup_deletes_only_selected_namespaces(self):
        bundle = self.path / 'bundle'
        bundle.mkdir()
        files = {'cluster-identity.json': '{"clusterUID":"cluster-a"}', 'k3s-state.db': 'db',
                 'k3s-host-and-pv-data.tar.gz': 'volumes'}
        for name, data in files.items(): (bundle / name).write_text(data)
        (bundle / 'BACKUP_COMPLETE').touch()
        (bundle / 'FULL_BACKUP_COMPLETE').touch()
        (bundle / 'SHA256SUMS').write_text(''.join(f'{hashlib.sha256(data.encode()).hexdigest()}  {name}\n' for name, data in files.items()))
        self.env['MCP_TLS_BACKUP_DIR'] = str(self.path / 'protected-backups')
        result = self.run_script('hack/deploy/reference/reset.sh', '--yes', '--confirm-cluster', 'cluster-a',
                                 '--recovery-snapshot', str(bundle))
        self.assertEqual(result.returncode, 0, result.stderr)
        commands = [c for c in self.commands() if 'delete' in c]
        self.assertTrue(commands)
        self.assertFalse(any('-A' in c or 'unrelated' in c or 'kube-system' in c for c in commands))
        self.assertTrue(any('mcp-platform' in c for c in commands))

    def test_live_test_url_rejects_production_even_with_kind_cluster(self):
        result = subprocess.run(['bash', '-c', f'source "{ROOT}/hack/lib/test-cluster.sh"; require_test_url https://platform.mcpruntime.org'],
                                env=self.env, capture_output=True)
        self.assertNotEqual(result.returncode, 0)

    def test_demo_session_selection_preserves_other_agents_and_servers(self):
        rows = {'sessions': [
            {'name': 'ours', 'serverRef': {'name': 'demo-run'}, 'subject': {'agentID': 'agent-a'}},
            {'name': 'other-agent', 'serverRef': {'name': 'demo-run'}, 'subject': {'agentID': 'real-agent'}},
            {'name': 'other-server', 'serverRef': {'name': 'real-server'}, 'subject': {'agentID': 'agent-a'}},
            {'name': 'anonymous', 'serverRef': {'name': 'demo-run'}, 'subject': {}}]}
        result = subprocess.run(['bash', '-c', f'source "{ROOT}/hack/lib/demo-sessions.sh"; mcpruntime_demo_session_names demo-run agent-a agent-b'],
                                input=json.dumps(rows), env=self.env, text=True, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout.strip(), 'ours')

    def test_missing_selected_perf_endpoint_fails(self):
        self.env.update(SCENARIOS='S3', BASE_URL='http://127.0.0.1:1', PERF_OUT_DIR=str(self.path / 'perf'),
                        PERF_BASELINE_DIR=str(self.path / 'baseline'))
        result = self.run_script('hack/qa/perf-regression.sh')
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse((self.path / 'baseline/S3.json').exists())

    def test_unknown_perf_scenario_fails(self):
        self.env.update(SCENARIOS='S4', PERF_OUT_DIR=str(self.path / 'perf'), PERF_BASELINE_DIR=str(self.path / 'baseline'))
        self.assertNotEqual(self.run_script('hack/qa/perf-regression.sh').returncode, 0)


    def test_adapter_port_check_preserves_existing_listener(self):
        with socket.socket() as listener:
            listener.bind(('127.0.0.1', 0)); listener.listen(1)
            port = listener.getsockname()[1]
            result = subprocess.run(['bash', '-c', f'source "{ROOT}/hack/lib/adapter-readiness.sh"; mcpruntime_adapter_require_free_port 127.0.0.1:{port}'],
                                    env=self.env, capture_output=True)
            self.assertNotEqual(result.returncode, 0)
            with socket.create_connection(('127.0.0.1', port), timeout=1): pass

    def test_adapter_port_check_accepts_time_wait_after_own_proxy_stops(self):
        with socket.socket() as listener:
            listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
            listener.bind(('127.0.0.1', 0)); listener.listen(1)
            port = listener.getsockname()[1]
            with socket.create_connection(('127.0.0.1', port), timeout=1) as client:
                connection, _ = listener.accept()
                connection.close()
                client.recv(1)
        result = subprocess.run(['bash', '-c', f'source "{ROOT}/hack/lib/adapter-readiness.sh"; mcpruntime_adapter_require_free_port 127.0.0.1:{port}'],
                                env=self.env, capture_output=True)
        self.assertEqual(result.returncode, 0, result.stderr)



class SnapshotTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        for name, content in {'cluster-identity.json': '{"clusterUID":"cluster-a"}', 'registry-tls.yaml': 'secret',
                              'k3s-state.db': 'state', 'k3s-host-and-pv-data.tar.gz': 'volumes'}.items():
            (self.root / name).write_text(content)
        (self.root / 'BACKUP_COMPLETE').touch()
        (self.root / 'FULL_BACKUP_COMPLETE').touch()
        self.checksums()

    def checksums(self):
        (self.root / 'SHA256SUMS').write_text(''.join(f'{hashlib.sha256(p.read_bytes()).hexdigest()}  {p.name}\n'
            for p in self.root.iterdir() if p.name not in {'SHA256SUMS', 'BACKUP_COMPLETE', 'FULL_BACKUP_COMPLETE'}))

    def test_valid_full_snapshot(self):
        self.assertEqual(snapshot.verify(self.root, 'cluster-a', True), self.root)

    def test_tls_snapshot_is_not_full_recovery(self):
        (self.root / 'FULL_BACKUP_COMPLETE').unlink()
        with self.assertRaises(ValueError): snapshot.verify(self.root, 'cluster-a', True)

    def test_other_cluster_rejected(self):
        with self.assertRaises(ValueError): snapshot.verify(self.root, 'cluster-b', True)

    def test_modified_credentials_rejected(self):
        (self.root / 'registry-tls.yaml').write_text('modified')
        with self.assertRaises(ValueError): snapshot.verify(self.root, 'cluster-a')

    def test_added_unverified_restore_file_rejected(self):
        (self.root / 'mcp-ui-credentials.yaml').write_text('unverified')
        with self.assertRaises(ValueError): snapshot.verify(self.root, 'cluster-a')

    def test_missing_full_data_rejected_even_with_marker(self):
        (self.root / 'k3s-state.db').unlink()
        self.checksums()
        with self.assertRaises(ValueError): snapshot.verify(self.root, 'cluster-a', True)

    def test_traversal_rejected(self):
        with (self.root / 'SHA256SUMS').open('a') as f: f.write('0' * 64 + '  ../outside\n')
        with self.assertRaises(ValueError): snapshot.verify(self.root, 'cluster-a')

    def test_symlink_to_external_credentials_rejected(self):
        (self.root / 'registry-tls.yaml').unlink()
        (self.root / 'registry-tls.yaml').symlink_to('/etc/passwd')
        with self.assertRaises(ValueError): snapshot.verify(self.root, 'cluster-a')



class MatrixTests(unittest.TestCase):
    def test_authenticated_handler_error_is_not_auth_failure(self):
        self.assertTrue(authz.expected({'expect_authenticated': True}, 400))
        for status in (301, 401, 403, 404, 405, 500): self.assertFalse(authz.expected({'expect_authenticated': True}, status))

    def test_exact_status_required(self):
        self.assertTrue(authz.expected({'expect': 403}, 403))
        self.assertFalse(authz.expected({'expect': 403}, 200))

    def test_admin_credential_cannot_stand_in_for_user(self):
        env = {f'AUTHZ_{s}_URL': 'http://localhost:12345' for s in ('PLATFORM', 'RUNTIME', 'ANALYTICS')}
        env.update(AUTHZ_ADMIN_KEY='same', AUTHZ_USER_KEY='same', AUTHZ_INGEST_KEY='ingest')
        with patch.dict(os.environ, env), self.assertRaises(ValueError): authz.run('unused')


@contextlib.contextmanager
def rpc_server(allowed_code=200, denied_code=403, public_code=401, denied_reason='tool_not_granted'):
    class Handler(BaseHTTPRequestHandler):
        def log_message(self, *_): pass
        def do_POST(self):
            request = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
            method = request['method']
            code = public_code if self.path == '/public' else 200
            payload = {'jsonrpc': '2.0', 'id': 1, 'result': {}}
            if method == 'tools/call' and self.path != '/public':
                if request['params']['name'] == 'aaa-ping':
                    code = allowed_code
                    payload['result'] = {'content': [{'type': 'text', 'text': 'pong'}]}
                else:
                    code = denied_code
                    payload = {'error': denied_reason}
                    if code == 200: payload = {'error': {'code': -32601, 'message': 'method not found'}}
            self.send_response(code)
            self.send_header('Content-Type', 'application/json')
            self.end_headers()
            self.wfile.write(json.dumps(payload).encode())
    server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
    thread = threading.Thread(target=server.serve_forever)
    thread.start()
    try:
        base = f'http://127.0.0.1:{server.server_port}'
        yield base + '/adapter', base + '/public'
    finally:
        server.shutdown(); thread.join(); server.server_close()


class HostedAcceptance(unittest.TestCase):
    def test_allowed_and_policy_denied_calls(self):
        with rpc_server() as urls: calls.check(*urls, attempts=1, delay=0)

    def test_enrollment_alone_cannot_pass_when_tool_identity_fails(self):
        with rpc_server(allowed_code=403) as urls, self.assertRaises(ValueError):
            calls.check(*urls, attempts=1, delay=0)

    def test_expired_identity_is_not_an_ungranted_tool_denial(self):
        with rpc_server(denied_reason='session_expired') as urls, self.assertRaises(ValueError):
            calls.check(*urls, attempts=1, delay=0)

    def test_server_crash_is_not_policy_denial(self):
        with rpc_server(denied_code=500) as urls, self.assertRaises(ValueError):
            calls.check(*urls, attempts=1, delay=0)

    def test_protocol_error_is_not_policy_denial(self):
        with rpc_server(denied_code=200) as urls, self.assertRaises(ValueError):
            calls.check(*urls, attempts=1, delay=0)

    def test_anonymous_success_fails(self):
        with rpc_server(public_code=200) as urls, self.assertRaises(ValueError):
            calls.check(*urls, attempts=1, delay=0)


if __name__ == '__main__': unittest.main()
