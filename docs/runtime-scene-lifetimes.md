# Idle runtime scene lifetime

The minute cleanup sweep now removes **cache-only real scenes** from both process-local runtime registries once their ingress idle time exceeds `cacheHours` (60 seconds with cache OFF) and their ordinary message cache is empty. A returning live event creates fresh transient state; persisted scene settings, source-event authority and recall tombstones are not deleted or reloaded as live messages.

Eviction is deliberately conservative. Any task, timer, first-pending timestamp, queued turn/span, deferred input, active routing turn, send guard, recalled-ID set or uncertain/partial-send protection prevents eviction. **All admission coordinators are pinned, including completed ones:** their bounded seen-ID sets still prevent replay. Cache OFF additionally preserves the existing 30-minute proactive opportunity; unexpired automatic cooldowns and backward clocks cannot accelerate eviction. Future-dated cached bodies remain governed by the ordinary body TTL.

Eviction clears only that scene's image cache. Adapters, plugin registrations and hooks remain installed, and operation receipts are untouched. Shutdown marks detached states closed and clears the scene registry; late send cleanup does not allocate replacement scene states.

## What is and is not bounded

This fixes perpetual retention of inactive passive/unscheduled scenes, a potentially large population when an adapter receives many groups but participation is selectively enabled. Message bodies and unprotected routing IDs in pinned scenes still follow existing per-scene limits and TTLs. There is **no hard global constant bound** across arbitrary active, replay-bearing or uncertain-delivery scenes. In particular, completed admissions retain process-local replay metadata and can keep a scene alive. This change does not introduce permanent seen-ID tombstones, disk-backed cold state, new persistence, or different restart replay policy. Durable configuration/source/recall storage retains its existing lifecycle.

Further reclamation of replay-bearing scenes requires an explicit replay-retention design; erasing their deduplication state or converting it to permanent disk suppression would not be an equivalent cache optimization.

## Offline verification

`runtime-scene-lifetimes.test.ts` runs 1,000 distinct scenes in ten waves for each cache mode (2,000 total). After each eligible TTL sweep the runtime state and scene counts return from 100 to zero; adapter and extension registrations survive. Assertions use retained-object counts, not noisy heap percentages. Separate tests cover all lifecycle pins including pending/active/WAIT/completed admissions, slow clocks, cooldowns, proactive horizon, future timestamps, rejoin, durable recall/source-event duplicate suppression, account isolation, receipt identity, adapter removal and late shutdown completion (including a pending transport returning unknown and an authoritative same-call sent receipt). No external API, model or QQ service is used. This is synthetic retention evidence, not a production-memory or GC guarantee.
