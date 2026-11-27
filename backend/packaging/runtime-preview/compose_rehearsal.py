#!/usr/bin/env python3
"""Exercise real Compose stop and reopen on one disposable preview project."""
import argparse
import ipaddress
import json
import os
import select
from pathlib import Path
import socket
import subprocess
from tempfile import TemporaryDirectory
import time
from uuid import uuid4

import acceptance
from acceptance import docker, read_continue, request, wait_for

CONFIG = Path(__file__).with_name("compose-rehearsal.yaml")
SNOW_ENTRY = "snowluma-http.v2.json"


def standalone_override(package):
    return {"services": {"humanengine": {
        "environment": {"HUMANENGINE_PLUGINS": SNOW_ENTRY},
        "volumes": [{"type": "bind", "source": str(package), "target": "/app/plugins",
                     "read_only": True, "bind": {"create_host_path": False}}],
    }}}


def assert_standalone_mount(inspect, package):
    mount, = (mount for mount in inspect["Mounts"] if mount["Destination"] == "/app/plugins")
    assert mount["Type"] == "bind" and mount["Source"] == str(package) and mount["RW"] is False


def assert_standalone_catalog(rows, version, enabled):
    row, = (row for row in rows if row["id"] == "snowluma")
    assert row["builtin"] is False and row["version"] == version and row["enabled"] is enabled


def assert_envelope(inspect):
    host = inspect["HostConfig"]
    tmp = set(host["Tmpfs"]["/tmp"].split(","))
    assert host["Init"] and host["ReadonlyRootfs"] and host["CapDrop"] == ["ALL"]
    assert {"rw", "noexec", "nosuid"} <= tmp and tmp & {"size=64m", "size=67108864"}
    assert any(option in host["SecurityOpt"] for option in
               ("no-new-privileges:true", "no-new-privileges=true"))
    assert inspect["Config"]["StopTimeout"] == 30
    assert inspect["Config"]["StopSignal"] == "SIGTERM"
    ports = inspect["NetworkSettings"]["Ports"]["3000/tcp"]
    assert len(ports) == 1 and ports[0]["HostIp"] == "127.0.0.1"
    mounts = {m["Destination"]: m["Name"] for m in inspect["Mounts"]
              if m["Type"] == "volume" and m["RW"]}
    assert mounts.keys() == {"/app/data", "/app/authority"}
    assert mounts["/app/data"] != mounts["/app/authority"]
    return int(ports[0]["HostPort"]), mounts


def bridge_gateway(project, network):
    assert network["Driver"] == "bridge"
    assert network["Labels"]["com.docker.compose.project"] == project
    addresses = [ipaddress.ip_address(row["Gateway"]) for row in network["IPAM"]["Config"]
                 if row.get("Gateway")]
    gateway, = (address for address in addresses if address.version == 4)
    assert gateway.is_private and not gateway.is_loopback
    return str(gateway)


def run_snow_fixture(binary, gateway, token, start, stop_overlap, stop):
    env = dict(os.environ, HE_COMPOSE_FIXTURE_BIND=gateway, HUMANENGINE_ADMIN_TOKEN=token)
    with subprocess.Popen([binary, "-test.run=^TestComposeSnowLumaPartialACK$", "-test.timeout=5m"],
                          env=env, stdin=subprocess.PIPE, stdout=subprocess.PIPE,
                          stderr=subprocess.DEVNULL, bufsize=0) as peer:
        try:
            def receive(label, allowed):
                readable, _, _ = select.select([peer.stdout], [], [], 70)
                assert readable, f"Snow fixture did not reach {label}"
                line = peer.stdout.readline()
                phase = json.loads(line) if line.startswith(b'"') else None
                assert phase in allowed, f"unexpected Snow fixture phase, expected {label}"
                return phase

            def exchange(phase, action):
                receive(phase, (phase,))
                peer.stdin.write(json.dumps(action()).encode() + b"\n")

            exchange("start", start)
            exchange("stop-overlap", stop_overlap)
            outcome = receive("admitted history outcome",
                              ("remote-history-stop-503", "remote-history-stop-local-fallback"))
            exchange("start", start)
            exchange("stop", stop)
            stdout, _ = peer.communicate(timeout=30)
            assert peer.returncode == 0 and stdout.strip() == b"PASS", "Snow fixture failed final assertions"
            return outcome
        finally:
            if peer.poll() is None:
                peer.kill()


