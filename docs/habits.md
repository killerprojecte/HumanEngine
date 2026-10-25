# Learned habits and bot expression preferences

`HabitService(store)` is a small SQLite-backed soft-hint service, not a rule engine or a persona editor. Fixed instructions, manual rules, and explicit current requests always take precedence. No code, prompt templates, or manual-rule tables are modified.

## Integration

- `list({ sceneId, simulation })`: active local scene rules plus structured shared bot preferences. Simulation receives only shared preferences. Shared results contain no source identifiers, original text, or person fields.
- `propose(ctx, { kind: 'sceneRule', content, sources })`: low-weight scene-only soft rule, maximum 1,000 characters.
- `propose(ctx, { kind: 'botExpression', preference, direction, sources })`: a shared **bot** expression preference. `preference` is only `concise`, `detail`, `questions`, `emoji`, or `meme_frequency`; `direction` is `1` or `-1`. Arbitrary shared text is never stored.
- `feedback(ctx, id, { direction, sources })`: explicit evidenced support/correction. It cannot act across scene-rule boundaries. Own usage, silence, model self-evaluation, and delivery success are not evidence.
- `adminList()`, `update(id, { locked })`, and `delete(id)` are trusted administration operations, not simulation/model tools. No arbitrary learned-text editing or global person mapping exists here. Deletion stores a content fingerprint tombstone.
- `recall({ sceneId, messageId })` records the persistent recall tombstone and transactionally removes only matching evidence. A final-source record becomes invalidated even if manually locked; independently supported shared preferences remain available with recomputed support.

The trusted core must construct `HabitContext.sourceEvidence` from its current live cache: `{sceneId, messageId, senderId, available, isBot, origin: 'live'}`. Do not accept evidence flags from model JSON or untrusted API callers. Every proposal/feedback transaction checks current scene learning enablement, evidence availability, nonbot authorship, live origin, and persisted recall tombstones. Historical backfill cannot teach habits. The core must cancel work dependent on subsequently recalled source material; invoke `recall` alongside other learning services before continuing generation.

A source is counted once per record. Each scene-local author contributes at most one vote; contradictory votes by the same author cancel that author's support rather than letting repetition overpower a correction. Confidence increases with independent author support, capped at 0.75; signed weights are capped at 0.30. Scene rules with nonpositive support are disputed, not inverted into authoritative opposite rules. These are conservative heuristics, not calibrated probabilities or proof that platform accounts represent independent humans. No cross-scene identity linkage is attempted. Locked records reject automated changes, except recall invalidation. Invalidated candidates are not silently resurrected.

## Verification

`node --import tsx --test tests/habits.test.ts` uses real in-memory SQLite to check scene isolation, structured shared output, simulation zero writes, repeated-author evidence, correction weakening, lock/delete tombstones, recall of single/multiple sources, and live evidence/current-switch rejection. It does not claim live provider or QQ verification.
