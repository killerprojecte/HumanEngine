# P1-B: natural meme planning and exact outgoing media

This is a deliberate behavior delta on the frozen Go migration baseline, not a
regeneration of its legacy oracle. The named prompt fragments come from legacy
behavior commit `04b1817af8055d58021c85e94d1f72be914125d6`, tested source commit
`b7f3f92b0d27168955f507535e38c00ed15c6826`. The fixture records both Git blob IDs
and SHA-256 hashes. To reproduce it from an exact source checkout:

```sh
cd backend
node tests/generate-meme-behavior.mjs /path/to/tested-legacy-checkout
```

The generator rejects different source files. The frozen TypeScript source,
source fingerprints and base runtime oracle are unchanged. Expected model
requests explicitly overlay only the named system-prompt paragraphs.

Planner may initiate and refine a semantic search in its existing tool loop.
Candidates retain IDs and analyzed tags. Search candidates are read evidence,
not dependencies of a future send: disappearance of an unchosen asset must not
invalidate meaningful text or another selected asset. Final chosen managed
media uses the existing runtime authority, source visibility, permission and
availability checks through `ValidateOutgoingScoped`. The runtime invokes this
helper on the complete post-hook payload and again on the exact current segment
at the physical transmit gate. The P1-C lifecycle retains those checks while
allowing unrelated ingress or unselected-candidate recall to leave a draft intact.
Existing media normalization and adapter original-file resolution remain.

After a real search, a standalone `[meme]`, `[表情包]` or equivalent marker is
not media. Grounding requests repair rather than silently truncating it. This
check does not reject ordinary discussion of memes or independent text.

## Verification

```sh
go test -race -tags sqlite_fts5 ./internal/memeengine ./internal/botruntime ./internal/replyplan
go vet -tags sqlite_fts5 ./internal/memeengine ./internal/botruntime ./internal/replyplan
```

`TestNaturalMemePlanningLoopNativeOwners` runs actual native model requests,
planner/replyer loops, SQLite-backed meme service and adapter byte resolution.
Cases cover search refinement, media-only output, meaningful text after no
match, no reply, serious text without search, marker repair, unselected removal,
selected disable/recall, permission revocation and post-hook media replacement.
The model is a deterministic fixture; this is not a claim about live-model
initiative. The exact outgoing callback also has a direct service-backed test,
including inherited callback preservation.
