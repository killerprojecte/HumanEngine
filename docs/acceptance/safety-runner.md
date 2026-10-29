# Frozen mechanism acceptance

This manifest contains exactly 200 distinct existing top-level literal `node:test` registrations across 27 mechanisms. Each binding has an exact file, test name, fixture hash, scope, authority change and expected behavior. The expected behavior is the existing test's declared contract, not independently annotated semantic gold. A test with several assertions still counts once. Different tests in one file retain distinct execution identities; no repeated run adds combinations.

Static validation checks AST registrations, uniqueness and frozen fixture hashes. It does **not** execute tests and reports `not_run`, never success. Fixture changes require explicit review/re-freezing of the affected manifest hash, not silent repair by the runner.

```sh
node --import tsx scripts/acceptance/runner.ts --output /tmp/safety-static-UNIQUE.json
node --import tsx scripts/acceptance/runner.ts --run --output /tmp/safety-run-UNIQUE.json
# Small selected execution; cannot authorize whole-manifest advancement:
node --import tsx scripts/acceptance/runner.ts --run --case safety-001 --output /tmp/safety-selected-UNIQUE.json
```

Actual execution selects exact escaped test names, groups by file and runs serial child processes. A structured Node reporter requires one actual top-level pass event with matching file/name, no skip/todo, and successful process exit. Missing registrations, process errors, timeouts, skipped tests, malformed reporter output, changed source snapshots and failed assertions block advancement. On first failed file later groups remain `not_run`. Reports are created exclusively and never overwrite earlier failures. Full-manifest pass is only one acceptance requirement; the separate actual longitudinal/ablation report and owner aggregate/type/UI/build gates remain necessary.

The runner strips inherited provider credentials/endpoints and NODE_OPTIONS. Its preloaded guard blocks accidental non-loopback HTTP/fetch/TCP, callback DNS lookup and datagrams; localhost is allowed for synthetic native-protocol fixtures. This is **not** a sandbox against malicious trusted code, arbitrary plugins, spawned binaries or deliberate native-network bypass. No real provider path is selected by this manifest. The runner does not read API keys or invoke package installation.

`acceptanceSourceHash(root)` covers sorted exact `src/`, `tests/`, `scripts/`, package/lock files and this manifest. Reports stay outside those roots. The longitudinal harness imports the same helper and emits its own `kind: longitudinal_ablation` report; passing reports may be combined only with the same source hash. Actual model quality, semantic truth, longitudinal benefit and human-reviewed semantic gold remain pending separately authorized evaluation.

The 120-second per-file watchdog and 8 MiB structured-output bound are harness engineering limits, not production performance claims. Timing includes process startup; it is not p95 latency. No benchmark is run by this command.

Run the actual synthetic longitudinal ablations from the repository root (choose a new output path each time):

```sh
node --import ./scripts/acceptance/network-deny.mjs --import tsx --input-type=module -e 'import { writeFileSync } from "node:fs"; import { runRuntimeLongitudinal } from "./scripts/acceptance/runtime-longitudinal.ts"; const report = await runRuntimeLongitudinal(); writeFileSync("/tmp/longitudinal-UNIQUE.json", JSON.stringify(report, null, 2), { flag: "wx" }); if (report.status !== "passed") process.exitCode = 1;'
```

No provider, URL or credential option is accepted. The configured protocol is a local scripted test double passing through the actual Engine/Router and SQLite lifecycle. Synthetic event dates span days; server receipt and accounting dates use the real clock. No platform send is requested.

After both actual reports pass on the same unchanged source snapshot:

```sh
node --import tsx scripts/acceptance/aggregate.ts /tmp/safety-run-UNIQUE.json /tmp/longitudinal-UNIQUE.json /tmp/combined-UNIQUE.json
```

The merge verifies all 200 exact test bindings and all eight required stages in each of `no-memory`, `ordinary`, and `qualified-feature`, with nonempty successful checks, actual learning-call differences, distinct configurations and no mechanical failures. These are trusted local execution receipts, not cryptographically authenticated claims from arbitrary report authors. Source and receipt hashes detect accidental mixing; they do not turn fabricated input JSON into independent audit evidence.

The process guard intercepts accidental outbound HTTP/fetch/TCP and callback `dns.lookup`; it does not wrap every DNS API (`dns.resolve*`, `dns.promises.*`, or custom `Resolver` instances). The selected trusted fixtures do not use those paths. This is not an operating-system sandbox or protection against arbitrary hostile plugins.
