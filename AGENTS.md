# HumanEngine contributor guide

- Keep the runtime TypeScript and the interface Nuxt UI; prefer small vertical slices.
- Source identifiers, comments, and commit messages use English. Translation strings belong in locale files.
- Preserve scene/person isolation, simulation isolation, recall tombstones, and no restart replay.
- OpenAPI is generated from server schemas. Regenerate the typed client after contract edits; never hand-edit generated files.
- Pin stable dependencies and commit the lockfile. Run contract checks, focused integration tests, type checks, and the production build before publication.
- Never commit credentials, local SQLite files, media, or raw chats. Do not print keys in errors.
- Keep tests focused on real boundaries rather than redundant unit coverage.
