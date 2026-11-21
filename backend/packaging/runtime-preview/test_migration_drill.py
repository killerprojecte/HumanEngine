import argparse
import io
import json
from contextlib import redirect_stdout
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest
from unittest.mock import patch

from migration_drill import AUTHORITY, DATABASE, Drill, TS_REVISION, main

HERE = Path(__file__).resolve().parent


class MigrationDrillTests(unittest.TestCase):
    def test_maintenance_using_canonical_mounts_is_offline_only(self):
        drill = Drill(argparse.Namespace(), {"images": {"ts_tools": "tool-id"}})
        with patch("migration_drill.docker") as docker:
            drill.owner = "go"
            with self.assertRaises(RuntimeError):
                drill.tool("node")
            docker.assert_not_called()
            drill.owner = None
            drill.tool("node", "--import", "tsx", "scripts/backup.ts", DATABASE,
                       "/app/data/media", "/drill/old-backup", data="live")
            args = docker.call_args.args
            self.assertIn("--network", args)
            self.assertEqual(args[args.index("--network") + 1], "none")
            self.assertIn(drill.mount("/app/data", "live"), args)
            self.assertIn(drill.mount(AUTHORITY, "authority"), args)
            drill.tool("node", "--import", "tsx", "scripts/restore.ts",
                       "/drill/old-backup", "/drill/restored", AUTHORITY,
                       authority="restore-authority")
            args = docker.call_args.args
            self.assertIn(drill.mount(AUTHORITY, "restore-authority"), args)
            self.assertNotIn(drill.mount(AUTHORITY, "authority"), args)

    def test_failure_evidence_reports_fixed_phase_without_error_details(self):
        with tempfile.TemporaryDirectory() as temp:
            evidence = Path(temp) / "evidence.json"
            args = ["drill", "--ts-image", "ts", "--ts-tools-image", "tools",
                    "--go-image", "go", "--go-revision", "a" * 40,
                    "--evidence", str(evidence)]

            def fail(drill):
                drill.phase = "restore-old-backup"
                drill.record("ts-rollback-reopened-latest-pair")
                raise RuntimeError("private fixture detail")

            with patch("sys.argv", args), patch.object(Drill, "run", fail), redirect_stdout(io.StringIO()) as out:
                self.assertTrue(main())
            receipt = evidence.read_text()
            self.assertEqual(json.loads(receipt)["failure_phase"], "restore-old-backup")
            self.assertEqual(json.loads(receipt)["checks"], ["ts-rollback-reopened-latest-pair"])
            self.assertIn("restore-old-backup", out.getvalue())
            self.assertNotIn("private fixture detail", receipt + out.getvalue())

    def test_restored_data_requires_replayed_flag_and_terminal_deletion(self):
        for initial in (False, True):
            with self.subTest(initial=initial):
                drill = Drill(argparse.Namespace(), {})
                with patch.object(drill, "has_memory", return_value=False) as has_memory, \
                        patch.object(drill, "setting", return_value=not initial) as setting:
                    drill.verify_restored_data(initial, "synthetic-id")
                    has_memory.assert_called_once_with("synthetic-id")
                    setting.return_value = initial
                    with self.assertRaises(RuntimeError):
                        drill.verify_restored_data(initial, "synthetic-id")
                    self.assertEqual(drill.phase, "verify-restored-configuration")
                    has_memory.return_value = True
                    setting.reset_mock()
                    with self.assertRaises(RuntimeError):
                        drill.verify_restored_data(initial, "synthetic-id")
                    self.assertEqual(drill.phase, "verify-restored-terminal-deletion")
                    setting.assert_not_called()

    def test_selector_is_independent(self):
        workflow = (HERE.parents[2] / ".github/workflows/golang-package-preview.yml").read_text()
        proof = workflow.split("- name: Detect focused package proofs\n", 1)[1]
        proof = proof.split("        run: |\n", 1)[1].split("      - name:", 1)[0]
        proof = textwrap.dedent(proof)
        self.assertIn("github.event_name == 'push' && github.ref == 'refs/heads/feat/refactor-golang'", workflow)
        self.assertIn(f"ref: {TS_REVISION}", workflow)
        self.assertLess(workflow.index("Exercise actual packaged startup"),
                        workflow.index("Verify packaged migration and restore"))
        with tempfile.TemporaryDirectory() as temp:
            def git(*args):
                return subprocess.check_output(["git", "-C", temp, *args], text=True).strip()

            git("init", "-q")
            git("config", "user.name", "Fixture")
            git("config", "user.email", "fixture@invalid")
            git("commit", "--allow-empty", "-qm", "base")
            base = git("rev-parse", "HEAD")
            output = Path(temp) / "output"

            def select(before):
                output.unlink(missing_ok=True)
                subprocess.run(["bash", "-e", "-o", "pipefail", "-c", proof], cwd=temp,
                               env=dict(os.environ, BEFORE=before, GITHUB_OUTPUT=str(output),
                                        GITHUB_SHA=git("rev-parse", "HEAD")), check=True)
                return dict(line.split("=") for line in output.read_text().splitlines())

            files = {"browser_smoke.py": (True, False), "browser-requirements.txt": (True, False),
                     "migration_drill.py": (False, True), "test_migration_drill.py": (False, True),
                     "ordinary.py": (False, False)}
            for file, (browser, restore) in files.items():
                with self.subTest(file=file):
                    path = Path(temp) / "backend/packaging/runtime-preview" / file
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.touch()
                    git("add", str(path))
                    git("commit", "-qm", file)
                    self.assertEqual(select(base), {"browser": str(browser).lower(),
                                                    "restore": str(restore).lower()})
                    base = git("rev-parse", "HEAD")
            path = Path(temp) / ".github/workflows/golang-package-preview.yml"
            path.parent.mkdir(parents=True)
            path.touch()
            git("add", str(path))
            git("commit", "-qm", "workflow alone")
            self.assertEqual(select(base), {"browser": "false", "restore": "false"})
            base = git("rev-parse", "HEAD")
            for file in ("browser_smoke.py", "migration_drill.py"):
                path = Path(temp) / "backend/packaging/runtime-preview" / file
                path.write_text("changed")
                git("add", str(path))
            git("commit", "-qm", "both proofs")
            self.assertEqual(select(base), {"browser": "true", "restore": "true"})
            self.assertEqual(select("0" * 40), {"browser": "false", "restore": "false"})


if __name__ == "__main__":
    unittest.main()
