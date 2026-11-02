# Bounded local performance validation — 2026-11-02

These are synthetic local measurements, not user-production results. No real chats, media,
credentials, QQ requests or provider/model calls were used. The machine ran Node 24.22.1.
CPU profiles include sampling overhead. A coordinated window avoided other heavy test jobs.

## Reproduced: messages stall administration even with reply and learning off

`scripts/ingress-load-benchmark.ts` uses a **persistent SQLite Store and actual Engine**,
including Engine receive hooks, rather than a Runtime-only test. Workload:

- 100 canonical OneBot-shaped scenes, numeric platform/sender IDs, reply/learning/proactive off;
- 250 durable recall records and 5,000 synthetic meme metadata rows;
- 2,000 ordinary text arrivals in bursts of five, yielding between bursts;
- simultaneous real localhost HTTP against settings, scenes, status and paginated memes;
- throwing model stub, with **zero model calls asserted**;
- 120-second measured-workload cooperative watchdog and sampled 768 MiB RSS stop threshold.
  These are not OS-enforced hard limits; a blocked event loop can delay checks.

```sh
node --cpu-prof --cpu-prof-dir=/tmp --cpu-prof-name=ingress.cpuprofile \
  --import tsx scripts/ingress-load-benchmark.ts /tmp/ingress-summary.json \
  >/tmp/ingress-synthetic.log 2>&1
```

The test creates and removes its own temporary database, including ordinary setup, request and
workload failure paths. An external kill or OS out-of-memory termination can still leave its
temporary directory behind. Redirected logs are synthetic but
verbose; do not substitute the user's live database. The recovery fixture is deliberately
small enough for bounded execution; a larger authority history can cost more. This measures
burst pressure, not a claim of a particular production arrival rate.

| Metric                         | Before receive-path fixes | After reviewed receive-path fixes | Plus exact-row SHA-proof memo |
| ------------------------------ | ------------------------: | --------------------------------: | ----------------------------: |
| Workload wall time             |                  104.83 s |                           30.19 s |                       24.14 s |
| Process CPU, user + system     |                  111.14 s |                           32.48 s |                       26.16 s |
| Ingress p95, first 1,000       |                  62.87 ms |                          18.02 ms |                      15.47 ms |
| Ingress p95, last 1,000        |                  95.16 ms |                          18.32 ms |                      14.53 ms |
| Event-loop delay p95 / max     |        398.46 / 445.91 ms |                102.37 / 143.26 ms |             77.59 / 102.89 ms |
| Settings HTTP p95 (50 samples) |                 779.05 ms |                         192.07 ms |                     153.18 ms |
| Scenes HTTP p95 (50 samples)   |                 779.80 ms |                         189.05 ms |                     152.68 ms |
| Status HTTP p95 (50 samples)   |                 785.11 ms |                         188.61 ms |                     157.00 ms |
| Memes HTTP p95 (50 samples)    |                 764.65 ms |                         182.70 ms |                     141.34 ms |
| Sampled peak RSS               |                325.92 MiB |                        321.57 MiB |                    302.16 MiB |

The before source was `f1e0006` (the concurrent exact-lookup meme patch does not execute for
this text-only workload). The after source included the reviewed no-reply feedback early
return, no-op identity write elimination, bounded dependency read scopes, exact meme lookup
and idle-scene lifecycle patch, on top of the Settings locale hotfix. It did **not** include
asynchronous logging or the subsequent recovery hash-proof cache. This comparison attributes
the combined receive-path changes, not an isolated single-change speedup. The third run adds
only the separately reviewed `e003ea3` exact-row SHA-proof memo. Source hashes confirmed
Store, Runtime, Memes and the benchmark script were unchanged between runs two and three;
only the ledger differed. This further reduced wall time by 20% and CPU by 19.5%. The memo
reuses only cryptographic proof of identical freshly read row tuples: it is not a cached head,
revocation result, permission, or recovery decision. No asynchronous logging was included in
any of these three runs.

### Actual CPU stack evidence

The baseline CPU sample tree attributed approximately **45.4 seconds** to these repeated
history branches under feedback admission:

```text
ExperienceRuntime.received
  Runtime.preview
    for each retained message
      isRecalled / presentMessage -> scene / get
        Store.assertRecoveryReady
          RecoveryAuthority.head
            RecoveryAuthority.entries
              readRecoveryStatement / cryptographic hashing
```

Even plain text without a reply target entered that preview before the old reply-target
check. Therefore disabling replies and learning did not remove the synchronous work. The
post-fix p95 no longer grows across the two halves of this fixture. Remaining sampled CPU
still concentrates in fresh recovery-chain reads and proof hashing: direct ingress settings
checks, scene reads, identity reads, learning-admission checks, and personalization checks.
Do **not** resolve this by caching permissions or skipping revocation/recovery authority.

## Large-image and cache boundary sample

`scripts/performance-load-benchmark.ts` runs real Sharp compression at JS concurrency two,
with unchanged native defaults (libvips concurrency one on this machine). Four operations
per size used generated solid-color PNGs: 1 MP p95 55 ms, 10 MP p95 393 ms, 40 MP p95
1,572 ms. Every output satisfied the existing 1,536-pixel / 10 MiB limits; 40,005,000 pixels
were rejected in 1.01 ms. Sampled process peak RSS was 553.29 MiB; event-loop p95 12.01 ms,
max 146.54 ms. Small n means reported p95 is effectively the observed maximum.

These solid-color fixtures exercise pixel/decode pressure, not worst-case entropy/encoding.
Two thousand multi-scene image-cache cycles produced exactly 2,000 misses, coalesces and
hits, then zero entries/flights after scene clear. Post-GC heap settled around 10 MiB and
ArrayBuffers stayed at 2.20 MiB, while RSS retained allocator/native high-water memory.
This is **not** an exhaustive leak study or a hard bound on arbitrary ingress concurrency.
The optional `--soak` mode adds noisy 10 MP inputs and 120 paced cache batches; do not claim
that optional mode passed until its result is separately recorded.

## Frontend / HTTP coverage and limits

- Actual compiled Chat lifecycle, image lifecycle, and preview queue tests: 5/5 passed.
- Authenticated empty-fixture production HTTP, 50 reads each: `/chat` p95 11.59 ms,
  `/memes` 9.66 ms, `/api/scenes` 8.90 ms, `/api/memes` 6.10 ms. This is transport/SSR-shell
  timing, **not** interactive browser navigation or rendering latency.
- All 4,869 locale messages compiled after `b980d9b`; before it, only Simplified/Traditional
  Chinese `stDirectCooldownHelp` failed the real Intlify compiler with codes 10/14/13.
- Supported CUA browser discovery succeeded, but its cloud browser could not reach this
  executor's localhost preview (`net::ERR_CONNECTION_REFUSED`). No denied browser-launch
  flags, alternate launch route or tunnel were used. Actual rendered repeated Chat/Meme
  navigation remains unverified in this environment.

Raw synthetic CPU profiles, summary JSON and exact after-source hashes are retained outside
the repository under the task workspace (`ingress-off-baseline.*`, `ingress-off-after.*`,
`ingress-off-after-manifest.txt`, `ingress-off-proof.*`, `ingress-off-proof-manifest.txt`,
`load-baseline-f1e0006.json`). They are local evidence,
not durable public download links. Do not commit private production diagnostic attachments.
