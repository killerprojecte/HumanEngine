# Go migration contributor guide

This branch implements the user-requested Go backend migration; it supersedes the repository's older TypeScript-runtime preference for this directory.

- Keep business logic in focused packages. The command composes services; HTTP handlers do not own bot state or call a global bot queue for queries.
- Preserve the baseline API and robot behavior. Add differential fixtures before replacing a domain. Do not proxy unfinished bot operations back to the TypeScript server and call them migrated.
- Keep scene/person isolation, simulation boundaries, recall/edit semantics, exact durable authority intent/ACK recovery, and no restart replay.
- Do not introduce protective resource caps or silent truncation. Distinguish existing product policy and intrinsic protocol widths from new admission limits.
- V1 uses a lazy shared Node process with unloadable worker isolates. Zero enabled V1 plugins means no Node process. Disabling the last V1 plugin exits that process.
- V2 builtin business logic executes in distributed WASM modules. A manifest or native callback wrapper alone is not a completed migration.
- Run gofmt, Go race tests/vet, actual WASM execution, source-free bundle tests, Node/Go differential tests and affected frontend checks. Keep generated REST types generated from source schemas.
- Use only synthetic fixtures. Never commit live databases, keys, chat content or credentials. If a publication step is blocked, leave it blocked rather than bypassing it.
- Progress commits belong only on feat/refactor-golang. An implementation checkpoint is not a declaration of full parity or production readiness.

- Backend SQLite builds require the `sqlite_fts5` profile. Use Makefile targets or explicitly pass that tag; bundle and native ICU builds combine it with their own tags. An untagged backend must reject database startup clearly rather than silently omit projections. WASI plugin builds remain separate.
