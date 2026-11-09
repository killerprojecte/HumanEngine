# Builtin core source oracles

Run from the repository root with its installed, lockfile-resolved Node dependencies
and Node 24 (the verified runtime was v24.22.1):

```sh
node --import tsx backend/plugins/snowluma/check-source.mjs --check
node --import tsx backend/plugins/typing-delay/check-source.mjs --check
node --import tsx backend/plugins/web-read/check-source.mjs --check
node --import tsx backend/plugins/web-search/check-source.mjs --check
```

Each script imports the real `src/plugins/builtin` TypeScript implementation and
reads its own `core/testdata/node-*.json`. It compares every fixture value and
fails on disagreement. `--check` does not write files. No network connections,
Go tests, WASM compilation, or application builds are needed. Source files and
Node dependencies must be present; these are source parity checks, not a
source-free distribution check.

`--print` instead prints `{ "file": filename, "data": result }` for each fixture,
using the same committed inputs and actual TypeScript implementation. It does
not overwrite fixtures. Review source changes and printed data before manually
replacing expected values; never regenerate expectations merely to hide a
parity failure. JSON field names and existing values are preserved.

SnowLuma calls `parseCqMessage` and the real `OneBotAdapter` identifier methods;
it constructs and disposes the adapter without connecting. Typing delay calls
`typingDelayMs` (including its real grapheme segmentation). Web read calls
`extractWebText` (including its real HTML parser). Search calls `parseApi` and
`publicResultUrl`; the fixture's empty string represents an undefined rejected
URL. No handwritten implementation of these algorithms is used.
