#!/usr/bin/env python3
"""Opt-in packaged TS→Go→TS and old-backup restore using only disposable Docker state."""
import argparse
from base64 import b64decode
from hashlib import sha256
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
PROVIDER = "migration-credential-fixture"
MODEL = "migration-text-fixture"
PROVIDER_PORT = 41273
SNOW_ACTION_PORT = 41274
SNOW_EVENT_PORT = 41275
# Taipei text, supplementary Han, a ZWJ emoji, and distinct decomposed/composed accents.
UNICODE_MEMORY = "\u53f0\u5317 \U00020bb7 \U0001f469\u200d\U0001f4bb e\u0301 / \u00e9"
MEDIA_PNGS = (
    "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGP4z8AARAwQCgAf7gP9i18U1AAAAABJRU5ErkJggg==",
    "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAD0lEQVR4nGNg+M8AQhAKABvyA/1tVLjHAAAAAElFTkSuQmCC",
)
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

NODE_CREDENTIAL_PROBE = """
const http = require('node:http');
const fs = require('node:fs');
async function run() {
  const {sentinel, model, providerPort} = JSON.parse(fs.readFileSync(0, 'utf8'));
  let received = 0, matched = 0;
  const mock = http.createServer(async (req, res) => {
    received++;
    try {
      const chunks = [];
      for await (const chunk of req) chunks.push(chunk);
      const {model: requested} = JSON.parse(Buffer.concat(chunks).toString('utf8'));
      if (req.method !== 'POST' || req.url !== '/v1/chat/completions' ||
          req.headers.authorization !== 'Bearer ' + sentinel || requested !== model) {
        res.writeHead(400).end();
        return;
      }
      matched++;
      res.writeHead(200, {'Content-Type':'application/json'}).end(JSON.stringify({
        choices:[{message:{role:'assistant',content:'OK'},finish_reason:'stop'}],
        usage:{prompt_tokens:1,completion_tokens:1,total_tokens:2}
      }));
    } catch { res.writeHead(400).end(); }
  });
  await new Promise((resolve, reject) =>
    mock.once('error', reject).listen(providerPort, '127.0.0.1', resolve));
  try {
    const response = await fetch('http://127.0.0.1:' + process.argv[1] +
      '/api/models/' + encodeURIComponent(model) + '/probe', {
      method: 'POST',
      headers: {'Content-Type':'application/json',
        Authorization:'Bearer ' + process.env.HUMANENGINE_ADMIN_TOKEN},
      body: JSON.stringify({capability:'text'}), signal: AbortSignal.timeout(8000)
    });
    const data = await response.json();
    const detail = data?.detail;
    console.log(JSON.stringify({
      status: response.status, ok: data?.ok === true, denied: data?.ok === false,
      text: data?.capability === 'text',
      detail: ['text_response_received','budget_restore_accounting_hold'].includes(detail)
        ? detail : 'unexpected_probe_result', received, matched
    }));
  } finally { mock.closeAllConnections(); mock.close(); }
}
run().catch(() => process.exitCode = 1);
"""


