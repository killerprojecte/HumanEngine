# Native Experience administration candidate

This layer serves the actual source route families:

- `GET`/`HEAD /api/memory-experience`
- `POST /api/memory-experience/{id}/validate`
- `POST /api/memory-experience/{id}/review`
- `DELETE /api/memory-experience/{id}`

`httpapi.New` and `Open` remain readonly previews. `NewWithExperience` borrows
an actual `experienceservice.Service` only when its root owns the exact same
pool and authority. `NewWithMemoryAdministration` also enables Habit routes.
The composing candidate constructors are explicit; production command startup
is not switched by this patch.

## Request and projection parity

The fixture generator imports the real source `createApp`, `Engine`, and
Experience owner. It does not reproduce the Hono handler in a mock. It pins
Hono, route, profile normalization, package-lock, and Zod implementation hashes.
Current Zod **4.6.7** measures `.max()` strings in Unicode **code points**,
including lone surrogates as one. The old handover's UTF-16 assumption was
rejected by actual-source astral boundary fixtures. Explicit source
`displayName(...).slice(0,128)` still uses 128 UTF-16 units.

Inputs preserve strict query/body fields, source numeric coercion, encoded path
segments, current source limit 40, MIME behavior and exact schema/scene/conflict
responses. HEAD never drains a response body. Trial projection is an allowlist,
not serialized owner state: no positives, source roots, snapshots, hashes,
requester IDs or private receipts escape. Identity display names and settings
come from owned native persisted reads.

## Final publication

Request bodies are read before entering any owned writer scope. Service actions
run once. Inside the native owner transaction, `CaptureReadWitness` records only
SELECT queries actually issued by that request through ordinary and prepared
session APIs. It copies bound arguments and captures post-operation rows in the
same scope. Ordered page membership, columns, SQLite types, exact source/scene/
settings/identity rows and the gate participate. There is no table census and
no full-database fallback.

After COMMIT, `Authority.Publish` independently checks readiness and replays the
captured SELECTs against its immutable main snapshot. The final callback only
enqueues owned bytes. Neither headers nor socket I/O occur under authority or
SQLite reservations. Schema 400, missing scene/experience 404, conflict 409,
and router 404 publication all retain the final authority gate. Lost responses,
failed final checks and rejected COMMITs never replay the original mutation.

Read capture accepts supported SELECT statements and copied SQLite argument
types, not arbitrary SQL or RETURNING writes. Capturing an unsupported future
read fails closed; it must be deliberately integrated rather than silently
ignored. Ordinary session queries without active capture are unchanged.

## Validation scope

The synthetic fixture matrix covers successful validate and all review states,
list/HEAD/filtering, trial stripping and names, authentication, MIME, malformed
JSON, safe integer versions, query/path/body Unicode boundaries, coercion,
unknown fields, route methods and encoded IDs. Native tests add real COMMIT
refusal, durable intent with rolled-back local state, lost-response no-replay,
request-body withdrawal, final 404/409 withdrawal, and a paused real TCP stream
while native writers and authority withdrawals complete.

This is a private candidate layer, not a claim of full engine migration,
production startup, all-package CI, or independent-review approval. See the
external validation manifest for exact executed commands and results.

## Owned candidate construction

`candidateservice.OpenWithExperience` and `OpenWithMemoryAdministration` take a
`NativeExperienceOptions` factory. The candidate creates its own source store
and runtime authority and passes that exact capability to `BuildRuntime`.
`Runtime.UsesAuthority` rejects a runtime created for another installation or
pool even if its scene/message IDs happen to match. The resulting Experience
owner uses `NativeRuntimePorts` with this same source store; it does not open a
second database connection when previewing inside a writer transaction.

The caller supplies actual model/policy/date/clock dependencies to its runtime
factory. It must not begin ingress/model work before candidate construction
returns. `AbortRuntime` is mandatory and disposes any returned runtime if a
later constructor step fails, before the pool and authority close. After a
successful constructor, runtime lifecycle is caller-owned: stop runtime ingress
before closing the candidate. Candidate Close never closes that runtime.

This composes the native owner, source proof, retained runtime preview and HTTP
administration. It does not claim to install every bot extension, plugin, model
transport or production boot dependency automatically.
