# Managed Meme outgoing and legacy V1 runtime

## Included behavior

The native Engine installs `media-scope` at priority -100. Each lowercase 64-hex managed media identifier is checked by MemeService.Original using the current scene and simulation policy. A real `message.sent` event records usage for each managed media part; failed/unknown sends do not create the event, and a missing receipt message ID does not prevent usage. Disabled learning suppresses persistence without turning a successful send into an error.

The Runtime reserves callback ownership while the accepted send still owns its physical task. It releases the scene lock before invoking domain code. Each reservation is bound to its originating Runtime, even if a sequencer is explicitly shared. The receipt's recall state is checked when the callback executes; recalled output has no media parts. Transport ACK and turn completion do not wait for usage SQL, but CloseAndWait retains accepted physical callbacks until that SQL returns.

The legacy V1 bridge uses the same native Runtime, model host, source authority and Meme filesystem owner. It covers installation/generation changes, ingress, exact edit results, sends, recalls, managed media resolution and physical callback shutdown. EditResult is captured by the actual source mutation, never reconstructed by a later head lookup. Legacy V1 readImage remains omitted, matching the source wrapper. V1 plugins intentionally use the shared unloadable Node host; native business owners do not proxy to a TypeScript backend.

## Validation

The pinned TypeScript Engine supplies eight outgoing hook/listener fixtures. Native tests use that same fixture for managed-part filtering, publication/recall payloads and actual SQLite usage outcomes. Separate real V1 tests exercise native planner/tool evidence, managed send authorization, restored asset identifiers, original bytes, successful/failed/unknown sends, missing IDs, learning changes and shutdown ownership. A real plugin recall invokes the native Runtime/source owner and verifies the persistent tombstone, canceled transport and zero usage.

Held-connection tombstone and learning changes in two fixtures test execution-time state visibility. They are not presented as tests of the complete durable recall API; the separate real-recall case covers that entry point. Concurrency tests establish the stated publication, recall and physical-drain boundaries, not equivalence of every JavaScript microtask schedule.

`make differential` runs the actual outgoing source generator in check mode so source drift cannot silently leave a static fixture accepted. Native verification uses the FTS5/native ICU profile, race testing and vet.

## Excluded scope

This slice contains no public media-fetch/cache implementation and no HTTP 101 Upgrade handling change. It does not add native SnowLuma transport, historical-image/vision composition, application/plugin-manager startup, or the full source flow-span/receipt persistence envelope. Those remain separate integration work; passing this slice is not a declaration of complete backend migration.
