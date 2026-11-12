import { StrictMode, type ReactElement } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider, type RouteObject } from 'react-router-dom';

import { AppLayout } from '@/components/layout/app-layout';
import { ThemeProvider } from '@/components/theme/theme-provider';
import { ApiKeyProvider } from '@/lib/api-key-context';
import { configurePublicApiBasePath } from '@/lib/api-base';
import { createRootErrorHandlers } from '@/lib/browser-sentry';
import { routeModules } from '@/lib/route-modules';
import { configureSsoRedirectPolicy } from '@/lib/sso-redirect';
import { defaultToolsConfig, loadRuntimeConfig, type SsoJwtConfig, type ToolsConfig } from '@/lib/runtime-config';
import { applySiteBranding } from '@/lib/site-branding';
import { ToolsConfigProvider } from '@/lib/tools-config-context';
import { HomePage } from '@/pages/home';
import { NotFoundPage } from '@/pages/not-found';
import { RouteError } from '@/pages/route-error';
import './index.css';

type RouterKind = 'mcp' | 'sso';

/**
 * bootstrap loads runtime configuration, builds the router, and renders the application.
 * It returns a promise that resolves when initialization completes or rejects on failure.
 */
export async function bootstrap() {
  const runtimeConfig = await loadRuntimeConfig();
  configurePublicApiBasePath(runtimeConfig?.publicApiBasePath);
  // A missing runtime configuration fails closed for the legacy bearer handoff.
  configureSsoRedirectPolicy(runtimeConfig);
  const routeContext = resolveRouteContext(
    window.location.pathname,
    runtimeConfig?.publicBasePath ?? import.meta.env.BASE_URL,
    runtimeConfig?.site?.router
  );
  const toolsConfig: ToolsConfig = runtimeConfig?.consoleTools ?? defaultToolsConfig;
  const turnstileSiteKey = runtimeConfig?.site?.turnstileSiteKey;
  // Hide the GitHub sign-in option unless the backend reports it configured, so
  // users never click through to a "github oauth client is not configured" error.
  const githubOAuthEnabled = runtimeConfig?.githubOAuthEnabled ?? false;
  applySiteBranding(runtimeConfig?.site);
  const routes =
    routeContext.routerKind === 'sso'
      ? buildSsoRoutes(turnstileSiteKey, githubOAuthEnabled, runtimeConfig?.ssoJwt ?? undefined)
      : buildMcpRoutes(turnstileSiteKey, githubOAuthEnabled, runtimeConfig?.ssoJwt ?? undefined);
  const router = createBrowserRouter(routes, { basename: routeContext.basename });
  // Keep the static agent-readable HTML on screen until the landing page's chunk has
  // loaded, instead of replacing it with an empty tree while the lazy route resolves.
  await waitForInitialRoute(router);

  const container = document.getElementById('root');
  if (!container) {
    throw new Error('Failed to find root element');
  }

  createRoot(container, createRootErrorHandlers()).render(
    <StrictMode>
      <ThemeProvider>
        <ToolsConfigProvider config={toolsConfig} prices={runtimeConfig?.pricing}>
          <ApiKeyProvider>
            <RouterProvider router={router} />
          </ApiKeyProvider>
        </ToolsConfigProvider>
      </ThemeProvider>
    </StrictMode>
  );
}


type BrowserRouter = ReturnType<typeof createBrowserRouter>;

/**
 * waitForInitialRoute resolves once the router has matched the current URL and loaded
 * every lazy page module that match needs, whether or not that load succeeded.
 */
function waitForInitialRoute(router: BrowserRouter): Promise<void> {
  if (router.state.initialized) {
    return Promise.resolve();
  }
  return new Promise((resolve) => {
    const unsubscribe = router.subscribe((state) => {
      if (state.initialized) {
        unsubscribe();
        resolve();
      }
    });
  });
}

/**
 * lazyPage adapts a page module loader into a React Router lazy route definition.
 * It accepts the loader and a render function that builds the page element from the
 * loaded module, and returns the function React Router calls on first match.
 */
function lazyPage<M>(load: () => Promise<M>, render: (module: M) => ReactElement): RouteObject['lazy'] {
  return async () => ({ element: render(await load()) });
}

/**
 * buildSsoPageRoutes builds the SSO page routes shared by both routers.
 * It accepts the path prefix ("/sso" in the MCP router, "" in the SSO router), the optional
 * Turnstile site key, the GitHub OAuth availability flag, and the SSO JWT config.
 */
function buildSsoPageRoutes(
  prefix: string,
  turnstileSiteKey: string | undefined,
  githubOAuthEnabled: boolean,
  ssoJwt: SsoJwtConfig | null | undefined
): RouteObject[] {
  const loginPage = lazyPage(routeModules.ssoLogin, ({ SsoLoginPage }) => (
    <SsoLoginPage turnstileSiteKey={turnstileSiteKey} githubOAuthEnabled={githubOAuthEnabled} />
  ));
  return [
    ...(prefix === '' ? [{ path: '/', lazy: loginPage }] : []),
    { path: `${prefix}/login`, lazy: loginPage },
    { path: `${prefix}/github/callback`, lazy: lazyPage(routeModules.ssoGithubCallback, ({ SsoGithubCallbackPage }) => <SsoGithubCallbackPage />) },
    {
      path: `${prefix}/profile`,
      lazy: lazyPage(routeModules.ssoProfile, ({ SsoProfilePage }) => <SsoProfilePage githubOAuthEnabled={githubOAuthEnabled} />),
    },
    { path: `${prefix}/token`, lazy: lazyPage(routeModules.ssoTokenDetails, ({ SsoTokenDetailsPage }) => <SsoTokenDetailsPage ssoJwt={ssoJwt} />) },
  ];
}

