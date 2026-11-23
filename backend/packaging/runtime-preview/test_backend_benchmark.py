import copy
from collections import Counter
from contextlib import closing, contextmanager, redirect_stdout
import http.server
import io
import json
import os
import sys
import threading
from types import SimpleNamespace
from unittest.mock import patch
import unittest

import backend_benchmark as bench

TIME = 1_795_342_140
TOKEN = "synthetic-benchmark-admin"
FIXTURE_SETTINGS = {"timezone": "UTC", "sourceEventRevisionsEnabled": True,
                    "debugModelIO": False, "cacheEnabled": True,
                    "promptContextWindowEnabled": True, "memoryWorkingSetEnabled": False}
FIXTURE_HISTORY = {
    "coverage": "partial", "reason": "platform_history_window",
    "nextCursor": "ob:s:WyJzbm93bHVtYSIsIjQyIiwiZ3JvdXAiLCI5MDAiXQ:m:IjEwMSI",
    "items": [
        {"id": "ob:s:WyJzbm93bHVtYSIsIjQyIiwiZ3JvdXAiLCI5MDAiXQ:m:" + suffix,
         "sceneId": "ob:s:WyJzbm93bHVtYSIsIjQyIiwiZ3JvdXAiLCI5MDAiXQ",
         "senderId": "ob:u:WyJzbm93bHVtYSIsIjQyIiwiNyJd",
         "senderPlatformId": "7", "senderDisplayName": "Synthetic sender",
         "senderAvatarUrl": "https://q1.qlogo.cn/g?b=qq&nk=7&s=100",
         "status": "available", "origin": "history", "sentAt": "2026-11-22T10:09:00.000Z",
         "receivedAt": "2026-11-22T10:09:01.000Z",
         "parts": [{"type": "text", "text": f"Synthetic recall source {number}"}]}
        for suffix, number in (("IjEwMSI", 101), ("IjEwMiI", 102))
    ],
}


@contextmanager
def endpoint(calls, bad=None):
    class Handler(http.server.BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *_):
            pass

        def do_GET(self):
            calls.append((self.path, self.client_address[1]))
            if self.headers.get("Authorization") != "Bearer " + TOKEN:
                code, value = 401, {}
            elif self.path == bench.ROUTES["settings"]:
                code, value = 200, FIXTURE_SETTINGS
            elif self.path == bench.ROUTES["history"]:
                code, value = 200, FIXTURE_HISTORY
            else:
                code, value = 404, {}
            body = json.dumps(value).encode()
            self.send_response(code)
            self.send_header("Content-Length", str(len(body) + (bad == "short")))
            if bad == "short":
                self.send_header("Connection", "close")
            self.end_headers()
            if bad == "trickle":
                stopped = threading.Event()
                try:
                    for byte in body:
                        self.wfile.write(bytes((byte,)))
                        stopped.wait(0.03)
                except OSError:
                    return
            else:
                self.wfile.write(body)

    with http.server.ThreadingHTTPServer(("127.0.0.1", 0), Handler) as server:
        worker = threading.Thread(target=server.serve_forever, kwargs={"poll_interval": 0.02})
        worker.start()
        try:
            yield server.server_port
        finally:
            server.shutdown()
            worker.join()


