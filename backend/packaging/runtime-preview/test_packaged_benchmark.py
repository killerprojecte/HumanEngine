import argparse
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import packaged_benchmark as bench

IMAGES = {"ts": "sha256:" + "1" * 64, "ts-tools": "sha256:" + "2" * 64, "go": "sha256:" + "3" * 64}
CANDIDATE = "a" * 40


class BenchmarkTests(unittest.TestCase):
    def run_mock(self, mismatch=False, cleanup_failure=False, cpus=2):
        report = {"windows": []}
        trial = bench.Paired(argparse.Namespace(ts_image="ts", ts_tools_image="ts-tools", go_image="go",
                                                 go_revision=CANDIDATE, cpus=cpus, window=1, warmup=1, rate=2, timeout=1),
                             report)
        seed = trial.seed
        actual = []
        calls = []
        live = None
        locations = []
        def docker(*args, **kwargs):
            nonlocal live
            calls.append(args)
            if args[:2] == ("image", "inspect"):
                revision = CANDIDATE if args[-1] == "go" else bench.TS_REVISION
                return IMAGES[args[-1]] + " " + revision
            if args[:2] == ("run", "-d") and args[args.index("--name") + 1] == trial.name:
                self.assertIsNone(live)
                self.assertIn("--no-healthcheck", args)
                self.assertEqual(args[args.index("--cpus") + 1], str(cpus))
                self.assertNotIn("-p", args)
                self.assertEqual(args[args.index("--network") + 1], "container:" + trial.peer)
                live = "go" if IMAGES["go"] in args else "ts"
                (cage / "cpu.stat").write_text("usage_usec 1\nthrottled_usec 0\nnr_periods 5\nnr_throttled 1\n")
                locations.append(trial.active_volume)
                return "container"
            if args[:2] == ("exec", "-i"):
                if "JSON.parse(require" in " ".join(args):
                    path = json.loads(kwargs["body"])["path"]
                    if path == "/api/plugins":
                        return '[200,[{"id":"snowluma","enabled":false}]]'
                    return '[200,{"ok":true}]'
                return json.dumps({"scene": bench.SCENE, "ids": list(bench.IDS)})
            if args[:3] == ("exec", trial.peer, "python3"):
                engine = args[args.index("--single") + 1]
                self.assertEqual(live, engine)
                actual.append(engine)
                (peer_cage / "cpu.stat").write_text(f"usage_usec {100 + 7 * len(actual)}\n")
                (cage / "cpu.stat").write_text("usage_usec 11\nthrottled_usec 22\nnr_periods 30\nnr_throttled 4\n")
                return json.dumps({"status": "passed", "windows": [{"engine": engine, "routes": {}}],
                                   "configuration": {"test": True},
                                   "settings_sha256": ("b" if mismatch and len(actual) == 2 else "a") * 64})
            if args[0] == "kill":
                self.assertIsNotNone(live)
                live = None
            if args[0] == "wait":
                return "0"
            if args[0] == "logs":
                if live == "go":
                    return 'ready | {"code":"server.listening","gomaxprocs":2}'
                return '{"code":"app.drained"}'
            if args[:3] == ("run", "--rm", "--name"):
                self.assertIsNone(live)
            if args[:2] == ("volume", "rm") and kwargs.get("check") is False and cleanup_failure:
                return None
            return ""
        with tempfile.TemporaryDirectory() as folder:
            cage = Path(folder)
            peer_cage = cage / "peer"
            peer_cage.mkdir()
            (peer_cage / "cpu.stat").write_text("usage_usec 100\n")
            for name, value in {"cpu.max": f"{cpus * 100000} 100000", "memory.max": str(1536 * 1024**2),
                                "cpu.stat": "usage_usec 1\nthrottled_usec 0\n",
                                "memory.current": "1200", "memory.peak": "1300"}.items():
                (cage / name).write_text(value)
            with patch("packaged_benchmark.docker", side_effect=docker), \
                    patch("migration_drill.docker", side_effect=docker), \
                    patch("packaged_benchmark.container_cgroup",
                          side_effect=lambda name: (peer_cage, "2-3") if name == trial.peer else (cage, "0-3")), \
                    patch.object(Path, "is_file", return_value=True):
                if mismatch:
                    with self.assertRaisesRegex(RuntimeError, "settings or workload differ"):
                        trial.run()
                else:
                    trial.run()
                self.assertEqual(trial.cleanup(), not cleanup_failure)
        return trial, report, actual, calls, locations

    def test_single_peer_disposable_abba(self):
        trial, report, actual, calls, locations = self.run_mock()
        self.assertEqual(tuple(actual), bench.ORDER)
        self.assertEqual(len(locations), 5)
        self.assertEqual(len(set(locations)), 5)
        self.assertEqual(locations[0], trial.seed)
        self.assertEqual(report["settings_sha256"], "a" * 64)
        self.assertEqual(len(report["windows"]), 4)
        isolated = [call for call in calls if call[:2] == ("run", "--rm")]
        self.assertTrue(all(call[call.index("--network") + 1] == "none" for call in isolated))
        self.assertEqual(sum(bench.VERIFY_PAIR in call for call in calls), 5)
        copies = [call for call in isolated if trial.name + "-clone" in call]
        self.assertEqual(len(copies), 4)
        self.assertTrue(all(f"type=volume,source={trial.seed},target=/seed,readonly" in call for call in copies))
        peer = next(call for call in calls if call[:2] == ("run", "-d") and trial.peer in call)
        self.assertEqual(peer[peer.index("--network") + 1], "none")
        self.assertNotIn("-p", peer)
        self.assertIn(("rm", "-f", trial.peer), calls)
        self.assertIn(("volume", "rm", trial.seed), calls)

    def test_cpu_evidence_separates_quota_and_go_startup_parallelism(self):
        for cpus in (1, 4):
            with self.subTest(cpus=cpus):
                _, report, _, _, _ = self.run_mock(cpus=cpus)
                self.assertEqual(report["runner"]["limits"]["cpus"], cpus)
                self.assertEqual(report["runner"]["cpu_topology"], "shared_host")
                self.assertIn("without_participation_or_learning", report["runner"]["workload"])
                for win in report["windows"]:
                    cpu = win["owner_cgroup"]
                    self.assertEqual(cpu["cpu_max_usec"], {"quota": cpus * 100000, "period": 100000})
                    self.assertEqual(cpu["leader_cpu_affinity"], "0-3")
                    self.assertEqual((cpu["cpu_usage_usec"], cpu["cpu_throttled_usec"],
                                      cpu["cpu_nr_periods"], cpu["cpu_nr_throttled"]), (10, 22, 25, 3))
                    self.assertEqual(cpu.get("gomaxprocs_at_ready"), 2 if win["engine"] == "go" else None)
                    self.assertEqual(cpu["memory_peak_boot_inclusive_bytes"], 1300)
                    self.assertGreaterEqual(win["driver_invocation_elapsed_ns"], 0)
                    self.assertEqual(win["peer_cgroup"]["leader_cpu_affinity"], "2-3")
                    self.assertEqual(win["peer_cgroup"]["cpu_usage_usec"], 7)

    def test_go_startup_receipt_must_be_unique_positive_integer(self):
        event = 'preview ready | {"code":"server.listening","gomaxprocs":2}'
        self.assertEqual(bench.go_parallelism("irrelevant\n" + event), 2)
        for invalid in ("", event + "\n" + event, event.replace(":2", ":true")):
            with self.assertRaisesRegex(RuntimeError, "Go startup scheduler receipt"):
                bench.go_parallelism(invalid)

    def test_parity_mismatch_aborts_and_cleanup_releases_trial(self):
        trial, report, actual, calls, _ = self.run_mock(mismatch=True)
        self.assertEqual(actual, ["ts", "go"])
        self.assertEqual(len(report["windows"]), 1)
        self.assertIn(("volume", "rm", trial.name + "-trial-2"), calls)
        self.assertLess(calls.index(("rm", "-f", trial.peer)), calls.index(("volume", "rm", trial.seed)))

    def test_failed_cleanup_is_not_success(self):
        self.run_mock(cleanup_failure=True)


if __name__ == "__main__":
    unittest.main()
