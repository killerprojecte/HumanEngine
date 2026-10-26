# Dashboard verification

The dashboard was exercised through the supported cloud CDP browser against a normal local production-server process on the same cloud desktop. No public tunnel or deployment was used. The earlier shell-launched Chromium attempt failed with a socket restriction; this run used the existing supported browser, without changing browser security flags or permissions.

## Safe fixture

All displayed conversations, identities, memories, rules, images and model replies are synthetic. The fixture uses a local plugin protocol and an adapter that cannot deliver platform messages. Two OpenAI provider configurations are intentionally unconfigured; no live API key or QQ account was used. The screenshot access token is disposable and unrelated to deployment credentials. Temporary data is separate from project/runtime data and is not shipped in the image.

## Defects found and fixed

- Rules loading/reset and plugin configuration selection/reset/save attempted to `structuredClone` Vue reactive proxies. This threw `DataCloneError`, displayed a misleading network error, and could disable the editor. Snapshots now clone Vue's raw data.
- Cache duration used a fractional minimum without a compatible input step, so the browser rejected the default 24 hours. It now accepts the schema's fractional precision.
- The same numeric-input audit corrected memory confidence so a valid value such as 0.73 can be edited without rounding to 0.05 increments.

## Observed desktop coverage

All ten main pages rendered with synthetic data: overview, models (providers/instances/routes), chat simulation and read-only history preview, memories, rules, memes, plugins, scenes, settings and technical logs. Light/dark and English/Chinese states were captured through the real browser, not generated mockups.

After rebuilding the fixes, browser retests confirmed:

- Rules load the correct scene; an empty rule disables save; reset restores the draft; valid edits save.
- Plugin configuration opens, edits, resets and saves; the example custom page reads the newly saved greeting.
- An invalid timezone is rejected, reset restores UTC, and the default 24-hour cache setting saves.
- A manual memory with confidence 0.73 saves and displays 73%.
- Local simulation returns a controlled reply; synthetic adapter history renders with explicit coverage; canceling memory edits returns to the list.

Screenshots are delivered separately as synthetic QA artifacts, not bundled in the application image. A rendered screenshot demonstrates the observed page state, not untested provider quality, full accessibility certification or every device layout. Live providers/QQ, recipient animation and target-machine performance still require their separate checks.

The fixed frontend passed runtime/Nuxt type checking, contract drift checks, formatting and a fresh production build. Container CI independently runs the full integration checks, builds and tests the final image before GHCR publication.

## Compact workspace pilot

The first redesign covers the shared shell and four pages, not a completed redesign of every settings screen. The official MaiBot dashboard was run locally with synthetic API responses as an information-layout reference; no upstream application code or visual assets were copied.

- Overview groups four status metrics, scene activity, runtime health and ordered model routes instead of a permanent onboarding block.
- Models separates providers, instances, routes and usage into searchable registries. One bounded drawer edits the selected entry; Cancel restores its opening snapshot, Close retains the draft, and Save persists configuration.
- Chat uses a scene rail, independently scrolling conversation and pinned composer. Simulation and read-only platform preview remain separate, with the existing isolation rules unchanged.
- Logs uses a dark monospace console with level/module/search filters and expandable technical context. Polling continues while the displayed snapshot is paused; scrolling upward stops follow, and Jump to latest resumes it. Both retained and displayed buffers are bounded to 500 entries. Temporary sensitive diagnostics remain a separate section.

Real desktop review used seven model instances, three providers, four scenes, long names and a synthetic event stream. It exercised empty search results, editor Save/Cancel/Close behavior, expanded navigation, light/dark layouts, simulation sending, read-only scene switching, log scroll pause and new-entry jump. Collapsed navigation exposed missing accessible link names during review; localized ARIA labels were added and manual sidebar preference is retained across reloads. This is a focused accessibility repair, not an accessibility certification.

The pilot passed the existing 47 integration checks, contract drift checks, runtime/Nuxt type checks, formatting and a fresh production build. The final navigation-only repair was rechecked with types, formatting and a fresh build; it did not change the backend or contract. Warning/error log colors and phone-sized layouts are not claimed as browser-verified by the INFO-only desktop fixture. Screenshots are actual synthetic browser captures, not design mockups, and do not establish live-provider or QQ compatibility.
