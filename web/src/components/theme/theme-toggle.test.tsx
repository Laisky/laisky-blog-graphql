import { render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { ThemeProvider } from '@/components/theme/theme-provider';

import { DEFAULT_THEME_TOGGLE_OPTIONS, ThemeToggle } from './theme-toggle';

const STORAGE_KEY = 'ui-theme-preference';

/**
 * installMatchMediaMock installs a deterministic matchMedia mock for ThemeProvider tests.
 * Parameters: prefersDark controls whether the mocked system preference reports dark mode.
 * Returns: This function does not return a value.
 */
function installMatchMediaMock(prefersDark: boolean) {
  const matchMediaMock = vi.fn().mockImplementation((query: string): MediaQueryList => {
    return {
      matches: prefersDark,
      media: query,
      onchange: null,
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      addListener: vi.fn(),
      removeListener: vi.fn(),
      dispatchEvent: vi.fn(),
    };
  });

  Object.defineProperty(window, 'matchMedia', {
    writable: true,
    value: matchMediaMock,
  });
}

describe('ThemeToggle', () => {
  beforeEach(() => {
    installMatchMediaMock(false);
    window.localStorage.removeItem(STORAGE_KEY);
    document.documentElement.classList.remove('dark');
    document.documentElement.removeAttribute('data-theme');
    document.documentElement.removeAttribute('data-theme-preference');
  });

  it('exports the canonical system/light/dark option set', () => {
    expect(DEFAULT_THEME_TOGGLE_OPTIONS.map((option) => option.value)).toEqual(['system', 'light', 'dark']);
  });

  it('defaults to system mode and resolves to light when system is light', async () => {
    render(
      <ThemeProvider>
        <ThemeToggle />
      </ThemeProvider>
    );

    await waitFor(() => {
      expect(document.documentElement.dataset.themePreference).toBe('system');
      expect(document.documentElement.dataset.theme).toBe('light');
      expect(window.localStorage.getItem(STORAGE_KEY)).toBe('system');
    });
  });

  it('reflects stored dark preference in the trigger icon', async () => {
    window.localStorage.setItem(STORAGE_KEY, 'dark');

    render(
      <ThemeProvider>
        <ThemeToggle />
      </ThemeProvider>
    );

    await waitFor(() => {
      expect(document.documentElement.dataset.themePreference).toBe('dark');
      expect(document.documentElement.dataset.theme).toBe('dark');
    });

    const triggerButton = screen.getByRole('button', { name: /toggle color theme/i });
    expect(triggerButton.querySelector('.lucide-moon')).toBeDefined();
  });
});
