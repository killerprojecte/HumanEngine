#!/usr/bin/env python3
"""Isolated synthetic model and OneBot peer, plus the fixed-rate mixed-load driver."""
import argparse
import base64
from collections import Counter
from concurrent.futures import ThreadPoolExecutor
import hashlib
import hmac
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import math
import os
from pathlib import Path
import re
import socket
import struct
import threading
import time
from urllib.request import Request, urlopen
import zlib

from migration_drill import SNOW_ACTION_PORT, SNOW_EVENT_PORT, require

MODEL_PORT = 41276
COUNT = 32
RATE = 4
WINDOW = 14
READS = RATE * WINDOW
GROUPS = tuple(range(900, 900 + COUNT))
REPLY = "Synthetic mixed-load reply."
OBSERVATION = "Synthetic observation of a four-color chart."
MARKER = re.compile(r"Mix sample (\d+):")
FACTS = (
    "Mercury has no natural moons.", "Venus rotates clockwise relative to most planets.",
    "Earth has one natural moon.", "Mars has two natural moons.",
    "Jupiter has the largest diameter among planets.", "Saturn has visible rings.",
    "Uranus rotates on a tilted axis.", "Neptune is beyond Uranus.",
    "Carbon has atomic number six.", "Oxygen has atomic number eight.",
    "The largest ocean is the Pacific.", "A triangle has three sides.",
    "A regular hexagon has six sides.", "Helium is a noble gas.",
    "Earth has an iron-rich core.", "Water freezes at zero Celsius at standard pressure.",
    "The Amazon River reaches the Atlantic.", "Copper conducts electricity.",
    "Quartz contains silicon and oxygen.", "Butterflies develop through metamorphosis.",
    "The Moon reflects sunlight.", "Earth rotates from west to east.",
    "The heart pumps blood.", "Chlorophyll absorbs visible light.",
    "The Indian Ocean borders Australia.", "An equilateral triangle has three equal angles.",
    "Saturn has a lower mean density than water.", "Hummingbirds can hover in place.",
    "Woodpeckers use their bills to excavate wood.", "Pumice can float when its pores hold air.",
    "Graphite conducts electricity.", "Sound travels through water.",
)
COUNTS = dict(participation__decide=COUNT, reply__plan=COUNT, messages__send=COUNT,
              learning__propose=COUNT // 2, vision=COUNT // 2, embedding=COUNT - 1)
ROLES = dict(planner=COUNT * 2, replyer=COUNT, learning=COUNT // 2,
             vision=COUNT // 2, embedding=COUNT - 1)


def encoded(item):
    return base64.urlsafe_b64encode(json.dumps(item, separators=(",", ":")).encode()).decode().rstrip("=")


SCENES = tuple("ob:s:" + encoded(["snowluma", "42", "group", str(g)]) for g in GROUPS)
SOURCES = tuple(scene + ":m:" + encoded(str(101 + i)) for i, scene in enumerate(SCENES))
ACK_IDS = tuple(scene + ":m:" + encoded(str(999 + i)) for i, scene in enumerate(SCENES))
SENDERS = tuple("ob:u:" + encoded(["snowluma", "42", str(7 + i)]) for i in range(COUNT))


def chart():
    rows = [bytes(color for x in range(32) for color in (30 if x < 16 else 190, 40 if y < 12 else 170, 80))
            for y in range(24)]

    def chunk(name, payload):
        return struct.pack(">I", len(payload)) + name + payload + struct.pack(">I", zlib.crc32(name + payload))

    png = (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 32, 24, 8, 2, 0, 0, 0))
           + chunk(b"IDAT", zlib.compress(b"".join(b"\0" + row for row in rows))) + chunk(b"IEND", b""))
    return base64.b64encode(png).decode(), hashlib.sha256(b"".join(rows)).hexdigest()


IMAGE, PIXELS = chart()
FIELDS = dict(sourceEventRevisionsEnabled=True, promptContextWindowEnabled=False, timezone="UTC",
              debounceMs=0, groupTurnCooldownMs=0, modelConcurrency=4, learningBatchMs=100,
              learningMaxWaitMs=100, learningMinIntervalMs=0, learningRetryAttempts=1, debugModelIO=False)