class BackendBenchmarkTests(unittest.TestCase):
    def test_fixture_is_strict_and_only_receipt_time_is_dynamic(self):
        bench.history(FIXTURE_HISTORY, TIME)
        later = copy.deepcopy(FIXTURE_HISTORY)
        later["items"][0]["receivedAt"] = "2026-11-22T10:12:34.567Z"
        bench.history(later, TIME)
        mutations = [
            ("coverage", "complete"), ("reason", "adapter_unavailable"),
            ("nextCursor", "opaque"), ("items", list(reversed(FIXTURE_HISTORY["items"]))),
        ]
        for key, value in mutations:
            with self.subTest(key=key), self.assertRaises(bench.BenchmarkError):
                bench.history(dict(FIXTURE_HISTORY, **{key: value}), TIME)
        for key, value in (("extra", 1), ("status", "recalled"), ("origin", "live"),
                           ("receivedAt", "2026-11-22T10:12:34+00:00"),
                           ("sentAt", "2026-11-22T10:12:34.000Z"),
                           ("parts", [{"type": "text", "text": "different"}])):
            different = copy.deepcopy(FIXTURE_HISTORY)
            different["items"][0][key] = value
            with self.subTest(key=key), self.assertRaises(bench.BenchmarkError):
                bench.history(different, TIME)

    def test_http_trickle_deadline_and_short_body(self):
        for bad, expect in (("trickle", "deadline"), ("short", "protocol")):
            with self.subTest(bad=bad), endpoint([], bad) as port, closing(bench.Client(port, TOKEN, 0.1)) as client:
                status, _, _, body, error = client.get(bench.ROUTES["settings"])
                self.assertEqual((status, body, error), (200, None, expect))

    def test_nearest_rank_and_never_catches_up(self):
        self.assertIsNone(bench.rank([], 99))
        self.assertEqual(bench.rank([1_000_000, 3_000_000, 2_000_000, 4_000_000], 50), 2)
        self.assertEqual(bench.rank([1_000_000, 3_000_000, 2_000_000, 4_000_000], 95), 4)
        state = SimpleNamespace(now=0, calls=[])

        def sleep(seconds):
            state.now += round(seconds * bench.NS)

        def get(_):
            state.calls.append(state.now)
            duration = 550_000_000
            state.now += duration
            return 200, duration, state.now, json.dumps(FIXTURE_SETTINGS), None

        with patch.object(bench.time, "monotonic_ns", lambda: state.now), patch.object(bench.time, "sleep", sleep):
            result = bench.lane(SimpleNamespace(get=get), "settings", 0, 0, 2, 8, TIME, FIXTURE_SETTINGS)
        self.assertEqual(state.calls, [0, 750_000_000, 1_500_000_000])
        self.assertEqual(result[3:], (5, 3, 2, [0, 0, 0]))

    def test_dispatch_lateness_is_distinct_from_http_latency_and_omits_misses(self):
        state = SimpleNamespace(now=0)

        def sleep(seconds):
            state.now += round(seconds * bench.NS) + 20_000_000

        def get(_):
            elapsed = 260_000_000
            state.now += elapsed
            return 200, elapsed, state.now, json.dumps(FIXTURE_SETTINGS), None

        with patch.object(bench.time, "monotonic_ns", lambda: state.now), patch.object(bench.time, "sleep", sleep):
            samples, _, _, missed, sent, _, arrivals = bench.lane(
                SimpleNamespace(get=get), "settings", 1, 0, 1, 10, TIME, FIXTURE_SETTINGS)
        self.assertEqual((missed, sent), (2, 3))
        self.assertEqual(samples, [260_000_000] * 3)
        self.assertEqual(arrivals, [20_000_000] * 3)

    def test_single_owner_only_probes_selected_port(self):
        args = ["driver", "--single", "go", "--fixture-timestamp", str(TIME)]
        with patch.object(sys, "argv", args), patch.dict(os.environ, {"HUMANENGINE_ADMIN_TOKEN": TOKEN}), \
                patch.object(bench, "preflight", return_value=FIXTURE_SETTINGS) as preflight, \
                patch.object(bench, "engine_window", return_value={}), redirect_stdout(io.StringIO()) as output:
            self.assertEqual(bench.main(), 0)
        result = json.loads(output.getvalue())
        preflight.assert_called_once_with(3001, TOKEN, TIME, 3)
        self.assertEqual([item["engine"] for item in result["windows"]], ["go"])
        self.assertEqual(len(result["settings_sha256"]), 64)

    def test_two_loopback_stubs_exercise_full_ab_ba_driver(self):
        ts_calls, go_calls = [], []
        output = io.StringIO()
        with endpoint(ts_calls) as ts, endpoint(go_calls) as go:
            args = ["driver", "--ts-port", str(ts), "--go-port", str(go), "--fixture-timestamp", str(TIME),
                    "--warmup", "1", "--timeout", "1", "--window", "1", "--rate", "2"]
            with patch.object(sys, "argv", args), patch.dict(os.environ, {"HUMANENGINE_ADMIN_TOKEN": TOKEN}), redirect_stdout(output):
                self.assertEqual(bench.main(), 0)
        result = json.loads(output.getvalue())
        self.assertEqual(result["status"], "passed")
        self.assertEqual([item["engine"] for item in result["windows"]], ["ts", "go", "go", "ts"])
        self.assertNotIn(TOKEN, output.getvalue())
        self.assertNotIn("Synthetic recall source", output.getvalue())
        for window in result["windows"]:
            for route in window["routes"].values():
                self.assertEqual((route["issued"], route["successful"], route["missed_ticks"]), (2, 2, 0))
                self.assertEqual(route["status"], {"200": 2})
                self.assertGreater(route["p99_ms"], 0)
                self.assertGreaterEqual(route["arrival_delay_ms"]["p99"], 0)
        for calls in (ts_calls, go_calls):
            # 3 preflight, then four worker connections per each of two windows.
            self.assertEqual(len(calls), 3 + 2 * (4 + 4))
            conn = [port for _, port in calls[3:]]
            self.assertEqual(sorted(Counter(conn).values()), [2] * 8)


if __name__ == "__main__":
    unittest.main()
