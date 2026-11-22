"""Drive a disposable real SnowLuma loopback peer for hosted browser smoke."""
from base64 import urlsafe_b64encode
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import subprocess
import time
from urllib.parse import parse_qs, unquote, urlsplit
from uuid import uuid4

from acceptance import docker, request, wait_for

GROUP = 901
NAME = "Synthetic dense group"
LOCAL = "Synthetic retained message witness."
REMOTE = "Synthetic remote-only history witness."
SCENE = "ob:s:" + urlsafe_b64encode(
    json.dumps(["snowluma", "42", "group", str(GROUP)], separators=(",", ":")).encode()
).decode().rstrip("=")
HISTORY = f"/api/scenes/{SCENE}/history"
ACTION_PORT, EVENT_PORT = 41274, 41275


@contextmanager
def peer(name, image, port):
    sidecar = name + "-chat-peer"
    script = Path(__file__).with_name("browser_chat_fixture.cjs").read_text()
    params = dict(group=GROUP, name=NAME, local=LOCAL, remote=REMOTE,
                  actionPort=ACTION_PORT, eventPort=EVENT_PORT, timestamp=int(time.time()),
                  actionToken=uuid4().hex, eventToken=uuid4().hex)

    def probe(mode):
        return docker("exec", sidecar, "node", "-e", script, mode) == "204"

    try:
        docker("run", "-d", "--name", sidecar, "--network", "container:" + name,
               "--entrypoint", "node", "-e", "HE_BROWSER_CHAT", image,
               "-e", script, "peer", env={**os.environ, "HE_BROWSER_CHAT": json.dumps(params)})
        wait_for(lambda: ready(probe), "synthetic browser SnowLuma peer", seconds=20)
        request(port, "PUT", "/api/scenes/" + SCENE, {
            "id": SCENE, "kind": "group", "adapterId": "snowluma", "accountId": "42",
            "platformSceneId": str(GROUP), "name": NAME,
            "participationEnabled": False, "learningEnabled": False, "proactiveEnabled": False,
        })
        request(port, "PUT", "/api/plugins/snowluma/config", {
            "transport": "http", "url": f"http://127.0.0.1:{ACTION_PORT}", "accountId": "42",
            "eventHost": "127.0.0.1", "eventPort": EVENT_PORT,
            "token": params["actionToken"], "eventToken": params["eventToken"],
        })
        assert request(port, "PUT", "/api/plugins/snowluma/enabled", {"enabled": True})["ok"]
        assert probe("seed"), "signed browser fixture ingress failed"

        def retained():
            data = request(port, "GET", HISTORY + "?source=local")
            rows = data["items"]
            return len(rows) == 30 and LOCAL in json.dumps(rows[-1]) and REMOTE not in json.dumps(rows)

        wait_for(retained, "30 retained synthetic rows", seconds=10)
        yield probe
        assert request(port, "PUT", "/api/plugins/snowluma/enabled", {"enabled": False})["ok"]
    finally:
        subprocess.run(["docker", "rm", "-f", sidecar], capture_output=True, timeout=20)


def ready(probe):
    try:
        return probe("ready")
    except subprocess.CalledProcessError:
        return False


def assert_history(page, origin, expect, probe):
    """Observe real browser fetch completion and transcript, not sidebar mirrors."""
    def source(response):
        url = urlsplit(response.url)
        if not response.url.startswith(origin + "/") or unquote(url.path) != HISTORY or response.request.method != "GET":
            return None
        return parse_qs(url.query).get("source", ["remote"])[0]

    completed = []

    def on_response(response):
        if kind := source(response):
            completed.append((kind, response.status))

    page.on("response", on_response)
    try:
        assert page.goto(origin + "/chat").status == 200
        scene = page.get_by_role("button", name=re.compile(re.escape(NAME)))
        row = page.locator("[data-chat-message]")
        retained = row.get_by_text(LOCAL, exact=True)
        remote = row.get_by_text(REMOTE, exact=True)
        scene.click()
        expect(retained).to_be_in_viewport(timeout=6000)
        wait_for(lambda: probe("held"), "browser SnowLuma history physically held", seconds=4)
        assert ("local", 200) in completed
        assert not any(kind == "remote" for kind, _ in completed)
        expect(remote).to_have_count(0)
        with page.expect_response(lambda response: source(response) == "remote") as result:
            assert probe("release"), "peer failed to release held history"
        assert result.value.status == 200
        expect(remote).to_be_in_viewport()
        expect(retained).to_be_visible()
    finally:
        page.remove_listener("response", on_response)
