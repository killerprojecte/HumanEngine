# Lossless tool results and HTTP requests

## Complete storage, bounded model views

Plugin tools can call `turn.cacheResult(source, options)` with UTF-8 text, bytes, or an
`AsyncIterable<Uint8Array>`. Streams are written incrementally with backpressure to
private temporary files. A result ID is published only after the entire source has
finished successfully. Failed, cancelled, recalled, edited, or disabled in-flight
results are not published; partial files are deleted. No download-size ceiling is
imposed. Available disk space remains a real resource constraint: storage failure is
explicit, never a successful truncated download.

The descriptor contains an opaque `resultId`, `totalBytes`, `complete` (the stored
source is complete), `contentType`, `expiresAt`, representation `encoding`, and
zero-based byte ranges. `truncated` describes the model view, not lost source data.
`requestComplete` says whether the requested range was fully returned. `nextOffset`
is an exact byte cursor for another read of the same immutable result.

Use `tool-results.read` with `{resultId, offset, length}` or `{resultId, full: true}`.
A full request returns all remaining content inline when it fits the model-view
budget. Otherwise it explicitly returns a page with `requestComplete: false` and
`nextOffset`; continue with that offset. Full access is available without refetching,
not a promise that arbitrarily large data fits a model's context. The 32 KiB view
budget accounts for JSON escaping/base64 expansion; the existing overall planner
context budget still applies. Request only relevant ranges for large results.
Invalid UTF-8 or ranges splitting a UTF-8 character return lossless base64 rather
than replacement characters. Offsets always refer to original bytes, not base64
characters or JavaScript string positions. Empty ranges and an offset at EOF are
valid; negative, fractional, unsafe, or past-EOF offsets are not.

All plugin read results larger than 48 KiB are automatically cached as complete JSON
before presentation. Tools can explicitly request this behavior even for small
results with `resultPreviewBytes`. For action/send results, prefer caching the large
payload with `turn.cacheResult` inside an ordinary outcome envelope, so status and
operational metadata remain available to the runtime. Action/send outcomes are not
automatically wrapped. Output-schema validation happens before optional caching.

## Ownership and lifetime

The cache is local to one plugin manager/runtime. IDs are not authorization: reads
require the same mode and full scene identity (scene, adapter, account, platform
scene). Simulation sessions have separate scene identities. Later turns in the
same scene may read the same ID until its 30-minute TTL expires. Reads carry forward
the original source dependencies and validity checks. Source recall/edit and plugin
disable invalidate both completed and in-flight results. Runtime shutdown removes
its temporary directory; results are not persistent across restarts or included in
backups. After a process crash, OS temporary-file cleanup governs orphaned private
files. Retrieval reports explicit expired/not-found/invalidated/range errors rather
than silently rerunning a tool, particularly a state-changing request.

## HTTP tools

The `web-read` plugin now exposes:

- `web-read.read`: compatible GET webpage reader with a bounded inert text preview
  and the full raw HTTP body cache descriptor. HTML is parsed only from the bounded
  preview, never an unlimited DOM. `previewIncomplete` and `textDerivedFromPreview`
  make that distinction explicit. Scripts and subresources are never executed.
- `web-read.request`: structured GET, HEAD, or OPTIONS with explicit headers,
  optional UTF-8/base64 request body, redirect controls, HTTP status, normalized and
  ordered raw response headers, content encoding, and cached raw response bytes.
- `web-read.send`: explicitly authorized POST, PUT, PATCH, or DELETE. This is a
  staged action, not speculative planner research; simulation does not execute it.
  Do not retry an uncertain state-changing request automatically.

Bodies are streamed without the former 1 MiB hard limit. Legacy saved
`maxResponseBytes` settings remain accepted but no longer limit downloads.
`maxChars` affects only the readable preview, bounded by the model-view budget.
Timeouts and concurrency remain explicit resource controls, with expanded configurable
ranges. Response status errors still preserve their bodies; a 404 is inspectable.
All MIME types and binary bodies can be cached. Content-encoded gzip/br/deflate bytes
are preserved exactly (not silently decoded); the response identifies the encoding.
No cookies, credentials, files, proxies, or environment secrets are inherited.

Network security boundaries are unchanged: public HTTP(S) destinations only,
DNS/address validation and pinning at each redirect, TLS verification, no insecure
HTTPS downgrade, and no Host/framing/hop-by-hop overrides. Explicit caller headers
are stripped on cross-origin redirects. The tools do not execute shell commands,
JavaScript, browser challenges, local-file protocols, or private/metadata requests.
These safeguards are not response-size restrictions. All source content remains
untrusted data, never instructions or authorization.

## Verification

Focused tests exercise real local HTTP transports (with explicit fixture-only DNS
injection), >1 MiB requests and chunked responses, redirects and raw headers,
non-200 bodies, exact binary ranges, split UTF-8, cancellation and stalled sources,
expiry/invalidation, plugin integration, and planner model-view packing. No real
model provider or external website is needed for these checks.
