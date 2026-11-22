#!/usr/bin/env python3
"""Real Docker/TCP package acceptance. Uses only synthetic disposable state.

No fake HTTP server and no skip-on-missing-Docker. Timeouts are test deadlines,
not new production shutdown/admission policies. Does not build or publish images.
"""
import argparse
import http.client
import json
import os
from datetime import datetime, timedelta, timezone
from http.cookies import SimpleCookie
import re
from pathlib import Path
import socket
import subprocess
import time
import uuid

TOKEN = "synthetic-package-token"


def docker(*args, env=None):
    return subprocess.check_output(["docker", *args], text=True, env=env).strip()



def node_pids(name):
    # Execute the non-Node observer as the image's ordinary user in its PID
    # namespace; output contains only PIDs, never argv, environment or tokens.
    script = Path(__file__).with_name("node-pids.sh").read_text()
    output = docker("exec", "--user", "node", name, "sh", "-c", script)
    rows = output.splitlines()
    assert all(row.isdecimal() for row in rows), "invalid executable identity PID"
    return sorted(int(row) for row in rows)


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


def frontend_session(port):
    origin = f"http://127.0.0.1:{port}"
    browser = {"Origin": origin, "X-HumanEngine-Session": "1", "Sec-Fetch-Site": "same-origin"}

    def visit(method, path, expected, headers=None, body=None):
        connection = http.client.HTTPConnection("127.0.0.1", port, timeout=30)
        try:
            request_headers = dict(headers or {})
            if body is not None:
                request_headers["Content-Type"] = "application/json"
            connection.request(method, path, None if body is None else json.dumps(body), request_headers)
            response = connection.getresponse()
            data = response.read()
            # Never include credentials or response headers in assertion output.
            assert response.status == expected, (method, path, response.status)
            return dict(response.getheaders()), data
        finally:
            connection.close()

    def api(method, path, expected, headers=browser, body=None):
        response, data = visit(method, path, expected, headers, body)
        assert response["Content-Type"].startswith("application/json"), "API returned a static fallback"
        return response, json.loads(data)

    nav = {"Accept": "text/html,application/xhtml+xml"}
    headers, index = visit("GET", "/", 200, nav)
    assert headers["Content-Type"].startswith("text/html") and "no-cache" in headers["Cache-Control"]
    assert b"<title>HumanEngine" in index and TOKEN.encode() not in index
    asset = re.search(rb'<script[^>]+src="(/_nuxt/[^"<>]+\.js)"', index)
    assert asset, "Nuxt boot script missing"
    headers, script = visit("GET", asset.group(1).decode(), 200)
    assert "javascript" in headers["Content-Type"] and "immutable" in headers["Cache-Control"]
    assert script and TOKEN.encode() not in script
    _, chat = visit("GET", "/chat", 200, nav)
    assert b"<title>HumanEngine" in chat
    visit("GET", "/_nuxt/missing-asset", 404, nav)
    api("GET", "/api/status", 401, nav)
    api("GET", "/api", 401, nav)
    api("POST", "/api/auth/session", 403, {**browser, "Origin": "http://other.invalid"}, {"token": TOKEN})
    headers, _ = api("POST", "/api/auth/session", 200, body={"token": TOKEN})
    cookie = SimpleCookie(headers["Set-Cookie"])["humanengine-session"]
    assert cookie["httponly"] and cookie["samesite"].lower() == "strict" and cookie["path"] == "/api"
    authenticated = {**browser, "Cookie": f"humanengine-session={cookie.value}"}
    api("GET", "/api/auth/session", 200, authenticated)
    for path in ("/api/status", "/api/scenes", "/api/logs", "/api/models", "/api/usage"):
        api("GET", path, 200, authenticated)
    api("GET", "/api/preview-not-implemented", 503, {**nav, **authenticated})
    _, settings = api("GET", "/api/settings", 200, authenticated)
    modified = dict(settings, debugModelIO=not settings["debugModelIO"])
    api("PUT", "/api/settings", 200, authenticated, modified)
    assert api("GET", "/api/settings", 200, authenticated)[1]["debugModelIO"] == modified["debugModelIO"]
    api("PUT", "/api/settings", 200, authenticated, settings)
    api("DELETE", "/api/auth/session", 200, authenticated)
    api("GET", "/api/status", 401, authenticated)



def read_continue(reader):
    assert reader.readline().startswith(b"HTTP/1.1 100 "), "missing 100 Continue"
    while True:
        header = reader.readline()
        assert header, "EOF during 100 Continue headers"
        if header == b"\r\n":
            return


