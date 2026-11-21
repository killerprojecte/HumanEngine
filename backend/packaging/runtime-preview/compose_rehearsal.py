#!/usr/bin/env python3
"""Exercise real Compose stop and reopen on one disposable preview project."""
import argparse
import json
import os
from pathlib import Path
import socket
import subprocess
import time
from uuid import uuid4

import acceptance
from acceptance import docker, read_continue, request, wait_for

CONFIG = Path(__file__).with_name("compose-rehearsal.yaml")


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


def run(report):
    tag = "he-compose-" + uuid4().hex
    project = "he-rehearsal-" + uuid4().hex
    acceptance.TOKEN = "synthetic-" + uuid4().hex
    image_id = report["image"]
    # A unique tag names this exact already-built local image for Compose.
    report["phase"] = "compose-image-tag"
    docker("image", "tag", image_id, tag)
    env = dict(os.environ, HE_COMPOSE_IMAGE=tag, HUMANENGINE_ADMIN_TOKEN=acceptance.TOKEN)
    compose = ["docker", "compose", "--env-file", "/dev/null", "--project-name", project,
               "--file", str(CONFIG)]

    def call(*args):
        return subprocess.check_output([*compose, *args], env=env, text=True,
                                       stderr=subprocess.PIPE, timeout=90).strip()

    def inspect(container):
        return json.loads(docker("inspect", container))[0]

    def boot(stage):
        report["phase"] = f"{stage}-up"
        call("up", "--detach", "--no-build", "--pull", "never")
        report["phase"] = f"{stage}-container-image"
        cid = call("ps", "-q", "humanengine")
        assert cid and inspect(cid)["Image"] == image_id
        report["phase"] = f"{stage}-readiness"
        def ready():
            assert inspect(cid)["State"]["Running"], "Compose runtime stopped before readiness"
            return '"code":"server.listening"' in docker("logs", cid)
        wait_for(ready, "Compose runtime ready")
        report["phase"] = f"{stage}-envelope"
        return cid, *assert_envelope(inspect(cid))

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
        settings = request(port, "GET", "/api/settings")
        settings["debugModelIO"] = not settings["debugModelIO"]
        expected = settings["debugModelIO"]
        body = json.dumps(settings).encode()
        with socket.create_connection(("127.0.0.1", port), timeout=30) as sock:
            sock.sendall((f"PUT /api/settings HTTP/1.1\r\nHost: localhost\r\n"
                          f"Authorization: Bearer {acceptance.TOKEN}\r\nContent-Type: application/json\r\n"
                          f"Content-Length: {len(body)}\r\nExpect: 100-continue\r\n"
                          "Connection: close\r\n\r\n").encode())
            with sock.makefile("rb") as reader:
                read_continue(reader)
                with subprocess.Popen([*compose, "stop", "humanengine"], env=env,
                                      stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL) as stopping:
                    began = time.monotonic()
                    wait_for(lambda: '"stage":"http"' in docker("logs", first),
                             "Compose stop reached HTTP drain", seconds=20)
                    assert stopping.poll() is None and inspect(first)["State"]["Running"]
                    sock.sendall(body)
                    assert reader.readline().startswith(b"HTTP/1.1 200 "), "admitted PUT did not commit"
                    reader.read()
                    assert stopping.wait(timeout=max(0, 30 - (time.monotonic() - began))) == 0
                    assert time.monotonic() - began <= 30, "Compose exceeded its shutdown grace"
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
        call("stop", "humanengine")
        assert_drained(second)
        report["checks"].append("fresh-container-reopened-canonical-data-and-authority")
    finally:
        try:
            call("down", "--volumes")
        finally:
            docker("image", "rm", tag)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="humanengine-go-preview:local")
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    report = {"status": "failed", "phase": "resolve-image", "checks": []}
    try:
        image = json.loads(docker("image", "inspect", args.image))[0]
        report.update(image=image["Id"], revision=image["Config"]["Labels"]["org.opencontainers.image.revision"])
        run(report)
        report["status"] = "passed"
        report.pop("phase")
    except (AssertionError, OSError, KeyError, ValueError, subprocess.SubprocessError):
        print("Compose rehearsal failed in phase: " + report["phase"])
    with args.evidence.open("x") as evidence:
        evidence.write(json.dumps(report, indent=2) + "\n")
    return report["status"] != "passed"


if __name__ == "__main__":
    raise SystemExit(main())
