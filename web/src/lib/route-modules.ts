/**
 * Route-level code splitting for the console and SSO pages.
 *
 * Each page is reached through a dynamic import, so a first visit downloads the
 * shell plus the one page it lands on instead of every console and SSO screen.
 * The module system memoizes each import, so warming a route on hover and then
 * navigating to it fetches the chunk only once.
 */

/** routeModules maps every code-split page to the dynamic import that loads it. */
export const routeModules = {
  askUser: () => import('@/features/mcp/ask-user/page'),
  callLog: () => import('@/features/mcp/call-log/page'),
  extractKeyInfo: () => import('@/features/mcp/extract-key-info/page'),
  fileIO: () => import('@/features/mcp/file-io/page'),
  inspector: () => import('@/features/mcp/inspector/page'),
  memory: () => import('@/features/mcp/memory/page'),
  settings: () => import('@/features/mcp/settings/page'),
  userRequests: () => import('@/features/mcp/user-requests/page'),
  webFetch: () => import('@/features/mcp/web-fetch/page'),
  webSearch: () => import('@/features/mcp/web-search/page'),
  ssoGithubCallback: () => import('@/pages/sso-github-callback'),
  ssoLogin: () => import('@/pages/sso-login'),
  ssoProfile: () => import('@/pages/sso-profile'),
  ssoTokenDetails: () => import('@/pages/sso-token-details'),
} as const;

const consolePathModules: Readonly<Record<string, () => Promise<unknown>>> = {
  '/debug': routeModules.inspector,
  '/settings': routeModules.settings,
  '/tools/ask_user': routeModules.askUser,
  '/tools/call_log': routeModules.callLog,
  '/tools/extract_key_info': routeModules.extractKeyInfo,
  '/tools/file_io': routeModules.fileIO,
  '/tools/get_user_requests': routeModules.userRequests,
  '/tools/memory': routeModules.memory,
  '/tools/web_fetch': routeModules.webFetch,
  '/tools/web_search': routeModules.webSearch,
};

/**
 * preloadConsoleRoute starts downloading the page chunk for a console path the user
 * is about to open (pointer hover or keyboard focus on its link). It accepts the
 * router-relative path and ignores unknown paths and load failures, because the
 * navigation itself retries the import and reports any error through the router.
 */
export function preloadConsoleRoute(path: string): void {
  const load = consolePathModules[path];
  if (!load) {
    return;
  }
  load().catch(() => undefined);
}

const staleChunkReloadKey = 'route-chunk-reload-at';
const staleChunkReloadWindowMs = 10_000;
const dynamicImportFailure =
  /Failed to fetch dynamically imported module|error loading dynamically imported module|Importing a module script failed|Unable to preload CSS/i;

/**
 * isStaleChunkError reports whether a router error came from a page chunk that
 * could not be downloaded, which is what an open tab sees after a deploy removes
 * the hashed assets it was built against.
 */
export function isStaleChunkError(error: unknown): boolean {
  return error instanceof Error && dynamicImportFailure.test(error.message);
}

/**
 * reloadForStaleChunk reloads the document once so the browser fetches the current
 * index.html and its new chunk names. It returns false without reloading when a
 * reload already happened within the last few seconds (so a genuinely missing
 * asset cannot cause a reload loop) or when session storage is unavailable.
 */
export function reloadForStaleChunk(
  storage: Pick<Storage, 'getItem' | 'setItem'> = window.sessionStorage,
  now: number = Date.now(),
  reload: () => void = () => window.location.reload()
): boolean {
  try {
    const last = Number(storage.getItem(staleChunkReloadKey) ?? Number.NaN);
    if (Number.isFinite(last) && now - last >= 0 && now - last < staleChunkReloadWindowMs) {
      return false;
    }
    storage.setItem(staleChunkReloadKey, String(now));
  } catch {
    return false;
  }
  reload();
  return true;
}
