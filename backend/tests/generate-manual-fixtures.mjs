// Source oracle: actual unmodified TS owners, SQLite transactions and projections.
import assert from "node:assert/strict";
import crypto from "node:crypto";
import { syncBuiltinESMExports } from "node:module";
import { fileURLToPath, pathToFileURL } from "node:url";
import { resolve } from "node:path";
import { writeFileSync, readFileSync, mkdirSync } from "node:fs";
const root = resolve(process.env.HUMANENGINE_SOURCE ?? process.cwd());
assert.equal(process.version, "v24.22.1");
assert.equal(Intl.DateTimeFormat().resolvedOptions().timeZone, "UTC");
const output = resolve(
  process.env.ORACLE_OUTPUT ??
    `${root}/backend/internal/memorymanual/testdata/manual-source.json`,
);
let sequence = 0;
crypto.randomUUID = () =>
  `00000000-0000-4000-8000-${String(++sequence).padStart(12, "0")}`;
syncBuiltinESMExports();
const NativeDate = Date;
class FixedDate extends NativeDate {
  constructor(...args) {
    super(...(args.length ? args : ["2026-10-28T12:00:00.000Z"]));
  }
  static now() {
    return 1793188800000;
  }
}
globalThis.Date = FixedDate;
const { Store } = await import(pathToFileURL(`${root}/src/store.ts`));
const { MemoryService } = await import(pathToFileURL(`${root}/src/memory.ts`));
const { ProposalCommand } = await import(
  pathToFileURL(`${root}/src/memory/write/proposal-command.ts`)
);

