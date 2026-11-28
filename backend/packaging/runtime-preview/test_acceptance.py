"""Driver tests only; these never replace the actual Docker acceptance gate."""
from hashlib import sha256
import io
import json
import os
import shutil
import subprocess
import tempfile
from pathlib import Path
import unittest
from unittest.mock import patch

import acceptance
from operator_packages import PACKAGES, load_packages


class DriverTests(unittest.TestCase):
    def test_operator_artifact_identity_includes_manifest_and_wasm(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            for plugin, filename in PACKAGES.items():
                wasm = filename.removesuffix(".v2.json") + ".wasm"
                binary = ("fixture-" + plugin).encode()
                (root / wasm).write_bytes(binary)
                (root / filename).write_text(json.dumps(dict(
                    id=plugin, apiVersion=2, artifact=wasm, artifactSha256=sha256(binary).hexdigest(),
                )))
            old = load_packages(root)
            snow = root / PACKAGES["snowluma"]
            snow.write_text(snow.read_text() + "\n")
            new = load_packages(root)
            self.assertNotEqual(old["snowluma"]["manifest"], new["snowluma"]["manifest"])
            self.assertEqual(old["snowluma"]["artifact"], new["snowluma"]["artifact"])
            (root / "snowluma-http.wasm").write_bytes(b"different")
            with self.assertRaisesRegex(ValueError, "does not match"):
                load_packages(root)

    def test_node_identity_probe_runs_inside_container_as_normal_user(self):
        for rows, expected in [("", []), ("42\n7", [7, 42])]:
            with self.subTest(rows=rows), patch.object(acceptance, "docker", return_value=rows) as call:
                self.assertEqual(acceptance.node_pids("fixture"), expected)
                self.assertEqual(call.call_args.args[:6], ("exec", "--user", "node", "fixture", "sh", "-c"))
                script = call.call_args.args[6]
                self.assertIn("/proc/[0-9]*/exe", script)
                self.assertNotIn("cmdline", script)
                self.assertNotIn("environ", script.split("set -eu", 1)[1])
        with patch.object(acceptance, "docker", return_value="MainThread"), self.assertRaises(AssertionError):
            acceptance.node_pids("fixture")

    def test_real_node_executable_identity_with_mainthread_name(self):
        # The same non-root /proc read used by docker exec, with real Node.
        self.assertNotEqual(os.geteuid(), 0, "run identity proof as an ordinary user")
        node = shutil.which("node")
        self.assertIsNotNone(node)
        script = Path(__file__).with_name("node-pids.sh")
        process = subprocess.Popen(
            [node, "-e", "process.title='MainThread';console.log(process.pid);setInterval(()=>{},1000)"],
            stdout=subprocess.PIPE, text=True)
        try:
            self.assertEqual(int(process.stdout.readline()), process.pid)
            self.assertEqual(Path(f"/proc/{process.pid}/comm").read_text().strip(), "MainThread")
            self.assertEqual(Path(f"/proc/{process.pid}/exe").resolve(), Path(node).resolve())
            output = subprocess.check_output(["sh", str(script)], text=True)
            self.assertIn(str(process.pid), output.splitlines())
            self.assertTrue(all(row.isdecimal() for row in output.splitlines()))
        finally:
            process.terminate()
            process.wait(timeout=10)
            process.stdout.close()
        output = subprocess.check_output(["sh", str(script)], text=True)
        self.assertNotIn(str(process.pid), output.splitlines())

    def test_parameterized_v1_fixture_has_two_independent_ids_and_echoes(self):
        with tempfile.TemporaryDirectory() as directory:
            for plugin_id in ("package-fixture-a", "package-fixture-b"):
                target = Path(directory) / (plugin_id + ".mjs")
                shutil.copyfile(Path(__file__).with_name("fixture.mjs"), target)
                source = """
import assert from 'node:assert/strict';
const {default: plugin} = await import(process.argv[1]);
assert.equal(plugin.manifest.id, process.argv[2]);
let echo;
plugin.setup({registerAdminAction(name, callback) { assert.equal(name, 'echo'); echo = callback; }});
assert.deepEqual(await echo({proof:'synthetic'}), {input:{proof:'synthetic'},packaged:true,pluginId:process.argv[2]});
"""
                subprocess.run(["node", "--input-type=module", "-e", source, target.as_uri(), plugin_id], check=True)

    def test_packaging_uses_modern_python_distro_and_matching_native_runtime(self):
        # Source contract only: actual libraries still require the Docker gate.
        dockerfile = Path(__file__).with_name("Dockerfile").read_text()
        bases = [line.split()[1] for line in dockerfile.splitlines() if line.startswith("FROM ")]
        self.assertEqual(bases, ["golang:1.27.3-trixie", "node:24.22.1-trixie-slim",
                                 "node:24.22.1-trixie-slim"])
        self.assertIn("python3 make g++", dockerfile)
        builder = Path(__file__).resolve().parents[2] / "native/icu/build.py"
        self.assertIn("filter='data'", builder.read_text())

    def test_continue_requires_complete_response_before_signal(self):
        acceptance.read_continue(io.BytesIO(b"HTTP/1.1 100 Continue\r\n\r\n"))
        acceptance.read_continue(io.BytesIO(b"HTTP/1.1 100 Continue\r\nX-Test: yes\r\n\r\n"))
        for response in (b"", b"HTTP/1.1 401 Unauthorized\r\n\r\n",
                         b"HTTP/1.1 100 Continue\r\nX-Test: yes\r\n"):
            with self.subTest(response=response), self.assertRaises(AssertionError):
                acceptance.read_continue(io.BytesIO(response))

    def test_deadline_is_failure_not_success(self):
        with patch.object(acceptance.time, "monotonic", side_effect=[0, 1]), self.assertRaises(AssertionError):
            acceptance.wait_for(lambda: False, "fixture", seconds=1)

    def test_ready_value_is_returned(self):
        self.assertEqual(acceptance.wait_for(lambda: "ready", "fixture"), "ready")

    def test_browser_usage_seed_mounts_only_the_stopped_main_volume(self):
        with patch.object(acceptance, "docker") as call:
            start = acceptance.seed_browser_usage("sha256:tested", "fixture-main")
        self.assertEqual((start.hour, start.minute, start.second, start.utcoffset().total_seconds()),
                         (16, 0, 0, 0))
        args = call.call_args.args
        self.assertEqual(args[:8], ("run", "--rm", "--network", "none", "--entrypoint",
                                    "node", "--mount", "type=volume,source=fixture-main,target=/app/data"))
        self.assertEqual(args[8:11], ("sha256:tested", "--input-type=module", "-e"))
        self.assertEqual(args[12:], ("/app/data/main.sqlite", start.isoformat(timespec="milliseconds")))
        self.assertNotIn("/app/authority", args)

    def test_http_request_keeps_token_out_of_url_and_checks_status(self):
        with patch.object(acceptance.http.client, "HTTPConnection") as constructor:
            connection = constructor.return_value
            response = connection.getresponse.return_value
            response.status = 200
            response.read.return_value = b'{"saved":true}'
            self.assertEqual(acceptance.request(1234, "PUT", "/api/settings", {"debugModelIO": False}), {"saved": True})
            args = connection.request.call_args.args
            self.assertEqual(args[:2], ("PUT", "/api/settings"))
            self.assertEqual(args[3]["Authorization"], "Bearer " + acceptance.TOKEN)
            connection.close.assert_called_once()
            response.status = 503
            with self.assertRaises(AssertionError):
                acceptance.request(1234, "GET", "/api/settings")

    def test_unauthenticated_probe_omits_header(self):
        with patch.object(acceptance.http.client, "HTTPConnection") as constructor:
            connection = constructor.return_value
            connection.getresponse.return_value.status = 401
            connection.getresponse.return_value.read.return_value = b'{}'
            acceptance.request(1234, "GET", "/api/settings", authenticated=False, expected=401)
            self.assertNotIn("Authorization", connection.request.call_args.args[3])


if __name__ == "__main__":
    unittest.main()
