#!/usr/bin/env python3
"""Install and remove an independent WASM package on the same Docker image."""
import argparse
from hashlib import sha256
import json
import os
from pathlib import Path
import subprocess
from uuid import uuid4

from acceptance import node_pids, wait_for
from migration_drill import docker, require

PLUGIN = "typing-delay"
ENTRY = PLUGIN + ".v2.json"
ARTIFACT = PLUGIN + ".wasm"
CATALOG = "/api/plugins"
CONFIG = f"{CATALOG}/{PLUGIN}/config"
ENABLED = f"{CATALOG}/{PLUGIN}/enabled"
NODE_REQUEST = """
const command = JSON.parse(process.argv[1])
fetch('http://127.0.0.1:3001'+command.path, {
  method: command.method,
  headers: {'Authorization':'Bearer '+process.env.HUMANENGINE_ADMIN_TOKEN,'Content-Type':'application/json'},
  body: command.value === null ? undefined : JSON.stringify(command.value),
  signal: AbortSignal.timeout(5000)
}).then(async r => {
  if (r.status !== 200) throw Error('status');
  process.stdout.write(await r.text())
}).catch(() => { process.exitCode = 1 })
"""


def mount_args(package):
    return ("--mount", f"type=bind,source={package},target=/app/plugins,readonly",
            "-e", f"HUMANENGINE_PLUGINS={ENTRY}")


def mounted_readonly(mounts, package):
    entries = [row for row in mounts if row["Destination"] == "/app/plugins"]
    return (len(entries) == 1 and entries[0]["Type"] == "bind"
            and entries[0]["RW"] is False and entries[0]["Source"] == str(package))


def plugin(row, version, enabled):
    return (row["id"], row["version"], row["apiVersion"], row["builtin"], row["enabled"]) == (
        PLUGIN, version, 2, False, enabled)


