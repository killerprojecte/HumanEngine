# Runtime flow diagnostics

Runtime logs describe actual work, not only low-level event names. Console stdout
contains a Chinese human-readable summary followed by `|` and the complete JSON
record. The rotating `runtime.jsonl` file remains pure JSON Lines. Consumers of
stdout that previously assumed each line was JSON should parse the suffix after
`|` or read `runtime.jsonl` instead.

## Following a conversation

Open **Logs**, select a trace, and read the steps in chronological order. The
trace filter also includes linked background learning batches. Expand a row to
inspect full correlation IDs, model role, numeric summaries, safe error causes
and code locations. The UI retains the existing bounded recent-log window;
missing earlier rows are not evidence that an earlier step never ran.

- `traceId` follows an accepted message batch through queueing and a reply turn.
- `turnId` identifies a particular foreground conversation turn.
- `spanId` pairs a real step's start with its terminal status; `parentSpanId`
  links nested operations. A span emits at most one terminal event.
- Background learning uses its own `batchId`/trace and bounded `linkedTraceIds`.
  It can run concurrently and is not presented as part of the reply turn.
- API failures retain `requestId`; technical diagnostics remain available.

States distinguish started, completed, skipped, failed, cancellation requested, cancelled and partial.
An abort immediately emits a non-terminal cancellation-requested event with an explicit
“cancellation requested; waiting for cleanup” reason. This does not claim an
external implementation that ignores cancellation has already stopped. Late
results are not accepted as successful work. The span remains pending until its actual work
settles, then emits a cancelled, failed or partial terminal outcome. A separate turn-cleanup step only
completes when the actual pending work settles and resources are released.

## What the steps mean

Ingress and routing report acceptance or exact fixed skip reasons. Debounced
messages share a bounded pending batch; a queue completion means work was
selected, not that a reply was sent. Context preparation reports applicable
rules and learned hints. It does **not** invent vector retrieval before the
planner: memory retrieval is logged only where the real memory-search tool or
learning pipeline requests it. Retrieval records the actual vector or lexical
fallback outcome.

Planner decisions distinguish reply, wait and silence without printing its
free-form reasoning. Model operations show the purpose, configured model,
attempt/fallback path, scheduler wait, duration, reported token usage, output
category and counts. Unknown provider usage is marked unknown rather than
invented zero usage. Existing usage/cost accounting is not increased by start,
queue or skip records.

Tool completion is separate from model completion. Sending records each prepared
message segment, optional typing delay, post-wait validation and authoritative
adapter receipt. Generated text and a model's completed response are **not**
platform send acknowledgements (not recipient delivery or read receipts). Unknown or failed receipts never become successful
sends, and logging introduces no new automatic message retry.

Background learning reports admission, extraction, conditional review/repair,
reconciliation and writes. Empty/disabled/duplicate/rejected outcomes differ
from failure. Partial or uncertain persistence preserves the existing no-replay
policy instead of claiming every candidate was saved.

Current-turn image descriptions are separate from background meme learning.
The configured vision route receives bounded, decoded image data when a relevant
live source is available; absence of the route, source invalidation, unreadable
media and format failures are explicit. Descriptions remain untrusted ephemeral
context, not original user text or durable facts.

## Privacy and limits

Default flow logs contain fixed stages/reasons, safe technical identifiers and
numeric counts. They omit chat bodies, prompts, model answers, rule text, tool
arguments/results, image data, image asset identifiers, URLs and credentials.
The optional existing model-I/O diagnostic capture is a separate explicit
setting with its own redaction policy. No live model or QQ account is required
for the synthetic local tests.

The format is independently implemented. MaiBot's operation-oriented diagnostics
were a design reference, not copied implementation code. This change adds no
claim that a model always obeys injected rules or that an unavailable provider
has succeeded.