def stop_with_admitted_setting(port, command, env, logs, running):
    settings = request(port, "GET", "/api/settings")
    settings["debugModelIO"] = not settings["debugModelIO"]
    body = json.dumps(settings).encode()
    with socket.create_connection(("127.0.0.1", port), timeout=30) as sock:
        sock.sendall((f"PUT /api/settings HTTP/1.1\r\nHost: localhost\r\n"
                      f"Authorization: Bearer {acceptance.TOKEN}\r\nContent-Type: application/json\r\n"
                      f"Content-Length: {len(body)}\r\nExpect: 100-continue\r\n"
                      "Connection: close\r\n\r\n").encode())
        with sock.makefile("rb") as reader:
            read_continue(reader)
            with subprocess.Popen(command, env=env, stdout=subprocess.DEVNULL,
                                  stderr=subprocess.DEVNULL) as stopping:
                began = time.monotonic()
                wait_for(lambda: any('"code":"app.shutdown_wait"' in line and '"stage":"http"' in line
                                     for line in logs().splitlines()),
                         "Compose stop reached HTTP drain", seconds=20)
                assert stopping.poll() is None and running()
                sock.sendall(body)
                assert reader.readline().startswith(b"HTTP/1.1 200 "), "admitted PUT did not commit"
                reader.read()
                assert stopping.wait(timeout=max(0, 30 - (time.monotonic() - began))) == 0
                assert time.monotonic() - began <= 30, "Compose exceeded its shutdown grace"
    return settings["debugModelIO"]


