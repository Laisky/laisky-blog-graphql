import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';

import { AppLayout } from '@/components/layout/app-layout';
import { ThemeProvider } from '@/components/theme/theme-provider';
import { AskUserPage } from '@/features/mcp/ask-user/page';
import { CallLogPage } from '@/features/mcp/call-log/page';
import { FileIOPage } from '@/features/mcp/file-io/page';
import { InspectorPage } from '@/features/mcp/inspector/page';
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
  const basename = normalizeBasename(runtimeConfig?.publicBasePath ?? import.meta.env.BASE_URL);
  const toolsConfig: ToolsConfig = runtimeConfig?.tools ?? defaultToolsConfig;
  const turnstileSiteKey = runtimeConfig?.site?.turnstileSiteKey;
  applySiteBranding(runtimeConfig?.site);
  const routerKind = resolveRouterKind(runtimeConfig?.site?.router);
  const routes = routerKind === 'sso' ? buildSsoRoutes(turnstileSiteKey) : buildMcpRoutes(turnstileSiteKey);
  const router = createBrowserRouter(routes, { basename });

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
