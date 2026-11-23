#!/usr/bin/env python3
"""Opt-in paired HTTP benchmark with one owner per disposable TS seed clone."""
import argparse
import json
import os
from pathlib import Path
import platform
import re
import subprocess
import time

from backend_benchmark import IDS, SCENE, bounded
from migration_drill import AUTHORITY, DATABASE, SNOW_ACTION_PORT, SNOW_EVENT_PORT, TS_REVISION, Drill, docker, require

HERE = Path(__file__).resolve().parent
ORDER = ("ts", "go", "go", "ts")
MEMORY_MIB = 1536
CPU_COUNTERS = {"cpu_usage_usec": "usage_usec", "cpu_throttled_usec": "throttled_usec",
                "cpu_nr_periods": "nr_periods", "cpu_nr_throttled": "nr_throttled"}
VERIFY_PAIR = r"""
import Database from 'better-sqlite3'
import { metadata, RecoveryAuthority, sameHead } from './src/memory/recovery/ledger.ts'
const db = new Database(process.argv[1], {readonly: true, fileMustExist: true})
const authority = new RecoveryAuthority(process.argv[2], undefined, true)
try {
  const gate = metadata(db)
  if (gate?.state !== 'ready' || gate.authority !== process.argv[2] || !sameHead(gate.head, authority.head()))
    throw new Error('benchmark_seed_head_mismatch')
} finally { authority.close(); db.close() }
"""


def owner_cgroup(name):
    ident, pid = docker("inspect", "--format", "{{.Id}} {{.State.Pid}}", name).split()
    require(re.fullmatch(r"[0-9a-f]{64}", ident) and pid.isdecimal() and int(pid) > 0,
            "benchmark owner process unavailable")
    proc = Path("/proc") / pid
    lines = (proc / "cgroup").read_text().splitlines()
    matches = [line.removeprefix("0::") for line in lines if line.startswith("0::/")]
    require(len(matches) == 1 and ident in Path(matches[0]).name, "benchmark requires a local cgroup v2 owner")
    relative = Path(matches[0].lstrip("/"))
    require(".." not in relative.parts, "invalid owner cgroup")
    path = Path("/sys/fs/cgroup") / relative
    require((path / "cgroup.procs").is_file(), "benchmark owner cgroup missing")
    affinity = next((line.split(":", 1)[1].strip() for line in (proc / "status").read_text().splitlines()
                     if line.startswith("Cpus_allowed_list:")), "")
    require(re.fullmatch(r"\d+(?:-\d+)?(?:,\d+(?:-\d+)?)*", affinity), "benchmark owner affinity missing")
    return path, affinity


def go_parallelism(logs):
    ready = []
    for line in logs.splitlines():
        _, found, record = line.partition(" | ")
        if found and '"code":"server.listening"' in record:
            event = json.loads(record)
            if event["code"] == "server.listening":
                ready.append(event["gomaxprocs"])
    require(len(ready) == 1 and type(ready[0]) is int and ready[0] > 0,
            "expected one Go startup scheduler receipt")
    return ready[0]


def counters(root):
    cpu = dict(line.split() for line in (root / "cpu.stat").read_text().splitlines())
    return {**{key: int(cpu[field]) for key, field in CPU_COUNTERS.items()},
            "memory_current_bytes": int((root / "memory.current").read_text()),
            "memory_peak_boot_inclusive_bytes": int((root / "memory.peak").read_text())}


