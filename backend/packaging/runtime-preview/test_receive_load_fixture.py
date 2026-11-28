import hashlib
import hmac
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from urllib.error import HTTPError

from packaged_mixed_benchmark import require_fixture_success
from receive_load_fixture import (EVENTS, SCENE_COUNT, ReceiveFailure, ReceiveWitness, SCENES, expected_id, has_image,
                                  post_event, profile_requests, synthetic_image, verify_live)


class ReceiveLoadTests(unittest.TestCase):
    def test_signed_messages_and_recalls_do_not_share_identity(self):
        with patch("receive_load_fixture.ask", return_value=(204, b"")) as ask:
            post_event(100, 1720000000, "synthetic")
            _, _, method, body, headers = ask.call_args.args
            text_id = body["message_id"]
            self.assertEqual(method, "POST")
            self.assertEqual(body["group_id"], 200001)
            self.assertTrue(expected_id(100).startswith(SCENES[0] + ":m:"))
            sig = hmac.new(b"synthetic", json.dumps(body).encode(), hashlib.sha1).hexdigest()
            self.assertEqual(headers["X-Signature"], "sha1=" + sig)
            post_event(-100, 1720000000, "synthetic", recall=True)
            recall = ask.call_args.args[3]
            self.assertEqual(recall["group_id"], body["group_id"])
            self.assertNotEqual(recall["message_id"], text_id)
            self.assertEqual(recall["notice_type"], "group_recall")

    def test_captioned_photo_subset_is_signed_and_checked_on_existing_history(self):
        photo = synthetic_image()
        picks = [i for i in range(EVENTS) if has_image(i)]
        self.assertEqual(len(picks), 100)
        self.assertEqual(len({i % SCENE_COUNT for i in picks}), SCENE_COUNT)
        self.assertGreater(len(photo), 100_000)
        with patch("receive_load_fixture.ask", return_value=(204, b"")) as ask:
            post_event(picks[0], 1720000000, "synthetic", image=photo)
            _, _, _, wire, headers = ask.call_args.args
            self.assertEqual([part["type"] for part in wire["message"]], ["text", "image"])
            digest = hmac.new(b"synthetic", json.dumps(wire).encode(), hashlib.sha1).hexdigest()
            self.assertEqual(headers["X-Signature"], "sha1=" + digest)

        buckets = {scene: [] for scene in SCENES}
        for i in range(EVENTS):
            parts = [{"type": "text"}]
            if has_image(i):
                parts.append({"type": "media"})
            buckets[SCENES[i % SCENE_COUNT]].append(dict(id=expected_id(i), status="available", parts=parts))
        def read(_port, _admin, path):
            if path == "/api/learning/status":
                return dict.fromkeys(("accepted", "queued", "active", "completed", "failed", "retried"), 0)
            return {"items": buckets[path.split("/")[3]]}
        with patch("receive_load_fixture.admin_read", side_effect=read):
            self.assertEqual(verify_live(3000, "synthetic", True)["retained_live_image_parts"], 100)
            buckets[SCENES[0]][0]["parts"].pop()
            with self.assertRaisesRegex(RuntimeError, "image part missing"):
                verify_live(3000, "synthetic", True)

    def test_image_http_failure_only_publishes_code_and_status(self):
        error = HTTPError("http://loopback", 503, "not for logs", None, None)
        with patch("receive_load_fixture.ask", side_effect=error):
            with self.assertRaises(ReceiveFailure) as rejected:
                post_event(0, 1720000000, "synthetic", image="aW1hZ2U=")
        self.assertEqual((rejected.exception.code, rejected.exception.status), ("image_status", 503))
        report = {}
        with self.assertRaisesRegex(RuntimeError, "synthetic receive fixture failed"):
            require_fixture_success({"fixture_failure": "image_status", "http_status": 503}, report)
        self.assertEqual(report, {"fixture_failure": "image_status", "fixture_http_status": 503})
        report = {}
        with self.assertRaises(RuntimeError):
            require_fixture_success({"fixture_failure": "not for logs", "http_status": "not for logs"}, report)
        self.assertEqual(report, {"fixture_failure": "unknown"})

    def test_deadline_report_keeps_only_numeric_fixture_timings(self):
        report = {}
        with self.assertRaisesRegex(RuntimeError, "synthetic receive fixture failed"):
            require_fixture_success({
                "fixture_failure": "burst_deadline", "dispatch_delay_ns": 515_000_000,
                "burst_lateness_ns": "not for logs", "dispatch_over_deadline": 3, "pending_reader": True,
                "ack_p95_us": 27_000, "raw": "not for logs",
            }, report)
        self.assertEqual(report, {"fixture_failure": "burst_deadline", "fixture_timing": {
            "dispatch_delay_ns": 515_000_000, "dispatch_over_deadline": 3, "ack_p95_us": 27_000,
        }})

    def test_peer_rejects_and_counts_forbidden_work(self):
        with tempfile.TemporaryDirectory() as tmp:
            witness = ReceiveWitness(Path(tmp))
            self.assertNotIn("model_requests", witness.snapshot()["counts"])
            witness.action("/get_login_info", {})
            self.assertEqual(profile_requests(json.dumps(witness.snapshot())), 0)
            witness.action("/get_group_info", {"group_id": "200001"})
            self.assertEqual(profile_requests(json.dumps(witness.snapshot())), 1)
            with self.assertRaisesRegex(RuntimeError, "unexpected model"):
                witness.model("/v1/embeddings", {})
            with self.assertRaisesRegex(RuntimeError, "unexpected action"):
                witness.action("/send_group_msg", {})
            with self.assertRaisesRegex(RuntimeError, "attempted model or outbound"):
                profile_requests(json.dumps(witness.snapshot()))

    def test_workflow_builds_external_only_without_publishing(self):
        workflow = (Path(__file__).resolve().parents[3] / ".github/workflows/golang-package-receive-benchmark.yml").read_text()
        self.assertIn("PREVIEW_PLUGIN_PROFILE=external-only", workflow)
        for target in ("wasm-snowluma-http-preview", "wasm-typing-delay-package",
                       "wasm-web-read-request-package", "wasm-web-search-structured-package"):
            self.assertIn(target, workflow)
        self.assertIn("--receive-only", workflow)
        self.assertIn("--receive-only --receive-images", workflow)
        self.assertNotRegex(workflow, r"docker (push|login)|push: true|permissions: write")
