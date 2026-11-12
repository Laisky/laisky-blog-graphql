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
| `performance` (code splitting) | `src/bootstrap.tsx`, `src/lib/route-modules.ts`, `vite.config.ts` | Every page except the overview is a React Router `lazy` route; React and the router build into `vendor-react`. The startup bundle went from one 814 kB chunk (250 kB gzip) to 180 kB + 316 kB (59 + 101 kB gzip). The initial render waits for the router to load the landing page's chunk, so the static agent-readable HTML stays on screen instead of flashing empty. |
| `performance` (prefetch for futures) | `src/components/layout/app-layout.tsx` | Console links warm their page chunk on pointer hover or keyboard focus. |
| `performance` (caching) | `internal/web/spa.go` | Files under `dist/assets/` are content-hashed, so they are served `Cache-Control: public, max-age=31536000, immutable`. `index.html` stays `no-store` and points at the new names after a deploy. |
| — (deploy safety for code splitting) | `src/pages/route-error.tsx` | A tab opened before a deploy asks for chunk names that no longer exist. A dynamic-import failure reloads the page once (guarded for 10 s in `sessionStorage`), then falls back to the normal error page. |
| `deprioritize-background-fetches`, `efficient-background-processing` | `src/lib/api-key-context.tsx`, MCP console pages | Background polls pause while `document.visibilityState === 'hidden'`, refresh on return, and send `priority: 'low'`. User-initiated loads keep the default priority. |
| `prevent-page-scroll-under-modal-dialog`, `css` | `src/index.css`, `app-layout.tsx` | `overflow-x: hidden` on `html`, `body` and the layout wrapper made them scroll containers that never scroll, which silently disabled the sticky header. All three use `overflow-x: clip`. The API-key banner and the header share one sticky container so they cannot overlap. |
| `responsive-design` | `src/index.css`, `app-layout.tsx` | Full-height layouts use `dvh`. |
| `dark-mode`, `dark-mode-toggle`, `browser-ui-theming` | `index.html`, `src/components/theme/*` | Added `<meta name="color-scheme" content="light dark">` and per-scheme `theme-color`; a forced theme rewrites both. "System" removes the stored override instead of storing `"system"`. Storage access is wrapped in `try`/`catch`, and the `storage` event syncs other tabs. The System option shows the resolved scheme. `accent-color` follows `--primary`. |
| `motion`, `spinner` | `src/index.css`, `src/components/ui/*` | Reduced motion is handled per animation, never with a global duration override: menus keep their fade but drop zoom/slide (`motion-safe:`); the dialog's entry/exit animation is motion-only because its slide offsets are tied to the centering transform; spinners slow to 6 s per turn instead of stopping; the pulse stops. |
| `typography` | `src/index.css` | `text-wrap: balance` for `h1`–`h3`, `pretty` for paragraphs. |
| `accessibility`, `html`, `navbar` | `app-layout.tsx`, `not-found.tsx`, `route-error.tsx` | Skip link to a focusable `<main id="main-content">`; `nav` labelled "Primary"; current page marked by underline, not color alone; the icon-only settings link has a name and `aria-current`. Console links are a navigation disclosure (`aria-expanded` + `aria-controls`, no `aria-haspopup`), Escape returns focus to the trigger, and tabbing away closes it. Unknown console paths render inside the layout; standalone fallbacks have their own `<main>`. |
| `accessibility`, `forms`, `autofill-sign-in-form` | SSO pages, `api-key-input.tsx`, `status-banner.tsx` | `StatusBanner` is `role="alert"` for errors and `role="status"` otherwise. Every SSO page has one `h1`, and section titles are `h2`. Profile forms are named by their headings, and their fields have labels. The login form is `method="post"`; the username field has `required`, `spellcheck=false` and no autocapitalize/autocorrect. Segmented toggles use `aria-pressed`. The TOTP field has a described hint, and the Turnstile area is a labelled group. A cancelled passkey prompt (`NotAllowedError`) shows a neutral notice, not an error. API-key history entries are real buttons, and the key field has a label. |
| `ime-safe-enter-submit`, `accessibility`, `forms`, `responsive-table`, `performance` (INP) | MCP console pages | Enter handlers ignore IME composition (`isComposing` or `keyCode === 229`, `shared/ime.ts`). One poller (`shared/visibility-poller.ts`) chains ticks after each response, pauses while hidden and marks background ticks `priority: 'low'`; the hold countdown no longer uses an overlapping `setInterval`. History search is debounced (250 ms), and stale responses are aborted. Image pre-shrink decodes with `createImageBitmap` where available and yields between files. Icon-only buttons and placeholder-only inputs have names. Settings tool toggles are `role="switch"`. Errors are `role="alert"` and results are `role="status"`. Scrollable tables are focusable named regions with captions. |

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
- **`Cache-Control: no-store` on `index.html`** blocks the back/forward cache (`performance`).
  It is kept on purpose: the same document serves the SSO pages, and a restored page could show
  account details after sign-out.
- **`<meta name="text-scale">`** (`respect-os-text-scale`). The header uses px-based Tailwind
  breakpoints and has no collapsed navigation, so large OS text scales would clip it. Opt in
  after switching the breakpoints to `em`.
- **View transitions between routes** (`same-document-transitions`). Optional polish, not
  needed for correctness.
- **Task-ID combobox, saved-command cards, `CardTitle` heading level, `<search>` wrappers.**
  These are structural rewrites, so they are kept out of this change. `project-picker.tsx`
  is the reference combobox pattern. The saved-command card's Enter/Space handler also fires
  when Enter is pressed on its nested buttons.
