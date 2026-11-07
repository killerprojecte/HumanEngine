# Candidate shared service

`Open` opens an existing installation's writable main database and one writer
`recovery.Authority`. The same coordinator is borrowed by the HTTP read handler
and both existing transactional stores. `SetSettings` and `SaveScene` retain
those stores' input requirements and failure semantics. Failed writes may leave
durable intent and must not be automatically retried.

This package does not enroll an installation, migrate schemas, reconcile an ACK
gap, expose HTTP writes, own a listener, or replace production startup. The
executable remains the explicit read-only preview. `httpapi.Open` retains its
independent preview resources; `httpapi.New` instead borrows caller resources.

The service owns only resources acquired by its Open. Callers own the HTTP server
and logger. Stop accepting requests and shut down the HTTP server before closing
the service. Close stops admission immediately, drains active calls and closes
resources once; repeated and concurrent Close calls await the same result. New
calls are rejected promptly while an earlier response is still draining. The
lifecycle mutex protects only admission accounting, never response callbacks,
authority acquisition, or database operations. Do not call Close reentrantly from
an active service call or response writer. Slow emission still holds the authority
reservation and can delay shutdown; this composition introduces no timeout or
protective limit. Request context cancellation releases a waiting request.

Once Close begins, writes return `ErrClosed` and HTTP requests retain authentication,
headers, and unsupported-route behavior; authenticated supported reads receive
the existing unavailable response without touching closed resources.
