#!/usr/bin/env python3
"""Paired packaged mixed-load benchmark on clones of one exact TypeScript installation."""
import argparse
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import time

import mixed_load_fixture as mix
import receive_load_fixture as rx
from operator_packages import PACKAGES, load_packages
from migration_drill import DATABASE, SNOW_ACTION_PORT, SNOW_HTTP_MANIFEST, TS_REVISION, docker, require
from packaged_benchmark import MEMORY_MIB, ORDER, Paired, container_cgroup, cpu_counters, cpu_delta, go_parallelism

HERE = Path(__file__).resolve().parent

VERIFY_VISUAL = r"""
const { createHash } = require('node:crypto')
const { readdirSync } = require('node:fs')
const sharp = require('sharp')
const [directory, count, digest] = process.argv.slice(1)
;(async () => {
  const files = readdirSync(directory)
  if (files.length !== Number(count)) throw Error('vision count')
  for (const file of files) {
    const { data, info } = await sharp(directory + '/' + file).removeAlpha().toColourspace('srgb')
      .raw().toBuffer({ resolveWithObject: true })
    if (info.width !== 32 || info.height !== 24 || info.channels !== 3 ||
        createHash('sha256').update(data).digest('hex') !== digest) throw Error('vision pixels')
  }
})().catch(() => { process.exitCode = 1 })
"""

VERIFY_DURABLE = r"""
const assert = require('node:assert/strict')
const Database = require('better-sqlite3')
const [path, fixture] = process.argv.slice(1)
const { scenes, sources, senders, facts, roles } = JSON.parse(fixture)
const db = new Database(path, { readonly: true, fileMustExist: true })
try {
  const memories = db.prepare("SELECT value FROM memories WHERE json_extract(value, '$.content') = ?")
  const heads = db.prepare('SELECT count(*) AS n FROM source_event_heads WHERE scene_id = ? AND message_id = ?')
  for (const [i, sceneId] of scenes.entries()) {
    const rows = memories.all(facts[i])
    assert.equal(heads.get(sceneId, sources[i]).n, 1)
    assert.equal(rows.length, i % 2)
    if (i % 2) {
      const [source, ...extras] = JSON.parse(rows[0].value).sources
      assert.equal(extras.length, 0)
      assert.deepEqual([source.sceneId, source.messageId, source.senderId], [sceneId, sources[i], senders[i]])
    }
  }
  const calls = db.prepare('SELECT role, status, count(*) AS n FROM model_calls GROUP BY role, status').all()
  assert.equal(calls.length, Object.keys(roles).length)
  for (const row of calls) {
    assert.equal(row.status, 'completed')
    assert.equal(row.n, roles[row.role])
  }
} finally { db.close() }
"""


def verify_limits(cage):
    quota, period = (int(value) for value in (cage / "cpu.max").read_text().split())
    ram = int((cage / "memory.max").read_text())
    swap = int((cage / "memory.swap.max").read_text())
    require(quota == period and ram == MEMORY_MIB * 1024**2 and swap == 0, "unequal or unexpected owner cgroup limits")
    return {"cpu_max_usec": {"quota": quota, "period": period}, "memory_max_bytes": ram, "memory_swap_max_bytes": swap}


def setup_seed(trial):
    settings = trial.api("GET", "/api/settings")
    trial.api("PUT", "/api/settings", {**settings, **mix.FIELDS})
    trial.api("PUT", "/api/models", mix.MODELS)
    trial.seed_snowluma_credential()
    require(trial.api("PUT", "/api/plugins/snowluma/enabled", {"enabled": True}).get("ok") is True,
            "could not enable SnowLuma for synthetic seed")
    for i in range(mix.COUNT):
        trial.api("PUT", "/api/scenes/" + mix.SCENES[i], mix.scene_config(i))


