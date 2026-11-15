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

const staleChunkReloadKey = 'route-chunk-reload-at';
const staleChunkReloadWindowMs = 10_000;
const dynamicImportFailure =
  /Failed to fetch dynamically imported module|error loading dynamically imported module|Importing a module script failed|Unable to preload CSS/i;

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
 *   with the browser's own offline error; the guard is left unarmed for a later retry.
 * - 'unavailable': a reload already happened moments ago (so the chunk is genuinely
 *   missing or the network is failing) or session storage cannot hold the loop guard.
 */
export type StaleChunkRecovery = 'reloading' | 'offline' | 'unavailable';

/** StaleChunkReloadOptions injects the environment reloadForStaleChunk reads, for tests. */
export interface StaleChunkReloadOptions {
  /** storage holds the loop guard; defaults to session storage, resolved defensively. */
  storage?: Pick<StorageLike, 'getItem' | 'setItem'>;
  /** now is the current time in milliseconds; defaults to Date.now(). */
  now?: number;
  /** reload starts the reload; defaults to reloadPage. */
  reload?: () => void;
  /** online reports connectivity; defaults to navigator.onLine. */
  online?: boolean;
}

/**
 * reloadForStaleChunk reloads the document at most once per guard window so the
 * browser fetches the current index.html and its new chunk names, which is what an
 * open tab needs after a deploy. It never reloads while the browser reports being
 * offline, and it never throws: blocked site data only disables the automatic reload.
 */
export function reloadForStaleChunk(options: StaleChunkReloadOptions = {}): StaleChunkRecovery {
  const online = options.online ?? (typeof navigator === 'undefined' || navigator.onLine !== false);
  if (!online) {
    return 'offline';
  }
  const now = options.now ?? Date.now();
  if (!armReloadGuard(options.storage, now)) {
    return 'unavailable';
  }
  (options.reload ?? reloadPage)();
  return 'reloading';
}

/**
 * armReloadGuard records a reload attempt and reports whether one may start now. It
 * refuses within the guard window, and whenever the guard cannot be both read and
 * stored, because an unrecorded reload could repeat forever on a genuinely missing asset.
 */
function armReloadGuard(storage: StaleChunkReloadOptions['storage'], now: number): boolean {
  try {
    const guard = storage ?? resolveStorage('session');
    if (!guard) {
      return false;
    }
    const last = Number(guard.getItem(staleChunkReloadKey) ?? Number.NaN);
    if (Number.isFinite(last) && now - last >= 0 && now - last < staleChunkReloadWindowMs) {
      return false;
    }
    guard.setItem(staleChunkReloadKey, String(now));
    return true;
  } catch {
    return false;
  }
}
