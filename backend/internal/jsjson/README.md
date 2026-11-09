# Lossless JavaScript JSON data

This migration foundation is integrated into candidate recovery-event validation,
typed event encoding, scene writes and settings writes. Other `encoding/json`,
`jsoncompat`, HTTP and plugin callers are not automatically migrated. Passing a
preserved string through those callers can still destroy lone-surrogate identity.

## Representation and API

`Parse(string)` and `ParseBytes([]byte)` return only:

- `nil` for JSON null;
- `bool`;
- `float64` for JavaScript Number, including overflow to infinity and signed
  zero on parse;
- Go `string` containing **canonical WTF-8**;
- `[]any` for an array;
- `*Object` for an ordered object.

`Undefined` is a separate native-only value for JavaScript own-property semantics.
`Parse` and `ParseBytes` never produce it: JSON null remains `nil`, and a string
spelling "undefined" remains an ordinary string. It is not represented by an
empty object, a magic property, or a caller-controlled text marker. `Get`, `Len`,
`Keys`, `Members`, and `Clone` preserve an explicitly set undefined property;
removing the property is observably different. Native deep copies preserve its
zero-field value, and equality distinguishes it from null and a missing member.

`Stringify` omits object members whose value is `Undefined`, while an array
containing that value emits `null` at that position. For a root `Undefined`,
`Stringify` and `Marshal` return `ErrUndefinedRoot`, because JavaScript produces
no JSON text rather than a JSON representation. This does not add support for
functions, Symbols, sparse-array holes, arbitrary Go structs, or Go maps.

`Stringify(any)` returns the compact JSON string for encodable values. `Marshal(any)` returns the same
bytes. Encoding does not HTML-escape `<`, `>`, or `&`, does not escape U+2028 or
U+2029, emits lowercase escapes for lone surrogates, and emits ordinary UTF-8 for
valid pairs. Its result is always valid UTF-8. It formats binary64 values with
JavaScript's shortest decimal presentation, fixed notation from 1e-6 through
values below 1e21, and the appropriate exponent sign without padded zeros.
Negative zero encodes as `0`; infinities and NaNs encode as `null`.

This is semantic losslessness for the JSON.parse data model, not preservation of
the input's lexical spelling or duplicate properties. `-0`, infinity, whitespace,
escape spelling, and duplicate keys change when JSON.stringify changes them.

Canonical WTF-8 is ordinary UTF-8 plus the three-byte encoding of an **unpaired**
UTF-16 surrogate. A paired high/low surrogate is always the corresponding
four-byte UTF-8 scalar. For example, `FromUTF16([]uint16{0xd800})` contains the
bytes `ed a0 80`, distinct from both another surrogate and U+FFFD (`ef bf bd`).
Canonicalization ensures Go equality and Go map keys agree with JavaScript
string equality. No Unicode normalization is performed.

- `FromUTF16([]uint16)` constructs the canonical representation without loss.
- `UTF16(string)` retrieves the original code units.
- `UTF16Len(string)` matches JavaScript `string.length`.
- `CodePointCount(string)` matches JavaScript `[...string].length`: a lone
  surrogate counts as one; a valid pair also counts as one.
- `ValidateString(string)` validates the canonical representation. Helpers
  return an error instead of silently substituting invalid bytes.

`Parse` accepts WTF-8 source text, including raw lone surrogates and adjacent
surrogate encodings, and normalizes each decoded string to canonical WTF-8.
This makes a JavaScript source string representable even when it cannot be
encoded as scalar-only UTF-8. Malformed bytes other than surrogate encodings are
syntax errors. On a normal UTF-8 JSON wire/file, lone surrogates should be
represented using `\ud800`-style escapes. `ParseBytes` does not emulate Node
Buffer's lossy UTF-8-to-string conversion; the caller owns that transport step.

## Objects and copying

`new(Object)` is an empty object. `NewObject(...Member)` accepts ordered members.
`Set`, `Get`, `Delete`, `Len`, `Keys`, and `Members` provide property access.
Keys must be canonical WTF-8. `Set` and `NewObject` reject malformed keys; value
types are checked when serialized.

