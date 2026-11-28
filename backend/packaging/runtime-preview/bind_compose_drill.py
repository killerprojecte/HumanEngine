#!/usr/bin/env python3
"""Disposable TS → Go → TS rehearsal using a data bind, named authority and read-only plugins."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

from migration_drill import AUTHORITY, DATABASE, TS_REVISION, Drill, docker, require
from operator_packages import PACKAGES, load_packages


HERE = Path(__file__).resolve().parent
TARGET = "/app/data"
LEGACY_ACTION = "HE_FIXTURE_SNOW_LEGACY_ACTION"
LEGACY_EVENT = "HE_FIXTURE_SNOW_LEGACY_EVENT"


def fixture_service(image, data, plugins, port):
    # Copy the operator's resource layout, but confine the fixture network and
    # published host port. The candidate overlay does not own these resources.
    return {
        "services": {"humanengine": {
            "image": image, "platform": "linux/amd64", "init": True, "restart": "unless-stopped",
            "ports": [{"target": 3000, "published": port, "host_ip": "127.0.0.1"}],
            "environment": {
                "HUMANENGINE_ADMIN_TOKEN": "${HUMANENGINE_ADMIN_TOKEN:?Set a synthetic token}",
                "HUMANENGINE_DB": DATABASE, "PORT": "3000",
                "HUMANENGINE_LOG_DIR": "/app/data/logs",
                "HUMANENGINE_RECOVERY_AUTHORITY_DIRECTORY": AUTHORITY,
                "HUMANENGINE_PLUGINS": "",
                LEGACY_ACTION: None,
                LEGACY_EVENT: None,
            },
            "volumes": [
                {"type": "bind", "source": str(data), "target": TARGET, "bind": {"create_host_path": False}},
                {"type": "volume", "source": "humanengine-authority", "target": AUTHORITY},
                {"type": "bind", "source": str(plugins), "target": "/app/plugins", "read_only": True,
                 "bind": {"create_host_path": False}},
            ],
            "read_only": True, "tmpfs": ["/tmp:rw,noexec,nosuid,size=64m"], "cap_drop": ["ALL"],
            "security_opt": ["no-new-privileges:true"], "stop_grace_period": "30s",
        }},
        "volumes": {"humanengine-authority": {}},
        "networks": {"default": {"internal": True}},
    }


class BindDrill(Drill):
    def compose(self, *args, check=True, timeout=90):
        files = ("-f", str(self.baseline))
        if self.candidate:
            files += ("-f", str(HERE / "compose-go-candidate.yaml"))
        return docker("compose", "--env-file", "/dev/null", "-p", self.project, *files,
                      *args, check=check, timeout=timeout, env=self.env)

    def start(self, engine, installed=True):
        require(self.owner is None, "overlapping Compose writers")
        self.candidate = engine == "go"
        image = self.args.go_image if self.candidate else self.args.ts_image
        self.env.update(HE_COMPOSE_IMAGE=self.args.go_image,
                        HE_COMPOSE_PLUGIN_ENTRIES=",".join(PACKAGES.values()) if installed else "")
        self.phase = f"{self.leg}-compose-config"
        resolved = json.loads(self.compose("config", "--format", "json"))["services"]["humanengine"]
        volumes = {v["target"]: v for v in resolved["volumes"]}
        present = tuple(bool(resolved["environment"].get(key)) for key in (LEGACY_ACTION, LEGACY_EVENT))
        legacy = self.leg == "ts-seed"
        require(present == (legacy, legacy), "unexpected legacy fixture environment")
        require(resolved["image"] == image and resolved["read_only"] is True and
                volumes[TARGET]["type"] == "bind" and volumes[TARGET]["source"] == str(self.data) and
                volumes[AUTHORITY]["type"] == "volume" and
                volumes["/app/plugins"]["read_only"] is True and
                volumes["/app/plugins"]["source"] == str(self.package),
                "Compose did not resolve the expected protected mounts")
        shape = tuple(resolved[key] for key in ("ports", "volumes", "read_only", "security_opt", "cap_drop", "restart"))
        if self.resource_shape is None:
            self.resource_shape = shape
        require(shape == self.resource_shape, "candidate changed the original Compose resources")
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
        require(self.authority == authority == self.project + "_humanengine-authority",
                "Compose replaced the enrolled authority")
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
        self.env[LEGACY_ACTION] = "legacy-" + self.snow_token
        self.env[LEGACY_EVENT] = "legacy-" + self.snow_event_token
        self.start("ts")
        marker = self.seed_unicode_memory()
        initial = self.setting()
        self.seed_credential()
        self.prove_credential()
        self.phase = "ts-legacy-snowluma"
        self.configure_snowluma_http(credentialEnv=LEGACY_ACTION, eventCredentialEnv=LEGACY_EVENT)
        row = next(p for p in self.api("GET", "/api/plugins") if p["id"] == "snowluma")
        require(row["configuredSecrets"] == {"token": False, "eventToken": False},
                "legacy SnowLuma already has database credentials")
        self.exchange_snowluma_login(self.env[LEGACY_ACTION])
        self.seed_snowluma_credential()
        self.prove_snowluma_login()
        self.record("ts-legacy-snowluma-env-to-database-and-reconnect")
        self.stop()
        del self.env[LEGACY_ACTION], self.env[LEGACY_EVENT]
        self.leg = "go-bare"
        self.start("go", installed=False)
        require(self.api("GET", "/api/plugins") == [], "bare candidate catalog not empty")
        self.stop()
        self.leg = "go-reopen"
        self.start("go")
        self.verify_unicode_memory(marker, self.leg)
        self.prove_credential()
        self.phase = "independent-catalog"
        rows = self.api("GET", "/api/plugins")
        require(sorted(p["id"] for p in rows) == sorted(PACKAGES) and
                all(p["builtin"] is False and p["apiVersion"] == 2 and p["enabled"] is False for p in rows),
                "expected exactly one inactive external package per plugin ID")
        self.phase = "loopback-snowluma-exchange"
        media = self.prove_snowluma_login(seed_media=True)
        self.verify_media(media)
        self.record("operator-bind-four-external-packages-and-active-synthetic-snowluma")
        require(self.setting(not initial) is not initial, "Go settings write did not persist")
        self.record("go-bind-identity-external-plugin-and-encrypted-key")
        self.stop()
        self.leg = "ts-rollback"
        self.start("ts")
        self.verify_unicode_memory(marker, self.leg)
        self.prove_credential()
        self.prove_snowluma_login()
        require(self.setting() is not initial, "TypeScript rollback did not read the latest Go state")
        self.stop()
        self.record("bind-compose-ts-go-ts-no-parallel-main-sqlite")

    def run(self):
        self.report["images"] = {
            "ts": self.image(self.args.ts_image, TS_REVISION),
            "go": self.image(self.args.go_image, self.args.go_revision),
        }
        self.phase = "prepare-synthetic-bind"
        self.package = self.args.external_package_dir.resolve(strict=True)
        self.report["external_packages"] = load_packages(self.package)
        require(json.loads(docker("run", "--rm", "--network", "none", self.report["images"]["go"],
                                  "--inspect-builtins")) == [], "Go candidate embeds plugins")
        self.project = self.name + "-bind"
        self.authority = self.resource_shape = None
        self.candidate = False
        # Hosted, serialized test. An occupied port fails rather than falling back.
        self.published = "31079"
        with tempfile.TemporaryDirectory(prefix="he-bind-") as tmp:
            work = Path(tmp).resolve()
            self.baseline = work / "fixture.yaml"
            self.data = work / "data"
            self.data.mkdir()
            # The fixture container is uid 1000; this directory has synthetic data only.
            self.data.chmod(0o777)
            self.baseline.write_text(json.dumps(fixture_service(self.args.ts_image, self.data, self.package,
                                                                self.published)))
            try:
                self.rehearse()
            finally:
                if self.baseline.is_file():
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
    parser.add_argument("--external-package-dir", required=True, type=Path)
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
