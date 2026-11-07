# Node Date.parse values and validity

This package ports the tokenizer, ES5-first/legacy continuation parser, and
composers from Node **v24.22.1**, `deps/v8/src/date/dateparser.h`,
`dateparser-inl.h`, `dateparser.cc`, and the final date/time clipping policy in
`date.cc`. It does not invoke Node in production. Source archive SHA-256:
`4f5379796280cdaeb0c2f358fd04b6a16cc4fb7dfa83b5a8c91a9d963b435231`.
The V8 license is retained in `LICENSE.v8`.

Call `New(sourceResolvedTimezone)` and retain the returned `*Parser`. The source
Node process's resolved timezone is required configuration, not inferred from
this machine. `Parser.ParseMillis` returns the exact epoch milliseconds and a validity flag;
an invalid source date corresponds to NaN and returns `valid=false`, not an error.
`Parser.Finite` reports `Number.isFinite(Date.parse(value))` for
canonical JS strings represented as WTF-8 by `jsjson`. Explicit UTC works without
native dependencies; other ICU system timezone identifiers require the pinned
ICU build (`humanengine_icu` and cgo). Unknown/missing configuration fails rather
than silently falling back to host Go tzdata or UTC.

The native path uses the existing `backend/native/icu` pinned build and verified
embedded data, with its own timezone wrapper and the single `nodeicu.Ensure` initialization
owner shared with `topiccollation`. V8's `UCAL_TZ_LOCAL_FORMER` choice for both
gaps and overlaps is retained. ICU failures during offset calculation have V8's
zero-offset behavior. The parser stops at embedded NUL just like V8, preserves
unpaired UTF-16 surrogates, retains legacy date overflow and numeric-prefix
rules, and applies V8's intrinsic Date range. The nine significant digit rule is
source parser behavior, not an added input-size admission limit.

`oracle.mjs` generates synthetic valid/invalid, legacy, whitespace, surrogate,
NUL, overflowing field, and range-boundary cases, comparing both validity and exact
milliseconds against the pinned actual Node
binary. Run it with Node v24.22.1 to refresh `testdata/oracle.json`. Native tests
exercise UTC, New York, Shanghai, Kiritimati, and Paris. Non-native tests exercise
UTC and skip zones unavailable in that build; this is not evidence for their
native behavior. Finite validity is the contract, not returned timestamps.

Scope: the pinned source Node build uses ICU timezone conversion. This does not
claim parity with Node compiled without ICU or arbitrary other V8 versions.

Validation: 16,503 unique synthetic strings × five timezone configurations
(82,515 source-oracle comparisons). Focused pure-Go tests, native ICU tests,
native race tests, and native vet passed during implementation. This finite
fixture set complements the source port; it is not an exhaustive proof over
all strings or all timezone IDs.

The numeric API uses the same parser and timezone conversion as the boolean API,
not a second implementation or a Go `time.Parse` fallback. Runtime TTL/topic age
and provider Retry-After adapters can consume exact numeric results without
trusting a caller-supplied timestamp that disagrees with the original body.
Canonical WTF-8 decoding happens once per call. Nil/unconfigured parsers and
noncanonical Go bytes return errors from ParseMillis; the older boolean-only
convenience retains its false result for non-JavaScript byte strings.
