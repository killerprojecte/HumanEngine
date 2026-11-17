#!/usr/bin/env python3
"""Real Docker/TCP package acceptance. Uses only synthetic disposable state.

No fake HTTP server and no skip-on-missing-Docker. Timeouts are test deadlines,
not new production shutdown/admission policies. Does not build or publish images.
"""
import argparse
import http.client
import json
from pathlib import Path
import socket
import subprocess
import time
import uuid

TOKEN = "synthetic-package-token"


def docker(*args):
    return subprocess.check_output(["docker", *args], text=True).strip()


def wait_for(check, label, seconds=90):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = check()
        if value:
            return value
        time.sleep(0.1)
    raise AssertionError("timed out: " + label)


def request(port, method, path, body=None, authenticated=True, expected=200):
    connection = http.client.HTTPConnection("127.0.0.1", port, timeout=30)
    headers = {"Content-Type": "application/json"}
    if authenticated:
        headers["Authorization"] = "Bearer " + TOKEN
    try:
        connection.request(method, path, None if body is None else json.dumps(body), headers)
        response = connection.getresponse()
        data = response.read()
        assert response.status == expected, (method, path, response.status, data)
        return json.loads(data) if data else None
    finally:
        connection.close()



def read_continue(reader):
    assert reader.readline().startswith(b"HTTP/1.1 100 "), "missing 100 Continue"
    while True:
        header = reader.readline()
        assert header, "EOF during 100 Continue headers"
        if header == b"\r\n":
            return


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="humanengine-go-preview:local")
    parser.add_argument("--evidence", type=Path, required=True)
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    docker("info")
    prefix = "he-package-" + uuid.uuid4().hex[:12]
    containers, volumes = [], []
    image = json.loads(docker("image", "inspect", args.image))[0]
    report = {"image": image["Id"],
              "revision": (image["Config"].get("Labels") or {}).get("org.opencontainers.image.revision"),
              "checks": []}

    def logs(name):
        return docker("logs", name)

    def running(name):
        return docker("inspect", "-f", "{{.State.Running}}", name) == "true"

    def exited(name):
        if running(name):
            return False
        return docker("inspect", "-f", "{{.State.ExitCode}}", name) == "0"

    def start(label, data, authority, plugin=False):
        name = prefix + "-" + label
        containers.append(name)
        command = ["run", "-d", "--name", name, "--platform", "linux/amd64",
                   "-p", "127.0.0.1::3001", "-e", "HUMANENGINE_ADMIN_TOKEN=" + TOKEN,
                   "-v", data + ":/app/data", "-v", authority + ":/app/authority"]
        if plugin:
            fixture = Path(__file__).with_name("fixture.mjs").resolve()
            command += ["--mount", f"type=bind,source={fixture},target=/app/plugins/fixture.mjs,readonly",
                        "-e", "HUMANENGINE_PLUGINS=fixture.mjs"]
        docker(*command, args.image)
        return name

    def ready(name):
        def check():
            if not running(name):
                raise AssertionError("startup failed: " + logs(name))
            return '"code":"server.listening"' in logs(name)
        wait_for(check, name + " ready")
        return int(docker("port", name, "3001/tcp").rsplit(":", 1)[1])

    def no_node(name):
        # docker top itself does not create a process inside the container.
        rows = docker("top", name, "-eo", "comm").splitlines()[1:]
        return not any(row.strip() in ("node", "nodejs") for row in rows)

    def drain(name):
        docker("kill", "--signal=TERM", name)
        wait_for(lambda: exited(name), "physical exit " + name)
        assert '"code":"app.drained"' in logs(name)

    try:
        for suffix in ("data", "authority"):
            volume = prefix + "-" + suffix
            docker("volume", "create", volume)
            volumes.append(volume)
        first = start("first", *volumes)
        port = ready(first)
        assert no_node(first), "zero V1 plugins spawned Node"
        report["checks"].append("zero-v1-no-node")
        request(port, "GET", "/api/settings", authenticated=False, expected=401)
        settings = request(port, "GET", "/api/settings")
        settings["debugModelIO"] = True
        request(port, "PUT", "/api/settings", settings)
        assert request(port, "GET", "/api/settings")["debugModelIO"] is True
        report["checks"].append("authenticated-write-read")
        second = start("second", *volumes)
        wait_for(lambda: not running(second), "same installation second writer rejected")
        assert docker("inspect", "-f", "{{.State.ExitCode}}", second) != "0"
        assert '"code":"server.listening"' not in logs(second)
        assert request(port, "GET", "/api/settings")["debugModelIO"] is True
        report["checks"].append("duplicate-writer-different-published-port")

        # 100 Continue proves entry into the actual authenticated body reader.
        settings["debugModelIO"] = False
        body = json.dumps(settings).encode()
        with socket.create_connection(("127.0.0.1", port), timeout=30) as sock:
            sock.sendall((f"PUT /api/settings HTTP/1.1\r\nHost: localhost\r\n"
                          f"Authorization: Bearer {TOKEN}\r\nContent-Type: application/json\r\n"
                          f"Content-Length: {len(body)}\r\nExpect: 100-continue\r\n"
                          "Connection: close\r\n\r\n").encode())
            reader = sock.makefile("rb")
            read_continue(reader)
            docker("kill", "--signal=TERM", first)
            wait_for(lambda: '"stage":"http"' in logs(first), "HTTP drain waiting")
            assert running(first) and '"code":"app.drained"' not in logs(first)
            sock.sendall(body)
            assert reader.readline().startswith(b"HTTP/1.1 200 ")
            reader.read()
        wait_for(lambda: exited(first), "held-request physical drain")
        assert '"code":"app.drained"' in logs(first)
        report["checks"].append("sigterm-held-http-commits-before-owner-drain")
        restarted = start("restart", *volumes, plugin=True)
        port = ready(restarted)
        assert request(port, "GET", "/api/settings")["debugModelIO"] is False
        assert no_node(restarted), "disabled V1 worker retained after startup inspection"
        report["checks"].append("restart-persistence")
        request(port, "PUT", "/api/plugins/package-fixture/enabled", {"enabled": True})
        wait_for(lambda: not no_node(restarted), "enabled V1 Node host")
        assert request(port, "POST", "/api/plugins/package-fixture/actions/echo",
                       {"proof": "source-free"}) == {"input": {"proof": "source-free"}, "packaged": True}
        request(port, "PUT", "/api/plugins/package-fixture/enabled", {"enabled": False})
        wait_for(lambda: no_node(restarted), "last V1 physically unloaded")
        report["checks"].append("packaged-v1-echo-enable-disable")
        drain(restarted)
        report["status"] = "passed"
    except BaseException:
        report["status"] = "failed"
        raise
    finally:
        for name in containers:
            try:
                content = logs(name)
                assert TOKEN not in content, "token leaked to logs"
                (args.evidence / (name + ".log")).write_text(content)
            except Exception as error:
                report["status"] = "failed"
                report.setdefault("evidence_errors", []).append(type(error).__name__)
            finally:
                subprocess.run(["docker", "rm", "-f", name], check=False, capture_output=True)
        for volume in volumes:
            subprocess.run(["docker", "volume", "rm", volume], check=False, capture_output=True)
        (args.evidence / "result.json").write_text(json.dumps(report, indent=2) + "\n")
    assert report["status"] == "passed", report
    print(json.dumps(report, indent=2))


if __name__ == "__main__":
    main()