const cases = [];
function scenario(name, run) {
  const store = new Store(":memory:");
  store.saveScene({
    id: "s",
    kind: "group",
    adapterId: "synthetic",
    accountId: "bot",
    platformSceneId: "s",
    name: "Synthetic",
    participationEnabled: true,
    learningEnabled: true,
    proactiveEnabled: false,
  });
  const memory = new MemoryService(store),
    steps = [];
  const names = [
    "memories",
    "memory_sources",
    "memory_supports",
    "memory_vectors",
    "memory_deleted",
    "memory_assertion_state",
    "memory_assertion_revisions",
    "memory_assertion_revision_sources",
    "memory_assertion_revision_parents",
    "memory_projection_outbox",
    "memory_fts",
    "memory_person_projection_state",
    "memory_recovery_intents",
    "memory_command_receipts",
    "scenes",
    "recall_tombstones",
  ];
  const snapshot = () =>
    Object.fromEntries(
      names
        .filter((n) =>
          store.db.prepare("SELECT 1 FROM sqlite_master WHERE name=?").get(n),
        )
        .map((n) => [n, store.db.prepare(`SELECT * FROM ${n}`).all()]),
    );
  const call = (operation, ...args) => {
    const before = snapshot();
    let result, error;
    try {
      result = memory[operation](...args);
    } catch (e) {
      error = e.message;
    }
    steps.push({ operation, args, before, after: snapshot(), result, error });
    return result;
  };
  run(call, memory, store);
  cases.push({ name, steps });
  store.close();
}
const input = (content = "Synthetic manual") => ({
  scope: { kind: "scene", sceneId: "s", personId: "p" },
  content,
  kind: "fact",
});
scenario("manual-create-update-lock", (call) => {
  const m = call("create", input("  Synthetic manual  "));
  call("lock", m.id, true);
  call("lock", m.id, false);
  call("update", m.id, { importance: "high" });
  call("update", m.id, { content: "Synthetic reviewed replacement" });
  call("update", m.id, { status: "invalidated" });
  call("update", "missing", { locked: true });
});
scenario("guarded-version-replay", (call) => {
  const m = call("create", input());
  call(
    "updateGuarded",
    m.id,
    { locked: true },
    { expectedVersion: 1, expectedDeletionEpoch: 0, idempotencyKey: "one" },
  );
  call(
    "updateGuarded",
    m.id,
    { locked: true },
    { expectedVersion: 1, expectedDeletionEpoch: 0, idempotencyKey: "one" },
  );
  call(
    "updateGuarded",
    m.id,
    { locked: false },
    { expectedVersion: 1, expectedDeletionEpoch: 0, idempotencyKey: "one" },
  );
  call(
    "updateGuarded",
    m.id,
    { locked: false },
    { expectedVersion: 1, expectedDeletionEpoch: 0, idempotencyKey: "two" },
  );
  call("update", m.id, { importance: "high" });
  call(
    "updateGuarded",
    m.id,
    { locked: true },
    { expectedVersion: 1, expectedDeletionEpoch: 0, idempotencyKey: "one" },
  );
});
scenario("invalid-guards", (call) => {
  const m = call("create", input());
  for (const guard of [
    { expectedVersion: 1, expectedDeletionEpoch: 0, idempotencyKey: "" },
    { expectedVersion: 1.5, expectedDeletionEpoch: 0, idempotencyKey: "x" },
    { expectedVersion: 1, expectedDeletionEpoch: 1, idempotencyKey: "x" },
    {
      expectedVersion: 1,
      expectedDeletionEpoch: 0,
      idempotencyKey: "x".repeat(161),
    },
  ])
    call("updateGuarded", m.id, { locked: true }, guard);
});
scenario("create-validation", (call) => {
  call("create", input(""));
  call("create", { ...input(), confidence: 2 });
  call("create", {
    ...input(),
    sources: [
      { sceneId: "s", messageId: "m", senderId: "p" },
      { sceneId: "s", messageId: "m", senderId: "p" },
    ],
  });
  call("create", input());
});
scenario("manual-promotion-rejected", (call) => {
  const m = call("create", input());
  call("promotePublic", { sceneId: "s", simulation: false }, m.id);
  call("promotePublic", { sceneId: "s", simulation: true }, m.id);
  call("promotePublic", { sceneId: "s", simulation: false }, "missing");
});
scenario("learned-lock-review-promotion", (call, memory) => {
  const ctx = {
    sceneId: "s",
    simulation: false,
    provenance: "source-only",
    sourceEvidence: [
      {
        sceneId: "s",
        messageId: "m",
        senderId: "p",
        available: true,
        isBot: false,
      },
    ],
  };
  const r = call("propose", ctx, {
    content: "Synthetic public technical fact",
    kind: "fact",
    sources: [{ sceneId: "s", messageId: "m", senderId: "p" }],
  });
  assert.equal(r.status, "saved");
  call("lock", r.memory.id, true);
  call("lock", r.memory.id, false);
  call("promotePublic", ctx, r.memory.id);
});
const learnedContext = {
  sceneId: "s",
  simulation: false,
  provenance: "source-only",
  sourceEvidence: [
    {
      sceneId: "s",
      messageId: "m",
      senderId: "p",
      available: true,
      isBot: false,
    },
  ],
};
const learnedCandidate = {
  content: "Synthetic technical reference",
  kind: "fact",
  sources: [{ sceneId: "s", messageId: "m", senderId: "p" }],
};
scenario("review-preserves-explicit-content", (call, memory) => {
  const r = memory.propose(learnedContext, learnedCandidate);
  call("update", r.memory.id, {});
  call("update", r.memory.id, { locked: true });
  call("update", r.memory.id, { content: r.memory.content });
  call("update", r.memory.id, { validUntil: null });
  call("update", r.memory.id, { confidence: 0 });
});
scenario("review-invalidated-return-vs-current", (call, memory, store) => {
  const r = memory.propose(learnedContext, learnedCandidate);
  store.db
    .prepare("DELETE FROM memory_supports WHERE memory_id=?")
    .run(r.memory.id);
  call("update", r.memory.id, { importance: "high" });
  call(
    "updateGuarded",
    r.memory.id,
    { content: r.memory.content },
    { expectedVersion: 1, expectedDeletionEpoch: 1, idempotencyKey: "review" },
  );
});
scenario("recalled-create", (call, memory, store) => {
  store.db
    .prepare("INSERT INTO recall_tombstones VALUES(?,?,?)")
    .run("s", "m", "2026-10-28T12:00:00.000Z");
  call("create", {
    ...input(),
    sources: [{ sceneId: "s", messageId: "m", senderId: "p" }],
  });
});
scenario("guard-unicode-and-order", (call) => {
  const m = call("create", input());
  call(
    "updateGuarded",
    m.id,
    { locked: true },
    {
      expectedVersion: 1,
      expectedDeletionEpoch: 0,
      idempotencyKey: "😀".repeat(80),
    },
  );
  call(
    "updateGuarded",
    m.id,
    { locked: false },
    {
      expectedVersion: 2,
      expectedDeletionEpoch: 0,
      idempotencyKey: "😀".repeat(81),
    },
  );
  call(
    "updateGuarded",
    m.id,
    { locked: false, importance: "high" },
    { expectedVersion: 2, expectedDeletionEpoch: 0, idempotencyKey: "order" },
  );
  call(
    "updateGuarded",
    m.id,
    { importance: "high", locked: false },
    { expectedVersion: 2, expectedDeletionEpoch: 0, idempotencyKey: "order" },
  );
});
for (const variant of [
  "disabled",
  "locked",
  "person",
  "validUntil",
  "kind",
  "recalled",
  "duplicate",
  "deleted",
  "parent",
])
  scenario(`promotion-${variant}`, (call, memory, store) => {
    const r = memory.propose(learnedContext, learnedCandidate),
      id = r.memory.id;
    if (variant === "disabled") {
      const scene = store.scene("s");
      store.saveScene({ ...scene, learningEnabled: false });
    } else if (variant === "recalled")
      store.db
        .prepare("INSERT INTO recall_tombstones VALUES(?,?,?)")
        .run("s", "m", "2026-10-28T12:00:00.000Z");
    else if (variant === "duplicate" || variant === "deleted") {
      const pub = memory.create({
        scope: { kind: "sharedPublicKnowledge" },
        content: learnedCandidate.content,
        kind: "fact",
      });
      if (variant === "deleted") memory.delete(pub.id);
    } else if (variant === "parent") {
      const proofs = memory.repository.supports(id);
      proofs[0].parents = ["missing"];
      store.db
        .prepare("UPDATE memory_supports SET value=? WHERE memory_id=?")
        .run(JSON.stringify(proofs), id);
    } else {
      const m = memory.repository.get(id);
      if (variant === "locked") m.locked = true;
      if (variant === "person") m.scope.personId = "p";
      if (variant === "validUntil") m.validUntil = "2027-01-01T00:00:00.000Z";
      if (variant === "kind") m.kind = "claim";
      store.db
        .prepare("UPDATE memories SET value=? WHERE id=?")
        .run(JSON.stringify(m), id);
    }
    call("promotePublic", learnedContext, id);
  });
