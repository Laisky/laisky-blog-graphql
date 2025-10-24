import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { createBrowserRouter, RouterProvider } from 'react-router-dom'

import './index.css'
import { ThemeProvider } from '@/components/theme/theme-provider'
import { AppLayout } from '@/components/layout/app-layout'
import { AskUserPage } from '@/features/mcp/ask-user/page'
import { InspectorPage } from '@/features/mcp/inspector/page'
import { HomePage } from '@/pages/home'
import { NotFoundPage } from '@/pages/not-found'

const basename = normalizeBasename(import.meta.env.BASE_URL)

const router = createBrowserRouter([
  {
    path: '/',
    element: <AppLayout />,
    errorElement: <NotFoundPage />,
    children: [
      { index: true, element: <HomePage /> },
      { path: 'tools/ask_user', element: <AskUserPage /> },
      { path: 'debug/*', element: <InspectorPage /> },
    ],
  },
  { path: '*', element: <NotFoundPage /> },
], { basename })

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ThemeProvider>
      <RouterProvider router={router} />
    </ThemeProvider>
  </StrictMode>
)

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
