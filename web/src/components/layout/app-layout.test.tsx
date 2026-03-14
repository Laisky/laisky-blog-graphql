import '@testing-library/jest-dom/vitest';
import { render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { MemoryRouter, Route, Routes } from 'react-router-dom';

import { AppLayout } from './app-layout';
import { useApiKey } from '@/lib/api-key-context';
import { useToolsConfig } from '@/lib/tools-config-context';

vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
}));

vi.mock('@/lib/tools-config-context', () => ({
  useToolsConfig: vi.fn(),
}));

vi.mock('@/components/theme/theme-toggle', () => ({
  ThemeToggle: () => <div>Theme toggle</div>,
}));

/**
 * renderLayout renders the shared app layout at a specific route for banner assertions.
 * It accepts the initial route and returns the Testing Library render result.
 */
function renderLayout(route: string) {
  return render(
    <MemoryRouter initialEntries={[route]}>
      <Routes>
        <Route element={<AppLayout />}>
          <Route index element={<div>Overview page</div>} />
          <Route path="settings" element={<div>Settings page</div>} />
        </Route>
      </Routes>
    </MemoryRouter>
  );
}

describe('AppLayout API key banner', () => {
  it.each([
    {
      status: 'none',
      isToolConsoleLocked: true,
      apiKey: '',
      title: 'API key required to use MCP tool consoles',
      cta: 'Set API Key',
    },
    {
      status: 'error',
      isToolConsoleLocked: true,
      apiKey: 'saved-key',
      title: 'The saved API key is invalid',
      cta: 'Open Settings',
    },
    {
      status: 'validating',
      isToolConsoleLocked: true,
      apiKey: 'saved-key',
      title: 'API key validation is in progress',
      cta: 'Open Settings',
    },
  ])('shows the prominent locked banner for $status status', ({ apiKey, cta, isToolConsoleLocked, status, title }) => {
    vi.mocked(useToolsConfig).mockReturnValue({
      ask_user: true,
      extract_key_info: true,
      file_io: true,
      get_user_request: true,
      memory: true,
      web_fetch: true,
      web_search: true,
    });
    vi.mocked(useApiKey).mockReturnValue({
      apiKey,
      disconnect: vi.fn(),
      history: [],
      isToolConsoleLocked,
      keyEntries: [],
      remainQuota: null,
      removeFromHistory: vi.fn(),
      sessionId: 0,
      setAliasForKey: vi.fn(),
      setApiKey: vi.fn(),
      status,
      switchApiKey: vi.fn(),
      validateApiKey: vi.fn(),
    });

    renderLayout('/');

    expect(screen.getByText(title)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: cta })).toHaveAttribute('href', '/settings');
  });

  it('hides the shared banner on the settings page', () => {
    vi.mocked(useToolsConfig).mockReturnValue({
      ask_user: true,
      extract_key_info: true,
      file_io: true,
      get_user_request: true,
      memory: true,
      web_fetch: true,
      web_search: true,
    });
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: '',
      disconnect: vi.fn(),
      history: [],
      isToolConsoleLocked: true,
      keyEntries: [],
      remainQuota: null,
      removeFromHistory: vi.fn(),
      sessionId: 0,
      setAliasForKey: vi.fn(),
      setApiKey: vi.fn(),
      status: 'none',
      switchApiKey: vi.fn(),
      validateApiKey: vi.fn(),
    });

    renderLayout('/settings');

    expect(screen.queryByText('API key required to use MCP tool consoles')).not.toBeInTheDocument();
  });
});
