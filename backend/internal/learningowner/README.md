# Native source learning owner

`New` borrows the real database, recovery authority, assertion/source stores,
MemoryService, HabitService, NativeModels, model-read policy, foreground runtime,
and logger. It checks database/authority binding before installing the native
learning coordinator as a runtime extension. It does not enumerate or replay
persisted messages. `SettingsChanged` refreshes coordinator timing policy from
schema-parsed current settings. `CloseAndWait` detaches, cancels and joins the
coordinator before borrowed owners may close.

The owner composes source reconciliation targets, source-only pronoun validation,
current source snapshots, lexical/vector search, conservative public review, and
embedding index persistence. Public review and embedding provider calls run after
the memory proposal's commit and outside the main writer. A provider failure
cannot roll back the proposal or authorize replay. Source query/target limits are
preserved as product policy, not introduced admission caps.

Build with the pinned `humanengine_icu` profile for source Chinese segmentation
and Unicode classification, and `sqlite_fts5` for the actual projection owners.
The unavailable ICU build returns an explicit dependency error, never silently
substitutes a different segmenter. Test fixtures use synthetic data and separate
sibling main-database and authority directories.