def run(report, snow_fixture=None, package=None, override=None):
    version = json.loads((package / SNOW_ENTRY).read_text())["version"] if package else None
    tag = "he-compose-" + uuid4().hex
    project = "he-rehearsal-" + uuid4().hex
    acceptance.TOKEN = "synthetic-" + uuid4().hex
    image_id = report["image"]
    # A unique tag names this exact already-built local image for Compose.
    report["phase"] = "compose-image-tag"
    docker("image", "tag", image_id, tag)
    env = dict(os.environ, HE_COMPOSE_IMAGE=tag, HE_COMPOSE_PORT="0",
               HUMANENGINE_ADMIN_TOKEN=acceptance.TOKEN)
    compose = ["docker", "compose", "--env-file", "/dev/null", "--project-name", project,
               "--file", str(CONFIG)]
    if package is not None:
        compose += ["--file", str(override)]

    def call(*args):
        return subprocess.check_output([*compose, *args], env=env, text=True,
                                       stderr=subprocess.PIPE, timeout=90).strip()

    def inspect(container):
        return json.loads(docker("inspect", container))[0]

    def boot(stage, enabled=False):
        report["phase"] = f"{stage}-up"
        call("up", "--detach", "--no-build", "--pull", "never")
        report["phase"] = f"{stage}-container-image"
        cid = call("ps", "-q", "humanengine")
        assert cid
        container = inspect(cid)
        assert container["Image"] == image_id
        if package is not None:
            assert_standalone_mount(container, package)
        report["phase"] = f"{stage}-readiness"
        def ready():
            assert inspect(cid)["State"]["Running"], "Compose runtime stopped before readiness"
            return '"code":"server.listening"' in docker("logs", cid)
        wait_for(ready, "Compose runtime ready")
        report["phase"] = f"{stage}-envelope"
        port, mounts = assert_envelope(inspect(cid))
        if version is not None:
            report["phase"] = f"{stage}-standalone-catalog"
            assert_standalone_catalog(request(port, "GET", "/api/plugins"), version, enabled)
        return cid, port, mounts

    def assert_drained(cid):
        state = inspect(cid)["State"]
        assert not state["Running"] and state["ExitCode"] == 0 and not state["OOMKilled"]
        assert '"code":"app.drained"' in docker("logs", cid)

    try:
        report["phase"] = "compose-config"
        call("config", "--quiet")
        first, port, mounts = boot("compose-start")
        report["checks"].append("installed-compose-envelope")
        report["phase"] = "admit-write-and-compose-stop"
        stop_command = [*compose, "stop", "humanengine"]
        expected = stop_with_admitted_setting(port, stop_command, env, lambda: docker("logs", first),
                                              lambda: inspect(first)["State"]["Running"])
        assert_drained(first)
        report["checks"].append("compose-stop-admitted-write-committed-exit-zero-within-grace")
        report["phase"] = "cold-reopen"
        call("rm", "--force", "humanengine")
        second, port, reopened = boot("cold-reopen")
        report["phase"] = "cold-reopen-persistence"
        assert second != first and mounts == reopened
        assert request(port, "GET", "/api/settings")["debugModelIO"] is expected
        docker("exec", second, "sh", "-ec",
               "test -f /app/data/humanengine.sqlite && test ! -e /app/data/main.sqlite")
        report["checks"].append("fresh-container-reopened-canonical-data-and-authority")
        if snow_fixture is None:
            call("stop", "humanengine")
            assert_drained(second)
        else:
            report["phase"] = "snow-partial-ack-fixture-network"
            network = json.loads(docker("network", "inspect", project + "_default"))[0]
            gateway = bridge_gateway(project, network)
            current = current_port = overlap_setting = None

            def start_snow():
                nonlocal current, current_port
                if current is None:
                    current, current_port = second, port
                else:
                    call("rm", "--force", "humanengine")
                    previous = current
                    current, current_port, reopened = boot("snow-cold-reopen", enabled=True)
                    assert current != previous and reopened == mounts
                    assert request(current_port, "GET", "/api/settings")["debugModelIO"] is overlap_setting
                report["phase"] = "snow-partial-ack-wire-and-history"
                return f"127.0.0.1:{current_port}"

            def stop_overlap():
                nonlocal overlap_setting
                report["phase"] = "snow-compose-overlap"
                if version is not None:
                    assert_standalone_catalog(request(current_port, "GET", "/api/plugins"), version, True)
                overlap_setting = stop_with_admitted_setting(
                    current_port, stop_command, env, lambda: docker("logs", current),
                    lambda: inspect(current)["State"]["Running"])
                assert_drained(current)
                return docker("logs", current)

            def stop_snow():
                report["phase"] = "snow-compose-stop"
                began = time.monotonic()
                call("stop", "humanengine")
                assert time.monotonic() - began <= 30, "Compose exceeded its shutdown grace"
                assert_drained(current)
                report["phase"] = "snow-post-stop-peer-and-trace"
                return docker("logs", current)

            report["admitted_remote_history_at_stop"] = run_snow_fixture(
                snow_fixture, gateway, acceptance.TOKEN, start_snow, stop_overlap, stop_snow)
            report["checks"].extend(("compose-snow-partial-ack-drained-cold-reopen-no-replay",
                                     "compose-overlap-held-snow-history-and-admitted-setting-drained",
                                     "compose-overlap-native-model-peer-closed-without-replay",
                                     "compose-overlap-contextual-sticker-cancelled-fresh-sticker-learned"))
            if version is not None:
                report["external_snow_version"] = version
                report["checks"].append("compose-separate-read-only-snowluma-package-on-cold-reopen")
    finally:
        try:
            call("down", "--volumes")
        finally:
            docker("image", "rm", tag)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="humanengine-go-preview:local")
    parser.add_argument("--evidence", type=Path, required=True)
    parser.add_argument("--snow-fixture", type=Path)
    parser.add_argument("--external-snow-package", type=Path)
    args = parser.parse_args()
    if args.external_snow_package and not args.snow_fixture:
        parser.error("--external-snow-package requires --snow-fixture")
    report = {"status": "failed", "phase": "resolve-image", "checks": []}
    try:
        image = json.loads(docker("image", "inspect", args.image))[0]
        report.update(image=image["Id"], revision=image["Config"]["Labels"]["org.opencontainers.image.revision"])
        if args.external_snow_package:
            report["phase"] = "resolve-external-package"
            package = args.external_snow_package.resolve(strict=True)
            with TemporaryDirectory(prefix="he-compose-config-") as folder:
                override = Path(folder) / "external.json"
                override.write_text(json.dumps(standalone_override(package)))
                run(report, args.snow_fixture, package, override)
        else:
            run(report, args.snow_fixture)
        report["status"] = "passed"
        report.pop("phase")
    except (AssertionError, OSError, KeyError, ValueError, subprocess.SubprocessError):
        print("Compose rehearsal failed in phase: " + report["phase"])
    with args.evidence.open("x") as evidence:
        evidence.write(json.dumps(report, indent=2) + "\n")
    return report["status"] != "passed"


if __name__ == "__main__":
    raise SystemExit(main())
