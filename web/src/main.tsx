import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router-dom'

import { AppLayout } from '@/components/layout/app-layout'
import { ThemeProvider } from '@/components/theme/theme-provider'
import { AskUserPage } from '@/features/mcp/ask-user/page'
import { CallLogPage } from '@/features/mcp/call-log/page'
import { InspectorPage } from '@/features/mcp/inspector/page'
import { UserRequestsPage } from '@/features/mcp/user-requests/page'
import { ApiKeyProvider } from '@/lib/api-key-context'
import { defaultToolsConfig, loadRuntimeConfig, type ToolsConfig } from '@/lib/runtime-config'
import { ToolsConfigProvider } from '@/lib/tools-config-context'
import { HomePage } from '@/pages/home'
import { NotFoundPage } from '@/pages/not-found'
import './index.css'

const routes = [
  {
    path: '/',
    element: <AppLayout />,
    errorElement: <NotFoundPage />,
    children: [
      { index: true, element: <HomePage /> },
      { path: 'tools/ask_user', element: <AskUserPage /> },
      { path: 'tools/get_user_requests', element: <UserRequestsPage /> },
      { path: 'tools/call_log', element: <CallLogPage /> },
      { path: 'debug/*', element: <InspectorPage /> },
    ],
  },
  { path: '*', element: <NotFoundPage /> },
]

async function bootstrap() {
  const runtimeConfig = await loadRuntimeConfig()
  const basename = normalizeBasename(runtimeConfig?.publicBasePath ?? import.meta.env.BASE_URL)
  const toolsConfig: ToolsConfig = runtimeConfig?.tools ?? defaultToolsConfig
  const router = createBrowserRouter(routes, { basename })

  const container = document.getElementById('root')
  if (!container) {
    throw new Error('Failed to find root element')
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
  )
}

bootstrap().catch((error) => {
  if (import.meta.env.DEV) {
    console.error('Failed to initialize application', error)
  }
})

function normalizeBasename(input: string | undefined): string {
  if (!input) {
    return '/'
  }

  const trimmed = input.trim()
  if (trimmed === '' || trimmed === '/') {
    return '/'
  }

  const stripped = trimmed.endsWith('/') ? trimmed.slice(0, -1) : trimmed
  return stripped || '/'
}
