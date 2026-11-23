#!/usr/bin/env python3
"""Compare two already-opened disposable HumanEngine HTTP snapshots over loopback."""
import argparse
import base64
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from contextlib import closing
from datetime import datetime, timezone
import hashlib
import http.client
import json
import os
import socket
import time

NS = 1_000_000_000
MAX_BODY = 256 * 1024
SETTINGS = {"timezone": str, "sourceEventRevisionsEnabled": bool,
            "debugModelIO": bool, "cacheEnabled": bool,
            "promptContextWindowEnabled": bool, "memoryWorkingSetEnabled": bool}


def encode(value):
    return base64.urlsafe_b64encode(json.dumps(value, separators=(",", ":")).encode()).decode().rstrip("=")


SCENE = "ob:s:" + encode(["snowluma", "42", "group", "900"])
IDS = tuple(SCENE + ":m:" + encode(str(number)) for number in (101, 102))
ROUTES = {"settings": "/api/settings", "history": f"/api/scenes/{SCENE}/history?source=remote"}


class BenchmarkError(Exception):
    pass


class DeadlineSocket(socket.socket):
    def apply_deadline(self):
        remaining = (self.deadline - time.monotonic_ns()) / NS
        if remaining <= 0:
            raise TimeoutError
        self.settimeout(remaining)

    def recv_into(self, *args):
        self.apply_deadline()
        return super().recv_into(*args)

    def sendall(self, *args):
        self.apply_deadline()
        return super().sendall(*args)


class Client(http.client.HTTPConnection):
    def __init__(self, port, token, timeout):
        super().__init__("127.0.0.1", port)
        self.token, self.timeout_ns = token, timeout * NS

    def connect(self):
        self.sock = DeadlineSocket()
        self.sock.deadline = self.deadline
        self.sock.apply_deadline()
        self.sock.connect((self.host, self.port))

    def get(self, path):
        start = time.monotonic_ns()
        self.deadline = start + self.timeout_ns
        if self.sock is not None:
            self.sock.deadline = self.deadline
        status = None
        try:
            self.request("GET", path, headers={"Authorization": "Bearer " + self.token})
            with self.getresponse() as response:
                status = response.status
                body = response.read(MAX_BODY + 1)
                if len(body) <= MAX_BODY and response.length not in (None, 0):
                    raise http.client.IncompleteRead(b"")
            finish = time.monotonic_ns()
            if finish >= self.deadline:
                raise TimeoutError
            if len(body) > MAX_BODY:
                self.close()
                return status, None, finish, None, "body_limit"
            return status, finish - start, finish, body, None
        except (OSError, http.client.HTTPException) as exc:
            self.close()
            kind = ("deadline" if isinstance(exc, TimeoutError)
                    else "protocol" if isinstance(exc, http.client.HTTPException) else "socket")
            return status, None, time.monotonic_ns(), None, kind


def settings(body):
    if not isinstance(body, dict) or any(type(body.get(k)) is not t for k, t in SETTINGS.items()):
        raise BenchmarkError("settings_shape")
    if body["sourceEventRevisionsEnabled"] is not True:
        raise BenchmarkError("fixture_revisions_disabled")
    return {k: body[k] for k in SETTINGS}


