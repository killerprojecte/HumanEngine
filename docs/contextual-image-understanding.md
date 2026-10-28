# Contextual image observations

Chat image rendering and model understanding are separate paths. An image being
visible in the dashboard does not by itself put its pixels or description into a
model request.

## Repaired gaps

The previous runtime selected only current-topic images and direct replies
inside its last 24 live messages. Historical tools returned media references but
never invoked the image-description hook. The composer also dropped visual
observations when a plan contained no factual citations. Together these could
make an ordinary follow-up or an emotional reply blind to an earlier image.

Initial selection now prioritizes quoted sources, current-topic sources, then
recent conversation images. Up to four missing direct quoted sources can be
resolved through the existing scene-scoped message lookup. Planner history reads
attach attributed visual observations to the same tool receipt. They do not
replay messages into live ingress or learning. The composer retains those
untrusted visual observations even for a plan with no cited facts.

Four authorized message/part sources share one budget across the entire turn,
including history tool calls. A mutable URL reused in another message is not
assumed to be the same image. After reading a source, matching verified byte hashes
can reuse successful model descriptions while retaining message attribution.
Failed reads and empty descriptions are attempted only once per source within
the turn; a new turn may retry. Repeated history tools cannot bypass the source
budget or repeat a failed source attempt. Omitted images explicitly report `image_budget_exhausted`;
this is not a claim that their contents were inspected. Unreferenced images older
than the bounded recent window still require a history lookup. This is not a
scan of all prior messages or an unlimited vision budget.

## Authority and failure behavior

Historical image authority is registered only from trusted, scene-filtered
history results and lasts for the current turn. Caller-provided snapshots cannot
authorize arbitrary assets or change a source part index. Reads check recall,
cancellation, adapter identity and exact source before and after fetching. URL-only
expired images use the same bounded exact-message refresh as the dashboard.
Managed outgoing meme images use the scene-authorized meme store, with availability
rechecked before cached observations are reused. Sources already unavailable at
selection time are marked unavailable without consuming the image budget or
invalidating unrelated valid images. A source revoked during processing still
cancels its dependent turn. History reads do not become live messages. Missing routes, media failures and
budget omissions remain explicit; platform metadata is not a substitute for
pixels.

## Expression versus intent

The vision prompt separately requests visible scene/OCR, visible expression and
posture, and conditional conventional meme/sticker uses with alternatives and
uncertainty. It does not treat a character's expression as proof of a person's
feelings or infer a sender's private intent. Image text remains untrusted data.
The five-minute bounded cache contains context-independent visual observations
and possible conventional uses, keyed by scene, asset, model configuration and
prompt version. It does not cache a question-specific interpretation. Planner and
composer use the actual current conversation to disambiguate possible uses.

For generic image descriptions, only the first frame is supplied to vision.
Known meme recognition can instead reuse the library’s bounded multiframe
analysis; see [known meme recognition](known-meme-recognition.md). Motion and later animation frames
remain unavailable and are disclosed; animated dashboard playback does not change
that boundary. A model can still misread an expression. Synthetic regression tests
prove source selection, pixels reaching the vision call, prompt requirements and
observations reaching participation/planning/composition; fake model responses
are not evidence of real-model meme interpretation quality.
