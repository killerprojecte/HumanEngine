#!/usr/bin/env python3
"""Signed receive-only workload and an independent witness for forbidden outbound work."""
import argparse
import base64
from concurrent.futures import ThreadPoolExecutor
import hashlib
import hmac
import json
import os
from pathlib import Path
import struct
import threading
import time
import zlib

from migration_drill import SNOW_ACTION_PORT, SNOW_EVENT_PORT, require
from mixed_load_fixture import MODEL_PORT, Witness, admin_read, ask, encoded, listen, quantiles

SCENE_COUNT = 100
RECALLS = 250
MEMES = 5000
EVENTS = 2000
BURST = 5
INTERVAL_NS = 80_000_000
ROUTES = ("settings", "scenes", "status", "memes?pageSize=12")
SCENES = tuple("ob:s:" + encoded(["snowluma", "42", "group", str(200001 + i)]) for i in range(SCENE_COUNT))
FIRST_MESSAGE = 300001


def has_image(i):
    return i % 20 == (i // 100) % 20


def synthetic_image(seed=0):
    width, height = 384, 256

    def chunk(name, data):
        return struct.pack(">I", len(data)) + name + data + struct.pack(">I", zlib.crc32(name + data))

    rows = (bytes([0] + [c for x in range(width) for c in (
        (x + y + seed * 17) % 256, (x // 2 + y * 2) % 256, (x * 3 // 4 + y // 2) % 256)]) for y in range(height))
    png = (b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", width, height, 8, 2, 0, 0, 0))
           + chunk(b"IDAT", zlib.compress(b"".join(rows))) + chunk(b"IEND", b""))
    return base64.b64encode(png).decode()


class ReceiveWitness(Witness):
    def model(self, path, body):
        with self.lock:
            self.counts["model_requests"] += 1
        raise RuntimeError("unexpected model request")

    def action(self, path, body):
        if path == "/get_group_info":
            with self.lock:
                self.counts["group_profile_requests"] += 1
        if path in ("/get_login_info", "/get_group_info"):
            return super().action(path, body)
        with self.lock:
            self.counts["unexpected_actions"] += 1
        raise RuntimeError("unexpected action")


def profile_requests(raw):
    receipt = json.loads(raw)
    profiles = receipt["counts"].pop("group_profile_requests", 0)
    require(receipt == {"counts": {}, "acks": {}, "errors": 0, "observations": 0},
            "receive-only workload attempted model or outbound actions")
    return profiles


def scene_config(i):
    return dict(id=SCENES[i], kind="group", adapterId="snowluma", accountId="42",
                platformSceneId=str(200001 + i), name=f"Synthetic receive group {i}",
                participationEnabled=False, learningEnabled=False, proactiveEnabled=False)


def post_event(i, timestamp, token, recall=False, image=None):
    body = dict(self_id=42, group_id=200001 + i % SCENE_COUNT, user_id=7 + i % 20,
                message_id=FIRST_MESSAGE + i, time=timestamp)
    if recall:
        body.update(post_type="notice", notice_type="group_recall", operator_id=body["user_id"])
    else:
        body.update(post_type="message", message_type="group", sub_type="normal",
                    message=[dict(type="text", data={"text": f"Synthetic receive {i}."})],
                    sender=dict(user_id=body["user_id"], nickname="Synthetic sender"))
        if image is not None:
            body["message"].append(dict(type="image", data={"base64": image}))
    signature = hmac.new(token.encode(), json.dumps(body).encode(), hashlib.sha1).hexdigest()
    before = time.monotonic_ns()
    status, _ = ask(SNOW_EVENT_PORT, "/onebot/events", "POST", body,
                    {"X-Self-ID": "42", "Content-Type": "application/json", "X-Signature": "sha1=" + signature})
    require(status == 204, "signed event not admitted")
    return time.monotonic_ns() - before


def wait_until(deadline):
    time.sleep(max(0, (deadline - time.monotonic_ns()) / 1e9))


def expected_id(i):
    return SCENES[i % SCENE_COUNT] + ":m:" + encoded(str(FIRST_MESSAGE + i))


def verify_live(port, admin, with_images):
    images = 0
    for scene in range(SCENE_COUNT):
        path = f"/api/scenes/{SCENES[scene]}/history?source=local"
        items = admin_read(port, admin, path)["items"]
        available = {item["id"]: item for item in items if item.get("status") == "available"}
        expected = {expected_id(n): n for n in range(scene, EVENTS, SCENE_COUNT)}
        require(available.keys() == expected.keys(), "receive history mismatch")
        if with_images:
            for id, n in expected.items():
                parts = available[id]["parts"]
                require([p["type"] for p in parts] == (["text", "media"] if has_image(n) else ["text"]),
                        "caption or image part missing or out of order")
                images += int(has_image(n))
    status = admin_read(port, admin, "/api/learning/status")
    require(not any(status[k] for k in ("accepted", "queued", "active", "completed", "failed", "retried")),
            "learning ran in receive-only workload")
    return {"retained_live": EVENTS, **({"retained_live_image_parts": images} if with_images else {})}


def drive(port, timestamp, admin, token, with_images):
    photos = tuple(synthetic_image(n) for n in range(8)) if with_images else ()
    ask(SNOW_ACTION_PORT, "/reset", "POST", {})
    start = time.monotonic_ns() + 250_000_000
    bursts = EVENTS // BURST

    def reads():
        samples = {path: [] for path in ROUTES}
        for j in range(bursts // 2):
            wait_until(start + (2 * j) * INTERVAL_NS + 10_000_000)
            path = ROUTES[j % len(ROUTES)]
            begin = time.monotonic_ns()
            value = admin_read(port, admin, "/api/" + path)
            samples[path].append(time.monotonic_ns() - begin)
            if path == ROUTES[-1]:
                require(value["total"] == MEMES and len(value["items"]) == 12, "meme page differs")
        return {path: quantiles(ns) for path, ns in samples.items()}

    def submit_event(i, due):
        delay = time.monotonic_ns() - due
        photo = photos[(i // 20) % len(photos)] if photos and has_image(i) else None
        return post_event(i, timestamp, token, image=photo), delay

    with ThreadPoolExecutor(max_workers=BURST + 1) as pool:
        reader = pool.submit(reads)
        inflight = []
        lateness = 0
        for burst in range(bursts):
            due = start + burst * INTERVAL_NS
            wait_until(due)
            lateness = max(lateness, time.monotonic_ns() - due)
            inflight.extend(pool.submit(submit_event, i, due)
                            for i in range(burst * BURST, (burst + 1) * BURST))
        end = start + (bursts + 25) * INTERVAL_NS
        wait_until(end)
        require(reader.done() and all(result.done() for result in inflight), "receive traffic exceeded fixed window")
        latencies, delays = zip(*(result.result() for result in inflight), strict=True)
        admin_http = reader.result()
    require(lateness < INTERVAL_NS and max(delays) < 500_000_000,
            "receive driver did not meet burst schedule")
    _, raw = ask(SNOW_ACTION_PORT, "/receipt")
    result = {"events": EVENTS, "model_requests": 0, "outbound_sends": 0,
            "group_profile_requests": profile_requests(raw),
            "ingress_http": quantiles(latencies), "admin_http": admin_http,
            "max_burst_lateness_ms": lateness / 1e6, "max_dispatch_delay_ms": max(delays) / 1e6}
    if with_images:
        result["image_ingress_http"] = quantiles([ns for i, ns in enumerate(latencies) if has_image(i)])
        result["text_ingress_http"] = quantiles([ns for i, ns in enumerate(latencies) if not has_image(i)])
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("serve", "recall", "drive", "verify", "receipt"))
    parser.add_argument("--owner-port", type=int)
    parser.add_argument("--timestamp", type=int)
    parser.add_argument("--with-images", action="store_true")
    args = parser.parse_args()
    if args.mode == "serve":
        path = Path("/tmp/receive-witness")
        path.mkdir()
        action, model = listen(ReceiveWitness(path), SNOW_ACTION_PORT, MODEL_PORT, os.environ["HE_MIX_SNOW_TOKEN"])
        threading.Thread(target=model.serve_forever, daemon=True).start()
        action.serve_forever()
    elif args.mode == "recall":
        for i in range(-RECALLS, 0):
            post_event(i, args.timestamp, os.environ["HE_MIX_EVENT_TOKEN"], recall=True)
    elif args.mode == "drive":
        print(json.dumps(drive(args.owner_port, args.timestamp, os.environ["HUMANENGINE_ADMIN_TOKEN"],
                               os.environ["HE_MIX_EVENT_TOKEN"], args.with_images)))
    elif args.mode == "receipt":
        _, body = ask(SNOW_ACTION_PORT, "/receipt")
        print(body.decode())
    else:
        print(json.dumps(verify_live(args.owner_port, os.environ["HUMANENGINE_ADMIN_TOKEN"], args.with_images)))


if __name__ == "__main__":
    main()
