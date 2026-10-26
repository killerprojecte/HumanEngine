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
- Models separates providers, model instances, task assignments and usage. The later single-page revision described below replaces the initial drawer/global-draft interaction.
- Chat uses a scene rail, independently scrolling conversation and pinned composer. Simulation and read-only platform preview remain separate, with the existing isolation rules unchanged.
- Logs uses a dark monospace console with level/module/search filters and expandable technical context. Polling continues while the displayed snapshot is paused; scrolling upward stops follow, and Jump to latest resumes it. Both retained and displayed buffers are bounded to 500 entries. Temporary sensitive diagnostics remain a separate section.

Real desktop review used seven model instances, three providers, four scenes, long names and a synthetic event stream. It exercised empty search results, editor Save/Cancel/Close behavior, expanded navigation, light/dark layouts, simulation sending, read-only scene switching, log scroll pause and new-entry jump. Collapsed navigation exposed missing accessible link names during review; localized ARIA labels were added and manual sidebar preference is retained across reloads. This is a focused accessibility repair, not an accessibility certification.

The pilot passed the existing 47 integration checks, contract drift checks, runtime/Nuxt type checks, formatting and a fresh production build. The final navigation-only repair was rechecked with types, formatting and a fresh build; it did not change the backend or contract. Warning/error log colors and phone-sized layouts are not claimed as browser-verified by the INFO-only desktop fixture. Screenshots are actual synthetic browser captures, not design mockups, and do not establish live-provider or QQ compatibility.

## Models: task-oriented single-page revision

The models page was reassessed separately after the initial compact pilot. The desktop navigation shows names by default, the model table has four explicitly sized columns, and display/API names share one identity column rather than competing with one another. Text cells align at their first line; longer content grows the entire row. Capabilities and task purposes are localized. Configured support is not a successful capability test.

A bounded 680px modal contains one isolated entity draft. Common settings, optional cost estimates and explicit paid capability verification are separate. Save fetches current configuration and merges only this entity; it rejects an already changed or removed record rather than replacing it silently. This reduces stale unrelated edits but is not atomic multi-administrator conflict control: the existing API has no revision/ETag precondition. No backend contract was changed.

Cancel, X, Escape and backdrop dismissal share the same discard confirmation. Browser reload warns about an unsaved draft. Model deletion requires confirmation and removes its task references in the same configuration PUT. A referenced provider cannot be deleted. Discovery for a saved provider can fill an unsaved model's API identifier, with manual entry retained when discovery fails. Probes require saved configuration, a current model/provider check and a second explicit charge confirmation; no probe runs on render, selection or Save.

Actual synthetic desktop checks exercised isolated model/provider saves, add-then-cancel without a leftover record, X/Escape/backdrop discard, primary/fallback presentation, provider-in-use deletion refusal, discovery filling the identifier, the manual-entry fallback, valid credential-variable submission, unknown/zero price guidance and the explicit paid-probe confirmation without executing it. Browser inspection found duplicate checkbox IDs and a discard-dialog leave transition that briefly announced deletion; both were corrected and rechecked. The first 500px render also exposed a global touch-height rule stretching checkbox squares; the correction was rechecked at 500px with 16×16px checkbox squares, 44px label rows and no overlap. The 1180px table uses 67px ordinary rows and grows deliberately for long values.

Actual model deletion and route cleanup, server/network save-failure retention, rendered English/Traditional Chinese states, and paid probe execution were not exercised in this revision's browser pass. The existing backend integration suite is not a substitute for those UI interactions. Screenshots with a native invalid-input bubble or a modal transition are retained only as diagnostic evidence, not final appearance captures. This is a reviewed single-page candidate, not a claim that all remaining pages have been redesigned or that the user has accepted its appearance.
