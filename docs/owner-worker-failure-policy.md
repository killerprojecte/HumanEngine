# Runtime worker failure behavior

The previous Nitro Node runtime installs handlers for `uncaughtException` and
`unhandledRejection`: it captures and logs those JavaScript errors without exiting.
Worker threads do not inherit those handlers. The owner installs equivalent
capture-and-report handlers, using existing sanitized diagnostics rather than raw
exceptions. A failed background callback is not retried, and capturing an error
does not promise its partial effects were rolled back. This preserves the prior
behavior, including transient recovery failures in existing scheduled callbacks.

An unexpected exit or error event from an already-ready owner is different: the
Engine, adapters and scheduler are no longer available. The HTTP host writes a
constant diagnostic and exits with code 1. It does not leave configuration reads
looking healthy indefinitely, create another owner, or replay requests. The
existing external process policy can then act; Docker health-check failure by
itself would not restart a still-running container.

An unexpected exit or error event from the already-ready publication verifier
also writes a distinct constant diagnostic and exits the host with code 1. The
verifier is required by every guarded publication; leaving the listener alive
would otherwise keep it permanently unavailable. Ordinary validation or locking
errors returned by the verifier remain fail-closed request errors, not fatal
worker loss, and later requests may succeed with the same verifier.

Failures before either worker's readiness retain cached startup failure and
authenticated 503 behavior. A deliberate `closeEngine()` marks a worker as closing
before its shutdown control is dispatched, so normal close/termination does not
trigger the fatal policy. This change installs no automatic worker replacement.

Fatal exit has no newly imposed shutdown deadline and does not wait for a drain.
A mutation or platform send may already have completed. Clients may see a closed
connection rather than a delivered 503, and must treat that as an unknown write
outcome rather than retry automatically. Previously queued or delivered bytes
cannot be retracted. This is not a transactional acknowledgement or a guarantee
that final logs have been flushed.

Worker-level exits can be supervised. A native addon segmentation fault or
process abort may terminate the entire OS process before JavaScript can handle it;
no clean error response or graceful cleanup is promised in that case. The test
suite covers real worker/host subprocess exits, not an induced native crash.
