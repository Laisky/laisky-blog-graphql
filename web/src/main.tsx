import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';

import { AppLayout } from '@/components/layout/app-layout';
import { ThemeProvider } from '@/components/theme/theme-provider';
import { AskUserPage } from '@/features/mcp/ask-user/page';
import { CallLogPage } from '@/features/mcp/call-log/page';
import { FileIOPage } from '@/features/mcp/file-io/page';
import { InspectorPage } from '@/features/mcp/inspector/page';
import { MemoryPage } from '@/features/mcp/memory/page';
import { SettingsPage } from '@/features/mcp/settings/page';
import { UserRequestsPage } from '@/features/mcp/user-requests/page';
import { WebFetchPage } from '@/features/mcp/web-fetch/page';
import { WebSearchPage } from '@/features/mcp/web-search/page';
import { ApiKeyProvider } from '@/lib/api-key-context';
import { defaultToolsConfig, loadRuntimeConfig, type ToolsConfig } from '@/lib/runtime-config';
import { applySiteBranding } from '@/lib/site-branding';
import { ToolsConfigProvider } from '@/lib/tools-config-context';
import { HomePage } from '@/pages/home';
import { NotFoundPage } from '@/pages/not-found';
import { SsoLoginPage } from '@/pages/sso-login';
import './index.css';

type RouterKind = 'mcp' | 'sso';

/**
 * bootstrap loads runtime configuration, builds the router, and renders the application.
 * It returns a promise that resolves when initialization completes or rejects on failure.
 */
async function bootstrap() {
  const runtimeConfig = await loadRuntimeConfig();
  const routeContext = resolveRouteContext(
    window.location.pathname,
    runtimeConfig?.publicBasePath ?? import.meta.env.BASE_URL,
    runtimeConfig?.site?.router
  );
  const toolsConfig: ToolsConfig = runtimeConfig?.tools ?? defaultToolsConfig;
  const turnstileSiteKey = runtimeConfig?.site?.turnstileSiteKey;
  applySiteBranding(runtimeConfig?.site);
  const routes = routeContext.routerKind === 'sso' ? buildSsoRoutes(turnstileSiteKey) : buildMcpRoutes(turnstileSiteKey);
  const router = createBrowserRouter(routes, { basename: routeContext.basename });

  const container = document.getElementById('root');
  if (!container) {
    throw new Error('Failed to find root element');
  }

  createRoot(container).render(
    <StrictMode>
      <ThemeProvider>
        <ToolsConfigProvider config={toolsConfig}>
          <ApiKeyProvider>
            <RouterProvider router={router} />
          </ApiKeyProvider>
        </ToolsConfigProvider>
      </ThemeProvider>
    </StrictMode>
  );
}

bootstrap().catch((error) => {
  if (import.meta.env.DEV) {
    console.error('Failed to initialize application', error);
  }
});

/**
 * buildMcpRoutes builds the router table for the MCP console experience.
 * It accepts the optional Turnstile site key and returns the route configuration used by React Router.
 */
function buildMcpRoutes(turnstileSiteKey: string | undefined) {
  return [
    { path: '/sso/login', element: <SsoLoginPage turnstileSiteKey={turnstileSiteKey} /> },
    {
      path: '/',
      element: <AppLayout />,
      errorElement: <NotFoundPage />,
      children: [
        { index: true, element: <HomePage /> },
        { path: 'tools/ask_user', element: <AskUserPage /> },
        { path: 'tools/get_user_requests', element: <UserRequestsPage /> },
        { path: 'tools/web_search', element: <WebSearchPage /> },
        { path: 'tools/web_fetch', element: <WebFetchPage /> },
        { path: 'tools/file_io', element: <FileIOPage /> },
        { path: 'tools/memory', element: <MemoryPage /> },
        { path: 'tools/call_log', element: <CallLogPage /> },
        { path: 'settings', element: <SettingsPage /> },
        { path: 'debug/*', element: <InspectorPage /> },
      ],
    },
    { path: '*', element: <NotFoundPage /> },
  ];
}

/**
 * buildSsoRoutes builds the router table for the standalone SSO experience.
 * It accepts the optional Turnstile site key and returns the route configuration used by React Router.
 */
function buildSsoRoutes(turnstileSiteKey: string | undefined) {
  return [
    { path: '/', element: <SsoLoginPage turnstileSiteKey={turnstileSiteKey} /> },
    { path: '/login', element: <SsoLoginPage turnstileSiteKey={turnstileSiteKey} /> },
    { path: '*', element: <NotFoundPage /> },
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
function resolveRouteContext(pathname: string, configuredBasename: string | undefined, configuredRouter: string | undefined): {
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