scenario("same-version-invalidated-replay", (call) => {
  const m = call("create", input());
  const guard = {
    expectedVersion: 1,
    expectedDeletionEpoch: 0,
    idempotencyKey: "invalidate",
  };
  call("updateGuarded", m.id, { locked: true }, guard);
  call("update", m.id, { status: "invalidated" });
  call("updateGuarded", m.id, { locked: true }, guard);
});
scenario("legacy-dependent-content-review", (call, memory, store) => {
  const root = memory.propose(learnedContext, learnedCandidate);
  const child = memory.propose(
    {
      ...learnedContext,
      provenance: "contextual",
      dependencyMemoryIds: [root.memory.id],
      dependencyParentReferences: memory.captureParentReferences(
        learnedContext,
        [root.memory],
      ),
    },
    { ...learnedCandidate, content: "Synthetic derived fact" },
  );
  assert.equal(child.status, "saved");
  const proofs = memory.repository.supports(child.memory.id);
  for (const proof of proofs) delete proof.parentReferences;
  store.db
    .prepare("UPDATE memory_supports SET value=? WHERE memory_id=?")
    .run(JSON.stringify(proofs), child.memory.id);
  call("update", root.memory.id, { content: "Synthetic reviewed root" });
});
const sourceFiles = [
  "src/memory/write/manual-commands.ts",
  "src/memory/write/guarded-command.ts",
  "src/memory/domain/record-values.ts",
  "src/memory/write/revision-coordinator.ts",
  "src/memory/storage/assertion-repository.ts",
  "src/memory.ts",
  "src/store.ts",
  "src/memory/read/assertion-authority.ts",
  "src/memory/lifecycle/proof-invalidation.ts",
  "src/memory/storage/assertion-history.ts",
  "src/memory/projections/coordinator.ts",
  "src/memory/projections/processor.ts",
  "src/memory/projections/people.ts",
  "src/memory/storage/projection-outbox.ts",
  "package-lock.json",
];
const sources = Object.fromEntries(
  sourceFiles.map((p) => [
    p,
    crypto
      .createHash("sha256")
      .update(readFileSync(resolve(root, p)))
      .digest("hex"),
  ]),
);
sources["backend/tests/generate-manual-fixtures.mjs"] = crypto
  .createHash("sha256")
  .update(readFileSync(fileURLToPath(import.meta.url)))
  .digest("hex");
const runtime = {
  node: process.version,
  timezone: "UTC",
  v8: process.versions.v8,
  icu: process.versions.icu,
  unicode: process.versions.unicode,
};
const runtimeHash = crypto
  .createHash("sha256")
  .update(JSON.stringify(runtime))
  .digest("hex");
const raw =
  JSON.stringify({ runtime, runtimeHash, sources, cases }, null, 2) + "\n";
if (process.argv.includes("--check"))
  assert.equal(readFileSync(output, "utf8"), raw);
else {
  mkdirSync(resolve(output, ".."), { recursive: true });
  writeFileSync(output, raw);
}
console.log(
  JSON.stringify({
    scenarios: cases.length,
    operations: cases.reduce((n, c) => n + c.steps.length, 0),
  }),
);
