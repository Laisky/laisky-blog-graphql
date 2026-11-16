/**
 * Route-level code splitting for the console and SSO pages.
 *
 * Each page is reached through a dynamic import, so a first visit downloads the
 * shell plus the one page it lands on instead of every console and SSO screen.
 * The module system memoizes each import, so warming a route on hover and then
 * navigating to it fetches the chunk only once.
 */
import type { ComponentType } from 'react';

import { resolveStorage, type StorageLike } from './safe-storage';

/** ConsolePage describes one code-split console page: where it lives and how to load it. */
export interface ConsolePage {
  /** path is the router-relative URL that navigation links point at. */
  path: string;
  /** pattern is the route pattern when it differs from path, such as a splat for nested views. */
  pattern?: string;
  /** load downloads the page chunk and resolves to the page component. */
  load: () => Promise<ComponentType>;
}

/**
 * consolePages is the single registry of code-split console pages. The MCP router
 * builds its routes from it and preloadConsoleRoute warms chunks from it, so adding
 * or renaming a page here updates both.
 */
export const consolePages: readonly ConsolePage[] = [
  { path: '/tools/ask_user', load: () => import('@/features/mcp/ask-user/page').then((m) => m.AskUserPage) },
  { path: '/tools/get_user_requests', load: () => import('@/features/mcp/user-requests/page').then((m) => m.UserRequestsPage) },
  { path: '/tools/web_search', load: () => import('@/features/mcp/web-search/page').then((m) => m.WebSearchPage) },
  { path: '/tools/web_fetch', load: () => import('@/features/mcp/web-fetch/page').then((m) => m.WebFetchPage) },
  { path: '/tools/extract_key_info', load: () => import('@/features/mcp/extract-key-info/page').then((m) => m.ExtractKeyInfoPage) },
  { path: '/tools/file_io', load: () => import('@/features/mcp/file-io/page').then((m) => m.FileIOPage) },
  { path: '/tools/memory', load: () => import('@/features/mcp/memory/page').then((m) => m.MemoryPage) },
  { path: '/tools/call_log', load: () => import('@/features/mcp/call-log/page').then((m) => m.CallLogPage) },
  { path: '/settings', load: () => import('@/features/mcp/settings/page').then((m) => m.SettingsPage) },
  { path: '/debug', pattern: '/debug/*', load: () => import('@/features/mcp/inspector/page').then((m) => m.InspectorPage) },
];

/** ssoPageModules maps every code-split SSO page to the dynamic import that loads it. */
export const ssoPageModules = {
  githubCallback: () => import('@/pages/sso-github-callback'),
  login: () => import('@/pages/sso-login'),
  profile: () => import('@/pages/sso-profile'),
  tokenDetails: () => import('@/pages/sso-token-details'),
} as const;

const consolePageLoaders: ReadonlyMap<string, ConsolePage['load']> = new Map(consolePages.map((page) => [page.path, page.load]));

/**
 * preloadConsoleRoute starts downloading the page chunk for a console path the user
 * is about to open (pointer hover or keyboard focus on its link). It accepts the
 * router-relative path and ignores unknown paths and load failures, because the
 * navigation itself retries the import and reports any error through the router.
 */
export function preloadConsoleRoute(path: string): void {
  consolePageLoaders
    .get(path)?.()
    .catch(() => undefined);
}

const staleChunkRecoveryKey = 'route-chunk-recovery';
// rememberedBuildLimit bounds the guard's size. A reload loop would need the server to
// rotate through more distinct builds than this, which only that many deploys can produce.
const rememberedBuildLimit = 8;
const dynamicImportFailure =
  /Failed to fetch dynamically imported module|error loading dynamically imported module|Importing a module script failed|Unable to preload CSS/i;

/**
 * currentBuild identifies the build this document runs. In a production bundle it is the
 * URL of the content-hashed chunk holding this module, and that chunk embeds the hashed
 * names of every page chunk it imports, so any frontend deploy changes it while a reload
 * that fetched the same index.html keeps it.
 */