class Mixed(Paired):
    fixture = "mixed_load_fixture.py"
    fields = mix.FIELDS

    def __init__(self, args, report):
        super().__init__(args, report)
        allowed = sorted(os.sched_getaffinity(0))
        require(len(allowed) >= 2, "benchmark needs distinct owner and peer CPUs")
        self.owner_cpu, self.peer_cpu = (str(core) for core in allowed[:2])
        self.limits += ("--memory-swap", f"{MEMORY_MIB}m", "--cpuset-cpus", self.owner_cpu)
        self.env.update(HE_MIX_SNOW_TOKEN=self.snow_token, HE_MIX_EVENT_TOKEN=self.snow_event_token)
        self.package = args.external_snow_package.resolve(strict=True)
        self.version = json.loads((self.package / SNOW_HTTP_MANIFEST).read_text())["version"]
        self.entries = (SNOW_HTTP_MANIFEST,)

    def start_peer(self, _):
        self.peer_started = True
        docker("run", "-d", "--name", self.peer, "--network", "none", "--cpuset-cpus", self.peer_cpu,
               "--mount", f"type=bind,source={HERE},target=/fixture,readonly",
               "--entrypoint", "python3", "-e", "PYTHONDONTWRITEBYTECODE=1",
               "-e", "HE_MIX_SNOW_TOKEN", "-e", "HE_MIX_EVENT_TOKEN", "-e", "HUMANENGINE_ADMIN_TOKEN",
               self.report["images"]["ts_tools"], "/fixture/" + self.fixture, "serve", env=self.env)
        for _ in range(50):
            if docker("exec", self.peer, "python3", "-c",
                      f"import urllib.request; urllib.request.urlopen('http://127.0.0.1:{SNOW_ACTION_PORT}/receipt',timeout=1)",
                      check=False) is not None:
                return
            time.sleep(0.1)
        raise RuntimeError("synthetic peer not ready")

    def seed_pair(self, timestamp):
        self.create(self.seed)
        self.tool("sh", "-ec", "mkdir /drill/live /drill/authority; chown 1000:1000 /drill/live /drill/authority",
                  setup=True)
        self.seed_offline()
        self.start("ts", network="container:" + self.peer, runtime_args=self.limits)
        self.seed_live(timestamp)
        self.stop()
        self.verify_pair()

    def seed_offline(self):
        pass

    def seed_live(self, timestamp):
        setup_seed(self)

    def external_settings(self, row):
        require(row["builtin"] is False and row["version"] == self.version, "wrong external package")

    def workload(self):
        return {"window_seconds": mix.WINDOW, "events_per_second": mix.RATE,
                "admin_reads": mix.READS, "order": ORDER, "external_snow_version": self.version}

    def window(self, engine, timestamp, index):
        label = f"{engine}-{index}"
        external = ("--mount", f"type=bind,source={self.package},target=/app/plugins,readonly",
                    "-e", "HUMANENGINE_PLUGINS=" + ",".join(self.entries))
        self.phase = f"start-{label}"
        self.start(engine, network="container:" + self.peer,
                   runtime_args=self.limits + (external if engine == "go" else ()))
        self.phase = f"verify-settings-{label}"
        row, = (row for row in self.api("GET", "/api/plugins") if row["id"] == "snowluma")
        require(row["enabled"] is True, "seed plugin is not enabled")
        if engine == "go":
            self.external_settings(row)
        settings = self.api("GET", "/api/settings")
        require({key: settings[key] for key in self.fields} == self.fields, "effective settings mismatch")
        self.phase = f"verify-cgroups-{label}"
        cage, affinity = container_cgroup(self.name)
        peer, peer_affinity = container_cgroup(self.peer)
        require(affinity == self.owner_cpu and peer_affinity == self.peer_cpu, "wrong CPU affinity")
        limits = verify_limits(cage)
        if engine == "go":
            limits["gomaxprocs_at_ready"] = go_parallelism(docker("logs", self.name))
        get_fixture = ("exec", self.peer, "python3", "/fixture/" + self.fixture)
        self.phase = f"measure-{label}"
        before = cpu_counters(cage)
        peer_before = cpu_counters(peer, ("cpu_usage_usec",))
        started = time.monotonic_ns()
        result = json.loads(docker(*get_fixture, "drive", "--owner-port", self.port,
                                   "--timestamp", str(timestamp), timeout=100))
        elapsed = time.monotonic_ns() - started
        counters = cpu_delta(before, cpu_counters(cage))
        peer_counters = cpu_delta(peer_before, cpu_counters(peer, ("cpu_usage_usec",)))
        memory = {"memory_current_after_bytes": int((cage / "memory.current").read_text()),
                  "memory_peak_boot_inclusive_bytes": int((cage / "memory.peak").read_text()),
                  "memory_swap_current_bytes": int((cage / "memory.swap.current").read_text())}
        self.phase = f"verify-live-{label}"
        checked = json.loads(docker(*get_fixture, "verify", "--owner-port", self.port, timeout=45))
        self.phase = f"verify-extra-{label}"
        self.verify_extra()
        self.phase = f"drain-{label}"
        self.stop()
        self.phase = f"verify-durable-{label}"
        durable = self.verify_durable()
        self.verify_pair()
        return dict(engine=engine, **result, **checked, **durable, driver_invocation_elapsed_ns=elapsed,
                    owner_cgroup={"leader_cpu_affinity": affinity, **limits, **counters, **memory},
                    peer_cgroup={"leader_cpu_affinity": peer_affinity, **peer_counters})

    def verify_extra(self):
        docker("exec", self.peer, "node", "-e", VERIFY_VISUAL, "/tmp/mixed-vision", str(mix.COUNT // 2), mix.PIXELS)

    def verify_durable(self):
        self.tool("node", "-e", VERIFY_DURABLE, DATABASE,
                  json.dumps(dict(scenes=mix.SCENES, sources=mix.SOURCES, senders=mix.SENDERS,
                                  facts=mix.FACTS, roles=mix.ROLES)), data="live")
        return dict(durable_facts=mix.COUNT // 2, decoded_images=mix.COUNT // 2,
                    durable_completed_model_calls=mix.ROLES)

    def run(self):
        self.report["images"] = {
            "ts": self.image(self.args.ts_image, TS_REVISION),
            "ts_tools": self.image(self.args.ts_tools_image, TS_REVISION),
            "go": self.image(self.args.go_image, self.args.go_revision),
        }
        self.report["runner"] = {
            "architecture": platform.machine(), "kernel": platform.release(),
            "logical_cpus": os.cpu_count(), "image_os": os.getenv("ImageOS"), "image_version": os.getenv("ImageVersion"),
            "owner_cpu": self.owner_cpu, "peer_cpu": self.peer_cpu,
            "cpu_scope": "driver invocation, including process startup and 250 ms lead-in, excluding verification",
            "memory_scope": "raw cgroup high-water from boot; includes charged cache, unlike process RSS or docker stats",
        }
        self.report["workload"] = self.workload()
        require(Path("/sys/fs/cgroup/cgroup.controllers").is_file(), "benchmark requires host cgroup v2")
        timestamp = int(time.time())
        self.phase = "start-isolated-peer"
        self.start_peer(timestamp)
        self.phase = "seed-exact-typescript"
        self.seed_pair(timestamp)
        for index, engine in enumerate(ORDER, 1):
            self.phase = f"clone-trial-{index}"
            self.clone(self.name + f"-trial-{index}")
            self.report["windows"].append(self.window(engine, timestamp, index))
            docker("volume", "rm", self.active_volume)
            self.created.remove(self.active_volume)


SEED_RECEIVE_METADATA = r"""
const Database = require('better-sqlite3')
const {createHash} = require('node:crypto')
const db = new Database(process.argv[1])
try {
  db.exec('CREATE TABLE meme_assets(id TEXT PRIMARY KEY, value TEXT NOT NULL)')
  const insert = db.prepare('INSERT INTO meme_assets VALUES (?,?)')
  db.transaction(() => {
    for (let n = 0; n < Number(process.argv[2]); n++) {
      const id = createHash('sha256').update('receive-only-asset-' + n).digest('hex')
      insert.run(id, JSON.stringify({id, format:'png', bytes:100, width:8, height:8, frames:1, sources:[],
        tags:{appearance:[], style:[], subject:[], emotion:[], intent:[], tone:[], intensity:[], memeMeaning:[], ocr:''},
        analyzed:false, available:false, shared:false, privacyReviewed:false, favorite:false, disabled:false,
        preference:0, usageCount:0, createdAt:'2026-01-01T00:00:00Z'}))
    }
  })()
} finally { db.close() }
"""

VERIFY_RECEIVE = r"""
const assert = require('node:assert/strict')
const Database = require('better-sqlite3')
const [path, sceneArg, first, events, recalls, memes] = process.argv.slice(1)
const scenes = JSON.parse(sceneArg)
const db = new Database(path, {readonly:true, fileMustExist:true})
const id = (n) => {
  const scene = scenes[(scenes.length + n % scenes.length) % scenes.length]
  return [scene, scene + ':m:' + Buffer.from(JSON.stringify(String(Number(first) + n))).toString('base64url')]
}
try {
  const heads = db.prepare('SELECT scene_id, message_id FROM source_event_heads').raw().all()
  const tombstones = db.prepare('SELECT scene_id, message_id FROM recall_tombstones').raw().all()
  const sort = rows => rows.map(x => JSON.stringify(x)).sort()
  assert.deepEqual(sort(tombstones), sort(Array.from({length:Number(recalls)}, (_, n) => id(n - Number(recalls)))))
  const recalled = new Set(sort(tombstones))
  const ingress = heads.filter(row => !recalled.has(JSON.stringify(row)))
  assert.deepEqual(sort(ingress), sort(Array.from({length:Number(events)}, (_, n) => id(n))))
  assert.equal(db.prepare('SELECT count(*) AS n FROM meme_assets').get().n, Number(memes))
  assert.equal(db.prepare('SELECT count(*) AS n FROM model_calls').get().n, 0)
} finally { db.close() }
"""


class ReceiveOnly(Mixed):
    fixture = "receive_load_fixture.py"
    fields = dict(timezone="UTC", sourceEventRevisionsEnabled=True, debugModelIO=False)

    def __init__(self, args, report):
        super().__init__(args, report)
        self.entries = tuple(PACKAGES.values())
        self.report["external_packages"] = load_packages(self.package)

    def seed_offline(self):
        self.tool("node", "-e", SEED_RECEIVE_METADATA, DATABASE, str(rx.MEMES), data="live")

    def seed_live(self, timestamp):
        self.api("PUT", "/api/settings", {**self.api("GET", "/api/settings"), **self.fields})
        self.api("PUT", "/api/models", mix.MODELS)
        self.seed_snowluma_credential()
        require(self.api("PUT", "/api/plugins/snowluma/enabled", {"enabled": True}).get("ok") is True,
                "could not enable the receive fixture")
        for n in range(rx.SCENE_COUNT):
            self.api("PUT", "/api/scenes/" + rx.SCENES[n], rx.scene_config(n))
        docker("exec", self.peer, "python3", "/fixture/" + self.fixture,
               "recall", "--timestamp", str(timestamp), timeout=65)

    def external_settings(self, row):
        super().external_settings(row)
        rows = self.api("GET", "/api/plugins")
        require({p["id"] for p in rows} == set(PACKAGES) and len(rows) == len(PACKAGES)
                and all(p["builtin"] is False and p["apiVersion"] == 2 and p["enabled"] == (p["id"] == "snowluma")
                        for p in rows), "unexpected external-only benchmark catalog")

    def workload(self):
        return dict(scenes=rx.SCENE_COUNT, recall_tombstones=rx.RECALLS, meme_metadata=rx.MEMES,
                    text_arrivals=rx.EVENTS, events_per_burst=rx.BURST,
                    burst_interval_ms=rx.INTERVAL_NS / 1e6, admin_reads_per_route=50, order=ORDER)

    def verify_extra(self):
        raw = docker("exec", self.peer, "python3", "/fixture/" + self.fixture, "receipt")
        require(json.loads(raw) == {"counts": {}, "acks": {}, "errors": 0, "observations": 0},
                "receive-only witness changed after the timing window")

    def verify_durable(self):
        self.tool("node", "-e", VERIFY_RECEIVE, DATABASE, json.dumps(rx.SCENES),
                  *(str(n) for n in (rx.FIRST_MESSAGE, rx.EVENTS, rx.RECALLS, rx.MEMES)), data="live")
        return dict(durable_arrivals=rx.EVENTS, durable_recalls=rx.RECALLS, durable_meme_metadata=rx.MEMES,
                    durable_model_calls=0)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("ts-image", "ts-tools-image", "go-image", "go-revision"):
        parser.add_argument("--" + name, required=True)
    for name in ("external-snow-package", "evidence"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--receive-only", action="store_true")
    parser.set_defaults(cpus=1)
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{40}", args.go_revision):
        parser.error("--go-revision must be a full SHA")
    report = {"kind": "paired_packaged_receive_only" if args.receive_only else "paired_packaged_mixed", "ts_revision": TS_REVISION,
              "go_revision": args.go_revision, "status": "failed", "windows": []}
    trial = None
    with args.evidence.open("x") as evidence:
        try:
            trial = (ReceiveOnly if args.receive_only else Mixed)(args, report)
            trial.run()
            report["status"] = "passed"
        except (OSError, RuntimeError, TypeError, ValueError, KeyError, subprocess.SubprocessError) as failure:
            report["failure_phase"] = trial.phase if trial else "prepare"
            report["failure_type"] = type(failure).__name__
        finally:
            if trial and not trial.cleanup():
                report.update(status="failed", cleanup_failed=True)
            evidence.write(json.dumps(report, indent=2) + "\n")
    if report["status"] != "passed":
        print("Packaged benchmark failed:", report.get("failure_phase", "cleanup"))
        return 1
    for row in report["windows"]:
        keys = ("engine", "owner_cgroup", "admin_http")
        keys += (("model_requests", "outbound_sends", "durable_arrivals", "ingress_http") if args.receive_only
                 else ("physical_ack", "durable_completed_model_calls"))
        print(json.dumps({key: row[key] for key in keys}))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
