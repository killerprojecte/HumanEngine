# Cached tool-result read authority

Meme Engine composition retains the actual model-read ticket as a cached-result
guard and preserves inherited extension capture hooks. A later turn checks that
ticket using its already-held scoped read authority. It does not open a nested
root publication, retain a transaction, renew an old ticket, or bind validity to
the originating turn's cancelled context.

This follows the source Engine capture boundary. The source does not have a
separate Meme asset/version exposure owner. Meme visibility callbacks are ordinary
current-turn validations and are not promoted into new cross-turn restrictions.
The other source exposure owners and complete application composition remain
separate unfinished work.

Validation on the generation-bound plugin checkpoint includes the actual TS
capture oracle, model-read revocation and re-enable cases, inherited-hook
composition, and real native Runtime turns with SQLite and the disk tool-result
cache. The three affected package vet checks passed. The complete default/native
suite remains pending feature branch CI for this checkpoint; it was not rerun
locally in full.

The source oracle is included in `make differential`. Application composition
must not additionally register the same model-read capture hook outside the Meme
Engine extension. No HTTP101 or private media-network behavior changes here.
