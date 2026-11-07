# Topic routing policy

`ValidReassignment` ports the actual complete-grouping check from
`src/runtime/topic-reassignment.ts`. It validates all groups before mutation,
keeps selected output bound to the running topic, rejects moving protected
messages/topics, and preserves source handling of missing delivered messages,
empty groups, repeated IDs and slot accounting. It never creates a topic or
allocates a UUID. The existing default four-slot policy is retained, not a new
protection limit.

Inputs carry canonical WTF-8 JavaScript string identities; callers decode JSON
with `jsjson`, not lossy `encoding/json`. The validator does not mutate inputs.
As with other pure policy packages, callers must own their input slices for the
duration of the call. External source/ACK ownership, topic admission, routing
metadata, runtime effects and complete bot orchestration remain separate work.

The source-pinned actual Node fixture contains 908 cases, including deterministic
mixed groupings and distinct lone surrogates. Numeric tests retain the source's
comparison behavior rather than adding a new configuration validator.

`RoutingState` ports scene-local deferred messages, acknowledgement ownership,
cue metadata, protected sends and turn identity maps. Its insertion-ordered maps
preserve JavaScript replacement/delete/reinsert behavior. The source's 60-entry
deferred and 500-entry acknowledgement policies are retained exactly, including
saturating the overflow count at Number.MAX_SAFE_INTEGER. Cancel clears only
pending/acknowledged routing metadata; it does not turn protected or uncertain
sends into retry opportunities. Forget/evict preserve the source's exact scope.
This state is owned by one scene actor, not a global lock, and must not be copied
after use or accessed concurrently. Runtime scheduling and persistence are not
implied by this pure metadata component.

The routing-state oracle hashes the complete ordered state after every actual
source transition, including duplicate/capacity/update boundaries and distinct
surrogate keys. No message body, model output or production database is involved.

`AssignTopic` now implements the source admission decision tree and `ParseTopicCue`
implements cue parsing. Eviction with equal (or NaN-difference) timestamps
requires an exact `LocaleCompare` provider. The native provider now exists in `topiccollation`;
production runtime composition still needs to supply its configured instance.
If none is supplied, it returns `ErrAdmissionCollation`, an explicit
migration blocker, not a new intended runtime policy. Do not wire that error
into a supposedly fully migrated bot or replace it with byte ordering. The
finite Node comparator lookup in tests is not a production implementation.

Candidate lists use JavaScript UTF-16 lexical order. Cue whitespace is the
ECMAScript set, length counts Unicode code points, normalization retains distinct
lone surrogates, and NFC does not insert stream-safe CGJs (unlike direct x/text
NFC.String on long combining sequences). Canonical decomposition/order/composition
uses the existing Unicode17 x/text tables. All source defaults, 1..16 slot
validation and recency boundaries are retained. Anchor ownership remains the
caller's trusted same-scene responsibility. No model calls, UUID generation,
state mutation, external effects or new protection caps are added.

The source-pinned Node24.22.1/ICU78.3/en-US fixture contains 2,097 decisions,
45 cue parses and 529 finite test-only comparator pairs. It covers every reason,
anchor precedence/conflicts, existing provisional reuse, recency and scene
boundaries, protected sends, capacity, duplicate/oversized snapshots, malformed
configuration, NFC/CGJ/Hangul/surrogate cases, UTF-16 ordering and non-UUID
collation ties. NaN/infinite touchedAt is not newly rejected: the small-array
sort follows V8's detected run plus binary insertion, preserving stable order
even when these values make the comparator nontransitive.

### Exact native collation provider

`topiccollation.New(sourceResolvedLocale)` supplies the exact native comparator
under the `humanengine_icu` build tag, with the pinned Node v24.22.1 ICU source and
full data statically linked. Assign `collator.Compare` to `AdmissionConfig.LocaleCompare`
and close it when its owning runtime shuts down. The locale must be captured from
the source deployment's resolved default, not assumed to be en-US or inherited
from the new Go process. Untagged builds return an explicit unavailable error.

The provider preserves UTF-16 code units and explicit lengths, including NUL and
lone surrogates; verifies the embedded data SHA-256 once per process; disables
external ICU data lookup; checks ICU/Unicode/CLDR versions; and applies V8's
normalization-on/tertiary attributes while retaining locale-specific defaults.
It does not start Node, so V1's zero-enabled-plugin rule is unaffected.

All 2,097 actual-source admission cases now also pass using the real native
provider. A separate 14,532-pair, 15-locale oracle verifies 217,980 comparisons,
including every lone surrogate, contractions, canonical equivalents, ignorables,
NULs, and arbitrary UTF-16. Focused race/vet and a source-free native distribution
proof pass. Exact archive/source/data fingerprints, the full ICU license,
reproducible serial build instructions, and packaging constraints are documented
in `backend/native/icu/README.md`. The upstream ICU 78.3/CLDR48.2 release and host
ICU76.1 are not substitutes for Node's vendored CLDR48.0 package.

This closes the native comparator implementation blocker, not full bot migration:
production admission/runtime composition and a final Go runtime image remain.
The current TypeScript Dockerfile is intentionally unchanged.

Normalization has an additional 40,016 pinned Node NFC probes: every Unicode17
scalar with canonical decomposition, its decomposed form and a composition /
reordering variant; all Hangul syllables; 256 long mixed-mark runs with lone
surrogate boundaries; and a 16,384-mark adversarial run. Canonical ordering uses
stable 256-entry CCC counting buckets per nonstarter run, linear work in input
length with no cap (CCC's byte width is intrinsic, not an admission restriction).
