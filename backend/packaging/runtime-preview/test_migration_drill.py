import argparse
from base64 import b64decode
from hashlib import sha256
import io
import json
from contextlib import redirect_stdout
import os
from pathlib import Path
import subprocess
import tempfile
import unicodedata
import unittest
from unittest.mock import patch

from migration_drill import (AUTHORITY, DATABASE, MEDIA_PNGS, MODEL, PROVIDER, PROVIDER_PORT,
                             TS_REVISION, UNICODE_MEMORY, Drill, main)

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

    def test_stored_credential_uses_write_only_mutation_and_isolated_probe(self):
        drill = Drill(argparse.Namespace(), {})
        drill.port = "3001"
        config = {"providers": [{"id": PROVIDER, "credentialConfigured": True}]}
        accepted = dict(status=200, ok=True, denied=False, text=True,
                        detail="text_response_received", received=1, matched=1)
        denied = dict(status=200, ok=False, denied=True, text=True,
                      detail="budget_restore_accounting_hold", received=0, matched=0)
        with patch.object(drill, "api", return_value=config) as api, \
                patch("migration_drill.docker", return_value=json.dumps(accepted)) as docker:
            drill.seed_credential()
            method, path, write = api.call_args.args
            self.assertEqual((method, path), ("PUT", "/api/models"))
            self.assertEqual(write["credentials"], {PROVIDER: {"action": "set", "value": drill.sentinel}})
            self.assertNotIn("credentialEnv", write["providers"][0])
            drill.prove_credential()
            api.assert_called_with("GET", "/api/models")
            args, kwargs = docker.call_args
            self.assertEqual(args[:5], ("exec", "-i", drill.name, "node", "-e"))
            self.assertEqual(args[-1], drill.port)
            self.assertNotIn(drill.sentinel, " ".join(args))
            self.assertEqual(json.loads(kwargs["body"]),
                             dict(sentinel=drill.sentinel, model=MODEL, providerPort=PROVIDER_PORT))
            docker.return_value = json.dumps(denied)
            drill.prove_credential(held=True)
            docker.return_value = json.dumps(dict(denied, received=1))
            with self.assertRaisesRegex(RuntimeError, "expected wire outcome"):
                drill.prove_credential(held=True)
            docker.return_value = drill.sentinel
            with self.assertRaisesRegex(RuntimeError, "^invalid synthetic probe receipt$") as e:
                drill.prove_credential()
            self.assertIsNone(e.exception.__cause__)
            self.assertNotIn(drill.sentinel, str(e.exception))
            api.return_value = dict(config, credentials=drill.sentinel)
            docker.reset_mock()
            with self.assertRaisesRegex(RuntimeError, "write-only configuration"):
                drill.prove_credential()
            docker.assert_not_called()

    def test_probes_cover_live_reopens_and_held_restore(self):
        drill = Drill(argparse.Namespace(ts_image="ts", ts_tools_image="tools", go_image="go",
                                        go_revision="rev"), {"checks": []})
        seen = []
        unicode_checks = []

        def prove(held=False):
            seen.append((drill.phase, held))

        def verify_unicode(memory_id, leg):
            unicode_checks.append((drill.owner, memory_id, leg))

        def start(owner, **_paths):
            drill.owner = owner

        with patch("migration_drill.docker"), patch.object(drill, "image"), patch.object(drill, "tool"), \
                patch.object(drill, "start", side_effect=start), patch.object(drill, "stop"), \
                patch.object(drill, "seed_unicode_memory", return_value="unicode-id"), \
                patch.object(drill, "verify_unicode_memory", side_effect=verify_unicode), \
                patch.object(drill, "run_snowluma_credential") as snowluma, \
                patch.object(drill, "run_media_restore") as media, \
                patch.object(drill, "run_source_recall_restore") as source_recall, \
                patch.object(drill, "has_memory", side_effect=[True, True, False, False]), \
                patch.object(drill, "setting", side_effect=[False, False, True, True]), \
                patch.object(drill, "verify_restored_data"), patch.object(drill, "prove_credential", side_effect=prove), \
                patch.object(drill, "api", side_effect=[
                    {"id": "fixture-memory"}, {}, {"ok": True},
                    {"held": True, "restorationCoverage": "unknown", "restoreId": "fixture-restore"},
                ]):
            drill.run()
            snowluma.assert_called_once_with()
            media.assert_called_once_with()
            source_recall.assert_called_once_with()
        self.assertEqual(seen, [
            ("seed-and-probe-typescript-credential", False),
            ("probe-go-credential", False),
            ("probe-rollback-typescript-credential", False),
            ("probe-restored-model-hold", True),
        ])
        self.assertEqual(unicode_checks, [
            ("ts", "unicode-id", "ts-seed"),
            ("go", "unicode-id", "go-reopen"),
            ("ts", "unicode-id", "ts-rollback"),
            ("go", "unicode-id", "restored-go"),
        ])

    def test_unicode_memory_witness_requires_exact_id_and_code_points(self):
        drill = Drill(argparse.Namespace(), {"checks": []})
        row = {"id": "unicode-id", "content": UNICODE_MEMORY}
        with patch.object(drill, "api", return_value=row) as api:
            self.assertEqual(drill.seed_unicode_memory(), row["id"])
            api.assert_called_once_with("POST", "/api/memories", {
                "scope": {"kind": "sharedPublicKnowledge"}, "kind": "fact",
                "content": UNICODE_MEMORY,
            })
            variants = [dict(row, id="changed-id"),
                        dict(row, content=unicodedata.normalize("NFC", UNICODE_MEMORY)),
                        dict(row, content=UNICODE_MEMORY.replace("\u200d", ""))]
            for different in variants:
                api.return_value = [different]
                with self.assertRaisesRegex(RuntimeError, "ID or text changed"):
                    drill.verify_unicode_memory(row["id"], "go-reopen")
            api.return_value = [row]
            drill.verify_unicode_memory(row["id"], "go-reopen")
            api.assert_called_with("GET", "/api/memories")
        self.assertEqual(drill.report["checks"], ["go-reopen-unicode-memory-exact-id-and-text"])

    def test_snowluma_lane_keeps_a_separate_pair_and_never_enables_restored_data(self):
        drill = Drill(argparse.Namespace(), {"checks": []})
        events = []

        with patch.object(drill, "tool") as setup, \
                patch.object(drill, "start", side_effect=lambda engine, **paths: events.append((engine, paths))), \
                patch.object(drill, "stop", side_effect=lambda: events.append("stop")), \
                patch.object(drill, "seed_snowluma_credential", side_effect=lambda: events.append("seed")), \
                patch.object(drill, "prove_snowluma_login", side_effect=lambda: events.append("login")):
            drill.run_snowluma_credential()
        self.assertTrue(setup.call_args.kwargs["setup"])
        self.assertIn("mkdir /drill/snow-live /drill/snow-authority", setup.call_args.args[-1])
        paths = {"data": "snow-live", "authority": "snow-authority"}
        self.assertEqual(events, [("ts", paths), "seed", "login", "stop",
                                  ("go", paths), "login", "stop", ("ts", paths), "login", "stop"])
        self.assertEqual(len(drill.report["checks"]), 3)

    def test_snowluma_login_requires_exact_good_wire_and_both_flag_checks(self):
        drill = Drill(argparse.Namespace(), {})
        good = dict(enableStatus=200, disableStatus=200, enabledOK=True, disabledOK=True,
                    received=1, matched=1)
        with patch.object(drill, "verify_snowluma_disabled") as disabled, \
                patch.object(drill, "probe_receipt", return_value=good) as probe:
            drill.prove_snowluma_login()
            self.assertEqual(disabled.call_count, 2)
            self.assertEqual(probe.call_args.args[1]["sentinel"], drill.snow_token)
            self.assertNotIn(drill.snow_event_token, json.dumps(probe.call_args.args[1]))
            for bad in (dict(good, received=2), dict(good, matched=0), dict(good, disabledOK=False)):
                probe.return_value = bad
                with self.assertRaisesRegex(RuntimeError, "stored credential exactly once"):
                    drill.prove_snowluma_login()

    def test_media_import_receipt_and_original_proof_are_exact(self):
        drill = Drill(argparse.Namespace(), {})
        ids = ["a" * 64, "b" * 64]
        good = dict(enableStatus=200, disableStatus=200, enabledOK=True, disabledOK=True,
                    received=1, matched=1)
        files = [{"status": 200, "type": "image/png", "hash": sha256(b64decode(png)).hexdigest()}
                 for png in MEDIA_PNGS]
        with patch.object(drill, "verify_snowluma_disabled"), \
                patch.object(drill, "probe_receipt", return_value=dict(good, mediaIds=ids)) as probe:
            self.assertEqual(drill.prove_snowluma_login(seed_media=True), ids)
            self.assertEqual(probe.call_args.args[1]["media"]["eventToken"], drill.snow_event_token)
            probe.return_value = dict(good, mediaIds=[ids[0], ids[0]])
            with self.assertRaisesRegex(RuntimeError, "distinct asset IDs"):
                drill.prove_snowluma_login(seed_media=True)
            unavailable = {"status": 400, "unavailable": True}
            probe.side_effect = files + [unavailable, files[1], files[0]]
            drill.verify_media(ids)
            drill.verify_media(ids, deleted=True)
            with self.assertRaisesRegex(RuntimeError, "retained or deleted outcome"):
                drill.verify_media(ids, deleted=True)

    def test_media_restore_drains_each_runtime_and_reopens_against_cloned_authority(self):
        drill = Drill(argparse.Namespace(), {"checks": []})
        ids = ["a" * 64, "b" * 64]
        events = []
        with patch.object(drill, "tool", side_effect=lambda *a, **kw: events.append(("tool", a, kw))), \
                patch.object(drill, "start", side_effect=lambda engine, **kw: events.append((engine, kw))), \
                patch.object(drill, "stop", side_effect=lambda: events.append("stop")), \
                patch.object(drill, "seed_snowluma_credential"), \
                patch.object(drill, "prove_snowluma_login", return_value=ids) as seed, \
                patch.object(drill, "verify_media", side_effect=lambda *a, **kw: events.append(("verify", kw))), \
                patch.object(drill, "api", side_effect=[{"ok": True}, {"held": True}]) as api:
            drill.run_media_restore()
        paths = {"data": "media-live", "authority": "media-authority"}
        clone = {"authority": "media-restore-authority"}
        seed.assert_called_once_with(seed_media=True)
        self.assertEqual(api.call_args_list[0].args, ("DELETE", "/api/memes/" + ids[0]))
        self.assertEqual([event for event in events if event == "stop" or event[0] in ("ts", "go", "verify")], [
            ("ts", paths), ("verify", {}), "stop",
            ("go", paths), ("verify", {}), ("verify", {"deleted": True}), "stop",
            ("ts", paths), ("verify", {"deleted": True}), "stop",
            ("go", dict(clone, data="media-restored")), ("verify", {"deleted": True}), "stop",
        ])
        backup = ("tool", ("node", "--import", "tsx", "scripts/backup.ts",
                           DATABASE, "/app/data/media", "/drill/media-backup"), paths)
        restore = ("tool", ("node", "--import", "tsx", "scripts/restore.ts",
                            "/drill/media-backup", "/drill/media-restored", AUTHORITY), clone)
        self.assertEqual(events[4], backup)
        corruption, = (index for index, event in enumerate(events)
                       if event[0] == "tool" and event[1][-2:] == (ids[1], AUTHORITY))
        self.assertEqual(events[corruption][1][:5], ("node", "--import", "tsx", "--input-type=module", "-e"))
        self.assertEqual(events[corruption][2], clone)
        self.assertLess(events.index(backup), corruption)
        self.assertLess(corruption, events.index(restore))
        self.assertLess(events.index(restore), events.index(("go", dict(clone, data="media-restored"))))
        self.assertEqual(events[-1][2], clone)
        self.assertEqual(drill.report["checks"][-2], "corrupt-media-backup-refused-without-publication-or-authority-change")
        self.assertEqual(drill.report["checks"][-1], "restore-replayed-go-media-deletion-and-retained-exact-original")

    def test_recall_uses_one_isolated_mock_and_unmodified_protected_settings(self):
        drill = Drill(argparse.Namespace(), {"checks": [], "images": {"ts_tools": "tools"}})
        drill.external_snow = ("external", Path("/temporary/standalone"), "test-version")
        trace = []
        seeded = {"scene": "synthetic", "ids": ["withdrawn", "independent"]}

        def docker(*args, **kwargs):
            trace.append(("docker", args))
            return ""

        def api(method, route, body=None):
            trace.append(("api", (method, route, body)))
            if (method, route) == ("GET", "/api/plugins"):
                return [{"id": "snowluma", "builtin": False, "enabled": True, "version": "test-version"}]
            return {"ok": True, "held": True}

        def tool(*args, **kwargs):
            trace.append(("tool", args, kwargs))

        def probe(_script, payload, **_kwargs):
            trace.append(("probe", payload["phase"]))
            return seeded

        with patch("migration_drill.docker", side_effect=docker), \
                patch.object(drill, "start", side_effect=lambda owner, **kw: trace.append((owner, kw))), \
                patch.object(drill, "stop", side_effect=lambda: trace.append(("stop",))), \
                patch.object(drill, "tool", side_effect=tool), patch.object(drill, "api", side_effect=api), \
                patch.object(drill, "seed_snowluma_credential"), patch.object(drill, "probe_receipt", side_effect=probe), \
                patch.object(drill, "verify_source_recall") as durable:
            drill.run_source_recall_restore()
            durable.assert_called_once_with(seeded, "recall-restore-authority")
        calls = [item[1] for item in trace if item[0] == "docker"]
        self.assertIn("--network", calls[0])
        self.assertEqual(calls[0][calls[0].index("--network") + 1], "none")
        self.assertEqual(calls[-1], ("rm", "-f", drill.name + "-recall"))
        self.assertEqual([item[1]["network"] for item in trace if item[0] in ("ts", "go")],
                         ["container:" + drill.name + "-recall"] * 4)
        for owner, args in (row for row in trace if row[0] in ("ts", "go")):
            if owner == "ts":
                self.assertNotIn("runtime_args", args)
                self.assertNotIn("image", args)
            else:
                self.assertEqual(args["image"], "external")
                self.assertEqual(args["runtime_args"], (
                    "--mount", "type=bind,source=/temporary/standalone,target=/app/plugins,readonly",
                    "-e", "HUMANENGINE_PLUGINS=snowluma-http.v2.json",
                ))
        self.assertEqual(trace.count(("api", ("GET", "/api/plugins", None))), 2)
        backup = next(i for i, call in enumerate(trace) if call[0] == "tool" and "scripts/backup.ts" in call[1])
        self.assertEqual(trace[backup - 1], ("stop",))
        self.assertFalse(any(row[0] == "api" and row[1][0] != "GET" for row in trace[backup:]))
        self.assertEqual([step[1] for step in trace if step[0] == "probe"],
                         ["seed", "before", "recall", "after", "after"])
        self.assertEqual(drill.report["checks"],
                         ["go-cold-source-recall-persisted-through-rollback-and-old-restore",
                          "external-snowluma-reopened-ts-credentials-and-preserved-recall"])

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
                "browser_chat_fixture.cjs": "browser", "browser_chat_fixture.py": "browser",
                "migration_drill.py": "restore", "test_migration_drill.py": "restore",
                "recall_fixture.cjs": "restore",
                "compose-rehearsal.yaml": "compose", "compose_rehearsal.py": "compose",
                "test_compose_rehearsal.py": "compose", "ordinary.py": None,
                "Dockerfile": "standalone", "build-host.sh": "standalone",
            }
            for file, proof in files.items():
                with self.subTest(file=file):
                    path = Path(temp) / "backend/packaging/runtime-preview" / file
                    path.parent.mkdir(parents=True, exist_ok=True)
                    path.touch()
                    git("add", str(path))
                    git("commit", "-qm", file)
                    self.assertEqual(select(base), {key: str(key == proof).lower()
                                                    for key in ("browser", "restore", "compose", "standalone")})
                    base = git("rev-parse", "HEAD")
            path = Path(temp) / ".github/workflows/golang-package-preview.yml"
            path.parent.mkdir(parents=True)
            path.touch()
            git("add", str(path))
            git("commit", "-qm", "workflow alone")
            self.assertEqual(select(base), {"browser": "false", "restore": "false", "compose": "false", "standalone": "true"})
            base = git("rev-parse", "HEAD")
            for file in ("browser_smoke.py", "migration_drill.py"):
                path = Path(temp) / "backend/packaging/runtime-preview" / file
                path.write_text("changed")
                git("add", str(path))
            git("commit", "-qm", "both proofs")
            self.assertEqual(select(base), {"browser": "true", "restore": "true", "compose": "false", "standalone": "false"})
            self.assertEqual(select("0" * 40), {"browser": "false", "restore": "false", "compose": "false", "standalone": "false"})

            base = git("rev-parse", "HEAD")
            source = Path(temp) / "backend/internal/externaladapter/onebot_websocket.go"
            source.parent.mkdir(parents=True)
            source.touch()
            git("add", str(source))
            git("commit", "-qm", "standalone adapter changed")
            self.assertEqual(select(base), {"browser": "false", "restore": "true", "compose": "false", "standalone": "false"})
            base = git("rev-parse", "HEAD")
            git("commit", "--allow-empty", "-qm",
                "Candidate\n\nHumanEngine-Package-Proofs: all")
            selected = git("rev-parse", "HEAD")
            git("commit", "--allow-empty", "-qm",
                "Ordinary commit\n\nHumanEngine-Package-Proofs: all\n"
                "This is an example, not an opt-in.\n\nAnother paragraph.")
            off = {"browser": "false", "restore": "false", "compose": "false", "standalone": "false"}
            self.assertEqual(select(base, sha=selected), {"browser": "true", "restore": "true", "compose": "true", "standalone": "true"})
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
