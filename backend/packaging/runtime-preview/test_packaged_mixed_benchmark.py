import json
from pathlib import Path
import tempfile
import unittest

from packaged_mixed_benchmark import mix, verify_limits


class MixedBenchmarkTests(unittest.TestCase):
    def test_limits_require_one_cpu_equal_ram_and_no_swap(self):
        with tempfile.TemporaryDirectory() as scratch:
            cage = Path(scratch)
            for name, value in (("cpu.max", "100000 100000"), ("memory.max", str(1536 * 1024**2)),
                                ("memory.swap.max", "0")):
                (cage / name).write_text(value)
            self.assertEqual(verify_limits(cage)["cpu_max_usec"], {"quota": 100000, "period": 100000})
            (cage / "memory.swap.max").write_text("1048576")
            with self.assertRaisesRegex(RuntimeError, "cgroup limits"):
                verify_limits(cage)

    def test_physical_ack_requires_exact_quoted_output_and_is_not_replayable(self):
        with tempfile.TemporaryDirectory() as scratch:
            witness = mix.Witness(Path(scratch))
            body = dict(group_id=900, message=[dict(type="reply", data={"id": "101"}),
                                              dict(type="text", data={"text": mix.REPLY})])
            response, lane = witness.action("/send_group_msg", body)
            self.assertEqual(response["data"]["message_id"], 999)
            self.assertEqual(lane, 0)
            self.assertEqual(witness.snapshot()["acks"], {})
            witness.ack(lane)
            self.assertGreater(witness.snapshot()["acks"][lane], 0)
            with self.assertRaisesRegex(RuntimeError, "duplicate"):
                witness.ack(lane)
            body["message"][0]["data"]["id"] = "102"
            with self.assertRaisesRegex(RuntimeError, "wrong reply wire"):
                witness.action("/send_group_msg", body)
            witness.reset()
            self.assertEqual(witness.snapshot()["acks"], {})

    def test_planner_requires_the_right_scene_and_uses_input_target(self):
        with tempfile.TemporaryDirectory() as scratch:
            witness = mix.Witness(Path(scratch))
            stages = [{"function": {"name": "participation__decide"}}]
            messages = [{"id": mix.SOURCES[1], "parts": [{"text": f"Mix sample 1: {mix.FACTS[1]}"}]}]

            def call():
                return witness.model("/v1/chat/completions",
                                     {"model": "mixed-load", "tools": stages,
                                      "messages": [{"role": "user", "content": json.dumps({"messages": messages})}]})
            reply = call()["choices"][0]["message"]["tool_calls"][0]["function"]
            self.assertEqual(json.loads(reply["arguments"])["targetMessageIds"], [mix.SOURCES[1]])
            messages.append({"id": mix.SOURCES[10], "parts": []})
            with self.assertRaisesRegex(RuntimeError, "foreign scene"):
                call()
            self.assertEqual(witness.snapshot()["counts"], {"participation__decide": 1})

    def test_publication_workflow_uses_separate_local_artifacts_and_no_release(self):
        workflow = (Path(__file__).resolve().parents[3] / ".github/workflows/golang-package-mixed-benchmark.yml").read_text()
        self.assertIn("branches: [feat/refactor-golang]", workflow)
        self.assertIn("PREVIEW_PLUGIN_PROFILE=without-snowluma", workflow)
        self.assertIn("wasm-snowluma-http-preview", workflow)
        self.assertIn('--external-snow-package "$RUNNER_TEMP/snowluma-preview"', workflow)
        self.assertIn("--target build", workflow)
        self.assertIn("paired-mixed.json", workflow)
        self.assertNotRegex(workflow, r"docker (push|login)|push: true|permissions: write")
