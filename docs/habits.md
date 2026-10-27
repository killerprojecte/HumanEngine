# Learned habits and bot expression preferences

`HabitService(store, options?)` supplies soft hints, never rules/persona edits. Fixed/manual instructions and the explicit current request always win. `options.now` is an injectable millisecond clock for tests; it does not change message dates or establish elapsed production reliability.

## Evidence and scope

The trusted engine constructs `sourceEvidence` from live messages: source identifiers, availability, bot flag, origin, original text and original `timestamp` (parsed `sentAt`). Model JSON cannot supply these evidence flags. The service checks learning enablement, simulation isolation, recall tombstones and finite event times at each proposal/feedback boundary. Events older than retention or over five minutes in the future are rejected. Delayed genuine events retain their original age.

Expression proposals require source-grounded expression vocabulary and scope. Single-image (“那张表情…少用它”), personal, temporary, quoted and joking requests are rejected as habit evidence, not widened to global frequency. Explicit bot-wide general wording may teach a shared preference. Group/technical wording stays local; an unspecified bot request defaults to the current scene, not all groups. Unsupported or ambiguous phrasing abstains. These conservative lexical guards deliberately favor false negatives over scope expansion; they are not general natural-language understanding.

`concise` and `detail` are opposite directions of one canonical `concise` axis. Technical-only verbosity has `context: 'technical'` and stays scene-local. Supported technical scene-rule paraphrases consolidate into that axis; a later implicit paraphrase requires existing technical support from its cited authors. General concision and technical detail can coexist as **different contextual scopes**, not two opposing global instructions. Consumers must respect the context and the more specific scope. Arbitrary scene rules are not fuzzily merged.

Shared expression results remove all source identifiers and internal author watermarks. Simulation lists only genuinely shared preferences and performs no learning or persistence writes. Local expressions/rules do not cross scenes.

## Corrections, decay and bounds

`DEFAULT_HABIT_POLICY` is exported: 30-day half-life, 90-day retention, 128 maximum sources. Constructor overrides support half-life 1–365 days, retention at least half-life and at most 730 days, source cap 8–512. These are internal policy options, not new admin settings.

- One latest event per authenticated adapter/account/sender namespace contributes one vote, including across groups. Display names are not used. Platform accounts are not proof of distinct humans; no cross-provider identity linking is attempted.
- A later explicit correction replaces that author's vote; old/new votes no longer cancel forever. Repetition cannot create additional independent support. Same-source replay cannot change its direction or refresh its timestamp. Out-of-order older events cannot replace a newer vote.
- Each retained vote decays exponentially from its original event time. Confidence is capped at 0.75, signed weight at 0.30. Expired or compacted evidence contributes **zero**. Reads recalculate without writes, including after restart. Natural expiry makes a record disputed and permits fresh evidenced renewal; explicit final-source recall invalidates it.
- Storage retains at most 128 latest independent-author sources plus 128 hashed-author high-watermarks per updated record. Compaction records a timestamp cutoff: events at/before the discarded chronology cannot re-enter. This intentionally rejects old delayed evidence below the cutoff rather than resurrecting pruned votes. Recall removes exactly its retained source; the bounded watermark prevents older evidence from that author reviving. There is no hidden aggregate weight left after support is removed.
- Locked records reject automated writes; recall still retracts support. Manual deletion stores fingerprint tombstones, including compatibility checks for old expression fingerprints. A final-source invalidated record is not silently resurrected.

Legacy records lacking versioned scope/time evidence are preserved for administration, but shown disputed with zero weight/confidence rather than invented timestamps or silently globalized. There is no destructive migration of legacy evidence. Fresh validated evidence can renew an unlocked compatible candidate; old unversioned source rows do not become votes. Archived raw evidence can therefore exceed the new cap until an authorized update; all new/renewed records are bounded.

## Verification

`node --import tsx --test tests/habits.test.ts` covers exact recorded single-image feedback, technical paraphrase consolidation, six-author reversals, repetition/cross-group identity, scope/simulation isolation, locks/deletion/recall, bounded retention and replay resistance. Injected-clock tests explicitly cover 30-day half decay, 91-day expiry/renewal, restart recalculation, delayed events and future-skew rejection. These are deterministic tests, not observed months of operation or live-provider quality claims.
