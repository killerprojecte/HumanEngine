# HumanEngine contributor guide

- Keep the interface in Nuxt UI and prefer small vertical slices.
- Source identifiers, comments, and commit messages use English. Translation strings belong in locale files.
- Preserve scene/person isolation, simulation isolation, recall tombstones, and no restart replay.
- OpenAPI is generated from server schemas. Regenerate the typed client after contract edits; never hand-edit generated files.
- Pin stable dependencies and commit the lockfile. Run contract checks, focused integration tests, type checks, and the production build before publication.
- Never commit credentials, local SQLite files, media, or raw chats. Do not print keys in errors.
- Keep tests focused on real boundaries rather than redundant unit coverage.

## Lean implementation

- Keep the Nuxt UI unchanged unless the task calls for a UI change. Follow backend/AGENTS.md for the Go migration; the TypeScript runtime remains the behavioral reference until cutover.
- Before adding or keeping a runtime guard, identify the invariant, where untrusted or mutable data first enters, and what concrete failure the guard prevents. Validate once at that boundary and pass typed, normalized data through trusted internal calls. Do not repeat a check unless the value or authority can change before use.
- Prefer one direct execution path. Remove unreachable branches, redundant nil/default checks, repeated parsing and normalization, speculative fallbacks, and duplicate copies, queries or allocations. Do not add new guard layers, generic wrappers, resource caps, or silent truncation without a demonstrated contract or runtime need.
- Keep authentication, authorization, isolation, validation of external input, and data integrity at their owning boundaries. Don't skip recovery or concurrency guarantees to reduce branch count; simplify their implementation without changing behavior.
- For each hot-path change, identify its call frequency and verify the claimed improvement with an appropriate benchmark or profile. Run affected regression tests and preserve Go/TypeScript behavior until cutover. Do not report a speedup without measurement.