NODE_SNOWLUMA_LOGIN = """
const http = require('node:http');
const fs = require('node:fs');
async function run() {
  const {sentinel, actionPort, media} = JSON.parse(fs.readFileSync(0, 'utf8'));
  let received = 0, matched = 0;
  const mock = http.createServer((req, res) => {
    req.resume();
    if (media && req.method === 'POST' && req.url === '/get_group_info' &&
        req.headers.authorization === 'Bearer ' + sentinel) {
      res.writeHead(200, {'Content-Type':'application/json'}).end(JSON.stringify({
        status:'ok', retcode:0, data:{group_id:900, group_name:'Synthetic media group'}
      }));
      return;
    }
    received++;
    if (req.method !== 'POST' || req.url !== '/get_login_info' ||
        req.headers.authorization !== 'Bearer ' + sentinel) {
      res.writeHead(400).end();
      return;
    }
    matched++;
    res.writeHead(200, {'Content-Type':'application/json'}).end(JSON.stringify({
      status:'ok', retcode:0, data:{user_id:42}
    }));
  });
  const admin = (method, path, body) => fetch('http://127.0.0.1:' + process.argv[1] + path, {
    method, headers: {'Content-Type':'application/json',
      Authorization:'Bearer ' + process.env.HUMANENGINE_ADMIN_TOKEN},
    body: JSON.stringify(body), signal: AbortSignal.timeout(20000)
  });
  const toggle = async enabled => {
    const response = await admin('PUT', '/api/plugins/snowluma/enabled', {enabled});
    return [response.status, (await response.json())?.ok === true];
  };
  const seedMedia = async () => {
    const {createHmac} = require('node:crypto');
    const encode = value => Buffer.from(JSON.stringify(value)).toString('base64url');
    const sceneId = 'ob:s:' + encode(['snowluma', '42', 'group', '900']);
    const scenePath = '/api/scenes/' + encodeURIComponent(sceneId);
    const ids = [];
    for (let index = 0; index < media.pngs.length; index++) {
      const id = index + 1;
      const messageId = sceneId + ':m:' + encode(String(id));
      const body = JSON.stringify({post_type:'message', message_type:'group', sub_type:'normal',
        self_id:42, group_id:900, user_id:7, message_id:id, time:Math.floor(Date.now() / 1000),
        message:[{type:'text', data:{text:'Synthetic image source ' + id}}],
        sender:{user_id:7, nickname:'Synthetic sender'}});
      const signature = createHmac('sha1', media.eventToken).update(body).digest('hex');
      const admitted = await fetch('http://127.0.0.1:' + media.eventPort + '/onebot/events', {
        method:'POST', headers:{'Content-Type':'application/json', 'X-Self-ID':'42',
          'X-Signature':'sha1=' + signature},
        body, signal:AbortSignal.timeout(5000)
      });
      if (admitted.status !== 204) throw new Error('synthetic event was not admitted');
      const deadline = Date.now() + 3000;
      let ready = false;
      while (!ready && Date.now() < deadline) {
        const response = await admin('GET', scenePath + '/history?source=local');
        const history = await response.json();
        ready = response.ok && history?.items?.some(row => row.id === messageId && row.status === 'available');
        if (!ready) await new Promise(resolve => setTimeout(resolve, 50));
      }
      if (!ready) throw new Error('synthetic source was not available');
      if (index === 0) {
        const listed = await admin('GET', '/api/scenes');
        const scene = (await listed.json()).find(row => row.id === sceneId);
        const updated = await admin('PUT', scenePath, {...scene, learningEnabled:true});
        if (!updated.ok || (await updated.json())?.learningEnabled !== true)
          throw new Error('synthetic scene learning was not enabled');
      }
      const imported = await admin('POST', '/api/memes/import', {sceneId, messageId, base64:media.pngs[index]});
      const asset = await imported.json();
      if (!imported.ok || !/^[a-f0-9]{64}$/.test(asset?.id ?? '')) throw new Error('synthetic import failed');
      ids.push(asset.id);
    }
    return ids;
  };
  await new Promise((resolve, reject) =>
    mock.once('error', reject).listen(actionPort, '127.0.0.1', resolve));
  try {
    let enableStatus, enabledOK, disableStatus, disabledOK, mediaIds;
    try {
      [enableStatus, enabledOK] = await toggle(true);
      if (media && enableStatus === 200 && enabledOK) mediaIds = await seedMedia();
    }
    finally { [disableStatus, disabledOK] = await toggle(false); }
    console.log(JSON.stringify({enableStatus, enabledOK, disableStatus, disabledOK, received, matched, mediaIds}));
  } finally { mock.closeAllConnections(); mock.close(); }
}
run().catch(() => process.exitCode = 1);
"""


