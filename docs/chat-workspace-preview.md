# Conversation workspace and identity integration

## Scope and reference

UI source: `app/pages/chat.vue`, `app/pages/scenes.vue`, `app/components/ChatAvatar.vue`,
`app/utils/chatIdentity.ts`, and `cw*` locale entries. Backend identity work is
integrated as a bounded delta on logging/image baseline
`26469121dbc3924e00e65ca0c6496e0dab133d9a`. The original preview was approved
before integration; unrelated provider, authentication, web-search, image, and
flow instrumentation changes were preserved.

Reference inspected directly from the primary MaiBot repository at commit
`fe88ec6b6053e40eaf430cb5ba7ed7e805add2cb`:
`dashboard/src/routes/chat/ChatWorkspaceSidebar.tsx` and `ChatUserIdentity.tsx`.
The reference informed the unified conversation sidebar, name/avatar presentation,
message previews, selected-row state, and separate observed conversations.
No MaiBot implementation was copied into this codebase.

## Behavior

- Fixed simulation entry above group and private conversations; selection directly
  opens its transcript. No secondary group picker.
- Sender names and avatars use backend-provided metadata, never decoded opaque IDs.
  Same-name senders keep distinct canonical identity and an explicit details view.
- Scenes show the same readable names and avatars. Missing or encoded/numeric
  fallback names are replaced with localized labels.
- Real conversation history stays read-only. Only the existing simulation endpoint
  has a composer; switching to a group cannot route its draft into a live send.
- Simulation completions received while another conversation is selected get an
  unread badge. Group unread counts are deliberately not invented: there is no
  live unread/conversation-summary API yet.
- Existing cancellation, uncertain-send reconciliation, discard confirmation,
  loading, history coverage, errors, refresh, and empty states remain in place.
- Mobile uses list/detail navigation with a back button and independent scrolling.

## Evidence

The integrated source passes contract regeneration/check, all 290 tests,
runtime and Nuxt typechecks, the repository formatting check, and the production
build. Backend regressions cover strict scene persistence, scene/account isolation,
late profile responses not replacing newer group cards, non-OneBot numeric IDs not
becoming QQ avatars, and the existing image/vision/flow/manual-rule behavior.

Browser QA used actual built Nuxt pages with deterministic synthetic API fixtures;
no real messages, API keys, adapters, or models were used. Verified group/private/
simulation selection, human names and avatars, distinct same-name details,
read-only history (no composer), simulation-only send, navigation discard dialog
and Cancel preserving session, scene names, mobile list/detail and history scrolling.
Mobile captures are 390 x 844 same-origin iframe responsive renderings, not a physical
mobile device test. The iframe contains the actual production `/chat` page.

The reusable Playwright test script was authored but could not run end-to-end in
this environment: Chromium local sockets were sandbox-blocked. Browser QA instead
used the existing supported CUA browser. Organization policy disables DevTools;
no policy, browser flag, or configuration was changed. Network error/retry, delayed
send unread, and repeated-click automation are not claimed as browser-tested.

Scripts `chat-ui-preview.py` and `chat-preview-server.py` are test-only tools.
The proxy handles every application API request using fixtures; only static icon
requests and UI assets pass through to the empty production test server.
The conversation layout was approved before the integration and publication step.
