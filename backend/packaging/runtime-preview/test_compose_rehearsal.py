import copy
import subprocess
import unittest
from pathlib import Path
from unittest.mock import patch

from compose_rehearsal import assert_envelope, run


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
                    if command in argv:
                        raise subprocess.CalledProcessError(1, argv, stderr="not for evidence")
                    return ""
                with patch("compose_rehearsal.docker"), \
                        patch("compose_rehearsal.subprocess.check_output", side_effect=compose):
                    with self.assertRaises(subprocess.CalledProcessError):
                        run(report)
                self.assertEqual(report["phase"], expected)

    def test_workflow_keeps_compose_opt_in_after_ordinary_package_acceptance(self):
        workflow = (Path(__file__).resolve().parents[3] /
                    ".github/workflows/golang-package-preview.yml").read_text()
        self.assertLess(workflow.index("Exercise actual packaged startup"),
                        workflow.index("Rehearse actual Compose stop and cold reopen"))
        step = workflow.split("Rehearse actual Compose stop and cold reopen", 1)[1].split("- name:", 1)[0]
        self.assertIn("if: steps.proofs.outputs.compose == 'true'", step)
        self.assertIn("compose_rehearsal.py", step)
        self.assertNotIn("docker build", step)


if __name__ == "__main__":
    unittest.main()