class Rehearsal:
    def __init__(self, args, report):
        self.args = args
        self.report = report
        self.phase = "image"
        self.name = "he-external-only-" + uuid4().hex
        self.volumes = []
        self.owner = None
        self.image = None
        self.package = args.package.resolve()
        self.env = {**os.environ, "HUMANENGINE_ADMIN_TOKEN": uuid4().hex + uuid4().hex}

    def api(self, method, path, value=None):
        command = json.dumps(dict(method=method, path=path, value=value))
        result = docker("exec", self.owner, "node", "-e", NODE_REQUEST, command)
        return json.loads(result)

    def start(self, installed):
        require(self.owner is None, "more than one rehearsal owner")
        self.owner = self.name + "-owner"
        options = mount_args(self.package) if installed else ("-e", "HUMANENGINE_PLUGINS=")
        docker("run", "-d", "--name", self.owner, "--network", "none",
               "-e", "HUMANENGINE_ADMIN_TOKEN", "-v", self.volumes[0] + ":/app/data",
               "-v", self.volumes[1] + ":/app/authority", *options, self.image, env=self.env)

        def ready():
            require(docker("inspect", "--format", "{{.State.Running}}", self.owner) == "true",
                    "rehearsal owner exited before readiness")
            return '"code":"server.listening"' in docker("logs", self.owner)

        wait_for(ready, self.phase)
        state, = json.loads(docker("inspect", self.owner))
        require(state["Image"] == self.image and state["HostConfig"]["NetworkMode"] == "none",
                "wrong owner image or network")
        mounts = state["Mounts"]
        volumes = {row["Destination"]: row["Name"] for row in mounts if row["Type"] == "volume"}
        require(volumes == dict(zip(("/app/data", "/app/authority"), self.volumes)),
                "installation volume identities changed")
        if installed:
            require(mounted_readonly(mounts, self.package), "external package not mounted read-only")
        else:
            require(not any(row["Destination"] == "/app/plugins" for row in mounts), "uninstalled package is still mounted")

    def stop(self):
        require(not node_pids(self.owner), "V1 Node worker present after the administration probe exited")
        docker("kill", "--signal=TERM", self.owner)
        require(docker("wait", self.owner, timeout=120) == "0", "owner did not exit cleanly")
        require('"code":"app.drained"' in docker("logs", self.owner), "missing physical drain receipt")
        docker("rm", self.owner)
        self.owner = None
        self.report["checks"].append(self.phase)

    def run(self):
        image, = json.loads(docker("image", "inspect", self.args.image))
        self.image = image["Id"]
        require(image["Config"]["Labels"]["org.opencontainers.image.revision"] == self.args.revision,
                "unexpected source revision")
        self.report["image"] = self.image
        require(json.loads(docker("run", "--rm", "--network", "none", self.image, "--inspect-builtins")) == [],
                "external-only image has embedded plugins")
        manifest = json.loads((self.package / ENTRY).read_text())
        require(manifest["id"] == PLUGIN and manifest["artifact"] == ARTIFACT and
                manifest["artifactSha256"] == sha256((self.package / ARTIFACT).read_bytes()).hexdigest(),
                "independent package does not match the manifest")
        version = self.report["version"] = manifest["version"]
        for suffix in ("data", "authority"):
            name = self.name + "-" + suffix
            docker("volume", "create", name)
            self.volumes.append(name)
        self.phase = "bare-empty-catalog"
        self.start(False)
        require(self.api("GET", CATALOG) == [], "bare host catalog not empty")
        self.stop()
        self.phase = "installed-default-disabled-then-enabled"
        self.start(True)
        row, = self.api("GET", CATALOG)
        require(plugin(row, version, False), "external catalog default differs")
        self.api("PUT", CONFIG, {"speedFactor": 5})
        require(self.api("PUT", ENABLED, {"enabled": True})["ok"] is True,
                "enable did not activate real guest")
        row, = self.api("GET", CATALOG)
        require(plugin(row, version, True), "enabled entry differs")
        self.stop()
        self.phase = "cold-reopen-retained-settings-and-disabled"
        self.start(True)
        row, = self.api("GET", CATALOG)
        require(plugin(row, version, True), "cold reopen did not restore enabled guest")
        require(self.api("GET", CONFIG) == {"speedFactor": 5}, "lost speed setting")
        require(self.api("PUT", ENABLED, {"enabled": False})["ok"] is True,
                "unable to disable external guest")
        row, = self.api("GET", CATALOG)
        require(plugin(row, version, False), "disabled entry differs")
        self.stop()
        self.phase = "removed-empty-catalog"
        self.start(False)
        require(self.api("GET", CATALOG) == [], "removed entry still registered")
        self.stop()

    def cleanup(self):
        commands = []
        if self.owner:
            commands.append(("rm", "-f", self.owner))
        commands.extend(("volume", "rm", name) for name in reversed(self.volumes))
        ok = True
        for args in commands:
            try:
                ok = (docker(*args, check=False, timeout=20) is not None) and ok
            except (OSError, subprocess.SubprocessError):
                ok = False
        return ok


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", required=True)
    parser.add_argument("--revision", required=True)
    parser.add_argument("--package", required=True, type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    report = dict(kind="external_only_typing_delay_lifecycle", revision=args.revision, status="failed", checks=[])
    trial = Rehearsal(args, report)
    with args.evidence.open("x") as evidence:
        try:
            trial.run()
            report["status"] = "passed"
        except (AssertionError, OSError, RuntimeError, ValueError, TypeError, KeyError, subprocess.SubprocessError) as error:
            report.update(failure_phase=trial.phase, failure_type=type(error).__name__)
        finally:
            if not trial.cleanup():
                report.update(status="failed", cleanup_failed=True)
            evidence.write(json.dumps(report, indent=2) + "\n")
    print(json.dumps(report))
    return int(report["status"] != "passed")


if __name__ == "__main__":
    raise SystemExit(main())
