#!/usr/bin/env python3
"""Disposable TS → Go → TS rehearsal using a data bind, named authority and read-only plugins."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

from migration_drill import AUTHORITY, DATABASE, SNOW_HTTP_MANIFEST, TS_REVISION, Drill, docker, require


HERE = Path(__file__).resolve().parent
TARGET = "/app/data"


class BindDrill(Drill):
    def compose(self, *args, check=True, timeout=90):
        return docker("compose", "--env-file", "/dev/null", "-p", self.project,
                      "-f", str(HERE / "compose-rehearsal.yaml"),
                      "-f", str(HERE / "compose-bind-mount.yaml"),
                      "-f", str(self.override), *args,
                      check=check, timeout=timeout, env=self.env)

    def start(self, engine):
        require(self.owner is None, "overlapping Compose writers")
        image = self.args.go_image if engine == "go" else self.args.ts_image
        # Keep the fixture off the public network even if the old image
        # has ambient defaults. All provider traffic is a loopback fixture.
        config = {"networks": {"default": {"internal": True}}}
        if engine == "ts":
            config["services"] = {"humanengine": {
                "command": ["node", ".output/server/index.mjs"],
                "environment": {"HUMANENGINE_PLUGINS": ""},
            }}
        self.override.write_text(json.dumps(config))
        self.env.update(HE_COMPOSE_IMAGE=image, HE_COMPOSE_PORT=self.published)
        self.phase = f"{self.leg}-compose-config"
        resolved = json.loads(self.compose("config", "--format", "json"))["services"]["humanengine"]
        volumes = {v["target"]: v for v in resolved["volumes"]}
        require(resolved["image"] == image and resolved["read_only"] is True and
                volumes[TARGET]["type"] == "bind" and volumes[TARGET]["source"] == str(self.data) and
                volumes[AUTHORITY]["type"] == "volume" and
                volumes["/app/plugins"]["read_only"] is True and
                volumes["/app/plugins"]["source"] == str(self.package),
                "Compose did not resolve the expected protected mounts")
        if engine == "go":
            cmd = resolved["command"]
            require(all(cmd[cmd.index(flag) + 1] == value for flag, value in (
                ("--database", DATABASE), ("--authority", AUTHORITY), ("--listen", "0.0.0.0:3000")
            )), "Go command does not use the deployed data filename and port")

        self.phase = f"{self.leg}-start"
        self.compose("up", "-d", "--no-build", "--pull", "never")
        container = self.compose("ps", "-q", "humanengine")
        require(bool(container), "Compose container missing")
        self.name, self.owner, self.port = container, engine, "3000"
        state = json.loads(docker("inspect", "--format", "{{json .}}", self.name))
        mounts = {v["Destination"]: v for v in state["Mounts"]}
        bindings = state["HostConfig"]["PortBindings"]["3000/tcp"]
        binding, = bindings
        require(state["Image"] == self.report["images"][engine] and mounts[TARGET]["Source"] == str(self.data)
                and mounts[TARGET]["Type"] == "bind" and mounts[TARGET]["RW"] is True
                and mounts["/app/plugins"]["Source"] == str(self.package)
                and mounts["/app/plugins"]["Type"] == "bind" and mounts["/app/plugins"]["RW"] is False
                and mounts[AUTHORITY]["Type"] == "volume" and mounts[AUTHORITY]["RW"] is True
                and binding["HostIp"] == "127.0.0.1" and binding["HostPort"] == self.published,
                "running container does not match Compose mount or port plan")
        authority = mounts[AUTHORITY]["Name"]
        if self.authority is None:
            self.authority = authority
        require(self.authority == authority, "Compose replaced the enrolled authority")
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            response = self.exchange("GET", "/api/status", None, check=False)
            if response and response[0] == 200:
                return
            require(docker("inspect", "-f", "{{.State.Running}}", self.name) == "true",
                    f"{engine} stopped before readiness")
            time.sleep(0.2)
        raise RuntimeError(f"{engine} readiness deadline")

    def stop(self):
        self.phase = f"{self.leg}-clean-drain"
        super().stop()
        require((self.data / "humanengine.sqlite").is_file() and not (self.data / "main.sqlite").exists(),
                "expected one live database at the original filename")
        self.record(f"{self.leg}-same-bind-authority-and-port-3000-clean-drain")

    def rehearse(self):
        self.leg = "ts-seed"
        self.start("ts")
        marker = self.seed_unicode_memory()
        initial = self.setting()
        self.seed_credential()
        self.prove_credential()
        self.stop()
        self.leg = "go-reopen"
        self.start("go")
        self.verify_unicode_memory(marker, self.leg)
        self.prove_credential()
        plugin = next((p for p in self.api("GET", "/api/plugins") if p["id"] == "snowluma"), {})
        require(plugin.get("apiVersion") == 2 and plugin.get("builtin") is False and
                plugin.get("enabled") is False, "separate read-only plugin did not load as an inactive external package")
        require(self.setting(not initial) is not initial, "Go settings write did not persist")
        self.record("go-bind-identity-external-plugin-and-encrypted-key")
        self.stop()
        self.leg = "ts-rollback"
        self.start("ts")
        self.verify_unicode_memory(marker, self.leg)
        self.prove_credential()
        require(self.setting() is not initial, "TypeScript rollback did not read the latest Go state")
        self.stop()
        self.record("bind-compose-ts-go-ts-no-parallel-main-sqlite")

    def run(self):
        self.report["images"] = {
            "ts": self.image(self.args.ts_image, TS_REVISION),
            "go": self.image(self.args.go_image, self.args.go_revision),
        }
        self.phase = "prepare-synthetic-bind"
        self.package = self.args.external_snow_package.resolve(strict=True)
        require((self.package / SNOW_HTTP_MANIFEST).is_file(), "missing external fixture package")
        self.project = self.name + "-bind"
        self.authority = None
        # Hosted, serialized test. An occupied port fails rather than falling back.
        self.published = "31079"
        with tempfile.TemporaryDirectory(prefix="he-bind-") as tmp:
            work = Path(tmp).resolve()
            self.override = work / "fixture.yaml"
            self.data = work / "data"
            self.data.mkdir()
            # The fixture container is uid 1000; this directory has synthetic data only.
            self.data.chmod(0o777)
            self.env.update(HE_COMPOSE_DATA_DIR=str(self.data), HE_COMPOSE_PLUGIN_DIR=str(self.package),
                            HE_COMPOSE_PLUGIN_ENTRIES=SNOW_HTTP_MANIFEST)
            try:
                self.rehearse()
            finally:
                if self.override.is_file():
                    self.compose("down", "--volumes", check=False, timeout=120)
                # Remove test data unreadable to the runner, never a user directory.
                docker("run", "--rm", "--network", "none", "--user", "0:0",
                       "--mount", f"type=bind,source={self.data},target=/fixture",
                       "--entrypoint", "chown", self.report["images"]["ts"],
                       "-R", f"{os.getuid()}:{os.getgid()}", "/fixture", timeout=60)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for key in ("ts-image", "go-image", "go-revision"):
        parser.add_argument("--" + key, required=True)
    parser.add_argument("--external-snow-package", required=True, type=Path)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    report = {"status": "failed", "ts_revision": TS_REVISION, "go_revision": args.go_revision, "checks": []}
    drill = BindDrill(args, report)
    try:
        drill.run()
        report["status"] = "passed"
        for check in report["checks"]:
            print(check)
    except (OSError, RuntimeError, ValueError, TypeError, KeyError, subprocess.SubprocessError):
        report["failure_phase"] = drill.phase
        print(f"Bind Compose drill failed in phase: {drill.phase}")
    finally:
        with args.evidence.open("x") as stream:
            stream.write(json.dumps(report, indent=2) + "\n")
    return report["status"] != "passed"


if __name__ == "__main__":
    raise SystemExit(main())