NODE_MEDIA_FILE = """
const {createHash} = require('node:crypto');
const {readFileSync} = require('node:fs');
const {id} = JSON.parse(readFileSync(0, 'utf8'));
async function run() {
  const response = await fetch('http://127.0.0.1:' + process.argv[1] + '/api/memes/' + id + '/file', {
    headers:{Authorization:'Bearer ' + process.env.HUMANENGINE_ADMIN_TOKEN},
    signal:AbortSignal.timeout(5000)
  });
  if (response.ok) {
    const hash = createHash('sha256').update(Buffer.from(await response.arrayBuffer())).digest('hex');
    console.log(JSON.stringify({status:response.status, type:response.headers.get('content-type'), hash}));
  } else {
    console.log(JSON.stringify({status:response.status, unavailable:(await response.json())?.error === 'Asset unavailable'}));
  }
}
run().catch(() => process.exitCode = 1);
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
        self.sentinel = uuid4().hex + uuid4().hex
        self.snow_token = uuid4().hex + uuid4().hex
        self.snow_event_token = uuid4().hex + uuid4().hex
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

    def seed_credential(self):
        self.api("PUT", "/api/models", {
            "providers": [{"id": PROVIDER, "name": "Migration fixture",
                           "protocol": "openai-chat", "baseUrl": f"http://127.0.0.1:{PROVIDER_PORT}/v1",
                           "timeoutMs": 3000, "retryCount": 0}],
            "models": [{"id": MODEL, "providerId": PROVIDER, "displayName": "Fixture",
                        "apiModelName": MODEL, "capabilities": ["text"], "streaming": False}],
            "routes": [],
            "credentials": {PROVIDER: {"action": "set", "value": self.sentinel}},
        })

    def prove_credential(self, held=False):
        models = self.api("GET", "/api/models")
        provider = next((row for row in models["providers"] if row["id"] == PROVIDER), {})
        require(provider.get("credentialConfigured") is True and not provider.get("credentialEnv")
                and "credentials" not in models and self.sentinel not in json.dumps(models),
                "stored credential not available as write-only configuration")
        outcome = self.probe_receipt(NODE_CREDENTIAL_PROBE, {
            "sentinel": self.sentinel, "model": MODEL, "providerPort": PROVIDER_PORT,
        }, timeout=15)
        calls = 0 if held else 1
        require(outcome == {
            "status": 200, "ok": not held, "denied": held, "text": True,
            "detail": "budget_restore_accounting_hold" if held else "text_response_received",
            "received": calls, "matched": calls,
        }, "synthetic credential probe did not match expected wire outcome")

    def probe_receipt(self, script, body, timeout):
        raw = docker("exec", "-i", self.name, "node", "-e", script, self.port,
                     body=json.dumps(body), timeout=timeout)
        try:
            return json.loads(raw)
        except (TypeError, ValueError):
            raise RuntimeError("invalid synthetic probe receipt") from None

    def verify_snowluma_disabled(self):
        config = self.api("GET", "/api/plugins/snowluma/config")
        plugin = next((row for row in self.api("GET", "/api/plugins")
                       if row.get("id") == "snowluma"), {})
        flags = plugin.get("configuredSecrets", {})
        serialized = json.dumps([config, plugin])
        require(plugin.get("enabled") is False and flags.get("token") is True
                and flags.get("eventToken") is True and config.get("transport") == "http"
                and config.get("accountId") == "42" and config.get("eventHost") == "127.0.0.1"
                and config.get("eventPort") == SNOW_EVENT_PORT
                and config.get("url") == f"http://127.0.0.1:{SNOW_ACTION_PORT}"
                and not (set(config) & {"token", "eventToken", "credentialEnv", "eventCredentialEnv"})
                and self.snow_token not in serialized and self.snow_event_token not in serialized,
                "SnowLuma disabled configuration did not preserve both write-only credentials")

    def seed_snowluma_credential(self):
        row = next((row for row in self.api("GET", "/api/plugins")
                    if row.get("id") == "snowluma"), {})
        require(row.get("enabled") is False, "SnowLuma must be disabled before fixture setup")
        self.api("PUT", "/api/plugins/snowluma/config", {
            "transport": "http", "url": f"http://127.0.0.1:{SNOW_ACTION_PORT}",
            "accountId": "42", "eventHost": "127.0.0.1", "eventPort": SNOW_EVENT_PORT,
            "token": self.snow_token, "eventToken": self.snow_event_token,
        })

    def prove_snowluma_login(self, seed_media=False):
        self.verify_snowluma_disabled()
        options = {"sentinel": self.snow_token, "actionPort": SNOW_ACTION_PORT}
        if seed_media:
            options["media"] = {"pngs": MEDIA_PNGS, "eventToken": self.snow_event_token,
                                "eventPort": SNOW_EVENT_PORT}
        outcome = self.probe_receipt(NODE_SNOWLUMA_LOGIN, options, timeout=60 if seed_media else 45)
        media_ids = outcome.pop("mediaIds", []) if seed_media else []
        require(outcome == {"enableStatus": 200, "disableStatus": 200,
                            "enabledOK": True, "disabledOK": True, "received": 1, "matched": 1},
                "SnowLuma login did not use its stored credential exactly once")
        self.verify_snowluma_disabled()
        if seed_media:
            require(isinstance(media_ids, list) and len(media_ids) == 2
                    and all(isinstance(value, str) and re.fullmatch(r"[a-f0-9]{64}", value) for value in media_ids)
                    and media_ids[0] != media_ids[1], "synthetic imports did not return two distinct asset IDs")
        return media_ids

    def verify_media(self, media_ids, deleted=False):
        for index, (media_id, png) in enumerate(zip(media_ids, MEDIA_PNGS, strict=True)):
            file = self.probe_receipt(NODE_MEDIA_FILE, {"id": media_id}, timeout=15)
            expected = ({"status": 400, "unavailable": True} if deleted and index == 0 else
                        {"status": 200, "type": "image/png", "hash": sha256(b64decode(png)).hexdigest()})
            require(file == expected, "media original did not match expected retained or deleted outcome")

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

    def seed_unicode_memory(self):
        return self.api("POST", "/api/memories", {
            "scope": {"kind": "sharedPublicKnowledge"}, "kind": "fact",
            "content": UNICODE_MEMORY,
        })["id"]

    def verify_unicode_memory(self, memory_id, leg):
        self.phase = f"{leg}-verify-unicode-memory"
        require(any(row["id"] == memory_id and row["content"] == UNICODE_MEMORY
                    for row in self.api("GET", "/api/memories")),
                "surviving Unicode memory ID or text changed")
        self.record(f"{leg}-unicode-memory-exact-id-and-text")

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
        unicode_memory_id = self.seed_unicode_memory()
        self.verify_unicode_memory(unicode_memory_id, "ts-seed")
        self.phase = "seed-and-probe-typescript-credential"
        self.seed_credential()
        self.prove_credential()
        self.record("typescript-stored-credential-matched-provider-wire")
        self.stop()
        self.phase = "backup-typescript-data"
        self.tool("node", "--import", "tsx", "scripts/backup.ts",
                  DATABASE, "/app/data/media", "/drill/old-backup", data="live")
        self.record("ts-seed-and-authoritative-old-backup")

        self.phase = "update-in-go"
        self.start("go")
        require(self.has_memory(memory_id) and self.setting() is initial, "Go did not read TS state")
        self.verify_unicode_memory(unicode_memory_id, "go-reopen")
        self.phase = "probe-go-credential"
        self.prove_credential()
        self.record("go-reopened-credential-matched-provider-wire")
        self.phase = "update-in-go"
        require(self.setting(not initial) is not initial, "Go settings write not visible")
        require(self.api("DELETE", "/api/memories/" + memory_id)["ok"] is True and
                not self.has_memory(memory_id), "Go terminal memory deletion failed")
        self.stop()
        self.record("go-wrote-setting-and-terminal-deletion-then-drained")

        self.phase = "reopen-latest-in-typescript"
        self.start("ts")
        require(self.setting() is not initial and not self.has_memory(memory_id),
                "TS does not recognize latest Go installation")
        self.verify_unicode_memory(unicode_memory_id, "ts-rollback")
        self.phase = "probe-rollback-typescript-credential"
        self.prove_credential()
        self.record("rollback-typescript-credential-matched-provider-wire")
        self.stop()
        self.record("ts-rollback-reopened-latest-pair")

        self.phase = "copy-current-authority"
        self.tool("sh", "-ec", "test ! -e /drill/live/main.sqlite; "
                  "cp -a /drill/authority/. /drill/restore-authority/")
        self.phase = "verify-old-and-current-heads"
        self.tool("node", "--import", "tsx", "--input-type=module", "-e", """
