# Bounded first-party outbound media encoding

The bundled SnowLuma adapter now sends authorized local originals through a shared
native worker. The worker owns the file read, SHA-256 identity check, base64 conversion
and complete HTTP/WS wire encoding. Only the small envelope is JSON-serialized;
verified originals are written directly as base64 into one exact-size transferable
buffer. Unique, exactly-once quoted markers preserve JSON escaping without scanning
or copying a multi-megabyte base64 JSON string. Small envelopes with a conservative JSON-size estimate at most
64 KiB retain a bounded synchronous fast path. Sharp's existing asynchronous image
processing is unchanged.

## Authority and outcomes

The parent authorizes the original before scheduling. Descriptors include its path,
length and content hash (legacy assets use their original content-hash ID). The worker
checks file size before allocation, reads exactly that length, checks post-read file
metadata and verifies the hash. Immediately before physical transmission, the parent
rechecks source visibility and identical metadata. Core eligibility, current adapter,
reply recall, grounding and the successfully validated topic version are checked after
the encoding await. There is no await between the final gate and transmission.

Cancellation before transmission has no network effect. Failed source/gate/admission
checks are known-unsent. An invoked transport with no authoritative receipt remains
unknown; neither worker failures nor late replies trigger replay. Success and usage
recording still require the normal platform acknowledgement.

## Resource ownership

- One reusable FIFO worker is shared across adapter instances. Each adapter cancels
  only its own jobs. The last owner's disposal terminates the pool. Idle workers retire
  after 30 seconds; a global maximum of two includes terminating-generation rollover.
  This is **not** a cap on other process workers (for example, logging or native Sharp).
- Across all adapters, at most eight jobs/retained bodies are admitted. Original bytes
  total at most 64 MiB per send; the wire output is capped at 96 MiB. There are at most
  64 original descriptors. Inline inputs have a conservative 1 MiB JSON-size estimate
  limit, 4,096 nodes and depth 16 before structured cloning.
- A reservation is eight times original bytes, plus four times the conservative inline
  estimate and 1 MiB overhead. The global ceiling defaults to 768 MiB. Set
  `HUMANENGINE_OUTBOUND_MAX_RESERVED_MIB` to an integer from 16 through 768 to lower
  the ceiling; invalid configuration fails explicitly. Node's cgroup-aware available
  memory check also requires outstanding reservations plus 128 MiB headroom at admission. These are conservative
  admission controls, not an OS-level RSS guarantee or a substitute for container sizing.
- A 64 MiB original reserves about 513 MiB, so two such jobs cannot be admitted
  concurrently. Small containers can reject one with `outbound_memory_unavailable`;
  configured capacity/job saturation returns `outbound_encoding_busy`. The original
  size limit is not silently reduced, and rejected work is not automatically retried.
- Caller cancellation does not release an active worker's reservation before its actual
  completion/termination. Encoded bodies remain charged through action settlement.
  WebSocket additionally retains ownership through its write callback/socket close.
  Prepared original-media HTTP uses `node:http`/`node:https` and likewise waits for
  write completion/close: it does not depend on fetch's copied BodyInit lifetime.
- HTTP keeps TLS verification, bearer headers, URL restrictions, no redirect following,
  combined deadlines/cancellation, the 8 MiB response limit and existing status/retcode
  interpretation. Ordinary non-original HTTP actions still use fetch. WS bytes retain
  text framing and the original echo envelope.

## Compatibility limits

`AdapterContext.media` / `AdapterServices.media` remain synchronous for external plugin
compatibility, including original-byte base64 conversion. They are legacy blocking
interfaces, **not** covered by the first-party worker guarantee. Existing plugins are
not silently given a Promise. The internal first-party source-descriptor callback is
additive. External inline media passed into OneBot is subject to the bounded inline
limit above; large originals should use the trusted source-descriptor path. External
adapters may ignore the additive `beforeTransmit` callback; only cooperating adapters
can claim its final physical-send freshness guarantee.

## Synthetic measurement (2026-11-02)

Local generated 64 MiB bytes, no model/provider or real-platform requests. Two isolated
sequential child processes, one job at a time, 60-second wall and 1 GiB RSS watchdog.
`node --import tsx scripts/outbound-encoding-profile.ts baseline|worker` reproduces the
encoding comparison. The worker run additionally tests retained admission through
simulated acknowledgement and a second cancelled encoding, then checks zero jobs/bytes.

| Measurement                         | Prior synchronous encoding | Worker encoding |
| ----------------------------------- | -------------------------: | --------------: |
| Wire bytes                          |                 89,478,546 |      89,478,546 |
| Main-thread heartbeat maximum delay |                  381.66 ms |         4.60 ms |
| Single encode wall time             |                  378.39 ms |       277.18 ms |
| Single encode process CPU           |                  422.05 ms |       325.53 ms |
| Process peak RSS                    |                 494.12 MiB |      491.69 MiB |

The worker RSS observation includes its second cancellation exercise. An initial
straightforward worker implementation increased CPU and wall time; eliminating full
base64 JSON serialization and duplicate wire scans produced the final numbers above.
RSS is still substantial. These figures measure encoding, not end-to-end HTTP/WS
throughput or production-wide CPU. The event-loop histogram alone missed the baseline
synchronous stall, so the independently scheduled heartbeat is reported.

### Warm steady-state comparison

`node --import tsx scripts/outbound-encoding-warm-profile.ts` runs three repetitions
per size after worker startup. Both paths perform source size/stat/hash verification;
the synchronous comparison produces the former JSON string, while the worker also
produces the transferable wire bytes. No encoded-media cache is involved.

| Source  |  Sync CPU / wall | Warm worker CPU / wall | Sync / worker heartbeat max |
| ------- | ---------------: | ---------------------: | --------------------------: |
| 0.1 MiB |   0.95 / 0.83 ms |         1.74 / 1.49 ms |              1.19 / 0.93 ms |
| 1 MiB   |   7.54 / 7.40 ms |         4.18 / 4.30 ms |              9.30 / 0.73 ms |
| 10 MiB  | 65.10 / 59.28 ms |       31.17 / 26.29 ms |             63.64 / 1.16 ms |

Small-media worker overhead remains real. Larger originals show lower measured CPU
as well as lower main-thread delay after allocation/serialization optimization; this
is not a claim that all workloads become faster. Functional loopback tests cover
transport framing/headers, source revocation, final core invalidation, held WS write
callbacks, slow HTTP readers, abort, late ACK, TLS rejection, compressed responses and
decoded response limits, worker exit, queued/active cancellation, exact Unicode/escaping
and multiple-source wire equality, legacy assets and shared adapter ownership.
