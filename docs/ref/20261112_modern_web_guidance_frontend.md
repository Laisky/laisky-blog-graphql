# Modern Web Guidance Applied to the Web Console (2026-11-12)

Source: Chrome's [modern web guidance](https://developer.chrome.com/docs/modern-web-guidance),
read through the `modern-web-guidance` CLI (`npx -y modern-web-guidance@latest retrieve <id>`,
guide set `2026_11_09`). This note records which guides apply to `web/`, what was changed, and
what was deliberately deferred, so later work can cite it instead of re-auditing.

Browser policy: the guides treat Baseline *widely available* features as safe without
fallbacks. Anything newer needs the guide's fallback. No custom browser-support policy is
defined for this repository.

## Applied

| Guide | Where | Decision |
| --- | --- | --- |
| `performance` (code splitting) | `src/bootstrap.tsx`, `src/lib/route-modules.ts`, `vite.config.ts` | Every page except the overview is a React Router `lazy` route; React and the router build into `vendor-react`. Console pages live in one registry (`consolePages`), which both the router and link prefetching read, so a path cannot drift between them; a layout test checks that every console link has a registry entry. The startup bundle went from one 814 kB chunk (250 kB gzip) to 184 kB + 316 kB (61 + 101 kB gzip). The initial render waits for the router to load the landing page's chunk, so the static agent-readable HTML stays on screen instead of flashing empty. |
| `performance` (prefetch for futures) | `src/components/layout/app-layout.tsx` | Console links warm their page chunk on pointer hover or keyboard focus. |
| `performance` (caching) | `internal/web/spa.go`, `vite.config.ts` | Content-hashed build output is served `Cache-Control: public, max-age=31536000, immutable`; `index.html` stays `no-store` and points at the new names after a deploy. The directory alone does not qualify a file: the name must match `assets/<name>-<8-character hash><ext>` (`fingerprintedAssetName`), so a stable-named file under `assets/` keeps revalidating. Vite's output names are set explicitly to that shape, and the `fingerprinted-asset-contract` build plugin fails the build if `public/assets/` exists or an emitted file under `assets/` lacks the hash. |
| — (deploy safety for code splitting) | `src/pages/route-error.tsx`, `src/lib/route-modules.ts` | A tab opened before a deploy asks for chunk names that no longer exist. The browser reports that exactly like a dropped request, so the recovery separates only what it can tell apart: while `navigator.onLine` is false it never reloads (the reload would only show the browser's offline page) and leaves the guard unarmed; otherwise it reloads at most once per build in a tab. The guard (`route-chunk-recovery` in `sessionStorage`) records which builds this tab already reloaded for, identified by `import.meta.url` of the guard's own chunk: that chunk is content-hashed and embeds every page chunk's hashed name, so any frontend deploy changes it, while a reload that fetched the same `index.html` keeps it. Counting builds instead of time bounds the number of reloads: a request that takes longer to fail than any time window cannot restart the loop, a rolling deploy that alternates old and new `index.html` stops after both builds were tried, and the list keeps only the last 8 builds. When it cannot reload (offline, already reloaded for this build, or storage refused, including a throwing `window.sessionStorage` getter) it shows "This page could not be loaded" with a manual "Reload page" button instead of "Page not found". Offline failures are not reported to Sentry. |
| `deprioritize-background-fetches`, `efficient-background-processing` | `src/lib/visibility-poller.ts`, `src/lib/api-key-context.tsx`, MCP console pages | Every background poll, including the insufficient-quota re-check, runs on the shared `createVisibilityPoller`: no overlapping runs, paused while `document.visibilityState === 'hidden'`, refreshed on return, background requests sent with `priority: 'low'`, in-flight work aborted on dispose, and a failing poll backed off with `createFailureBackoff` (`src/lib/backoff.ts`: doubling per consecutive failure up to a cap, equal jitter so tabs that failed together do not retry together, reset by a success). Lists poll every 5 s and back off from 8 s to 60 s; the hold state polls every second and backs off from 2 s to 30 s. User-initiated loads keep the default priority. On the user-requests console the saved preferences (return mode, command template) load on their own lifecycle (`usePreferenceRestore`), not alongside the first list refresh: a failed list refresh cannot discard them, a preference load that failed for a reason that may pass (transport failure, 5xx, 408, 425, 429; `isRetryableRequestFailure` in `src/lib/http-error.ts`) retries with capped exponential backoff (2 s doubling to 30 s, paused while hidden), a final HTTP refusal (401, 403, 404, …) stops the timer and shows an alert with a Retry button (a "Retry now" link is offered while retrying), the template editor stays read-only until the saved template is known (including after a refusal), and a return mode the user picks while the read is in flight wins over the stale response. The quota re-check is upgrade-only: it applies a successful quota reading and otherwise keeps the last status. It backs off (20 s doubling to 5 min, jittered) only while `/query/` itself cannot answer (transport failure or non-2xx); an answered check, including the GraphQL error an exhausted key gets, keeps the 10 s cadence so a top-up is noticed within one interval. None of its failures is a verdict on the key: the resolver reports a failing balance endpoint as a GraphQL error over HTTP 200, and the upstream OneAPI `/api/token/balance` answers HTTP 401 both for an unknown key and for a valid key whose quota is exhausted, which is where an "insufficient" key is heading. An explicit validation still reports every failure as `error`. Every result is dropped once superseded: each explicit validation takes a new generation and `AbortController`, and a newer explicit validation, a key switch, clearing the key or disconnect advance the generation synchronously and abort the older request, so a late answer for key A can never set key B's status (or unlock the console after a disconnect). |
| `prevent-page-scroll-under-modal-dialog`, `css` | `src/index.css`, `app-layout.tsx` | `overflow-x: hidden` on `html`, `body` and the layout wrapper made them scroll containers that never scroll, which silently disabled the sticky header. All three use `overflow-x: clip`. The API-key banner and the header share one sticky container so they cannot overlap. |
| `responsive-design` | `src/index.css`, `app-layout.tsx` | Full-height layouts use `dvh`. |
| `dark-mode`, `dark-mode-toggle`, `browser-ui-theming` | `index.html`, `src/components/theme/*` | Added `<meta name="color-scheme" content="light dark">` and per-scheme `theme-color`; a forced theme rewrites both. "System" removes the stored override instead of storing `"system"`. Storage goes through `src/lib/safe-storage.ts`, and the `storage` event syncs other tabs through `subscribeStorageKey`, which treats the event only as a signal that something changed and re-reads the key. The browser captures `newValue` when the write happens and dispatches the event later as a queued task (WHATWG HTML, Web storage: "queue a global task"), so two tabs switching the theme moments apart can deliver an event whose `newValue` storage no longer holds. Headless Chrome showed this: the second of three back-to-back writes arrived as `newValue: "light"` while `getItem` already returned `"dark"`. Re-reading also keeps a `key === null` event from a same-tab frame's `sessionStorage.clear()` from resetting the `localStorage` override to System. The System option shows the resolved scheme. `accent-color` follows `--primary`. |
| `motion`, `spinner` | `src/index.css`, `src/components/ui/*` | Reduced motion is handled per animation, never with a global duration override: menus keep their fade but drop zoom/slide (`motion-safe:`); the dialog's entry/exit animation is motion-only because its slide offsets are tied to the centering transform; spinners slow to 6 s per turn instead of stopping; the pulse stops. |
| `typography` | `src/index.css` | `text-wrap: balance` for `h1`–`h3`, `pretty` for paragraphs. |
| `accessibility`, `html`, `navbar` | `app-layout.tsx`, `not-found.tsx`, `route-error.tsx` | Skip link to a focusable `<main id="main-content">`; `nav` labelled "Primary"; current page marked by underline, not color alone; the icon-only settings link has a name and `aria-current`. Console links are a navigation disclosure (`aria-expanded` + `aria-controls`, no `aria-haspopup`), Escape returns focus to the trigger, and tabbing away closes it. A disclosure whose panel is unmounted while collapsed sets `aria-controls` only while expanded (an ID reference must name an element in the document); `findDanglingIdRefs` (`src/test-utils/aria-idrefs.ts`) asserts this in component tests. Unknown console paths render inside the layout; standalone fallbacks have their own `<main>`. |
| `accessibility`, `forms`, `autofill-sign-in-form` | SSO pages, `api-key-input.tsx`, `status-banner.tsx` | `StatusBanner` is `role="alert"` for errors and `role="status"` otherwise. Every SSO page has one `h1`, and section titles are `h2`. Profile forms are named by their headings, and their fields have labels. The login form is `method="post"`; the username field has `required`, `spellcheck=false` and no autocapitalize/autocorrect. Segmented toggles use `aria-pressed`. The TOTP field has a described hint, and the Turnstile area is a labelled group. A cancelled passkey prompt (`NotAllowedError`) shows a neutral notice, not an error. API-key history entries are real buttons, and the key field has a label. |
| `ime-safe-enter-submit`, `accessibility`, `forms`, `responsive-table`, `performance` (INP) | MCP console pages | Enter handlers ignore IME composition (`isComposing` or `keyCode === 229`, `shared/ime.ts`) and test it before `preventDefault()`, so the confirming keydown keeps its browser default; tests assert `defaultPrevented` stays false. One poller (`src/lib/visibility-poller.ts`) chains ticks after each response, pauses while hidden and marks background ticks `priority: 'low'`; the hold countdown no longer uses an overlapping `setInterval`. History search is debounced (250 ms), and stale responses are aborted. Image pre-shrink decodes with `createImageBitmap` where available and yields between files. Icon-only buttons and placeholder-only inputs have names. Settings tool toggles are `role="switch"`. Errors are `role="alert"` and results are `role="status"`. Scrollable tables are focusable named regions with captions. |

| — (blocked site data) | `src/lib/safe-storage.ts`, `api-key-context.tsx`, `theme-provider.tsx`, `route-modules.ts` | `readStorage`/`writeStorage`/`removeStorage` resolve the storage area inside their own `try`/`catch`, because reading `window.localStorage` itself throws when site data is blocked. The API-key provider, the user-requests console preferences, the memory input draft and the inspector's header defaults no longer crash on blocked storage. A stored cleanup-option index that is not a valid option (for example `99`) selects the first option instead of crashing the user-requests page. It also trusts only settled stored statuses: a stored `validating` (tab closed mid-check) or an unknown value loads as `none` and is revalidated on mount, instead of locking the consoles until the key is re-entered, and a non-numeric stored quota loads as unknown. |

## Deferred

- **Native popover for the Console menu** (`navbar`). Popover is Baseline *newly* available
  (2025-01-27) and CSS anchor positioning is not yet in Chrome, Edge or Firefox per the guide
  data. Without a polyfill, older Safari would render the list inline. Revisit when Popover
  becomes widely available (about mid-2027), then drop the hand-written Escape/outside-click
  handlers.
- **Native `<dialog>` instead of Radix Dialog** (`dialog`). Radix already provides the focus
  trap, Escape, focus return and a visible close button. Replacing it touches 5 call sites for
  little user-visible gain.
- **Passkey conditional UI** (`passkey-authentication`: `autocomplete="username webauthn"` +
  `mediation: "conditional"`). This needs a server challenge on every login-page view, which
  is a backend design change.
- **`Cache-Control: no-store` on `index.html`** is kept, but it is **not** what protects the SSO
  pages after a sign-out, and in Chrome it no longer blocks the back/forward cache by itself.
  Chrome (experiment since 116, rolled out to all users in March–April 2025) restores a
  `no-store` page unless cookies or another authentication method changed, the page made a
  `fetch`/XHR whose response was `no-store`, or it used WebSocket/WebTransport/WebRTC; such a
  page is kept for 3 minutes instead of 10 (developer.chrome.com/docs/web-platform/bfcache-ccns,
  updated 2025-09-09; enterprise policy `AllowBackForwardCacheForCacheControlNoStorePageEnabled`).
  Signing out here only clears the `localStorage` token, which none of those rules see, so a
  restored profile could have shown the account it had loaded. The SSO pages therefore follow the
  token themselves: `subscribeSsoToken` (`src/lib/sso-session.ts`) re-reads it on `pageshow` with
  `event.persisted` and on every `storage` event for the key (another tab signing in or out). The
  profile remounts its account view whenever the token changes or the page is restored (so the
  loaded account, a started TOTP setup and typed passwords are dropped and the account is
  reloaded), and a sign-out elsewhere replaces the entry with the sign-in page; the token page
  hides the bearer token at once. Other browsers may still refuse `no-store` pages, which the
  header keeps as defense in depth. Switching the entry document to `no-cache` would let console
  pages use the bfcache in every browser; that is a separate decision.
- **`<meta name="text-scale">`** (`respect-os-text-scale`). Since the Tailwind 4 upgrade the
  default breakpoints are rem-based (`sm` is `40rem`), so they already follow the user's default
  font size. The header still has no collapsed navigation, so large OS text scales would clip it.
  Opt in after the header can collapse.
- **View transitions between routes** (`same-document-transitions`). Optional polish, not
  needed for correctness.
- **Splitting the largest console components** (`user-requests/page.tsx` ≈1.5k lines, `sso-login.tsx`, `sso-profile.tsx`,
  `saved-commands.tsx`). Preference restore was extracted first (`usePreferenceRestore`) because sharing the list poll's
  lifecycle lost the preferences. The remaining natural seams are `useRequestListPolling`, `useHoldStatePolling` and
  `useHistorySearch`, each with its own lifecycle and tests. This is a separate refactor; it changes no behavior and is easier to review alone.
- **SSO token storage** (`src/lib/sso-session.ts`) still calls `localStorage` directly and throws when site data is
  blocked. It holds a credential rather than a preference, so whether a refused write should fail the sign-in visibly
  or fall back to a page-lifetime session is a product decision for the SSO flow, not a mechanical migration. The other
  remaining direct calls (FileIO drafts, task-ID history, inspector defaults, FileIO introduction) already catch their
  own failures.
- **Task-ID combobox, saved-command cards, `CardTitle` heading level, `<search>` wrappers.**
  These are structural rewrites, so they are kept out of this change. `project-picker.tsx`
  is the reference combobox pattern. The saved-command card's Enter/Space handler also fires
  when Enter is pressed on its nested buttons.