Replacing a key preserves its original position. Deleting then reinserting it
moves it to the end of non-index insertion order. `Keys`, `Members`, and encoding
use JavaScript own-property order: canonical integer-index keys 0 through
4294967294 in ascending order, followed by other strings in insertion order.
`"01"`, `"-0"`, `"1.0"`, and `"4294967295"` are not integer indices.
`"__proto__"` is an ordinary data key, never a prototype mutation.

`Members` returns a shallow copy of the property list. `Clone` gives a new object
with independent property storage but **shared nested objects/arrays**, like an
ordinary shallow JS object copy. Do not copy an initialized Object by value or
mutate it concurrently. There is no implicit deep-copy API: a JSON round trip
would change negative zero and nonfinite numbers, which is not a general clone.

Go maps have no insertion order. This package rejects them, along with structs,
integer types, float32, and typed slices, rather than inventing an order or
assuming a conversion. Explicitly construct `*Object`, `[]any`, and `float64`
values. A nil `[]any` represents an empty array; a nil `*Object` is rejected;
untyped nil represents JSON null. Cycles are rejected while repeated noncyclic
references are allowed.

## Migration boundaries

Use the lossless decoder at the first JSON boundary. `encoding/json` has already
lost information after decoding a lone surrogate, so wrapping its result later
cannot recover an ID. Do not encode `Object` or WTF-8 strings with `encoding/json`
(including through a `json.Marshaler` interface: its validation/escaping behavior
does not preserve this byte contract).

Go `range` over string, `[]rune`, `unicode/utf8`, `unicode/utf16.Decode`, most
Unicode transforms, regular expressions, and standard JSON processing do not
understand canonical WTF-8 surrogate encodings. Do not use them on preserved
identifiers or assume rune counts match a JavaScript validator. In particular,
Zod code-point limits and handwritten JavaScript UTF-16 limits need different
helpers. JS regex semantics (including `u`/non-`u` mode), collation, case mapping,
trimming, arbitrary JS string operations, transport Buffer decoding, hash-input
encoding and collation are **not** implemented here. The explicit `UTF8Text` helper
covers Node’s string-to-UTF-8 boundary only. Each caller
must select and test its actual baseline behavior, including its hashing and
storage boundaries.

The package covers plain JSON data. It does not implement revivers, replacers,
indentation, source-text coercion, `toJSON`, accessors, prototypes, symbols,
undefined, array holes, BigInt, or JS object execution. Syntax errors are Go
errors with zero-based byte offsets, not engine-specific JS message text.
There are no package-imposed input, nesting, property-count, or output limits;
normal process memory and stack constraints still apply.

## Verification

The synthetic oracle is pinned to Node **24.22.1**:

```sh
node backend/tests/generate-jsjson-fixtures.mjs
node backend/tests/generate-jsjson-fixtures.mjs --check
cd backend
go test -race ./internal/jsjson
go vet ./internal/jsjson
go test ./internal/jsjson -run '^$' -fuzz FuzzParseStringify -fuzztime 10s
```

The fixture contains 724 parse cases, 2,311 string cases, 12,001 distinct binary64
bit patterns, and four ordered-object mutation traces. It checks parsed number
bits (including -0 and overflow), every UTF-16 code unit, every lone surrogate,
valid/broken/mixed raw-and-escaped pairs, string lengths, duplicate keys,
prototype-named keys, property order, controls, HTML, separators, numeric decimal
rounding, power-of-ten neighbors, and seeded random cases. UTF-16 unit arrays
carry raw strings through the fixture container without `encoding/json` loss.

Additional tests cover malformed encodings, exact identity, shallow-copy
behavior, cycles/shared references, unsupported types, parse/encode stability,
and nesting beyond the standard Go decoder's 10,000-level cap. The corpus is not
an exhaustive proof over all binary64 values or all strings. No performance or
full-server migration claim follows from these focused tests.

`UTF8Text` is the separate Node text-binding boundary: canonical lone surrogates
become U+FFFD and embedded NUL remains intact. Twelve actual Node Buffer and
better-sqlite3 cases verify byte output, lookup behavior and intentional key
collisions against Go SQLite. Do not apply it to JSON.stringify hash material or
JavaScript equality; those must retain the original code units.
