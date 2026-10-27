# Security notes

Keep management on loopback by default. Use your own long administration token and write-only provider keys stored with authenticated encryption (see [credential storage](docs/secrets.md)). Do not deploy development servers publicly. Trusted local plugins have host privileges; this is not a sandbox for untrusted code.

## Dependency audit, 2026-10-25

The locked Nuxt 4.6.2 → Nitro 2.13.5 → listhen 1.10.1 development-tool chain includes `node-forge` **1.4.0**. [GHSA-86w9-cpqp-85rv](https://github.com/advisories/GHSA-86w9-cpqp-85rv) reports an RSA PKCS#1 v1.5 signature-verification issue through 1.4.0 and lists **no patched release**. Seven high-severity audit entries propagate this one root issue through dependent packages; they are not seven independent vulnerabilities.

The inspected production Node-server output contains neither node-forge nor listhen. HumanEngine uses a bearer-token comparison for management authentication, not forge signature verification. The dependency's observed use is development certificate/PFX handling; this project does not enable development HTTPS or forge verification. This limits the observed exposure but is **not** a universal no-risk assertion. Do not supply untrusted certificate/PFX material to that tooling. Recheck before changing build presets, HTTPS tooling, or dependencies. No deployment was performed.

[GHSA-g7r4-m6w7-qqqr](https://github.com/advisories/GHSA-g7r4-m6w7-qqqr) affects esbuild's Windows development file server in versions 0.27.3–0.28.0. The fontless-only transitive esbuild is pinned to stable **0.28.2** via an explicit override; fontless uses its CSS transform API, not the development server. This override is tested with the build and CSS transformation. Nuxt font downloading is disabled.

Audit findings are not ignored or called clean. No forced Nuxt downgrade or prerelease dependency is used to hide the remaining forge advisory. Review the actual locked tree with `npm audit --omit=dev` and `npm ls node-forge esbuild`.

## Reporting

Avoid including secrets or raw private conversations in issue reports. Reproduce with local fixtures and include only sanitized technical event codes, versions, and affected boundaries.

## Browser administration sessions

The browser exchanges the administration token for a random, HttpOnly, SameSite=Strict
session cookie; the administration token is never persisted in localStorage, sessionStorage,
or SSR state. Sessions have an absolute seven-day lifetime (activity does not extend it).
Disconnect revokes the session on the server, including other tabs using that cookie.
Sessions are held in server memory: server restarts and administration-token changes
require a new login. This is a single-process management deployment, not a shared-session
multi-replica service. The existing bearer-token API remains available to API clients.

HTTP loopback and trusted LAN access work without additional configuration. HTTP does
not encrypt credentials or cookies: use HTTPS on untrusted networks. Direct TLS requests
receive Secure cookies. For a TLS-terminating reverse proxy, preserve the original Host
and set `HUMANENGINE_SECURE_COOKIES=true` on the server; do not use that setting for an
HTTP-only deployment. Arbitrary `X-Forwarded-Proto` / `X-Forwarded-Host` values are not
trusted to select the cookie policy. Cookie-authenticated API calls require a same-origin
custom header and reject cross-origin browser requests; no cross-origin session API is
exposed. Logout failures are visible and can be retried rather than silently claiming
that the server session was revoked.

Cookie-changing login/logout requests are serialized within each browser document.
Other tabs share the cookie and revalidate on authentication notifications or focus.
Simultaneous independent logins in different tabs are not a globally ordered transaction;
a later completed explicit login can establish a new session after another tab logs out.
