import base64
import http.client
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
from concurrent.futures import ThreadPoolExecutor
from types import SimpleNamespace
import unittest
from unittest.mock import Mock, patch

from acceptance import wait_for
from browser_chat_fixture import AVATAR_URLS, CDN_IP, GROUP, LOCAL, NAME, REMOTE, SENDER, peer
from browser_smoke import browser_request_guard


class BrowserRequestGuardTests(unittest.TestCase):
    def test_fixed_cdn_address_is_reserved_before_dynamic_runtime_join(self):
        def request(port, method, path, body=None):
            if method == "GET":
                return [{"id": "snowluma", "builtin": False, "enabled": True, "version": "preview"}]
            return {"ok": True}
        with (patch("browser_chat_fixture.docker", return_value="204") as docker,
              patch("browser_chat_fixture.configure"),
              patch("browser_chat_fixture.subprocess.run")):
            with peer("fixture", "test-image", 3001, request, "preview"):
                pass
        commands = [call.args for call in docker.call_args_list]
        reserved = next(i for i, command in enumerate(commands)
                        if command[0] == "run" and "--ip" in command and CDN_IP in command)
        joined = next(i for i, command in enumerate(commands) if command[:2] == ("network", "connect"))
        self.assertLess(reserved, joined)

    def test_fixture_avatar_attempts_are_blocked_without_hiding_other_egress(self):
        origin = "http://127.0.0.1:3001"
        avatars = [f"https://p.qlogo.cn/gh/{GROUP}/{GROUP}/100",
                   f"https://q1.qlogo.cn/g?b=qq&nk={SENDER}&s=100"]
        blocked = {"fixture_avatars": 0, "unexpected_external": 0}
        guard = browser_request_guard(origin, AVATAR_URLS, blocked)

        def route(method, kind, url, same_origin=False):
            intercepted = Mock(request=SimpleNamespace(method=method, resource_type=kind, url=url))
            guard(intercepted)
            if same_origin:
                intercepted.continue_.assert_called_once_with()
                intercepted.abort.assert_not_called()
            else:
                intercepted.abort.assert_called_once_with()
                intercepted.continue_.assert_not_called()

        route("GET", "fetch", origin + "/api/status", same_origin=True)
        for avatar in avatars:
            route("GET", "image", avatar)
        route("GET", "fetch", avatars[0])
        route("POST", "image", avatars[0])
        route("GET", "image", avatars[0] + "?other=1")
        route("GET", "image", "https://q1.qlogo.cn/g?b=qq&nk=8&s=100")
        self.assertEqual(blocked, {"fixture_avatars": 2, "unexpected_external": 4})


@unittest.skipUnless(shutil.which("node"), "Node is required for the standalone action peer")
class ChatPeerTests(unittest.TestCase):
    def test_authenticated_action_physically_waits_for_explicit_release(self):
        with socket.socket() as endpoint, socket.socket() as cdn_endpoint:
            endpoint.bind(("127.0.0.1", 0))
            cdn_endpoint.bind(("127.0.0.1", 0))
            port = endpoint.getsockname()[1]
            cdn_port = cdn_endpoint.getsockname()[1]
        config = dict(actionPort=port, group=GROUP, sender=SENDER, name=NAME, local=LOCAL, remote=REMOTE,
                      timestamp=1_900_000_000, actionToken="synthetic-peer-test", eventToken="unused",
                      imageURL=f"http://127.0.0.1:{cdn_port}/expired-sticker")

        def request(method, path, auth=True, value=None):
            connection = http.client.HTTPConnection("127.0.0.1", port, timeout=4)
            headers = {"Authorization": "Bearer " + config["actionToken"]} if auth else {}
            try:
                connection.request(method, path, json.dumps(value) if value is not None else None, headers)
                response = connection.getresponse()
                return response.status, response.read()
            finally:
                connection.close()

        def ready():
            try:
                return request("GET", "/fixture/ready")[0] == 204
            except ConnectionRefusedError:
                return False

        script = Path(__file__).with_name("browser_chat_fixture.cjs").read_text()
        env = {**os.environ, "HE_BROWSER_CHAT": json.dumps(config)}
        def origin(path, authorization=None):
            connection = http.client.HTTPConnection("127.0.0.1", cdn_port, timeout=4)
            try:
                connection.putrequest("GET", path, skip_accept_encoding=True)
                if authorization is not None:
                    connection.putheader("Authorization", authorization)
                connection.endheaders()
                return connection.getresponse().status
            finally:
                connection.close()

        def cdn_ready():
            try:
                return origin("/fixture/ready") == 204
            except ConnectionRefusedError:
                return False

        with (subprocess.Popen(["node", "-e", script, "peer"], env=env) as process,
              subprocess.Popen(["node", "-e", script, "cdn"], env=env) as cdn):
            try:
                wait_for(ready, "real Node action peer", seconds=4)
                self.assertEqual(request("POST", "/get_group_msg_history", auth=False)[0], 401)
                self.assertEqual(request("POST", "/fixture/release")[0], 409)
                with ThreadPoolExecutor(max_workers=1) as executor:
                    future = executor.submit(request, "POST", "/get_group_msg_history", True,
                                             {"group_id": str(GROUP), "count": 30, "reverse_order": True})
                    wait_for(lambda: request("GET", "/fixture/held")[0] == 204, "actual held response", seconds=2)
                    self.assertFalse(future.done())
                    self.assertEqual(request("POST", "/fixture/release")[0], 204)
                    status, wire = future.result(timeout=2)
                    self.assertEqual(status, 200)
                    rows = json.loads(wire)["data"]["messages"]
                    self.assertEqual([row["message"][0]["data"]["text"] for row in rows], [REMOTE])
                self.assertEqual(request("GET", "/fixture/held")[0], 409)
                part = rows[0]["message"][1]
                self.assertEqual(part, {"type": "mface", "data": {
                    "url": config["imageURL"], "file_id": "synthetic-sticker-200"}})
                self.assertEqual(request("GET", "/fixture/media")[0], 409)
                status, wire = request("POST", "/get_msg", value={"message_id": "200"})
                self.assertEqual(status, 200)
                self.assertEqual(json.loads(wire)["data"], rows[0])
                status, wire = request("POST", "/get_image", value={"file": part["data"]["file_id"]})
                self.assertEqual(status, 200)
                png = base64.b64decode(json.loads(wire)["data"]["file"].split("://", 1)[1])
                self.assertEqual(png[:8], b"\x89PNG\r\n\x1a\n")
                self.assertEqual(png[16:24], b"\0\0\0\x03\0\0\0\x02")
                self.assertEqual(request("GET", "/fixture/media")[0], 204)
                wait_for(cdn_ready, "synthetic CDN", seconds=4)
                self.assertEqual(origin("/expired-sticker", "Bearer synthetic-peer-test"), 400)
                self.assertEqual(origin("/expired-sticker"), 403)
                self.assertEqual(origin("/fixture/media"), 204)
            finally:
                for child in (process, cdn):
                    child.terminate()
                    child.wait(timeout=4)
