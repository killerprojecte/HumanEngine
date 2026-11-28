import hashlib
import hmac
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from receive_load_fixture import ReceiveWitness, SCENES, expected_id, post_event


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

    def test_peer_rejects_and_counts_forbidden_work(self):
        with tempfile.TemporaryDirectory() as tmp:
            witness = ReceiveWitness(Path(tmp))
            self.assertNotIn("model_requests", witness.snapshot()["counts"])
            witness.action("/get_login_info", {})
            with self.assertRaisesRegex(RuntimeError, "unexpected model"):
                witness.model("/v1/embeddings", {})
            with self.assertRaisesRegex(RuntimeError, "unexpected action"):
                witness.action("/send_group_msg", {})
            self.assertEqual(witness.snapshot()["counts"], {"unexpected_actions": 1, "model_requests": 1})

    def test_workflow_builds_external_only_without_publishing(self):
        workflow = (Path(__file__).resolve().parents[3] / ".github/workflows/golang-package-receive-benchmark.yml").read_text()
        self.assertIn("PREVIEW_PLUGIN_PROFILE=external-only", workflow)
        for target in ("wasm-snowluma-http-preview", "wasm-typing-delay-package",
                       "wasm-web-read-request-package", "wasm-web-search-structured-package"):
            self.assertIn(target, workflow)
        self.assertIn("--receive-only", workflow)
        self.assertNotRegex(workflow, r"docker (push|login)|push: true|permissions: write")
