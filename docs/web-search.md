# Built-in web search

`web-search` adds the read-only model tool `web-search.search`. It is disabled by default, and has no separate admin “search form”. Configure and enable it from **Plugins → Web search**. Queries are disclosed to the configured external search provider; do not send private conversations or credentials.

## Providers and compatibility

| Provider ID  | Implementation                                                               | Credential   |
| ------------ | ---------------------------------------------------------------------------- | ------------ |
| `bing`       | Experimental public Bing HTML search, not retired Bing v7                    | None         |
| `google`     | Experimental public Google HTML search                                       | None         |
| `sogou`      | Experimental public Sogou HTML search; unresolved tracking links omitted     | None         |
| `duckduckgo` | Experimental public DuckDuckGo HTML, **not** Python DDGS multi-engine parity | None         |
| `tavily`     | Official basic Search API, generated answer/raw content disabled             | Tavily key   |
| `you`        | Official POST unified Search API, web section                                | You key      |
| `you_news`   | Same official unified Search API, news section (can be empty)                | You key      |
| `deepseek`   | Official raw Search API; no model-generated summary or page fetch            | DeepSeek key |

The provider IDs correspond to the MaiBot community `YukiSakiko/google_search_plugin` v4.1.0 provider set. This is an independent implementation, not copied AGPL code. You News and DeepSeek intentionally use current official raw-search protocols, not the older MaiBot `/livenews` or Anthropic-compatible model calls.

HTML providers require **Allow experimental HTML** to be checked. Markup changes, region restrictions, redirects, CAPTCHA and anti-bot pages can make them unavailable. No challenges are solved or bypassed, and no availability claim is based on fixture tests. Bing v7 retired on 2025-08-11 and is not implemented.

## Credentials and controls

Keys are write-only and stored in the existing encrypted SQLite secret store under `plugin:web-search`; the installation encryption key stays outside SQLite. Blank input preserves a saved key. Use the explicit clear checkbox to delete it. Ordinary plugin config and API responses never return keys. A selected API provider (including explicit fallback) must have a key before enabling.

Choose at most one fallback provider. The default is `none`. Fallback can incur that provider's charges and only occurs after failure; valid empty results do not trigger another paid request. There are no automatic retries. The model cannot choose providers, fallback, endpoint URLs, keys or resource budgets.

Defaults: 5 results, 10-second **total** timeout, 256 KiB response cap, two simultaneous searches. Configurable bounds: 1–10 results, 0.5–30 seconds, 4 KiB–1 MiB response, concurrency 1–4. Excess concurrency is rejected, not queued. Disabling/reconfiguring aborts in-flight searches through the plugin manager lifecycle.

## Trust boundary

Results are `{title,url,snippet,provider}` plus `trust: untrusted_web_content`, a fixed instruction reminder, attempt metadata and status (`ok`, `no_results`, `unavailable`). Retrieved text remains lower-trust tool data, never system instructions. No page HTML or scripts are executed. The plugin does not follow result links or fetch page bodies. Only fixed HTTPS provider endpoints are requested; redirects and arbitrary endpoint configuration are prohibited. Local, IP-literal and active-content result URLs are filtered.

Logs use fixed provider/error codes only: no queries, credentials, response bodies or upstream exception text. Failure, authentication failure, rate limits, invalid markup, oversized responses, timeout and cancellation are not presented as successful empty search results.

## Verification and sources

Automated tests use independently authored synthetic fixtures: all eight provider parsers, explicit fallback, no-results handling, timeout/cancellation/concurrency, response-size and URL restrictions, encrypted key preserve/clear, plus real Runtime model-tool dispatch and plugin disable. These are not live-provider availability tests; no live credentials or paid calls are required.

Protocol sources checked 2026-10-27:

- [Tavily Search](https://docs.tavily.com/documentation/api-reference/endpoint/search)
- [You Search](https://you.com/docs/api-reference/search/v1-search)
- [DeepSeek Search](https://api-docs.deepseek.com/api/search/)
- [Bing API retirement](https://learn.microsoft.com/en-us/lifecycle/announcements/bing-search-api-retirement)
- [MaiBot community provider reference](https://github.com/YukiSakiko/google_search_plugin/tree/0dd3ad29e603bb6ac6d02c32cce55934ba07cb32)
