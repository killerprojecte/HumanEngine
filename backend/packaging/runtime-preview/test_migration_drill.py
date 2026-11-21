import argparse
import io
import json
from contextlib import redirect_stdout
import os
from pathlib import Path
import subprocess
import tempfile
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
        selector = HERE / "select-proofs.sh"
        self.assertIn(f"bash backend/packaging/runtime-preview/{selector.name}", workflow)
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
            branch = "refs/heads/feat/refactor-golang"

            def select(before, *, sha=None, event="push", ref=branch):
                output = subprocess.check_output(
                    ["bash", selector], cwd=temp, text=True,
                    env=dict(os.environ, BEFORE=before, GITHUB_EVENT_NAME=event,
                             GITHUB_REF=ref, GITHUB_SHA=sha or git("rev-parse", "HEAD")),
                )
                return dict(line.split("=") for line in output.splitlines())

            files = {
                "browser_smoke.py": "browser", "browser-requirements.txt": "browser",
                "migration_drill.py": "restore", "test_migration_drill.py": "restore",
                "compose-rehearsal.yaml": "compose", "compose_rehearsal.py": "compose",
                "test_compose_rehearsal.py": "compose", "ordinary.py": None,
            }
            for file, proof in files.items():
                with self.subTest(file=file):
                    path = Path(temp) / "backend/packaging/runtime-preview" / file
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.touch()
                    git("add", str(path))
                    git("commit", "-qm", file)
                    self.assertEqual(select(base), {key: str(key == proof).lower()
                                                    for key in ("browser", "restore", "compose")})
                    base = git("rev-parse", "HEAD")
            path = Path(temp) / ".github/workflows/golang-package-preview.yml"
            path.parent.mkdir(parents=True)
            path.touch()
            git("add", str(path))
            git("commit", "-qm", "workflow alone")
            self.assertEqual(select(base), {"browser": "false", "restore": "false", "compose": "false"})
            base = git("rev-parse", "HEAD")
            for file in ("browser_smoke.py", "migration_drill.py"):
                path = Path(temp) / "backend/packaging/runtime-preview" / file
                path.write_text("changed")
                git("add", str(path))
            git("commit", "-qm", "both proofs")
            self.assertEqual(select(base), {"browser": "true", "restore": "true", "compose": "false"})
            self.assertEqual(select("0" * 40), {"browser": "false", "restore": "false", "compose": "false"})

            base = git("rev-parse", "HEAD")
            git("commit", "--allow-empty", "-qm",
                "Candidate\n\nHumanEngine-Package-Proofs: all")
            selected = git("rev-parse", "HEAD")
            git("commit", "--allow-empty", "-qm",
                "Ordinary commit\n\nHumanEngine-Package-Proofs: all\n"
                "This is an example, not an opt-in.\n\nAnother paragraph.")
            off = {"browser": "false", "restore": "false", "compose": "false"}
            self.assertEqual(select(base, sha=selected), {"browser": "true", "restore": "true", "compose": "true"})
            self.assertEqual(select(base), off)
            for event, ref, before in [
                ("pull_request", branch, ""),
                ("workflow_dispatch", branch, ""),
                ("push", "refs/heads/master", base),
                ("push", branch, "0" * 40),
            ]:
                with self.subTest(event=event, ref=ref, before=before):
                    self.assertEqual(select(before, sha=selected, event=event, ref=ref), off)


if __name__ == "__main__":
    unittest.main()
