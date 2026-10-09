"""Exercise deployment decisions without a Docker daemon or a docs host."""
import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class DeploymentTest(unittest.TestCase):
    def deploy(self, mode):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            shim = root / "shim"
            shim.write_text("""#!/usr/bin/env python3
import json, os, pathlib, sys
name = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
with open(os.environ['COMMAND_LOG'], 'a') as log:
    log.write(json.dumps([name, *args]) + '\\n')
mode = os.environ['TEST_MODE']
if name == 'docker':
    if args[0] == 'ps' and mode == 'port-conflict':
        print('another-service')
    elif args[:2] == ['container', 'inspect']:
        sys.exit(0)
    elif args[0] == 'inspect':
        print('pkgsite:previous')
    elif args[:2] == ['image', 'ls']:
        print('pkgsite:previous\\npkgsite:new\\npkgsite:old')
    elif args[0] == 'run' and args[-1] == 'pkgsite:new' and mode == 'start-failure':
        sys.exit(1)
    elif args[0] == 'exec':
        sys.exit(99)
elif name == 'curl':
    count_file = pathlib.Path(os.environ['CURL_COUNT'])
    count = int(count_file.read_text()) + 1 if count_file.exists() else 1
    count_file.write_text(str(count))
    sys.exit(1 if mode == 'unready' or count < 3 else 0)
""")
            shim.chmod(0o755)
            for name in ("docker", "curl", "sleep"):
                (root / name).symlink_to(shim)
            log = root / "commands.jsonl"
            env = dict(os.environ, PATH=f"{root}:{os.environ['PATH']}",
                       PKGSITE_IMAGE="pkgsite:new", TEST_MODE=mode,
                       COMMAND_LOG=str(log), CURL_COUNT=str(root / "count"))
            result = subprocess.run(
                ["bash", str(Path(__file__).with_name("deploy-remote.sh"))],
                env=env, capture_output=True, text=True, timeout=20,
            )
            commands = [json.loads(line) for line in log.read_text().splitlines()]
            return result, commands

    def test_retries_host_http_then_keeps_previous_image(self):
        result, commands = self.deploy("ready")
        self.assertEqual(result.returncode, 0, result.stderr)
        probes = [command for command in commands if command[0] == "curl"]
        self.assertEqual(len(probes), 3)
        self.assertIn("http://127.0.0.1:8083/github.com/mcp-runtime/mcp-runtime/pkg/access", probes[0])
        self.assertFalse(any(command[:2] == ["docker", "exec"] for command in commands))
        self.assertIn(["docker", "image", "rm", "pkgsite:old"], commands)
        self.assertNotIn(["docker", "image", "rm", "pkgsite:previous"], commands)
        run = next(command for command in commands if command[:2] == ["docker", "run"])
        for flag, value in (("--pids-limit", "256"), ("--memory", "1536m"), ("--cpus", "2")):
            self.assertEqual(run[run.index(flag) + 1], value)

    def test_readiness_failure_restores_previous_image(self):
        result, commands = self.deploy("unready")
        self.assertEqual(result.returncode, 1)
        self.assertEqual(sum(command[0] == "curl" for command in commands), 90)
        runs = [command[-1] for command in commands if command[:2] == ["docker", "run"]]
        self.assertEqual(runs, ["pkgsite:new", "pkgsite:previous"])
        self.assertFalse(any(command[:3] == ["docker", "image", "rm"] for command in commands))

    def test_start_failure_restores_previous_image(self):
        result, commands = self.deploy("start-failure")
        self.assertEqual(result.returncode, 1)
        runs = [command[-1] for command in commands if command[:2] == ["docker", "run"]]
        self.assertEqual(runs, ["pkgsite:new", "pkgsite:previous"])
        self.assertFalse(any(command[0] == "curl" for command in commands))

    def test_port_conflict_preserves_running_container(self):
        result, commands = self.deploy("port-conflict")
        self.assertEqual(result.returncode, 1)
        self.assertIn("another-service", result.stderr)
        self.assertFalse(any(command[:2] in (["docker", "run"], ["docker", "rm"]) for command in commands))


if __name__ == "__main__":
    unittest.main()
