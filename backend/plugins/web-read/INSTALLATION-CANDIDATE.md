# Isolated manager installation candidate

This checkpoint implements and validates an isolated `web_read_candidate`
installation path. It is **not production enablement**, and no validation result is implied by
the presence of the tests. HTTP 101 remains pending. Its sentinel and network
behavior are unchanged, the default builtin list is unchanged, and no distributed
WASM bytes are changed by this checkpoint.

## Actual source contract

- `src/plugins/builtin/web-read.ts`: setup parses WebReadConfigSchema, registers
  read/request as read tools and send as a learningCandidate, and joins plugin
  disposal. Legacy maxResponseBytes is accepted but does not cap downloads.
- `src/plugins/manager.ts`: generation guard, simulation rejection for candidate
  actions, **validateConfig before service execution**, original turn-owned cache,
  cancellation/revocation checks, and automatic JSON caching above 48 KiB for
  read tools. Tool-result pages are byte-budgeted, but complete bytes are retained.
- `src/plugins/config.ts`: strict small schema vocabulary. In particular the
  existing request headers schema has propertyNames, so the manager rejects an
  input containing headers with unsupported_schema_keyword. The installation
  preserves this source behavior rather than silently making it more permissive.
- `src/network-request.ts` and `src/media-fetch.ts`: existing candidate guest
  decisions and native public-address DNS/TCP/TLS/stream facilities. This assembly
  does not replace those implementations or reinterpret the HTTP 101 branch.

## Composition

The common manager bridge installs guest tool definitions into its actual Runtime
and uses its existing owner(), cache, guard adoption and finalization functions.
The plugin aggregate owns schema policy, configuration policy and service/network
continuations in WASM, plus native DNS/TCP/TLS and physical stream leases. No V1
worker is created by this builtin. A default/old guest fails closed instead of
being advertised as a working service.

Disabled configuration validation loads a private temporary guest, runs its
complete validation under cancellable admission, and synchronously unloads it on
success, error or cancellation. Source has no prepare/secret hooks or state across
validation calls; those generic no-hook paths do not load a guest. No policy guest
remains resident between operations.

The manager bridge also owns a generation-local physical work group spanning
its whole tool continuation, including automatic JSON finalization after plugin
Execute returns. Each cache callback gets a bridge lease. Disable seals and joins
this bridge before WASM unload, in addition to the plugin's stream/DNS group.

Cache owners carry an opt-in Generation capability tied to the original plugin
lifecycle. Cache admission and publication check it under the existing cache
mutex, independently of asynchronous cancellation propagation. Thus a Store
arriving after InvalidatePlugin cannot register or publish for a cancelled load.
Legacy cache owners without this capability keep their existing behavior. A
fresh enabled generation remains admissible; there is no persistent plugin ban.
The deterministic late-registration/finalization tests passed with race detection
in the prior c5bd253 installation validation, including the real registered
physical-cleanup cancellation probe and legacy cache negative control.

Each service generation owns its OSResolver and physical work group. Logical
Dispose cancels and removes advertisement; CloseAndWait joins admitted service,
stream Finish, guest, and DNS work before the manager unloads its guest. An OS
resolver that stalls can still make physical shutdown unbounded. Cancellation is
not evidence of physical completion.

The application must explicitly select `web-read` **and** use the candidate build
tag and a separately built candidate artifact in an isolated validation tree.
Untagged application admission remains an error even if someone supplies
candidate artifact bytes. Default enabled state and distributed artifacts remain
outside this checkpoint.

## Validation and its limits

Actual source checks have passed 23 configurations, 3 tool definitions, 30
manager input cases and the existing 26 service cases. These are source-side
observations. The prior c5bd253 validation also passed the actual candidate WASM
configuration/tool comparisons, default admission and guest lifecycle suites.

The prior c5bd253 runner completed 16 stages successfully, including source,
default/candidate WASM builds, Manager/cache race tests, native network/DNS and
actual guest policy/service checks. Its installed Application test timed out at
240 seconds during disabled configuration validation after its first planner
receipt and physical service unload. It did not finish re-enable, vet or the final
aggregate gate. Earlier error-only binder assertions could also accept the old
150-second fixture context expiring, so they are not exact generation proof.

The revised fixture follows the existing installed Snow test's 10-minute total
and 3-minute administrative-phase budgets, with phase timings. In the isolated
0109fb8-based r6 run it passed under race detection in 314.43 seconds. All three
planner receipts, hot replacement, physical disable, disabled configuration and
re-enable assertions passed. Each retained binder rejected with ErrNotLoaded
under a live, independently bounded context, not a deadline/cancellation error.

The same r6 run passed affected-package vet and final source/artifact hashes.
Its strict aggregate skip/actual-DNS-child gate combines this current application
and vet evidence with seven original r4 passing leaf suites after checking 478
exact source/resource inputs and their original exit/log hashes. The reused guest
matches 74 exact source/resource inputs. This is explicitly combined evidence,
not a fresh rerun of every suite. The original r4 timeout evidence is retained.

The cache bridge unit test uses a synthetic source-validity observation and real
cache files. It is not a substitute for installed network/cache authority tests.
The installed Application case uses genuine native source authority and the real
WASM service, but its private-literal URL deliberately performs no DNS or socket
operation. Separate existing network/DNS suites remain required. Full installed
in-flight transport/cache/finalization races, TLS/SNI/HTTPS downgrade source
coverage, all framing/charset cases and source-free application execution are not
claimed by these prepared tests.

HTTP 101 resolution, the remaining compatibility work, executed differential/race
validation and independent review are still prerequisites to production
admission. Do not turn a synthetic-only or tagged test result into a production
installation-complete claim.
