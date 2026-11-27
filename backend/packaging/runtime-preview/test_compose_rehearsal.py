import copy
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from compose_rehearsal import (assert_envelope, assert_standalone_catalog, assert_standalone_mount,
                               bridge_gateway, run, run_snow_fixture, standalone_override)


class ComposeRehearsalTests(unittest.TestCase):
    def test_inspect_requires_actual_distinct_mounts_and_runtime_constraints(self):
        inspect = {
            "HostConfig": {"Init": True, "ReadonlyRootfs": True, "CapDrop": ["ALL"],
                           "Tmpfs": {"/tmp": "rw,noexec,nosuid,size=67108864"},
                           "SecurityOpt": ["no-new-privileges:true"]},
            "Config": {"StopTimeout": 30, "StopSignal": "SIGTERM"},
            "NetworkSettings": {"Ports": {"3000/tcp": [{"HostIp": "127.0.0.1", "HostPort": "32775"}]}},
            "Mounts": [{"Destination": "/app/data", "Name": "fixture-data", "Type": "volume", "RW": True},
                       {"Destination": "/app/authority", "Name": "fixture-authority", "Type": "volume", "RW": True}],
        }
        self.assertEqual(assert_envelope(inspect),
                         (32775, {"/app/data": "fixture-data", "/app/authority": "fixture-authority"}))
        changes = [
            (("HostConfig", "Init"), False),
            (("HostConfig", "ReadonlyRootfs"), False),
            (("HostConfig", "Tmpfs", "/tmp"), "rw,exec,nosuid,size=64m"),
            (("Config", "StopTimeout"), 10),
            (("NetworkSettings", "Ports", "3000/tcp", 0, "HostIp"), "0.0.0.0"),
            (("Mounts", 1, "Name"), "fixture-data"),
        ]
        for path, value in changes:
            with self.subTest(path=path):
                wrong = copy.deepcopy(inspect)
                current = wrong
                for key in path[:-1]:
                    current = current[key]
                current[path[-1]] = value
                with self.assertRaises(AssertionError):
                    assert_envelope(wrong)

    def test_startup_failure_phase_distinguishes_config_up_and_image(self):
        for command, expected in (("config", "compose-config"), ("up", "compose-start-up"),
                                  ("ps", "compose-start-container-image")):
            with self.subTest(command=command):
                report = {"image": "sha256:fixture"}
                def compose(argv, **kwargs):
                    self.assertEqual(kwargs["env"]["HE_COMPOSE_PORT"], "0")
                    self.assertTrue(argv[argv.index("--project-name") + 1].startswith("he-rehearsal-"))
                    if command in argv:
                        raise subprocess.CalledProcessError(1, argv, stderr="not for evidence")
                    return ""
                with patch("compose_rehearsal.docker"), \
                        patch("compose_rehearsal.subprocess.check_output", side_effect=compose):
                    with self.assertRaises(subprocess.CalledProcessError):
                        run(report)
                self.assertEqual(report["phase"], expected)

    def test_snow_uses_private_gateway_of_its_own_bridge(self):
        network = {"Driver": "bridge", "Labels": {"com.docker.compose.project": "fixture"},
                   "IPAM": {"Config": [{"Gateway": "172.20.0.1"}, {"Gateway": "fd00::1"}]}}
        self.assertEqual(bridge_gateway("fixture", network), "172.20.0.1")
        with self.assertRaises(AssertionError):
            bridge_gateway("other", network)
        network["IPAM"]["Config"][0]["Gateway"] = "127.0.0.1"
        with self.assertRaises(AssertionError):
            bridge_gateway("fixture", network)

    def test_snow_host_protocol_orders_two_process_lifetimes(self):
        with tempfile.TemporaryDirectory() as folder:
            binary = Path(folder, "fixture")
            binary.write_text("""#!/usr/bin/env python3
import json, os
assert os.environ["HE_COMPOSE_FIXTURE_BIND"] == "172.20.0.1"
assert os.environ["HUMANENGINE_ADMIN_TOKEN"] == "synthetic"
for phase, expected in [("start", "first"), ("stop-overlap", "partial"),
                        ("start", "second"), ("stop", "drained")]:
    print(json.dumps(phase), flush=True)
    assert json.loads(input()) == expected
    if phase == "stop-overlap":
        print(json.dumps("remote-history-stop-503"), flush=True)
print("PASS")
""")
            binary.chmod(0o700)
            addresses = iter(("first", "second"))
            logs = iter(("partial", "drained"))
            self.assertEqual(run_snow_fixture(binary, "172.20.0.1", "synthetic",
                                              lambda: next(addresses), lambda: next(logs), lambda: next(logs)),
                             "remote-history-stop-503")

    def test_standalone_proof_requires_read_only_bind_and_exact_external_catalog(self):
        package = Path("/tmp/synthetic-snow")
        service = standalone_override(package)["services"]["humanengine"]
        self.assertEqual(service["environment"]["HUMANENGINE_PLUGINS"], "snowluma-http.v2.json")
        bind, = service["volumes"]
        self.assertEqual((bind["source"], bind["target"], bind["read_only"]),
                         (str(package), "/app/plugins", True))
        inspected = {"Mounts": [{"Destination": bind["target"], "Source": bind["source"],
                                 "Type": "bind", "RW": False}]}
        assert_standalone_mount(inspected, package)
        inspected["Mounts"][0]["RW"] = True
        with self.assertRaises(AssertionError):
            assert_standalone_mount(inspected, package)
        exact = {"id": "snowluma", "builtin": False, "enabled": True, "version": "synthetic"}
        assert_standalone_catalog([exact], "synthetic", True)
        for key, value in (("builtin", True), ("enabled", False), ("version", "another")):
            with self.subTest(key=key), self.assertRaises(AssertionError):
                assert_standalone_catalog([dict(exact, **{key: value})], "synthetic", True)

    def test_workflow_keeps_compose_opt_in_after_ordinary_package_acceptance(self):
        workflow = (Path(__file__).resolve().parents[3] /
                    ".github/workflows/golang-package-preview.yml").read_text()
        self.assertLess(workflow.index("Exercise actual packaged startup"),
                        workflow.index("Rehearse actual Compose stop and cold reopen"))
        step = workflow.split("Rehearse actual Compose stop and cold reopen", 1)[1].split("- name:", 1)[0]
        self.assertIn("if: steps.proofs.outputs.compose == 'true'", step)
        self.assertIn("compose_rehearsal.py", step)
        self.assertNotIn("docker build", step)
        self.assertIn("--snow-fixture", step)
        self.assertIn("--image humanengine-go-external:local", step)
        self.assertIn("--external-snow-package", step)
        for prerequisite in ("Verify opt-in host excludes SnowLuma", "Build the separate SnowLuma test package"):
            definition = workflow.split(prerequisite, 1)[1].split("- name:", 1)[0]
            self.assertIn("steps.proofs.outputs.compose == 'true'", definition)


if __name__ == "__main__":
    unittest.main()