def seed_browser_usage(image, data_volume):
    # Yesterday's UTC date keeps the fixture inside retention even on slow
    # or retried runners; 16:00 UTC is the next day's midnight in Taipei.
    start = datetime.now(timezone.utc).replace(hour=16, minute=0, second=0, microsecond=0) - timedelta(days=1)
    seed = Path(__file__).with_name("browser_usage_seed.mjs").read_text()
    docker("run", "--rm", "--network", "none", "--entrypoint", "node",
           "--mount", f"type=volume,source={data_volume},target=/app/data",
           image, "--input-type=module", "-e", seed, "/app/data/main.sqlite",
           start.isoformat(timespec="milliseconds"))
    return start


def main():
    global TOKEN
    parser = argparse.ArgumentParser()
    parser.add_argument("--image", default="humanengine-go-preview:local")
    parser.add_argument("--evidence", type=Path, required=True)
    parser.add_argument("--browser-smoke", action="store_true", help="run real host Chromium against this package")
    args = parser.parse_args()
    if args.browser_smoke:
        TOKEN = "synthetic-package-" + uuid.uuid4().hex
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
                   "-p", "127.0.0.1::3001", "-e", "HUMANENGINE_ADMIN_TOKEN",
                   "-v", data + ":/app/data", "-v", authority + ":/app/authority"]
        if plugin:
            fixture = Path(__file__).with_name("fixture.mjs").resolve()
            for plugin_id in ("package-fixture-a", "package-fixture-b"):
                command += ["--mount", f"type=bind,source={fixture},target=/app/plugins/{plugin_id}.mjs,readonly"]
            command += ["-e", "HUMANENGINE_PLUGINS=package-fixture-a.mjs,package-fixture-b.mjs"]
        docker(*command, args.image, env={**os.environ, "HUMANENGINE_ADMIN_TOKEN": TOKEN})
        return name

    def ready(name):
        def check():
            if not running(name):
                raise AssertionError("preview startup failed" if args.browser_smoke else "startup failed: " + logs(name))
            return '"code":"server.listening"' in logs(name)
        wait_for(check, name + " ready")
        return int(docker("port", name, "3001/tcp").rsplit(":", 1)[1])

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
        assert not node_pids(first), "zero V1 plugins spawned Node"
        report["checks"].append("zero-v1-no-node")
        request(port, "GET", "/api/settings", authenticated=False, expected=401)
        settings = request(port, "GET", "/api/settings")
        settings["debugModelIO"] = True
        request(port, "PUT", "/api/settings", settings)
        assert request(port, "GET", "/api/settings")["debugModelIO"] is True
        report["checks"].append("authenticated-write-read")
        frontend_session(port)
        assert not node_pids(first), "frontend spawned a Node runtime"
        report["checks"].append("same-origin-static-frontend-and-go-session-http")
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
        usage_start = seed_browser_usage(image["Id"], volumes[0]) if args.browser_smoke else None
        restarted = start("restart", *volumes, plugin=True)
        port = ready(restarted)
        assert request(port, "GET", "/api/settings")["debugModelIO"] is False
        wait_for(lambda: not node_pids(restarted), "disabled V1 startup inspection settled")
        report["checks"].append("restart-persistence")
        if args.browser_smoke:
            import browser_chat_fixture
            import browser_smoke
            report["browser"] = {"status": "failed", "failed_phase": "dense-chat-fixture"}
            with browser_chat_fixture.peer(restarted, image["Id"], port) as chat_history:
                report["browser"].pop("failed_phase")
                browser_smoke.run(port, TOKEN, report["browser"], usage_start, chat_history)
            assert not node_pids(restarted), "browser caused a Node runtime inside the image"
            report["checks"].append("hosted-chromium-real-ui")
        a, b = "package-fixture-a", "package-fixture-b"

        def echo(plugin_id):
            assert request(port, "POST", f"/api/plugins/{plugin_id}/actions/echo",
                           {"proof": "source-free"}) == {
                               "input": {"proof": "source-free"},
                               "packaged": True, "pluginId": plugin_id}

        request(port, "PUT", f"/api/plugins/{a}/enabled", {"enabled": True})
        first_host = wait_for(lambda: node_pids(restarted), "first enabled V1 Node host")
        assert len(first_host) == 1, "first V1 must own exactly one shared host"
        echo(a)
        assert node_pids(restarted) == first_host
        request(port, "PUT", f"/api/plugins/{b}/enabled", {"enabled": True})
        echo(b)
        assert node_pids(restarted) == first_host, "second V1 must share the original host"
        echo(a)
        request(port, "PUT", f"/api/plugins/{a}/enabled", {"enabled": False})
        request(port, "POST", f"/api/plugins/{a}/actions/echo", {}, expected=503)
        echo(b)
        assert node_pids(restarted) == first_host, "remaining V1 lost or replaced its host"
        request(port, "PUT", f"/api/plugins/{b}/enabled", {"enabled": False})
        wait_for(lambda: not node_pids(restarted), "last V1 physically unloaded")
        report["checks"].append("packaged-two-v1-shared-pid-and-physical-unload")
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
                if not args.browser_smoke:
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