def history(body, timestamp):
    sent = datetime.fromtimestamp(timestamp, timezone.utc).isoformat(timespec="milliseconds").replace("+00:00", "Z")
    if not isinstance(body, dict) or not isinstance(body.get("items"), list):
        raise BenchmarkError("history_shape")
    expected = {
        "coverage": "partial", "reason": "platform_history_window", "nextCursor": IDS[0],
        "items": [
            {"id": key, "sceneId": SCENE, "senderId": "ob:u:" + encode(["snowluma", "42", "7"]),
             "senderDisplayName": "Synthetic sender", "senderPlatformId": "7",
             "senderAvatarUrl": "https://q1.qlogo.cn/g?b=qq&nk=7&s=100",
             "sentAt": sent, "origin": "history", "status": "available",
             "parts": [{"type": "text", "text": f"Synthetic recall source {number}"}]}
            for key, number in zip(IDS, (101, 102))
        ],
    }
    rows = []
    for row in body["items"]:
        if not isinstance(row, dict) or not isinstance(row.get("receivedAt"), str):
            raise BenchmarkError("history_received_at")
        received = row["receivedAt"]
        try:
            parsed = datetime.fromisoformat(received.replace("Z", "+00:00"))
        except ValueError:
            raise BenchmarkError("history_received_at") from None
        if parsed.tzinfo != timezone.utc or parsed.isoformat(timespec="milliseconds").replace("+00:00", "Z") != received:
            raise BenchmarkError("history_received_at")
        # Receipt time is dynamically normalized by the platform action owner.
        rows.append({k: v for k, v in row.items() if k != "receivedAt"})
    if dict(body, items=rows) != expected:
        raise BenchmarkError("history_fixture_mismatch")


def validate(route, body, timestamp, expected_settings=None):
    try:
        value = json.loads(body)
    except (ValueError, UnicodeError):
        raise BenchmarkError("invalid_json") from None
    if route == "history":
        history(value, timestamp)
        return None
    subset = settings(value)
    if expected_settings is not None and subset != expected_settings:
        raise BenchmarkError("settings_mismatch")
    return subset


def preflight(port, token, timestamp, timeout, expected_settings=None):
    with closing(Client(port, token + "-invalid", timeout)) as denied:
        status, _, _, _, error = denied.get(ROUTES["settings"])
        if status != 401 or error:
            raise BenchmarkError("unauthorized_probe")
    with closing(Client(port, token, timeout)) as client:
        for route, path in ROUTES.items():
            status, _, _, body, error = client.get(path)
            if error or status != 200:
                raise BenchmarkError(f"preflight_{route}_http")
            result = validate(route, body, timestamp, expected_settings)
            if route == "settings":
                expected_settings = result
    return expected_settings