import Database from 'better-sqlite3'
import {readFileSync, lstatSync} from 'node:fs'
import {metadata, RecoveryAuthority, sameHead} from './src/memory/recovery/ledger.ts'
const open = path => new Database(path, {readonly: true, fileMustExist: true})
const manifest = JSON.parse(readFileSync('/drill/old-backup/manifest.json', 'utf8'))
const keyRecord = manifest.files.filter(record => record.path === '.secrets.key')
const key = lstatSync('/drill/old-backup/.secrets.key')
if (keyRecord.length !== 1 || keyRecord[0].bytes !== 32 || !key.isFile() ||
    key.size !== 32 || (key.mode & 0o077) !== 0)
  throw new Error('synthetic backup credential key manifest missing')
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
        self.verify_unicode_memory(unicode_memory_id, "restored-go")
        self.phase = "verify-restored-model-hold"
        hold = self.api("GET", "/api/budget/recovery")
        require(hold.get("held") is True and hold.get("restorationCoverage") == "unknown"
                and hold.get("restoreId"), "restored model admission is not held")
        self.phase = "probe-restored-model-hold"
        self.prove_credential(held=True)
        self.record("restored-credential-configured-model-hold-blocked-all-wire")
        self.phase = "drain-restored-go"
        self.stop()
        self.phase = "verify-restored-database-path"
        self.tool("sh", "-ec", "test ! -e /drill/restored/main.sqlite",
                  authority="restore-authority")
        self.record("restore-current-authority-replayed-configuration-and-terminal-deletion-and-held-models")
        self.run_snowluma_credential()
        self.run_media_restore()

    def run_snowluma_credential(self):
        # Enablement changes are security-state mutations. This pair must not
        # advance the authority used by the independent positive old restore.
        self.phase = "initialize-snowluma-fixture"
        self.tool("sh", "-ec",
                  "mkdir /drill/snow-live /drill/snow-authority; "
                  "chown 1000:1000 /drill/snow-live /drill/snow-authority", setup=True)
        for engine, leg in (("ts", "initial-typescript"), ("go", "go-reopen"),
                            ("ts", "rollback-typescript")):
            self.phase = f"snowluma-{leg}-startup"
            self.start(engine, data="snow-live", authority="snow-authority")
            if leg == "initial-typescript":
                self.phase = "snowluma-seed-credential"
                self.seed_snowluma_credential()
            self.phase = f"snowluma-{leg}-login"
            self.prove_snowluma_login()
            self.phase = f"snowluma-{leg}-drain"
            self.stop()
            self.record(f"snowluma-{leg}-matched-login-wire-and-disabled")

    def run_media_restore(self):
        self.phase = "initialize-media-fixture"
        self.tool("sh", "-ec",
                  "mkdir /drill/media-live /drill/media-authority /drill/media-restore-authority; "
                  "chown 1000:1000 /drill/media-live /drill/media-authority /drill/media-restore-authority",
                  setup=True)
        paths = {"data": "media-live", "authority": "media-authority"}
        self.phase = "seed-typescript-media"
        self.start("ts", **paths)
        self.seed_snowluma_credential()
        media_ids = self.prove_snowluma_login(seed_media=True)
        self.verify_media(media_ids)
        self.stop()
        self.record("typescript-imported-two-scoped-originals")

        # No connector setting or enablement changes after this snapshot.
        self.phase = "backup-typescript-media"
        self.tool("node", "--import", "tsx", "scripts/backup.ts",
                  DATABASE, "/app/data/media", "/drill/media-backup", **paths)
        self.phase = "go-reopen-and-delete-media"
        self.start("go", **paths)
        self.verify_media(media_ids)
        self.record("go-reopened-two-exact-originals")
        require(self.api("DELETE", "/api/memes/" + media_ids[0])["ok"] is True,
                "Go terminal media deletion failed")
        self.verify_media(media_ids, deleted=True)
        self.stop()
        self.phase = "typescript-reopen-go-media"
        self.start("ts", **paths)
        self.verify_media(media_ids, deleted=True)
        self.stop()
        self.record("rollback-typescript-kept-only-independent-original")

        self.phase = "copy-media-authority"
        self.tool("sh", "-ec", "cp -a /app/authority/. /drill/media-restore-authority/",
                  authority="media-authority")
        restore_authority = "media-restore-authority"
        self.phase = "verify-media-backup-and-live"
        self.tool("node", "--input-type=module", "-e", """
import assert from 'node:assert/strict'
import {createHash} from 'node:crypto'
import {existsSync, readFileSync} from 'node:fs'
const [removed, retained, removedHash, retainedHash] = process.argv.slice(1)
const root = '/drill/media-backup/'
const manifest = JSON.parse(readFileSync(root + 'manifest.json', 'utf8'))
const hash = path => createHash('sha256').update(readFileSync(path)).digest('hex')
for (const [id, expected] of [[removed, removedHash], [retained, retainedHash]]) {
  const path = 'media/' + id + '.png'
  assert.equal(manifest.files.find(row => row.path === path)?.sha256, expected)
  assert.equal(hash(root + path), expected)
}
assert.equal(existsSync('/drill/media-live/media/' + removed + '.png'), false)
assert.equal(hash('/drill/media-live/media/' + retained + '.png'), retainedHash)
""", *media_ids, *(sha256(b64decode(png)).hexdigest() for png in MEDIA_PNGS),
                  authority=restore_authority)
        self.phase = "restore-old-media-backup"
        self.tool("node", "--import", "tsx", "scripts/restore.ts",
                  "/drill/media-backup", "/drill/media-restored", AUTHORITY,
                  authority=restore_authority)
        self.phase = "go-reopen-restored-media"
        self.start("go", data="media-restored", authority=restore_authority)
        self.verify_media(media_ids, deleted=True)
        require(self.api("GET", "/api/budget/recovery").get("held") is True,
                "model admission unexpectedly allowed after media restore")
        self.stop()
        self.phase = "verify-physical-restored-media"
        self.tool("sh", "-ec",
                  'test ! -e "/drill/media-restored/media/$1.png"; '
                  'test -f "/drill/media-restored/media/$2.png"',
                  "check-media", *media_ids, authority=restore_authority)
        self.record("restore-replayed-go-media-deletion-and-retained-exact-original")

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
