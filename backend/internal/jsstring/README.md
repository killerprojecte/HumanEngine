# Pinned JavaScript fingerprint text

`FingerprintText(string) (string, error)` implements exactly the text step in
`src/memory/domain/record-values.ts`:

```js
content.trim().replace(/\s+/g, ' ').toLowerCase()
```

`Lower(string) (string, error)` exposes the locale-independent lowercase step.
Both accept and return `jsjson`'s canonical WTF-8 representation. Valid surrogate
pairs are decoded as supplementary code points; lone UTF-16 surrogates survive
unchanged and interrupt cased context. Invalid or noncanonical WTF-8 returns
`jsjson.ErrStringEncoding`. No input/output cap, truncation, Unicode normalization,
host locale dependency, ICU library, or Node process is introduced at runtime.

These operations do not hash or serialize. The caller must build the actual
scope/person/content array with `jsjson.Stringify` before SHA-256. Passing the
result through `encoding/json`, scalar-only rune decoding, or `UTF8Text` first
would lose lone-surrogate identity.

## Tables and contextual casing

The generator runs on **Node 24.22.1 / ICU 78.3 / Unicode 17.0**, the migration's
existing baseline. It enumerates all 1,114,112 code points, including surrogates,
to obtain every full singleton lowercase mapping, Cased and Case_Ignorable
property ranges, and ECMAScript whitespace. It also verifies singleton trim
and `\s` agree everywhere. There are 1,488 lowercase mappings, 158 Cased ranges,
464 Case_Ignorable ranges, and 10 whitespace ranges. FEFF is whitespace; 0085,
180E, and 200B are not. Go's Unicode tables are not used.

The conditional behavior was audited in the exact pinned Node source files;
URLs, SHA-256 pins, and audit conclusions are in `provenance.json`, referring to
the same official archive pinned by `backend/native/icu/provenance.json`.

- V8's `Intl::ConvertToLower` uses ICU's root locale, with an equivalent
  context-free Latin1 fast path.
- ICU's `ucase_toFullLower` evaluates all conditional lowercase branches.
  Lithuanian More_Above and precomposed-accent rules, and Turkish/Azeri After_I,
  Not_Before_Dot, and dotted-I rules require those locales. They do not apply to
  JavaScript `toLowerCase`.
- Root U+0130 expands to U+0069 U+0307. All other context-free full mappings,
  including mappings of newer and supplementary letters, are enumerated by the
  generator rather than replaced with simple lowercase tables.
- Final_Sigma is the sole root context-dependent lowercase rule. U+03A3 becomes
  U+03C2 when preceded by a cased character through any case-ignorable sequence
  and not followed by one through such a sequence. Case_Ignorable is checked
  first when a character has both properties, including U+0345. Context uses
  the original input to the lowercase step, after whitespace normalization for
  fingerprints, not already-lowercased output.

The left context is tracked incrementally. Right lookahead skips only
Case_Ignorable characters; sigma is non-ignorable, so lookaheads from distinct
sigmas cannot overlap. Processing is linear in input length with binary table
lookups and linear temporary storage, without a separate input-size limit.

## Verification

```sh
node backend/tests/generate-jsstring-fixtures.mjs --node-source /path/to/node-v24.22.1
node backend/tests/generate-jsstring-fixtures.mjs --check
cd backend
go test -race ./internal/jsstring
go vet ./internal/jsstring
```

`--node-source` additionally verifies both audited source file hashes. The normal
generation/check commands verify runtime versions and the existing source pins,
then regenerate the tables and synthetic fixture byte-for-byte. No downloaded
code is executed. The checked-in fixture stores strings as UTF-16 arrays to
avoid lossy JSON decoding in Go.

There are 3,142 actual JavaScript differential cases: every whitespace character,
all Cased/Case_Ignorable range edges in five sigma contexts, dotted-I expansion,
non-applicable locale tailoring, combining marks, supplementary and Unicode
16/17 letters, all high/low lone surrogates, broken/valid pairs, 512 seeded mixed
UTF-16 cases, and an ignorable sequence exceeding 65,000 code units. Additional
Node JSON-string SHA-256 digests cover the entire Unicode codespace in both
operations, including unchanged characters. Tests also reject malformed WTF-8
and verify table ordering. Fixture sizes describe test coverage, not runtime
admission limits. These are focused string-operation checks, not a claim of
full memory API or backend parity.
