# Joinable native DNS port

`NewOSResolver` provides an optional `Network.Lookup` implementation under the
`web_read_candidate` build tag. It is not registered with the manager or selected
by default. This addition does not enable web-read in production, change HTTP 101
behavior, or replace distributed WASM artifacts.

## Ownership and completion

The adapter calls libc `getaddrinfo` synchronously. Addresses are copied before
`freeaddrinfo` and input-buffer cleanup finish. Only afterward does the worker
publish its buffered result and close `Drained`. Abandoning the result does not
prevent native cleanup. No Go `DefaultResolver` cancellation is treated as proof
of underlying completion.

The network layer retains its five-second logical DNS deadline and cancellation
behavior; its physical lease remains held until `Drained`. After admission,
context cancellation does not interrupt libc. `Close` prevents new admissions;
`Wait` and `CloseAndWait` close admission and join all admitted calls. A stalled
OS resolver can therefore keep physical shutdown incomplete indefinitely.
Manager integration must explicitly own and join this resolver before reporting
shutdown complete. Bounded OS cancellation is not implemented.

## Source compatibility

The port receives the guest's URL-normalized ASCII hostname. It preserves Node's
default `dns.lookup(host, {all: true})` hints (`AF_UNSPEC`, `SOCK_STREAM`, zero
flags), verbatim native order, duplicates and address families. Native failures
remain `media_dns_failed` at the network boundary. General Unicode DNS inputs and
Node process-wide result-order overrides are not part of this internal port.

The existing libuv 256-byte IDNA output buffer includes a terminating NUL:
normalized hostnames of 256 or more bytes fail before libc, while 255 bytes reach
the native resolver. This is a source compatibility boundary, not a new URL or
DNS-label policy. URL/Unicode normalization remains in the guest.

Source references: [Node v24.22.1](https://github.com/nodejs/node/blob/v24.22.1/src/cares_wrap.cc),
[libuv v1.52.1 resolver](https://github.com/libuv/libuv/blob/v1.52.1/src/unix/getaddrinfo.c),
[libuv IDNA buffer handling](https://github.com/libuv/libuv/blob/v1.52.1/src/idna.c),
and `src/media-fetch.ts` / `src/network-request.ts`.

## Platforms and validation

Native implementation targets are Linux, macOS, FreeBSD, OpenBSD, NetBSD and
DragonFly with cgo. Only Linux has been runtime-tested. Unsupported builds,
including Windows and `CGO_ENABLED=0`, return `ErrOSDNSUnavailable`; there is no
silent resolver fallback.

Tests cover real libc numeric/localhost lookups, Node source comparisons,
cancellation and late outcomes, unread results, and explicit shutdown. Linux
interposition fixtures hold actual native call boundaries and verify that
255/256/257-byte ASCII and Unicode-normalized hostnames match source admission.
Interposition runs only in test-owned subprocesses, never in the application.
These tests do not establish full manager, application or cross-platform parity.
