# Media library and shared bot taste

`MemeService` keeps original image bytes in a configured local media directory and
metadata/provenance in `Store.db`. It accepts bounded uploaded bytes only, not a
remote URL. Fetching a platform attachment requires a separate, scoped downloader
with its own network policy; this service never follows arbitrary media URLs.

## Import, analysis, and review

`import(context, bytes, source)` validates a real, available, nonbot source from
the core's evidence and checks the current learning switch and recall tombstones
both before decoding and before committing. Uploads are limited to 10 MiB,
20 million pixels per frame, 400 frames, and 80 million aggregate frame pixels.
Magic signatures and sharp decoding must identify PNG, JPEG, GIF, or WebP. SVG,
HTML, and other formats are rejected. Files are stored by SHA-256 with restrictive
permissions; exact duplicate bytes reuse one asset while retaining independent
source relations. Similar images with different text are not deduplicated.

`analyze(context, id, router, {force?, signal?})` uses the configured vision route.
It samples up to eight evenly spaced frame indexes, always including the first
and last, removes consecutive identical samples, and sends the remaining frames
in order with their original indexes. GIF and animated WebP originals remain
unchanged. A sampling limit can miss brief intermediate changes; the tags are not
a claim of exhaustive frame-by-frame understanding. Force reanalysis is explicit,
not performed during ordinary search or use.

The model returns separate `appearance`, `style`, `subject`, `emotion`, `intent`,
`tone`, `intensity`, and `memeMeaning` tag arrays, plus `ocr`. The JSON shape and
lengths are validated. Content and OCR are treated as untrusted material, never
instructions. A failed analysis leaves the asset unavailable and records a fixed
error code, not a provider response or hidden reasoning. Concurrent same-scene
analysis of one asset shares one promise. Recall, disabling, manual tag edits,
and learning switch-off are rechecked after model work so late output cannot
overwrite a newer manual decision or revive withdrawn material.

The Engine supplies the character, structured bot preferences, visible favorite-tag examples, and original-scene source context to the same vision call. No second full-library VLM pass is made during selection.

- A conservative `selection` result can automatically make an analyzed asset available: usable, at least 0.90 selection confidence, expressive intent/meaning, and no declared personal data or real-person photo.
- Automatic sharing additionally requires source context still available, an explicit source-independent generic-content decision, no source-specific/private flags, and the OCR screen. Unknown context or uncertain results keep the asset scene-bound or unavailable.
- These are bounded model-assisted decisions, **not perfect privacy identification or independently verified facts**. Removing a name alone does not make private screenshots or project material public.
- `selectionOrigin` and `sharingOrigin` distinguish automatic decisions from manual choices. Manual availability/sharing choices, disabled state, manual tags, and favorites take priority; reanalysis cannot silently reverse them. Existing analyzed assets without an origin marker retain their prior choices.
- No sharing means only scenes with a valid source can retrieve the asset. Shared tool results still omit original source identities and context. Administrator controls remain available for correction.

Recall and disable cancel applicable pending analysis, and commits recheck current sources/settings. Shutdown aborts and drains analysis before closing storage.

## Query, send, and feedback

`search(context, query, tags?)` ranks existing metadata and returns at most twelve
available candidates. It does not invoke a VLM. Results omit all scene/person
source provenance. Explicit global favorites and feedback-derived preferences
influence ranking; recent usage supplies a small repetition penalty, not a rule
requiring every message to use a different meme.

`original(context, id)` returns the authorized local path and content type for the
server's asset endpoint or core send adapter. Do not expose the filesystem path
to browsers or accept a user-supplied path. The core must check scene/send
eligibility again after resolving media. Local animated playback does not prove
SnowLuma/QQ recipient animation support; that adapter capability remains
unverified until tested on the deployed platform.

`recordUsage` tracks successful real usage without changing taste.
`recordFeedback` accepts explicit positive/negative human feedback with validated
source evidence. A source message affects an asset only once. Bot messages,
repeated automatic use, and silence are not evidence of human preference.
Feedback removal on recall recomputes the preference rather than interpreting a
recall as a dislike.

## Recall and simulation

`recall(sceneId, messageId)` shares the core's persistent tombstones, removes only
the affected asset/feedback sources, and disables assets with no remaining source.
An exact-byte asset with independent surviving sources is retained; one recall
does not delete the global library or another scene's source. Pending analysis
and later retrieval also consult persisted tombstones.

Simulation can search and read only available, privacy-reviewed shared assets.
Import, analysis, feedback, and usage recording reject simulation before writes.
The core must not invoke administrator-only `update` in a simulation tool context.
Required technical model usage may be recorded by the router separately, without
persisting simulated conversation text or learning.

## Verification

The focused integration suite uses SQLite and real sharp-generated two-frame GIF
and animated WebP fixtures. It checks preserved bytes, distinct ordered vision
frames, reuse without another VLM call, hash deduplication, source isolation,
manual sharing precedence, simulation table snapshots, analysis failures, recall
races, independent source retention, feedback deduplication, and rejection of bot
evidence. Model outputs are controlled fixture responses, not live VLM accuracy
measurements. No QQ delivery or recipient animation was tested.

Sharp 0.35.5 was selected from the official npm registry's stable version during
implementation. Relevant official references:

- [Sharp constructor and frame options](https://sharp.pixelplumbing.com/api-constructor/)
- [Sharp image metadata](https://sharp.pixelplumbing.com/api-input/)
- [Sharp output formats](https://sharp.pixelplumbing.com/api-output/)
