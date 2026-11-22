import http.client
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
from concurrent.futures import ThreadPoolExecutor
import unittest

from acceptance import wait_for
from browser_chat_fixture import GROUP, LOCAL, NAME, REMOTE


@unittest.skipUnless(shutil.which("node"), "Node is required for the standalone action peer")
class ChatPeerTests(unittest.TestCase):
    def test_authenticated_action_physically_waits_for_explicit_release(self):
        with socket.socket() as endpoint:
            endpoint.bind(("127.0.0.1", 0))
            port = endpoint.getsockname()[1]
        config = dict(actionPort=port, group=GROUP, name=NAME, local=LOCAL, remote=REMOTE,
                      timestamp=1_900_000_000, actionToken="synthetic-peer-test", eventToken="unused")

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
        with subprocess.Popen(["node", "-e", script, "peer"], env=env) as process:
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
            finally:
                process.terminate()
                process.wait(timeout=4)
