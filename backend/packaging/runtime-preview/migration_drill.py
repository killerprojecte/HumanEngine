#!/usr/bin/env python3
"""Opt-in packaged TS→Go→TS and old-backup restore using only disposable Docker state."""
import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import time
from uuid import uuid4

TS_REVISION = "39d8deb8922684298bab8ecb139be04555e8474a"
DATABASE = "/app/data/humanengine.sqlite"
AUTHORITY = "/app/authority"
NODE_REQUEST = """
const {method,path,body} = JSON.parse(require('fs').readFileSync(0, 'utf8'));
fetch('http://127.0.0.1:' + process.argv[1] + path, {
  method, headers: {'Content-Type':'application/json',
    Authorization:'Bearer ' + process.env.HUMANENGINE_ADMIN_TOKEN},
  body: body === null ? undefined : JSON.stringify(body),
  signal: AbortSignal.timeout(5000)
}).then(async r => console.log(JSON.stringify([r.status, await r.json()])))
  .catch(() => process.exitCode = 1);
"""


def docker(*args, body=None, env=None, check=True, timeout=90):
    # Neither a child exception nor its raw output belongs in the receipt.
    result = subprocess.run(["docker", *args], input=body, env=env, text=True,
                            capture_output=True, timeout=timeout)
    if check and result.returncode:
        raise RuntimeError(f"Docker {args[0]} failed")
    return result.stdout.strip() if result.returncode == 0 else None


def require(condition, label):
    if not condition:
        raise RuntimeError(label)


