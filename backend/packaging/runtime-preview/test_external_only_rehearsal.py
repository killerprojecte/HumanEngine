import argparse
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import external_only_rehearsal as proof


class ExternalOnlyTests(unittest.TestCase):
    def test_same_image_empty_then_external_reopen_and_remove(self):
        with tempfile.TemporaryDirectory() as base:
            package = Path(base)
            payload = b"fixture-wasm"
            (package / proof.ARTIFACT).write_bytes(payload)
            (package / proof.ENTRY).write_text(json.dumps(
                {"id": proof.PLUGIN, "artifact": proof.ARTIFACT, "version": "fixture",
                 "artifactSha256": proof.sha256(payload).hexdigest()}))
            args = argparse.Namespace(package=package, image="local", revision="d" * 40)
            report = {"checks": []}
            trial = proof.Rehearsal(args, report)
            stored = {}
            row = dict(id=proof.PLUGIN, version="fixture", apiVersion=2, builtin=False, enabled=False)
            launches = []
            removed = []
            live = installed = False

            def docker(*command, **_):
                nonlocal live, installed
                if command[:2] == ("image", "inspect"):
                    return json.dumps([dict(Id="sha256:t", Config={"Labels": {
                        "org.opencontainers.image.revision": args.revision}})])
                if command[:2] == ("run", "--rm"):
                    self.assertEqual(command[-2:], ("sha256:t", "--inspect-builtins"))
                    return "[]"
                if command[:2] == ("run", "-d"):
                    self.assertFalse(live)
                    self.assertEqual(command[-1], "sha256:t")
                    self.assertEqual(command[command.index("--network") + 1], "none")
                    self.assertNotIn("-p", command)
                    installed = "--mount" in command
                    if installed:
                        self.assertIn(proof.mount_args(package)[1], command)
                    launches.append(installed)
                    live = True
                    return ""
                if command[:2] == ("inspect", "--format"):
                    return str(live).lower()
                if command[0] == "inspect":
                    return json.dumps([dict(Image="sha256:t", HostConfig={"NetworkMode": "none"}, Mounts=[
                        dict(Type="volume", Name=trial.volumes[0], Destination="/app/data"),
                        dict(Type="volume", Name=trial.volumes[1], Destination="/app/authority"),
                        *([dict(Type="bind", Source=str(package), RW=False, Destination="/app/plugins")] if installed else []),
                    ])])
                if command[0] == "logs":
                    return '{"code":"server.listening"}' if live else '{"code":"app.drained"}'
                if command[0] == "exec":
                    self.assertTrue(live)
                    wire = json.loads(command[-1])
                    if wire["path"] == proof.CATALOG:
                        return json.dumps([row] if installed else [])
                    if wire["path"] == proof.CONFIG:
                        if wire["method"] == "PUT":
                            stored.update(wire["value"])
                        return json.dumps(stored)
                    self.assertEqual(wire["path"], proof.ENABLED)
                    row["enabled"] = wire["value"]["enabled"]
                    return '{"ok":true}'
                if command[0] == "kill":
                    self.assertTrue(live)
                    live = False
                if command[0] == "wait":
                    self.assertFalse(live)
                    return "0"
                if command[:2] == ("volume", "rm"):
                    removed.append(command[-1])
                return ""

            with patch.object(proof, "docker", side_effect=docker), patch.object(proof, "node_pids", return_value=[]) as pid:
                trial.run()
                self.assertTrue(trial.cleanup())
                self.assertEqual(pid.call_count, 4)
            self.assertEqual(launches, [False, True, True, False])
            self.assertEqual(report["checks"], ["bare-empty-catalog", "installed-default-disabled-then-enabled",
                                               "cold-reopen-retained-settings-and-disabled", "removed-empty-catalog"])
            self.assertEqual(removed, list(reversed(trial.volumes)))

    def test_rejects_wrong_path_and_writable_package_mount(self):
        host = Path("/independent/guest")
        mount = {"Type": "bind", "Source": str(host), "RW": False, "Destination": "/app/plugins"}
        self.assertTrue(proof.mounted_readonly([mount], host))
        self.assertFalse(proof.mounted_readonly([dict(mount, RW=True)], host))
        self.assertFalse(proof.mounted_readonly([dict(mount, Source="/different")], host))
        self.assertFalse(proof.mounted_readonly([mount, mount], host))
