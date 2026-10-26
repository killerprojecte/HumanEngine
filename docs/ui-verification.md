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