def rank(samples, percentile):
    return None if not samples else sorted(samples)[(len(samples) * percentile + 99) // 100 - 1] / 1_000_000


def lane(client, route, index, start, seconds, rate, timestamp, expected_settings):
    samples, arrivals, statuses, errors = [], [], Counter(), Counter()
    missed = issued = in_window = 0
    end = start + seconds * NS
    ready = start
    for tick in range(index, seconds * rate, 2):
        due = start + tick * NS // rate
        if due < ready:
            missed += 1
            continue
        delay = due - time.monotonic_ns()
        if delay > 0:
            time.sleep(delay / NS)
        dispatch = time.monotonic_ns()
        if dispatch >= min(end, due + (2 * NS) // rate):
            missed += 1
            continue
        issued += 1
        arrivals.append(dispatch - due)
        status, elapsed, finish, body, error = client.get(ROUTES[route])
        if status is not None:
            statuses[status] += 1
        if error:
            errors[error] += 1
        elif status != 200:
            errors["http_status"] += 1
        else:
            try:
                validate(route, body, timestamp, expected_settings)
            except BenchmarkError as exc:
                errors[str(exc)] += 1
            else:
                samples.append(elapsed)
                in_window += finish < end
        ready = time.monotonic_ns()
    return samples, statuses, errors, missed, issued, in_window, arrivals


def worker(port, token, route, index, start, args, expected_settings):
    with closing(Client(port, token, args.timeout)) as client:
        warmup = lane(client, route, index, start, args.warmup, args.rate,
                      args.fixture_timestamp, expected_settings)
        # Warm-up traffic must stop before any timed lane begins. The gap
        # drains its bounded tail without measuring procedure bookkeeping.
        measured = lane(client, route, index, start + (args.warmup + args.timeout + 1) * NS,
                        args.window, args.rate, args.fixture_timestamp, expected_settings)
        return warmup[2], measured


def engine_window(port, token, args, expected_settings):
    start = time.monotonic_ns() + NS // 10
    with ThreadPoolExecutor(max_workers=4) as pool:
        work = {route: [pool.submit(worker, port, token, route, i, start, args, expected_settings)
                        for i in range(2)] for route in ROUTES}
        out = {}
        for route, futures in work.items():
            warm_errors, statuses, errors = Counter(), Counter(), Counter()
            samples, arrivals, missed, issued, successful_in_window = [], [], 0, 0, 0
            for future in futures:
                warm, (latency, codes, failures, skipped, sent, in_window, dispatch_delays) = future.result()
                warm_errors.update(warm)
                errors.update(failures)
                statuses.update(codes)
                samples.extend(latency)
                arrivals.extend(dispatch_delays)
                missed += skipped
                issued += sent
                successful_in_window += in_window
            out[route] = {
                "scheduled": args.window * args.rate, "issued": issued, "missed_ticks": missed,
                "successful": len(samples), "successful_in_window": successful_in_window,
                "successful_rps_in_window": successful_in_window / args.window,
                "status": dict(statuses), "errors": dict(errors), "warmup_errors": dict(warm_errors),
                "arrival_delay_ms": {"p50": rank(arrivals, 50), "p95": rank(arrivals, 95),
                                     "p99": rank(arrivals, 99)},
                "p50_ms": rank(samples, 50), "p95_ms": rank(samples, 95), "p99_ms": rank(samples, 99),
            }
        return out


def bounded(low, high):
    def parse(value):
        n = int(value)
        if not low <= n <= high:
            raise argparse.ArgumentTypeError(f"expected {low}..{high}")
        return n
    return parse


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--single", choices=("ts", "go"),
                        help="measure one owner; wrapper compares the settings digest across AB/BA")
    parser.add_argument("--ts-port", type=bounded(1024, 65535), default=3000)
    parser.add_argument("--go-port", type=bounded(1024, 65535), default=3001)
    parser.add_argument("--fixture-timestamp", required=True, type=bounded(1_700_000_000, 2_000_000_000))
    parser.add_argument("--window", type=bounded(1, 120), default=20, help="seconds per measured window")
    parser.add_argument("--warmup", type=bounded(1, 30), default=2, help="seconds before each window")
    parser.add_argument("--rate", type=bounded(2, 30), default=6, help="target requests/sec per route")
    parser.add_argument("--timeout", type=bounded(1, 10), default=3, help="absolute request deadline seconds")
    args = parser.parse_args()
    token = os.environ.get("HUMANENGINE_ADMIN_TOKEN", "")
    if (not args.single and args.ts_port == args.go_port) or not token.isascii() or not token.isprintable() or " " in token:
        parser.error("require different ports and a synthetic HUMANENGINE_ADMIN_TOKEN")
    receipt = {"kind": "isolated_http_snapshot", "configuration": {
        "window_seconds": args.window, "warmup_seconds": args.warmup, "request_timeout_seconds": args.timeout,
        "target_rps_per_route": args.rate, "workers_per_route": 2, "fixture_timestamp": args.fixture_timestamp,
    }, "windows": []}
    try:
        ports = {"ts": args.ts_port, "go": args.go_port}
        order = (args.single,) if args.single else ("ts", "go", "go", "ts")
        expected = preflight(ports[order[0]], token, args.fixture_timestamp, args.timeout)
        if not args.single:
            preflight(ports["go"], token, args.fixture_timestamp, args.timeout, expected)
        receipt["settings_sha256"] = hashlib.sha256(
            json.dumps(expected, sort_keys=True, separators=(",", ":")).encode()
        ).hexdigest()
        for engine in order:
            receipt["windows"].append({"engine": engine, "routes": engine_window(ports[engine], token, args, expected)})
    except BenchmarkError as exc:
        receipt["failure"] = str(exc)
    bad = bool(receipt.get("failure")) or any(
        route["errors"] or route["warmup_errors"] or not route["successful"]
        for window in receipt["windows"] for route in window["routes"].values()
    )
    receipt["status"] = "failed" if bad else "passed"
    print(json.dumps(receipt, sort_keys=True))
    return int(bad)


if __name__ == "__main__":
    raise SystemExit(main())