MODELS = {
    "providers": [dict(id="loopback", name="Fixture", protocol="openai-chat",
                       baseUrl=f"http://127.0.0.1:{MODEL_PORT}/v1", retryCount=0, timeoutMs=30000)],
    "models": [dict(id=i, providerId="loopback", displayName=i, apiModelName=name, capabilities=caps,
                    **({"embeddingDimensions": 2} if i == "e" else {}))
               for i, name, caps in (("m", "mixed-load", ["text", "tools"]), ("l", "held-learning", ["text", "tools"]),
                                      ("e", "fixture-embedding", ["embedding"]), ("v", "held-vision", ["text", "vision"]))],
    "routes": [dict(role=role, orderedModelIds=[i], timeoutMs=30000)
               for role, i in (("planner", "m"), ("replyer", "m"), ("learning", "l"), ("embedding", "e"), ("vision", "v"))],
}


def scene_config(i):
    return dict(id=SCENES[i], kind="group", adapterId="snowluma", accountId="42",
                platformSceneId=str(GROUPS[i]), name=f"Synthetic group {GROUPS[i]}",
                participationEnabled=True, learningEnabled=bool(i % 2), proactiveEnabled=False)


def marker(parts):
    for part in parts:
        if match := MARKER.search(part.get("text", "")):
            return int(match[1])


class Witness:
    def __init__(self, image_dir):
        self.lock = threading.Lock()
        self.counts = Counter()
        self.acks = {}
        self.errors = 0
        self.observations = 0
        self.image_dir = image_dir

    def reset(self):
        with self.lock:
            self.counts.clear()
            self.acks.clear()
            self.errors = self.observations = 0
            for file in self.image_dir.glob("vision-*.png"):
                file.unlink()

    def snapshot(self):
        with self.lock:
            return dict(counts=dict(self.counts), acks=dict(self.acks), errors=self.errors,
                        observations=self.observations)

    def model(self, path, body):
        if path == "/v1/embeddings":
            with self.lock:
                self.counts["embedding"] += 1
            return {"data": [{"index": i, "embedding": [1, 0]} for i, _ in enumerate(body["input"])],
                    "usage": {"prompt_tokens": 1, "total_tokens": 1}}
        require(path == "/v1/chat/completions", "unexpected model path")
        wire = json.dumps(body, ensure_ascii=False)
        if body["model"] == "held-vision":
            url, = [part["image_url"]["url"] for msg in body["messages"] if isinstance(msg["content"], list)
                    for part in msg["content"] if part["type"] == "image_url"]
            header, data = url.split(",", 1)
            require(header == "data:image/png;base64", "unexpected image transport")
            require(all(SCENES[i] not in wire and FACTS[i] not in wire for i in range(1, COUNT, 2)),
                    "foreign scene in vision prompt")
            image = base64.b64decode(data, validate=True)
            with self.lock:
                self.counts["vision"] += 1
                (self.image_dir / f"vision-{self.counts['vision']}.png").write_bytes(image)
            return completion(content=OBSERVATION)
        tools = {tool["function"]["name"] for tool in body.get("tools", [])}
        user = json.loads([msg["content"] for msg in body["messages"] if msg["role"] == "user"][-1])
        lanes = {i for i in range(COUNT)
                 if SCENES[i] in wire or SOURCES[i] in wire or FACTS[i] in wire or f"Mix sample {i}:" in wire}
        require(len(lanes) <= 1, "foreign scene in model prompt")
        lane = next(iter(lanes), None)
        require(lane is None or lane % 2 == 0 or OBSERVATION not in wire, "foreign image observation")
        if "learning__propose" in tools:
            name = "learning__propose"
            row, = user["messages"]
            require(lane == marker(row["parts"]) and lane % 2 == 1, "wrong learning lane")
            args = dict(content=FACTS[lane], kind="fact", sourceMessageIds=[row["id"]])
        elif "participation__decide" in tools:
            name = "participation__decide"
            lane_index, target = max((index, row["id"]) for row in user["messages"]
                                     if (index := marker(row["parts"])) is not None)
            require(lane_index == lane, "missing input message")
            args = dict(action="reply", reasonCode="fixture", targetMessageIds=[target])
        elif "reply__plan" in tools:
            name = "reply__plan"
            plan = user["planContract"]
            if lane is not None and lane % 2 == 0:
                require(OBSERVATION in wire, "missing image observation in planner")
                with self.lock:
                    self.observations += 1
            args = dict(action="reply", deliveryTool="messages.send", targetMessageIds=plan["targetMessageIds"],
                        contextMessageIds=plan["contextMessageIds"], strategy="Answer the synthetic question.",
                        constraints=[], facts=[], assets=[], emoji=[], actions=[])
        elif "messages__send" in tools and not any(msg["role"] == "tool" for msg in body["messages"]):
            name = "messages__send"
            target, = user["plan"]["targetMessageIds"]
            args = dict(operationId="mix-fixture-" + target,
                        segments=[dict(replyToId=target, parts=[dict(type="text", text=REPLY)])])
        else:
            raise RuntimeError("unexpected model stage")
        with self.lock:
            self.counts[name] += 1
        return completion(name, args)

    def action(self, path, body):
        if path == "/get_login_info":
            data = {"user_id": 42}
        elif path == "/get_group_info":
            data = {"group_id": body["group_id"], "group_name": "Synthetic group"}
        elif path == "/send_group_msg":
            i = GROUPS.index(int(body["group_id"]))
            require(body["message"] == [dict(type="reply", data={"id": str(101 + i)}),
                                        dict(type="text", data={"text": REPLY})], "wrong reply wire")
            return {"status": "ok", "retcode": 0, "data": {"message_id": 999 + i}}, i
        else:
            raise RuntimeError("unexpected OneBot action")
        return {"status": "ok", "retcode": 0, "data": data}, None

    def ack(self, i):
        when = time.monotonic_ns()
        with self.lock:
            require(i not in self.acks, "duplicate reply")
            self.acks[i] = when


