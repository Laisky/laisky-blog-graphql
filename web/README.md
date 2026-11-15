# laisky web workspace

This Vite + React project hosts the browser interfaces for the GraphQL service. Multiple sub-modules share a single toolchain (pnpm, React Router, Tailwind, shadcn/ui) so that local development and production builds stay aligned.

## Quick start

- `pnpm install` – install dependencies.
- `pnpm dev` – start the Vite dev server on <http://localhost:5173>.
- `pnpm build` – emit a production bundle in `dist/` consumed by the Go HTTP handlers.
- `pnpm preview` – serve the production build locally.

## Routing layout

- `/` – unified workspace overview.
- `/mcp/tools/ask_user` – React rewrite of the MCP ask_user console, matching the legacy path.

Add new module pages under `src/pages` (or feature-specific directories) and register their routes in `src/bootstrap.tsx`.

Every page except the overview is code-split, never statically imported (a static import pulls the page back into the startup bundle). Add a console page to the `consolePages` registry in `src/lib/route-modules.ts`: the MCP router builds its routes from it, and its header links warm the chunk on hover or focus. SSO pages, which take props, are listed in `ssoPageModules` and wired with `lazyPage(...)` in `src/bootstrap.tsx`. The React runtime and router build into a separate `vendor-react` chunk.

## Browser conventions

The frontend follows Chrome's [modern web guidance](https://developer.chrome.com/docs/modern-web-guidance); `docs/ref/20261112_modern_web_guidance_frontend.md` records which guides apply and the decisions taken. In short:

- Never put `overflow-x: hidden` on `html`, `body` or the layout wrapper; use `overflow-x: clip`, which does not break the sticky header.
- Use `dvh` for full-height layouts.
- Respect reduced motion per animation (`motion-safe:` for spatial movement), not with a global duration override. Spinners slow down instead of stopping.
- Background polling uses `createVisibilityPoller` (`src/lib/visibility-poller.ts`): it never overlaps runs, pauses while the tab is hidden and sends its requests with `priority: 'low'`.
- Read and write Web Storage through `src/lib/safe-storage.ts`; blocked site data must degrade to "not remembered", never break the page.
- In Enter handlers, test `isImeComposingKeyDown` before `preventDefault()`, so the key that confirms an IME conversion keeps its default.
- Build output names are a contract with `internal/web/spa.go`: only `assets/<name>-<8-character hash><ext>` is cached as immutable. Do not add a `public/assets/` directory; the build fails if one exists.
- The theme override lives in `localStorage` (`ui-theme-preference`, absent for "system"). The inline script in `index.html` and `applyTheme` in `src/components/theme/theme-provider.tsx` must stay in sync, including the `color-scheme` and `theme-color` meta tags.

## Go backend integration

Build artifacts land in `web/dist` and are served by the Go API via Go's static file server. The handler automatically looks in:

- `/app/web/dist` (used by the Docker image)
- `<repo>/web/dist` while running locally

Override the search path with `WEB_FRONTEND_DIST_DIR=/custom/dist`. The older `ask_user` HTML shim continues to work; when the bundle is missing the server falls back to the legacy template for that route only.

## Container build

The root `Dockerfile` now compiles the front-end before creating the runtime image. A multi-stage build installs pnpm, runs `pnpm build`, and copies the resulting `dist/` into `/app/web/dist` alongside the Go binary. Rebuild the container whenever front-end code or dependencies change:

```sh
docker build -t laisky-blog-graphql .
```