const currentBuild = import.meta.url;

/**
 * isStaleChunkError reports whether a router error came from a page chunk that
 * could not be downloaded. The browser message is the same whether a deploy removed
 * the hashed asset or the network dropped the request, so callers must not treat it
 * as proof of a deploy; reloadForStaleChunk separates the cases it can tell apart.
 */
export function isStaleChunkError(error: unknown): boolean {
  return error instanceof Error && dynamicImportFailure.test(error.message);
}

/** reloadPage reloads the document so the browser fetches the current index.html. */
export function reloadPage(): void {
  window.location.reload();
}

/**
 * StaleChunkRecovery is the outcome of a recovery attempt:
 * - 'reloading': a reload was started.
 * - 'offline': the browser reports no network, so a reload could only replace the page
 *   with the browser's own offline error; no attempt is recorded, so a later failure may retry.
 * - 'unavailable': this tab already reloaded automatically while running this build, so
 *   another reload would fetch the same build again; or session storage cannot hold the guard.
 */
export type StaleChunkRecovery = 'reloading' | 'offline' | 'unavailable';

/** StaleChunkReloadOptions injects the environment reloadForStaleChunk reads, for tests. */
export interface StaleChunkReloadOptions {
  /** storage holds the loop guard; defaults to session storage, resolved defensively. */
  storage?: Pick<StorageLike, 'getItem' | 'setItem'>;
  /** build identifies the running build; defaults to this module's chunk URL. */
  build?: string;
  /** reload starts the reload; defaults to reloadPage. */
  reload?: () => void;
  /** online reports connectivity; defaults to navigator.onLine. */
  online?: boolean;
}

/**
 * reloadForStaleChunk reloads the document so the browser fetches the current index.html
 * and its new chunk names, which is what an open tab needs after a deploy. It reloads at
 * most once per build in a tab: if the reloaded document runs the same build, reloading
 * again cannot help, however long each failing request took. It never reloads while the
 * browser reports being offline, and it never throws: blocked site data only disables
 * the automatic reload.
 */
export function reloadForStaleChunk(options: StaleChunkReloadOptions = {}): StaleChunkRecovery {
  const online = options.online ?? (typeof navigator === 'undefined' || navigator.onLine !== false);
  if (!online) {
    return 'offline';
  }
  if (!recordReloadAttempt(options.storage, options.build ?? currentBuild)) {
    return 'unavailable';
  }
  (options.reload ?? reloadPage)();
  return 'reloading';
}

/**
 * recordReloadAttempt records an automatic reload for the build and reports whether one
 * may start. It refuses when this tab already reloaded for the build, and whenever the
 * guard cannot be both read and stored, because an unrecorded reload could repeat forever
 * on a genuinely missing asset. The guard lives in session storage, which survives the
 * reload and belongs to this tab only.
 */
function recordReloadAttempt(storage: StaleChunkReloadOptions['storage'], build: string): boolean {
  try {
    const guard = storage ?? resolveStorage('session');
    if (!guard) {
      return false;
    }
    const attempted = parseAttemptedBuilds(guard.getItem(staleChunkRecoveryKey));
    if (attempted.includes(build)) {
      return false;
    }
    guard.setItem(staleChunkRecoveryKey, JSON.stringify([...attempted, build].slice(-rememberedBuildLimit)));
    return true;
  } catch {
    return false;
  }
}

/**
 * parseAttemptedBuilds decodes the stored guard. A missing or malformed value counts as no
 * prior attempt; the next recorded attempt overwrites it with a well-formed list.
 */
function parseAttemptedBuilds(raw: string | null): string[] {
  if (!raw) {
    return [];
  }
  try {
    const parsed: unknown = JSON.parse(raw);
    return Array.isArray(parsed) ? parsed.filter((entry): entry is string => typeof entry === 'string') : [];
  } catch {
    return [];
  }
}