def completion(name=None, args=None, content=""):
    message = {"role": "assistant", "content": content}
    if name:
        message["tool_calls"] = [dict(id="mix-" + name, type="function",
                                      function=dict(name=name, arguments=json.dumps(args)))]
    return {"choices": [{"message": message, "finish_reason": "tool_calls" if name else "stop"}],
            "usage": {"prompt_tokens": 4, "completion_tokens": 2, "total_tokens": 6}}


def listen(witness, action_port, model_port, token):
    class Handler(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def setup(self):
            super().setup()
            self.connection.setsockopt(socket.IPPROTO_TCP, socket.TCP_NODELAY, 1)

        def log_message(self, *_):
            pass

        def respond(self, value, status=200):
            data = json.dumps(value).encode()
            self.send_response(status)
            self.send_header("Content-Type", "application/json")
            self.send_header("Content-Length", str(len(data)))
            self.end_headers()
            self.wfile.write(data)
            self.wfile.flush()

        def do_GET(self):
            if self.server.server_port == action_port and self.path == "/receipt":
                self.respond(witness.snapshot())
            else:
                self.respond({}, 404)

        def do_POST(self):
            try:
                body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
                if self.server.server_port == model_port:
                    self.respond(witness.model(self.path, body))
                elif self.path == "/reset":
                    witness.reset()
                    self.respond({})
                else:
                    require(self.headers.get("Authorization") == "Bearer " + token, "wrong synthetic action token")
                    value, lane = witness.action(self.path, body)
                    self.respond(value)
                    if lane is not None:
                        witness.ack(lane)
            except (OSError, ValueError, RuntimeError, KeyError, TypeError):
                with witness.lock:
                    witness.errors += 1
                self.close_connection = True

    return (ThreadingHTTPServer(("127.0.0.1", action_port), Handler),
            ThreadingHTTPServer(("127.0.0.1", model_port), Handler))


def ask(port, path, method="GET", body=None, headers=None):
    request = Request(f"http://127.0.0.1:{port}{path}", method=method, headers=headers or {},
                      data=json.dumps(body).encode() if body is not None else None)
    with urlopen(request, timeout=4) as response:
        return response.status, response.read()


def history_path(i):
    return "/api/scenes/" + SCENES[i] + "/history?source=local"


def event(i, timestamp):
    parts = [dict(type="text", data={"text": f"Mix sample {i}: {FACTS[i]}"})]
    if i % 2 == 0:
        parts.append(dict(type="image", data={"base64": IMAGE}))
    return dict(post_type="message", message_type="group", sub_type="normal", self_id=42, group_id=GROUPS[i],
                user_id=7 + i, message_id=101 + i, time=timestamp, message=parts,
                sender=dict(user_id=7 + i, nickname=f"Synthetic sender {i}"))


def quantiles(intervals):
    values = sorted(intervals)
    return {"samples": len(values), **{f"p{p}_ms": values[math.ceil(p / 100 * len(values)) - 1] / 1e6
                                     for p in (50, 95)}}


def admin_read(port, admin, path):
    status, body = ask(port, path, headers={"Authorization": "Bearer " + admin})
    require(status == 200, "admin read failed")
    return json.loads(body)


def drive(port, timestamp, admin, event_token, action_port=SNOW_ACTION_PORT, event_port=SNOW_EVENT_PORT):

    def until(at):
        time.sleep(max(0, (at - time.monotonic_ns()) / 1e9))

    def read_traffic(start):
        reads = []
        for j in range(READS):
            until(start + j * interval + 50_000_000)
            begin = time.monotonic_ns()
            admin_read(port, admin, routes[j % len(routes)])
            reads.append(time.monotonic_ns() - begin)
        return reads

    routes = ["/api/status", "/api/settings", "/api/usage", *(history_path(i) for i in range(COUNT))]
    require(admin_read(port, admin, "/api/learning/status")["completed"] == 0, "nonempty learning counters before run")
    ask(action_port, "/reset", "POST", {})
    interval = 1_000_000_000 // RATE
    start = time.monotonic_ns() + 250_000_000
    end = start + WINDOW * 1_000_000_000
    arrived = []
    with ThreadPoolExecutor(max_workers=1) as pool:
        reads = pool.submit(read_traffic, start)
        for i in range(COUNT):
            body = event(i, timestamp)
            signature = hmac.new(event_token.encode(), json.dumps(body).encode(), hashlib.sha1).hexdigest()
            until(start + i * interval)
            arrived.append(time.monotonic_ns())
            status, _ = ask(event_port, "/onebot/events", "POST", body,
                            {"X-Self-ID": "42", "Content-Type": "application/json", "X-Signature": "sha1=" + signature})
            require(status == 204, "signed event not admitted")
        until(end)
        require(reads.done(), "admin workload did not finish in the fixed window")
    _, raw = ask(action_port, "/receipt")
    receipt = json.loads(raw)
    require(receipt["errors"] == 0 and receipt["observations"] == COUNT // 2 and receipt["counts"] == COUNTS,
            "model, vision or action workload differs")
    acks = {int(index): ns for index, ns in receipt["acks"].items()}
    require(set(acks) == set(range(COUNT)) and all(arrived[i] < acks[i] <= end for i in range(COUNT)),
            "missing, early or late physical ACK")
    lateness_ms = max(ns - start - i * interval for i, ns in enumerate(arrived)) / 1e6
    require(lateness_ms < 250 / 2, "generator missed its event schedule")
    return {"events": COUNT, "calls": receipt["counts"], "image_observations": receipt["observations"],
            "physical_ack": quantiles([acks[i] - arrived[i] for i in range(COUNT)]),
            "admin_http": quantiles(reads.result()), "window_seconds": WINDOW, "maximum_event_lateness_ms": lateness_ms}


def verify_live(port, admin):
    status = admin_read(port, admin, "/api/learning/status")
    require(status["accepted"] == status["completed"] == COUNT // 2
            and not any(status[k] for k in ("failed", "queued", "active", "retried")), "learning did not complete")
    for i in range(COUNT):
        available = {row["id"]: row for row in admin_read(port, admin, history_path(i))["items"]
                     if row.get("status") == "available"}
        require(set(available) == {SOURCES[i], ACK_IDS[i]} and available[SOURCES[i]]["senderId"] == SENDERS[i],
                "history or sender differs")
    return {"learning_completed": status["completed"], "history_pairs": COUNT}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("serve", "drive", "verify"))
    parser.add_argument("--images", type=Path, default=Path("/tmp/mixed-vision"))
    parser.add_argument("--owner-port", type=int)
    parser.add_argument("--timestamp", type=int)
    args = parser.parse_args()
    if args.mode == "serve":
        args.images.mkdir()
        action, model = listen(Witness(args.images), SNOW_ACTION_PORT, MODEL_PORT, os.environ["HE_MIX_SNOW_TOKEN"])
        threading.Thread(target=model.serve_forever, daemon=True).start()
        action.serve_forever()
    elif args.mode == "drive":
        print(json.dumps(drive(args.owner_port, args.timestamp, os.environ["HUMANENGINE_ADMIN_TOKEN"],
                               os.environ["HE_MIX_EVENT_TOKEN"])))
    else:
        print(json.dumps(verify_live(args.owner_port, os.environ["HUMANENGINE_ADMIN_TOKEN"])))


if __name__ == "__main__":
    main()