class Drill:
    def __init__(self, args, report):
        self.args = args
        self.report = report
        self.name = "he-migration-" + uuid4().hex
        self.env = dict(os.environ, HUMANENGINE_ADMIN_TOKEN=uuid4().hex + uuid4().hex)
        self.owner = None
        self.volume = False
        self.phase = "verify-images"

    def image(self, image, revision):
        image_id, label = docker(
            "image", "inspect", "--format",
            '{{.Id}} {{index .Config.Labels "org.opencontainers.image.revision"}}',
            image).split()
        require(label == revision and re.fullmatch(r"sha256:[0-9a-f]{64}", image_id),
                "image revision or identifier mismatch")
        return image_id

    def mount(self, target, child=""):
        suffix = f",volume-subpath={child}" if child else ""
        return f"type=volume,source={self.name},target={target}{suffix}"

    def tool(self, *args, authority="authority", data=None, setup=False):
        require(self.owner is None, "offline tool started while a server owns the installation")
        mounts = ["--mount", self.mount("/drill")]
        if not setup:
            mounts += ["--mount", self.mount(AUTHORITY, authority)]
        if data:
            mounts += ["--mount", self.mount("/app/data", data)]
        user = "0:0" if setup else "1000:1000"
        docker("run", "--rm", "--name", self.name + "-tool", "--network", "none",
               "--user", user, *mounts, "--entrypoint", args[0],
               self.report["images"]["ts_tools"], *args[1:])

    def start(self, engine, data="live", authority="authority"):
        require(self.owner is None, "overlapping server owners")
        self.owner = engine
        self.port = "3000" if engine == "ts" else "3001"
        flags = []
        if engine == "go":
            flags = ["--runtime-preview", "--database", DATABASE, "--authority", AUTHORITY,
                     "--listen", "127.0.0.1:3001", "--plugin-root", "/app/plugins",
                     "--ui-root", "/opt/humanengine/ui", "--image-library",
                     "/opt/humanengine/lib/libvips-cpp.so.8.18.7", "--locale", "en-US",
                     "--source-timezone", "UTC"]
        docker("run", "-d", "--name", self.name, "--network", "none",
               "--mount", self.mount("/app/data", data),
               "--mount", self.mount(AUTHORITY, authority),
               "-e", "HUMANENGINE_ADMIN_TOKEN", self.report["images"][engine], *flags,
               env=self.env)
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            response = self.exchange("GET", "/api/status", None, check=False)
            if response and response[0] == 200:
                return
            require(docker("inspect", "-f", "{{.State.Running}}", self.name) == "true",
                    f"{engine} stopped before readiness")
            time.sleep(0.2)
        raise RuntimeError(f"{engine} readiness deadline")

    def exchange(self, method, path, body, check=True):
        result = docker("exec", "-i", self.name, "node", "-e", NODE_REQUEST, self.port,
                        body=json.dumps(dict(method=method, path=path, body=body)), check=check)
        return json.loads(result) if result is not None else None

    def api(self, method, path, body=None):
        status, data = self.exchange(method, path, body)
        require(status == 200, f"{self.owner} {method} {path.split('/')[2]} returned {status}")
        return data

    def stop(self):
        docker("kill", "--signal=TERM", self.name)
        require(docker("wait", self.name, timeout=120) == "0", f"{self.owner} exit not clean")
        if self.owner == "go":
            require('"code":"app.drained"' in docker("logs", self.name), "missing Go drain receipt")
        docker("rm", self.name)
        self.owner = None

    def setting(self, new=None):
        settings = self.api("GET", "/api/settings")
        if new is not None:
            settings["debugModelIO"] = new
            self.api("PUT", "/api/settings", settings)
            settings = self.api("GET", "/api/settings")
        return settings["debugModelIO"]

    def has_memory(self, memory_id):
        return any(row["id"] == memory_id for row in self.api("GET", "/api/memories"))

    def verify_restored_data(self, initial, memory_id):
        self.phase = "verify-restored-terminal-deletion"
        require(not self.has_memory(memory_id), "post-backup terminal deletion did not reconcile")
        self.phase = "verify-restored-configuration"
        require(self.setting() is (not initial), "post-backup debugModelIO flag did not replay")

    def record(self, label):
        self.report["checks"].append(label)

    def run(self):
        self.report["images"] = {
            "ts": self.image(self.args.ts_image, TS_REVISION),
            "ts_tools": self.image(self.args.ts_tools_image, TS_REVISION),
            "go": self.image(self.args.go_image, self.args.go_revision),
        }
        self.phase = "initialize-fixture"
        docker("volume", "create", self.name)
        self.volume = True
        self.tool("sh", "-ec",
                  "mkdir /drill/live /drill/authority /drill/restore-authority; "
                  "chown 1000:1000 /drill /drill/live /drill/authority /drill/restore-authority",
                  setup=True)
        self.phase = "seed-typescript"
        self.start("ts")
        initial = self.setting()
        memory = self.api("POST", "/api/memories", {
            "scope": {"kind": "sharedPublicKnowledge"}, "kind": "fact",
            "content": "Disposable old-backup assertion " + uuid4().hex,
        })
        memory_id = memory["id"]
        require(self.has_memory(memory_id), "TS seed assertion missing")
        self.stop()
        self.phase = "backup-typescript-data"
        self.tool("node", "--import", "tsx", "scripts/backup.ts",
                  DATABASE, "/app/data/media", "/drill/old-backup", data="live")
        self.record("ts-seed-and-authoritative-old-backup")

        self.phase = "update-in-go"
        self.start("go")
        require(self.has_memory(memory_id) and self.setting() is initial, "Go did not read TS state")
        require(self.setting(not initial) is not initial, "Go settings write not visible")
        require(self.api("DELETE", "/api/memories/" + memory_id)["ok"] is True and
                not self.has_memory(memory_id), "Go terminal memory deletion failed")
        self.stop()
        self.record("go-wrote-setting-and-terminal-deletion-then-drained")

        self.phase = "reopen-latest-in-typescript"
        self.start("ts")
        require(self.setting() is not initial and not self.has_memory(memory_id),
                "TS does not recognize latest Go installation")
        self.stop()
        self.record("ts-rollback-reopened-latest-pair")

        self.phase = "copy-current-authority"
        self.tool("sh", "-ec", "test ! -e /drill/live/main.sqlite; "
                  "cp -a /drill/authority/. /drill/restore-authority/")
        self.phase = "verify-old-and-current-heads"
        self.tool("node", "--import", "tsx", "--input-type=module", "-e", """
import Database from 'better-sqlite3'
import {metadata, RecoveryAuthority, sameHead} from './src/memory/recovery/ledger.ts'
const open = path => new Database(path, {readonly: true, fileMustExist: true})
const old = open('/drill/old-backup/humanengine.sqlite')
const live = open('/drill/live/humanengine.sqlite')
const authority = new RecoveryAuthority('/app/authority')
try {
  const before = metadata(old)
  const latest = metadata(live)
  const head = authority.head()
  const id = process.argv[1]
  const contains = database => database.prepare('SELECT 1 FROM memories WHERE id=?').get(id)
  if (before.state !== 'ready' || latest.state !== 'ready' ||
      before.head.instanceId !== head.instanceId || before.head.sequence >= head.sequence ||
      !sameHead(latest.head, head) || !contains(old) || contains(live))
    throw new Error('synthetic restore predecessors differ')
} finally {
  authority.close()
  live.close()
  old.close()
}
""", memory_id, authority="restore-authority")
        self.phase = "restore-old-backup"
        self.tool("node", "--import", "tsx", "scripts/restore.ts",
                  "/drill/old-backup", "/drill/restored", AUTHORITY,
                  authority="restore-authority")
        self.phase = "start-go-on-restored-data"
        self.start("go", data="restored", authority="restore-authority")
        self.verify_restored_data(initial, memory_id)
        self.phase = "verify-restored-model-hold"
        hold = self.api("GET", "/api/budget/recovery")
        require(hold.get("held") is True and hold.get("restorationCoverage") == "unknown"
                and hold.get("restoreId"), "restored model admission is not held")
        self.phase = "drain-restored-go"
        self.stop()
        self.phase = "verify-restored-database-path"
        self.tool("sh", "-ec", "test ! -e /drill/restored/main.sqlite",
                  authority="restore-authority")
        self.record("restore-current-authority-replayed-configuration-and-terminal-deletion-and-held-models")

    def cleanup(self):
        if self.owner is not None:
            docker("rm", "-f", self.name, check=False)
        if self.volume:
            docker("rm", "-f", self.name + "-tool", check=False)
            docker("volume", "rm", self.name, check=False)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("ts-image", "ts-tools-image", "go-image", "go-revision", "evidence"):
        parser.add_argument("--" + name, required=True)
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{40}", args.go_revision):
        parser.error("--go-revision must be a full commit SHA")
    report = {"ts_revision": TS_REVISION, "go_revision": args.go_revision,
              "checks": [], "status": "failed"}
    drill = Drill(args, report)
    try:
        drill.run()
        report["status"] = "passed"
    except (RuntimeError, ValueError, TypeError, KeyError, subprocess.SubprocessError):
        report["failure_phase"] = drill.phase
        print(f"Packaged migration drill failed in phase: {drill.phase}")
    finally:
        drill.cleanup()
        with Path(args.evidence).open("x") as evidence:
            evidence.write(json.dumps(report, indent=2) + "\n")
    return report["status"] != "passed"


if __name__ == "__main__":
    raise SystemExit(main())
