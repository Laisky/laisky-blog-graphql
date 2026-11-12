import '@testing-library/jest-dom/vitest';
import { act, render, screen } from '@testing-library/react';
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
      bannerText: 'API key required. Set one in Settings to enable tools.',
      cta: 'Set API Key',
    },
    {
      status: 'error',
      isToolConsoleLocked: true,
      apiKey: 'saved-key',
      bannerText: 'Invalid API key. Update it in Settings.',
      cta: 'Settings',
    },
    {
      status: 'validating',
      isToolConsoleLocked: true,
      apiKey: 'saved-key',
      bannerText: 'Validating API key...',
      cta: 'Settings',
    },
  ])('shows the prominent locked banner for $status status', ({ apiKey, cta, isToolConsoleLocked, status, bannerText }) => {
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

    expect(screen.getByText(bannerText)).toBeInTheDocument();
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

    expect(screen.queryByText('API key required. Set one in Settings to enable tools.')).not.toBeInTheDocument();
  });
});

describe('AppLayout navigation semantics', () => {
  /** mockValidKey renders the layout for an authenticated key with every tool enabled. */
  function mockValidKey() {
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
      apiKey: 'saved-key',
      disconnect: vi.fn(),
      history: [],
      isToolConsoleLocked: false,
      keyEntries: [],
      remainQuota: 300000,
      removeFromHistory: vi.fn(),
      sessionId: 0,
      setAliasForKey: vi.fn(),
      setApiKey: vi.fn(),
      status: 'valid',
      switchApiKey: vi.fn(),
      validateApiKey: vi.fn(),
    });
  }

  it('offers a skip link that targets the focusable main landmark', () => {
    mockValidKey();
    renderLayout('/');

    const skip = screen.getByRole('link', { name: 'Skip to main content' });
    expect(skip).toHaveAttribute('href', '#main-content');
    const main = screen.getByRole('main');
    expect(main).toHaveAttribute('id', 'main-content');
    expect(main).toHaveAttribute('tabindex', '-1');
  });

  it('names the icon-only settings link and marks it current on the settings page', () => {
    mockValidKey();
    renderLayout('/settings');

    expect(screen.getByRole('link', { name: 'Settings: Authenticated' })).toHaveAttribute('aria-current', 'page');
  });

  it('exposes the console links as a disclosure that Escape closes back to its trigger', () => {
    mockValidKey();
    renderLayout('/');

    const trigger = screen.getByRole('button', { name: /console/i });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    expect(trigger).not.toHaveAttribute('aria-haspopup');
    const list = document.getElementById(trigger.getAttribute('aria-controls') ?? '');
    expect(list).not.toBeNull();
    expect(list).not.toBeVisible();

    act(() => trigger.click());
    expect(trigger).toHaveAttribute('aria-expanded', 'true');
    expect(list).toBeVisible();
    expect(screen.getByRole('link', { name: 'Inspector (opens in a new tab)' })).toHaveAttribute('target', '_blank');

    act(() => {
      screen.getByRole('link', { name: 'web_search' }).focus();
      document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    expect(trigger).toHaveFocus();
  });
});
