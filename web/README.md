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

The MCP ask_user HTTP handler looks for the compiled assets in `web/dist` by default. Override the location with `MCP_ASKUSER_DIST_DIR=/path/to/dist` when running the API if the assets live elsewhere. When no bundle is available the server falls back to the minimal legacy HTML view.