/**
 * buildMcpRoutes builds the router table for the MCP console experience.
 * It accepts the optional Turnstile site key and GitHub OAuth availability flag and returns the route configuration used by React Router.
 * Every page except the overview is code-split, so the landing page never downloads the tool consoles.
 */
function buildMcpRoutes(turnstileSiteKey: string | undefined, githubOAuthEnabled: boolean, ssoJwt: SsoJwtConfig | null | undefined): RouteObject[] {
  return [
    {
      errorElement: <RouteError standalone />,
      children: [
        ...buildSsoPageRoutes('/sso', turnstileSiteKey, githubOAuthEnabled, ssoJwt),
        {
          path: '/',
          element: <AppLayout />,
          errorElement: <RouteError standalone />,
          children: [
            {
              // Page failures (including a page chunk that failed to load) render inside the layout.
              errorElement: <RouteError />,
              children: [
                { index: true, element: <HomePage /> },
                { path: 'tools/ask_user', lazy: lazyPage(routeModules.askUser, ({ AskUserPage }) => <AskUserPage />) },
                { path: 'tools/get_user_requests', lazy: lazyPage(routeModules.userRequests, ({ UserRequestsPage }) => <UserRequestsPage />) },
                { path: 'tools/web_search', lazy: lazyPage(routeModules.webSearch, ({ WebSearchPage }) => <WebSearchPage />) },
                { path: 'tools/web_fetch', lazy: lazyPage(routeModules.webFetch, ({ WebFetchPage }) => <WebFetchPage />) },
                { path: 'tools/extract_key_info', lazy: lazyPage(routeModules.extractKeyInfo, ({ ExtractKeyInfoPage }) => <ExtractKeyInfoPage />) },
                { path: 'tools/file_io', lazy: lazyPage(routeModules.fileIO, ({ FileIOPage }) => <FileIOPage />) },
                { path: 'tools/memory', lazy: lazyPage(routeModules.memory, ({ MemoryPage }) => <MemoryPage />) },
                { path: 'tools/call_log', lazy: lazyPage(routeModules.callLog, ({ CallLogPage }) => <CallLogPage />) },
                { path: 'settings', lazy: lazyPage(routeModules.settings, ({ SettingsPage }) => <SettingsPage />) },
                { path: 'debug/*', lazy: lazyPage(routeModules.inspector, ({ InspectorPage }) => <InspectorPage />) },
                { path: '*', element: <NotFoundPage /> },
              ],
            },
          ],
        },
      ],
    },
  ];
}

/**
 * buildSsoRoutes builds the router table for the standalone SSO experience.
 * It accepts the optional Turnstile site key and GitHub OAuth availability flag and returns the route configuration used by React Router.
 */
function buildSsoRoutes(turnstileSiteKey: string | undefined, githubOAuthEnabled: boolean, ssoJwt: SsoJwtConfig | null | undefined): RouteObject[] {
  return [
    {
      errorElement: <RouteError standalone />,
      children: [...buildSsoPageRoutes('', turnstileSiteKey, githubOAuthEnabled, ssoJwt), { path: '*', element: <NotFoundPage standalone /> }],
    },
  ];
}

/**
 * resolveRouterKind normalizes the router identifier into a supported kind.
 * It accepts the raw router name and returns a known router kind.
 */
function resolveRouterKind(raw: string | undefined): RouterKind {
  const normalized = raw?.trim().toLowerCase();
  if (normalized === 'sso') {
    return 'sso';
  }
  return 'mcp';
}

/**
 * resolveRouteContext chooses the effective router kind and basename for the active URL.
 * It accepts the current pathname, configured basename, and configured router, then returns the routing context to use.
 */
function resolveRouteContext(
  pathname: string,
  configuredBasename: string | undefined,
  configuredRouter: string | undefined
): {
  basename: string;
  routerKind: RouterKind;
} {
  const basename = normalizeBasename(configuredBasename);
  const routerKind = resolveRouterKind(configuredRouter);

  if (isPathOutsideBasename(pathname, basename) && isRootMcpConsolePath(pathname)) {
    return {
      basename: '/',
      routerKind: 'mcp',
    };
  }

  return {
    basename,
    routerKind,
  };
}

/**
 * isPathOutsideBasename reports whether the current pathname is outside the configured basename.
 * It accepts the pathname and basename and returns true when the URL cannot be matched by that basename.
 */
function isPathOutsideBasename(pathname: string, basename: string): boolean {
  if (basename === '/') {
    return false;
  }

  return pathname !== basename && !pathname.startsWith(`${basename}/`);
}

/**
 * isRootMcpConsolePath reports whether the active path targets a root-level MCP console route.
 * It accepts the current pathname and returns true for MCP routes such as /debug, /settings, and /tools/*.
 */
function isRootMcpConsolePath(pathname: string): boolean {
  if (!pathname.startsWith('/')) {
    return false;
  }

  switch (true) {
    case pathname === '/debug':
    case pathname.startsWith('/debug/'):
    case pathname === '/settings':
    case pathname.startsWith('/settings/'):
    case pathname === '/tools':
    case pathname.startsWith('/tools/'):
      return true;
    default:
      return false;
  }
}

/**
 * normalizeBasename normalizes the router basename to a non-empty string.
 * It accepts a candidate basename and returns "/" when the input is empty.
 */
function normalizeBasename(input: string | undefined): string {
  if (!input) {
    return '/';
  }

  const trimmed = input.trim();
  if (trimmed === '' || trimmed === '/') {
    return '/';
  }

  const stripped = trimmed.endsWith('/') ? trimmed.slice(0, -1) : trimmed;
  return stripped || '/';
}
