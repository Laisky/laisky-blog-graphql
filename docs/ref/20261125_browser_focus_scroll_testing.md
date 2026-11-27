# Browser focus, scroll anchoring and real-browser tests (2026-11)

Research notes for the PR #105 review: the File Preview layout and its focus rules.
They were verified on 2026-11-25 against specs, browser source, MDN and the npm
registry. Cite this note instead of researching the same questions again.

## Focus fixup: disabling or removing the focused element

- HTML's focus fixup rule (`#focus-fixup-rule`, in "update the rendering") says that
  when the focused area stops being focusable, for example because it was disabled,
  focus moves to the viewport. This happens asynchronously and usually fires `blur`.
  Removing the element from the DOM is a separate, synchronous fixup with no `blur`.
  In both cases `document.activeElement` ends up as `<body>`.
  https://html.spec.whatwg.org/multipage/webappapis.html#focus-fixup-rule
- Chrome blurs a disabled focused element one task later, via a zero-delay timer in
  `Document::ClearFocusedElementIfNeeded`. Firefox 110+ and WebKit (2023, bug 237273)
  also implement the rule.
- Measured in headless Chrome 157: a focused Save button that becomes `disabled`
  while its request runs leaves focus on `<body>`. Inside a Radix dialog, removing
  the focused element makes `FocusScope` move focus to the dialog container. Its
  MutationObserver sees a removed node while `<body>` is focused.
- Consequence: unmounting a control inside a `focusout` handler races the browser's
  own focus move, so the focus trap can take focus. Wait one task, then check
  `document.activeElement` (see `web/src/lib/use-focus-within.ts`).
- During `blur`/`focusout`, `document.activeElement` is `<body>` for a normal focus
  move. It is still the element when only the window lost focus.

## aria-disabled instead of disabled

- APG: native `disabled` removes a control from the tab sequence. Use
  `aria-disabled="true"` when the control must stay focusable or discoverable.
  https://www.w3.org/WAI/ARIA/apg/practices/keyboard-interface/
- MDN: `aria-disabled` only changes semantics. Activation must be blocked in script,
  because `pointer-events: none` does not stop the keyboard. The control is also not
  styled by `:disabled`.
  https://developer.mozilla.org/en-US/docs/Web/Accessibility/ARIA/Reference/Attributes/aria-disabled
- `<select>` has no `readonly`. With `aria-disabled`, guard `change`; a controlled
  React select then snaps back. `<textarea readonly>` stays focusable and selectable.

## CSS scroll anchoring

- Support: Chrome 56+, Firefox 66+, Safari 27. Safari 26.x has no
  `overflow-anchor` (BCD 8.1.10, WebKit WWDC26 post).
- No anchor is selected while the scroller is at offset 0. Content inserted above at
  `scrollTop` 0 therefore pushes the rest down. This is in spec §2.1/§2.2.2, and
  Blink's `ScrollAnchor::NotifyBeforeLayout` clears the anchor at offset 0.
  https://drafts.csswg.org/css-scroll-anchoring/
- Feature-detect with `CSS.supports('overflow-anchor', 'auto')`.

## Caret reveal

- Blink's `FrameSelection::RevealSelection` → `ScrollRectToVisible` also scrolls
  ancestor scrollers. A `field-sizing: content` textarea that does not scroll still
  keeps the caret visible inside a scrolling ancestor.

## Vitest Browser Mode with Playwright

- The packages are `@vitest/browser-playwright` and `playwright`. The provider's peer
  dependency is pinned to the exact `vitest` version: 4.1.11 needs vitest 4.1.11.
  Upgrade both together.
- Config: `test.browser = { enabled, headless, provider: playwright({ launchOptions:
  { channel: 'chrome' } }), instances: [{ browser: 'chromium' }] }`. The `chrome`
  channel uses an installed Google Chrome. https://v4.vitest.dev/config/browser/playwright
- The `playwright` npm package has no install script and downloads no browser.
  GitHub-hosted Ubuntu 24.04/26.04 images include Google Chrome.
- `@testing-library/react` works in Browser Mode. `vitest-browser-react` is optional.
  Real input comes from `userEvent` in `vitest/browser` (`click`, `keyboard`, `wheel`),
  and `page.viewport(w, h)` resizes the test iframe.
- A CSS px viewport emulates zoom for layout: 640×360 is a 1280×720 window at 200%,
  and 320×256 is 1280×1024 at 400% (WCAG 1.4.10 reflow).
- Wait for the dialog's open animation (`document.getAnimations()`) before comparing
  positions. Otherwise its transform shifts every rect.
