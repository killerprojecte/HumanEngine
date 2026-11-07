# Credential v1 byte compatibility

This package preserves the **canonical envelopes produced by**
`src/secrets/store.ts`: AES-256-GCM, a 12-byte random nonce, a 16-byte tag,
standard padded base64, and `v1.nonce.tag.ciphertext`. It does not yet claim
compatibility with every noncanonical envelope accepted by that source reader.

## String boundaries

- Inputs use `jsjson`'s canonical WTF-8 representation of JavaScript strings.
  Decode source JSON with `jsjson.Parse` before calling this package;
  `encoding/json` cannot recover a lone surrogate it has already replaced.
- AAD is the exact UTF-8 encoding of `JSON.stringify([namespace, name])`.
  Lone surrogates remain distinct lowercase JSON escapes. Valid pairs use
  ordinary UTF-8. HTML, U+2028, and U+2029 are not escaped. No normalization or
  trimming takes place.
- Plaintext uses Node's `cipher.update(value, 'utf8')` conversion: each lone
  surrogate becomes U+FFFD, while valid pairs and embedded NULs are preserved.
- Authenticated decrypted bytes use Node `Buffer.toString('utf8')` replacement
  semantics. The pinned `golang.org/x/text/encoding/unicode.UTF8` decoder uses
  maximal-subpart replacement and preserves BOMs. This is not Go rune decoding
  or `strings.ToValidUTF8`, which differ on malformed sequences.
- Invalid/noncanonical internal WTF-8 strings are rejected, not silently
  normalized. Decryption failures return only `secret_decryption_failed`.

`Cipher` is an encryption helper, not the full SecretStore mutation validator.
The source's nonblank-value and UTF-16-length policy belongs to its caller.
In particular, raw AES-GCM encryption can produce an empty body, but the source
reader rejects an empty ciphertext segment. `Decrypt` now matches that rejection.
No new protection limits have been introduced.

## Explicit remaining noncanonical reader differences

The source takes the first four `split('.')` fields and ignores later fields.
This candidate still requires exactly four fields.

Node `Buffer.from(text, 'base64')` accepts unpadded input, the URL-safe alphabet,
spaces/tabs, ignored punctuation, and data after padding. The candidate retains
Go's `base64.StdEncoding.DecodeString` behavior. Both accept CR/LF and nonzero
unused padding bits in the covered cases. The reader fixtures distinguish these
behaviors rather than describing the Go decoder as universally strict.

The source Node/OpenSSL reader accepts variable nonce sizes and authentication
tag sizes 4, 8, and 12 through 16 bytes. Fixtures demonstrate nonce sizes
1, 8, 16, 32, and 128 bytes, those shorter valid tags, and a 16-byte nonce with a
12-byte tag. These are examples of accepted inputs, not an assertion that every
nonce size is supported. Node 24 reports a deprecation warning for a tag shorter
than 16 bytes when its size was not specified to `createDecipheriv`.
The candidate still only accepts nonce 12/tag 16.

Go's public standard-library AEAD constructors offer nonstandard nonce size
with a 16-byte tag (`NewGCMWithNonceSize`), or tag sizes 12 through 16 with a
12-byte nonce (`NewGCMWithTagSize`). They expose neither 4/8-byte GCM tags nor
combined nonstandard nonce and tag sizes. Full source-reader parity cannot be
obtained by merely selecting one of these constructors. No private/internal
crypto implementation, truncated-tag workaround, or custom cryptography is used
here. Any expansion needs an explicit vetted implementation decision and tests.

These residual differences are not evidence of lost app-produced data: the
source writer always emits nonce 12/tag 16 and canonical base64. They still
block an unqualified claim of arbitrary v1 reader compatibility.

## Synthetic evidence

Run from the repository root:

```sh
node --import tsx backend/tests/generate-credential-byte-fixtures.mjs --check
cd backend
go test ./internal/credentials -run 'TestNode|TestCredential' -count=1
```

The generator pins Node 24.22.1 and verifies the actual `src/secrets/store.ts`
source hash. It calls `SecretStore.apply/get` with in-memory SQLite and scoped
deterministic random bytes. It never supplies a key path or opens an installation
database. Fixed keys and repeated nonces are exclusively synthetic test vectors,
not an encryption example to copy into production.

- 14 canonical envelopes cover unchanged `node-v1.json` bytes, JSON input
  decoding, valid pairs, unpaired high/low surrogates, surrogate identity versus
  U+FFFD, NULs, HTML, line separators, controls, BOMs and normalization distinctions.
- 18 validly authenticated byte envelopes exercise Node's malformed UTF-8
  decoding. They are crafted with Node crypto and then read by the actual store.
- 38 reader cases record success/error from the actual source, including
  malformed/tampered inputs and 20 known noncanonical source-success differences.
- 237,889 separate byte inputs cover all empty/one/two-byte strings, combinations
  of three/four-byte boundary classes, and deterministic longer byte sequences.
  Length-delimited digest comparisons preserve both input and decoded-output
  bytes and verify the pinned Go UTF-8 decoder against actual Node Buffer output.

These are focused crypto/string tests, not a full application migration gate.
Key custody, file permissions, paths, source code and the existing canonical
fixture are unchanged.
