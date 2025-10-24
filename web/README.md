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

Add new module pages under `src/pages` (or feature-specific directories) and register their routes in `src/main.tsx`.

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
