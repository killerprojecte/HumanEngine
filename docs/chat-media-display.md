# Authenticated chat image display

## Primary-source design review

The archived MaiBot-Dashboard README directs development to MaiBot's main repository. The current implementation was reviewed directly:

- [MessageRenderer](https://github.com/Mai-with-u/MaiBot/blob/main/dashboard/src/routes/chat/MessageRenderer.tsx): render image/emoji segments as images rather than serialized metadata.
- [Image gallery](https://github.com/Mai-with-u/MaiBot/blob/main/dashboard/src/components/image-gallery.tsx): authenticated bytes become local object URLs; release them when no longer needed, with loading/error states and accessible preview controls.
- [Zoomable image](https://github.com/Mai-with-u/MaiBot/blob/main/dashboard/src/components/zoomable-image.tsx): separated preview navigation and zoom behavior.
- [Chat serializer](https://github.com/Mai-with-u/MaiBot/blob/main/src/webui/routers/chat/serializers.py): use actual component/cache bytes and MIME detection, not a vision description as the picture.
- [Emoji resource API](https://github.com/Mai-with-u/MaiBot/blob/main/src/webui/routers/emoji/routes.py): authenticated resource lookup, unavailable-file errors, and distinct thumbnail/original paths.

HumanEngine implements this independently in Vue. It does not adopt token query strings, arbitrary remote image URLs, or HTML injection fallbacks from other implementations.

## Boundaries

The UI consumes server-projected scene/message/part media resources. It fetches bytes with the existing session header and same-origin cookie, then uses a local object URL. No signed platform URL, local filesystem path, or base64 asset locator is needed in the browser. Only PNG, JPEG, GIF and WebP bytes are displayable. SVG and HTML are not accepted. The original image bytes are used, so a supported animated file is not silently replaced by a static frame.

The thumbnail is a size-constrained rendering of the source, not a claim that server-side thumbnail generation exists. Preview opens via a keyboard-accessible button, supports zoom controls and keyboard shortcuts, and returns with Escape, its close/back action, or browser Back. Loading, expired/unavailable media and retry are explicit. Switching messages, logout and component disposal cancel outstanding requests and revoke local URLs.

Display projection and model context are separate. Platform-generated unsupported metadata is not human message text. Actual text parts are never stripped based on bracket syntax or regular expressions; a literal user-written `[image/unknown]` remains literal text. A missing legacy image locator cannot be reconstructed from a caption: it remains unavailable rather than fabricating a thumbnail or treating metadata as recognition.

## Public retrieval and model projection

`web-read` is a separate, disabled-by-default built-in plugin. Enabling it gives the planner a read-only public-page tool without requiring a search-provider API key. Its defaults are 10 seconds, 1 MiB of response bytes, 12,000 extracted characters and two concurrent requests. It reads inert HTML, plain text or JSON: no scripts execute and no browser cookies or credentials are sent. A failed/private/challenged/unsupported response remains an explicit failure, not fabricated page contents.

The shared public transport accepts public HTTP(S) destinations rather than a QQ-only hostname list. This does not authorize private networks: DNS/address checks, redirect validation, IP pinning, protocol/credential rejection and response limits remain enforced. Tests use injected public DNS fixtures and local synthetic adapters, never a user's signed download URL.

Model-boundary projection replaces implementation identifiers and asset locators with scoped opaque references; runtime source records retain their canonical identifiers. Tool arguments are resolved back only within their valid scope. Message timestamps and current time/timezone remain available. Presentation projection, model projection and actual stored user text are not interchangeable.

## Validation (2026-10-28)

- Full contract generation/check, 399 tests, runtime TypeScript and Nuxt typecheck passed; final production build passed.
- Independent focused review reran media authorization, public transport, compact projection, feedback grounding and image lifecycle boundaries.
- The actual production UI rendered synthetic inbound PNG and two-frame GIF bytes, opened a larger preview, zoomed with keyboard controls, closed with Escape/browser Back, restored with browser Forward, and recovered a deliberately corrupt source via Reload after its fixture bytes were repaired.
- Managed outgoing image bytes were also rendered through the same message-bound endpoint. Proposed, disabled, missing, deleted, recalled and cross-scene managed assets remain inaccessible, including on cache hits.
- A synthetic fixture preserves literal user text resembling old placeholders in the API test; system-generated unsupported metadata is omitted independently. No real chat, signed user URL, model credential or live adapter was used for validation.
