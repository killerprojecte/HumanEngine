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

from acceptance import docker, wait_for

GROUP = 901
SENDER = 7
AVATAR_URLS = frozenset({
    f"https://p.qlogo.cn/gh/{GROUP}/{GROUP}/100",
    f"https://q1.qlogo.cn/g?b=qq&nk={SENDER}&s=100",
})
NAME = "Synthetic dense group"
CARD = "Synthetic group card"
LOCAL = "Synthetic retained message witness."
REMOTE = "Synthetic remote-only history witness."
SCENE = "ob:s:" + urlsafe_b64encode(
    json.dumps(["snowluma", "42", "group", str(GROUP)], separators=(",", ":")).encode()
).decode().rstrip("=")
HISTORY = f"/api/scenes/{SCENE}/history"
STICKER_ROW = SCENE + ":m:" + urlsafe_b64encode(b'"200"').decode().rstrip("=")
STICKER_MEDIA = f"/api/scenes/{SCENE}/messages/{STICKER_ROW}/media/1"
# These addresses only exist on a disposable internal Docker network.
CDN_IP = "192.0.1.2"
CDN_URL = f"http://{CDN_IP}:41276/expired-sticker"
ACTION_PORT, EVENT_PORT = 41274, 41275


@contextmanager
def peer(name, image, port, request, standalone_version=None):
    sidecar = name + "-chat-peer"
    cdn = name + "-chat-cdn"
    network = name + "-chat-origin"
    script = Path(__file__).with_name("browser_chat_fixture.cjs").read_text()
    params = dict(group=GROUP, sender=SENDER, name=NAME, card=CARD, local=LOCAL, remote=REMOTE,
                  actionPort=ACTION_PORT, eventPort=EVENT_PORT, timestamp=int(time.time()),
                  actionToken=uuid4().hex, eventToken=uuid4().hex)
    if standalone_version is not None:
        params["imageURL"] = CDN_URL
    env = {**os.environ, "HE_BROWSER_CHAT": json.dumps(params)}

    def fixture(container, mode):
        return docker("exec", container, "node", "-e", script, mode) == "204"

    def probe(mode):
        return fixture(cdn if mode.startswith("cdn-") else sidecar, mode)

    try:
        if standalone_version is not None:
            # Keep the CDN test address isolated; never alter the hosted
            # browser/runner resolver or the product's public URL policy.
            docker("network", "create", "--internal", "--subnet", "192.0.1.0/29", network)
            docker("run", "-d", "--name", cdn, "--network", network, "--ip", CDN_IP,
                   "--entrypoint", "node", "-e", "HE_BROWSER_CHAT", image,
                   "-e", script, "cdn", env=env)
            # Reserve the CDN's explicit address before the runtime joins
            # with a dynamically allocated address on this same bridge.
            docker("network", "connect", network, name)
            wait_for(lambda: ready(probe, "cdn-ready"), "isolated synthetic CDN", seconds=20)
        docker("run", "-d", "--name", sidecar, "--network", "container:" + name,
               "--entrypoint", "node", "-e", "HE_BROWSER_CHAT", image,
               "-e", script, "peer", env=env)
        wait_for(lambda: ready(probe), "synthetic browser SnowLuma peer", seconds=20)
        configure(port, request, params, probe, discover_names=standalone_version is not None)
        if standalone_version is not None:
            row, = (row for row in request(port, "GET", "/api/plugins") if row["id"] == "snowluma")
            assert row["builtin"] is False and row["enabled"] is True and row["version"] == standalone_version
        yield probe
        assert request(port, "PUT", "/api/plugins/snowluma/enabled", {"enabled": False})["ok"]
    finally:
        subprocess.run(["docker", "rm", "-f", sidecar], capture_output=True, timeout=20)
        if standalone_version is not None:
            for command in (("rm", "-f", cdn), ("network", "disconnect", network, name),
                            ("network", "rm", network)):
                subprocess.run(["docker", *command], capture_output=True, timeout=20)


def configure(port, request, params, probe, discover_names=False):
    if discover_names:
        assert all(row["id"] != SCENE for row in request(port, "GET", "/api/scenes"))
    else:
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
    if discover_names:
        # All standard events acknowledged while the action peer is still holding the lookup.
        wait_for(lambda: ready(probe, "group-held"), "asynchronous group profile lookup", seconds=5)
        assert any(row["id"] == SCENE and row["name"] == str(GROUP) for row in request(port, "GET", "/api/scenes"))
        assert probe("group-release"), "group profile lookup did not release"
        wait_for(lambda: any(row["id"] == SCENE and row["name"] == NAME
                             for row in request(port, "GET", "/api/scenes")), "resolved group name", seconds=5)

    def retained():
        data = request(port, "GET", HISTORY + "?source=local")
        rows = data["items"]
        return (len(rows) == 30 and LOCAL in json.dumps(rows[-1]) and REMOTE not in json.dumps(rows)
                and (not discover_names or rows[-1].get("senderDisplayName") == CARD))

    wait_for(retained, "30 retained synthetic rows", seconds=10)


def ready(probe, mode="ready"):
    try:
        return probe(mode)
    except subprocess.CalledProcessError:
        return False


def assert_history(page, origin, expect, probe, with_image=False):
    """Observe real browser fetch completion and transcript, not sidebar mirrors."""
    def source(response):
        url = urlsplit(response.url)
        if not response.url.startswith(origin + "/") or unquote(url.path) != HISTORY or response.request.method != "GET":
            return None
        return parse_qs(url.query).get("source", ["remote"])[0]

    completed = []
    media = []

    def on_response(response):
        if kind := source(response):
            completed.append((kind, response.status))
        url = urlsplit(response.url)
        if (response.url.startswith(origin + "/") and unquote(url.path) == STICKER_MEDIA
                and response.request.method == "GET"):
            media.append(response)

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
        if with_image:
            expect(row.filter(has_text=LOCAL).get_by_role("button", name=CARD, exact=True)).to_be_visible()
            sticker = row.filter(has_text=REMOTE).locator('img[src^="blob:"]')
            expect(sticker).to_be_in_viewport()
            assert sticker.evaluate("async img => { await img.decode(); return [img.naturalWidth, img.naturalHeight] }") == [3, 2]
            response, = media
            assert response.status == 200 and response.header_value("content-type") == "image/png"
            assert response.request.header_value("x-humanengine-session") == "1"
            assert probe("media") and probe("cdn-media"), "expected one exact message, one file action and one expired origin request"
    finally:
        page.remove_listener("response", on_response)
