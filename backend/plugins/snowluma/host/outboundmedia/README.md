# SnowLuma outbound IO encoder

This package implements only native outbound encoding and its bounded memory/IO
ownership. It is not the full SnowLuma sender: source resolution, remote fetching,
guest business logic, network transport, and source/core/send authorization gates
remain outside this package. The sender must rerun those gates immediately before
transport. No source caps or reservation formula have been relaxed.

Use the process-wide `NewEncoder` entrypoint, or one explicitly shared `Manager`,
across adapters. Keep each successful lease through logical settlement and physical
wire drain: acquire `Hold` before handing bytes to an async writer, `Release` at
settlement, and close the hold only after physical write/close completion.
`Source.Retain` retains a callback owner only; it must not pin files, defer source
removal/revocation, or suppress mutation checks.

The source limits remain 64 MiB of originals, 96 MiB of wire, 8 admitted jobs,
16–768 MiB configured reserved capacity, and at most two worker lifetimes while
obsolete work drains. Native scheduling uses Go goroutines rather than Node workers.

## Validation and platform limits

The Linux available-memory probe was validated against the unmodified official
Node v24.22.1 / libuv 1.52.1 function excerpt using 25 synthetic fixtures and 120
ordered IO observations. See `testdata/libuv-memory` for provenance and license.
The frozen serial validation passed the C oracle comparison, Go race tests, and
Go vet. Source wire observations cover 15 byte-exact wire hashes and 8 error-order
cases. These are package-level checks, not an end-to-end sender or full application
validation claim.

The non-Linux default probe still returns zero. Non-Linux platform parity remains
unfinished and requires an explicitly verified platform implementation. Do not
interpret the Linux checks as complete cross-platform support.