class Paired(Drill):
    def __init__(self, args, report):
        super().__init__(args, report)
        self.peer = self.name + "-peer"
        self.seed = self.name + "-seed"
        self.active_volume = self.seed
        self.created = []
        self.peer_started = False
        self.limits = ("--no-healthcheck", "--cpus", str(args.cpus), "--memory", f"{MEMORY_MIB}m")

    def mount(self, target, child=""):
        suffix = f",volume-subpath={child}" if child else ""
        return f"type=volume,source={self.active_volume},target={target}{suffix}"

    def create(self, name):
        docker("volume", "create", name)
        self.created.append(name)

    def verify_pair(self):
        # TS tools independently inspect the copied installation while no writer owns it.
        self.tool("node", "--import", "tsx", "--input-type=module", "-e", VERIFY_PAIR,
                  DATABASE, AUTHORITY, data="live")

    def start_peer(self, timestamp):
        script = (HERE / "recall_fixture.cjs").read_text()
        fixture = json.dumps({"actionPort": SNOW_ACTION_PORT, "token": self.snow_token, "timestamp": timestamp})
        self.peer_started = True
        docker("run", "-d", "--name", self.peer, "--network", "none",
               "--mount", f"type=bind,source={HERE / 'backend_benchmark.py'},target=/benchmark.py,readonly",
               "--entrypoint", "node", "-e", "HE_RECALL_MOCK", "-e", "HUMANENGINE_ADMIN_TOKEN",
               self.report["images"]["ts_tools"], "-e", script, "mock",
               env={**self.env, "HE_RECALL_MOCK": fixture})
        for _ in range(50):
            ready = docker("exec", self.peer, "node", "-e",
                           f"fetch('http://127.0.0.1:{SNOW_ACTION_PORT}/ready')"
                           ".then(r=>{if(!r.ok)process.exitCode=1}).catch(()=>process.exitCode=1)",
                           check=False)
            if ready is not None:
                return
            time.sleep(0.1)
        raise RuntimeError("benchmark peer readiness")

    def require_history(self, phase, timestamp):
        seeded = self.probe_receipt((HERE / "recall_fixture.cjs").read_text(), {
            "phase": phase, "eventPort": SNOW_EVENT_PORT,
            "eventToken": self.snow_event_token, "timestamp": timestamp,
        }, timeout=25)
        require(seeded == {"scene": SCENE, "ids": list(IDS)}, "benchmark fixture history differs")

    def seed_pair(self, timestamp):
        self.create(self.seed)
        self.tool("sh", "-ec",
                  "mkdir /drill/live /drill/authority; chown 1000:1000 /drill/live /drill/authority",
                  setup=True)
        self.start("ts", network="container:" + self.peer, runtime_args=self.limits)
        settings = self.api("GET", "/api/settings")
        settings.update(timezone="UTC", sourceEventRevisionsEnabled=True, debugModelIO=False)
        self.api("PUT", "/api/settings", settings)
        self.seed_snowluma_credential()
        require(self.api("PUT", "/api/plugins/snowluma/enabled", {"enabled": True}).get("ok") is True,
                "benchmark SnowLuma enablement")
        self.api("PUT", "/api/scenes/" + SCENE, {
            "id": SCENE, "kind": "group", "adapterId": "snowluma", "accountId": "42",
            "platformSceneId": "900", "name": "Synthetic recall group",
            "participationEnabled": False, "learningEnabled": False, "proactiveEnabled": False,
        })
        self.require_history("seed", timestamp)
        self.stop()
        self.verify_pair()

    def clone(self, name):
        require(self.owner is None, "clone attempted with an active owner")
        self.create(name)
        docker("run", "--rm", "--name", self.name + "-clone", "--network", "none",
               "--user", "0:0", "--mount", f"type=volume,source={self.seed},target=/seed,readonly",
               "--mount", f"type=volume,source={name},target=/clone", "--entrypoint", "sh",
               self.report["images"]["ts_tools"], "-ec",
               "cp -a /seed/live /seed/authority /clone/; "
               "diff -qr /seed/live /clone/live; diff -qr /seed/authority /clone/authority")
        self.active_volume = name
        self.verify_pair()

    def window(self, engine, timestamp, index):
        label = f"{engine}-{index}"
        self.phase = f"start-{label}"
        self.start(engine, network="container:" + self.peer, runtime_args=self.limits)
        self.phase = f"qualify-history-{label}"
        self.require_history("before", timestamp)
        self.phase = f"cgroup-before-{label}"
        cage, affinity = owner_cgroup(self.name)
        quota, period = [int(value) for value in (cage / "cpu.max").read_text().split()]
        require(quota == self.args.cpus * period and int((cage / "memory.max").read_text()) == MEMORY_MIB * 1024**2,
                "unexpected cgroup CPU or memory limit")
        cpu = {"cpu_max_usec": {"quota": quota, "period": period}, "leader_cpu_affinity": affinity}
        if engine == "go":
            cpu["gomaxprocs_at_ready"] = go_parallelism(docker("logs", self.name))
        before = counters(cage)
        self.phase = f"measure-{label}"
        receipt = json.loads(docker("exec", self.peer, "python3", "/benchmark.py", "--single", engine,
                                    "--fixture-timestamp", str(timestamp), "--window", str(self.args.window),
                                    "--warmup", str(self.args.warmup), "--rate", str(self.args.rate),
                                    "--timeout", str(self.args.timeout), timeout=240))
        self.phase = f"cgroup-after-{label}"
        after = counters(cage)
        deltas = {key: after[key] - before[key] for key in CPU_COUNTERS}
        require(all(delta >= 0 for delta in deltas.values()), "owner counters regressed")
        self.phase = f"drain-{label}"
        self.stop()
        self.phase = f"compare-receipt-{label}"
        windows = receipt["windows"]
        require(receipt["status"] == "passed" and len(windows) == 1 and windows[0]["engine"] == engine,
                "benchmark engine receipt")
        digest = receipt["settings_sha256"]
        require(re.fullmatch(r"[0-9a-f]{64}", digest), "benchmark missing projection digest")
        return digest, receipt["configuration"], {
            **windows[0], "owner_cgroup": {
                "scope": "includes preflight and warmup; Docker inspect and cgroup reads outside latency window",
                **cpu, **deltas,
                "memory_current_after_bytes": after["memory_current_bytes"],
                "memory_peak_boot_inclusive_bytes": after["memory_peak_boot_inclusive_bytes"],
            },
        }

    def run(self):
        self.report["images"] = {
            "ts": self.image(self.args.ts_image, TS_REVISION),
            "ts_tools": self.image(self.args.ts_tools_image, TS_REVISION),
            "go": self.image(self.args.go_image, self.args.go_revision),
        }
        self.report["runner"] = {"architecture": platform.machine(), "kernel": platform.release(),
                                 "logical_cpus": os.cpu_count(), "image_os": os.getenv("ImageOS"),
                                 "image_version": os.getenv("ImageVersion"),
                                 "limits": {"cpus": self.args.cpus, "memory_mib": MEMORY_MIB, "healthcheck": False}}
        require(Path("/sys/fs/cgroup/cgroup.controllers").is_file(), "benchmark requires host cgroup v2")
        timestamp = int(time.time())
        self.phase = "start-loopback-peer"
        self.start_peer(timestamp)
        self.phase = "seed-pinned-typescript-pair"
        self.seed_pair(timestamp)
        settings_hash = configuration = None
        for index, engine in enumerate(ORDER, 1):
            self.phase = f"clone-trial-{index}"
            self.clone(self.name + f"-trial-{index}")
            digest, config, result = self.window(engine, timestamp, index)
            if index == 1:
                settings_hash, configuration = digest, config
            require(digest == settings_hash and config == configuration,
                    "TS/Go benchmark settings or workload differ")
            self.report["windows"].append(result)
            docker("volume", "rm", self.active_volume)
            self.created.remove(self.active_volume)
        self.report["configuration"] = configuration
        self.report["settings_sha256"] = settings_hash

    def cleanup(self):
        # Fixed names belong only to this invocation. Release owners before namespaces/volumes.
        failed = False

        def remove(*args, required):
            nonlocal failed
            try:
                output = docker(*args, check=False, timeout=20)
                failed |= required and output is None
            except (OSError, subprocess.SubprocessError):
                failed = True

        if self.owner is not None:
            remove("rm", "-f", self.name, required=True)
        for suffix in ("-tool", "-clone"):
            remove("rm", "-f", self.name + suffix, required=False)
        if self.peer_started:
            remove("rm", "-f", self.peer, required=True)
        for name in reversed(self.created):
            remove("volume", "rm", name, required=True)
        return not failed


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("ts-image", "ts-tools-image", "go-image", "go-revision", "evidence"):
        parser.add_argument("--" + name, required=True)
    for name, low, high, default in (("window", 1, 120, 20), ("warmup", 1, 30, 2),
                                     ("rate", 2, 30, 6), ("timeout", 1, 10, 3)):
        parser.add_argument("--" + name, type=bounded(low, high), default=default)
    parser.add_argument("--cpus", type=int, choices=(1, 2, 4), default=2,
                        help="identical Docker CPU bandwidth quota for both engines")
    args = parser.parse_args()
    if not re.fullmatch(r"[0-9a-f]{40}", args.go_revision):
        parser.error("--go-revision must be a full SHA")
    report = {"kind": "paired_packaged_http", "ts_revision": TS_REVISION,
              "go_revision": args.go_revision, "status": "failed", "windows": []}
    trial = Paired(args, report)
    with Path(args.evidence).open("x") as evidence:
        try:
            trial.run()
            report["status"] = "passed"
        except (OSError, RuntimeError, TypeError, ValueError, KeyError, subprocess.SubprocessError):
            report["failure_phase"] = trial.phase
        finally:
            if not trial.cleanup():
                report.update(status="failed", cleanup_failed=True)
            evidence.write(json.dumps(report, indent=2) + "\n")
    return int(report["status"] != "passed")


if __name__ == "__main__":
    raise SystemExit(main())
