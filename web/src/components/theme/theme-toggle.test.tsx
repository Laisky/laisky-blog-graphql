import { act, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ThemeProvider, THEME_COLORS, useTheme } from '@/components/theme/theme-provider';

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
      // "System" is the absence of an override, so nothing is persisted for it.
      expect(window.localStorage.getItem(STORAGE_KEY)).toBeNull();
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

  describe('document and storage sync', () => {
    /** installThemeMetas adds the color-scheme and theme-color meta tags shipped in index.html. */
    function installThemeMetas() {
      document.head.innerHTML = `
        <meta name="color-scheme" content="light dark">
        <meta name="theme-color" media="(prefers-color-scheme: light)" content="${THEME_COLORS.light}">
        <meta name="theme-color" media="(prefers-color-scheme: dark)" content="${THEME_COLORS.dark}">`;
    }

    /** PreferenceButtons exposes setTheme for the tests below. */
    function PreferenceButtons() {
      const { setTheme } = useTheme();
      return (
        <>
          <button type="button" onClick={() => setTheme('dark')}>dark</button>
          <button type="button" onClick={() => setTheme('system')}>system</button>
        </>
      );
    }

    afterEach(() => {
      document.head.innerHTML = '';
    });

    it('pins the browser color-scheme and theme-color to an explicit override and restores them for system', async () => {
      installThemeMetas();
      render(
        <ThemeProvider>
          <PreferenceButtons />
        </ThemeProvider>
      );

      act(() => screen.getByRole('button', { name: 'dark' }).click());
      expect(window.localStorage.getItem(STORAGE_KEY)).toBe('dark');
      expect(document.querySelector('meta[name="color-scheme"]')?.getAttribute('content')).toBe('dark');
      const themeColors = () => [...document.querySelectorAll('meta[name="theme-color"]')].map((meta) => meta.getAttribute('content'));
      expect(themeColors()).toEqual([THEME_COLORS.dark, THEME_COLORS.dark]);

      act(() => screen.getByRole('button', { name: 'system' }).click());
      expect(window.localStorage.getItem(STORAGE_KEY)).toBeNull();
      expect(document.querySelector('meta[name="color-scheme"]')?.getAttribute('content')).toBe('light dark');
      expect(themeColors()).toEqual([THEME_COLORS.light, THEME_COLORS.dark]);
    });

    it('still renders and applies the theme when site data access throws', async () => {
      vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
        throw new DOMException('blocked', 'SecurityError');
      });
      vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
        throw new DOMException('blocked', 'SecurityError');
      });

      render(
        <ThemeProvider>
          <PreferenceButtons />
        </ThemeProvider>
      );
      expect(document.documentElement.dataset.theme).toBe('light');

      act(() => screen.getByRole('button', { name: 'dark' }).click());
      expect(document.documentElement.dataset.theme).toBe('dark');
    });

    /**
     * changeInOtherTab mimics another tab's write: the shared storage area already holds
     * the new value by the time this tab's `storage` event is dispatched.
     */
    function changeInOtherTab(value: string | null) {
      if (value === null) {
        window.localStorage.removeItem(STORAGE_KEY);
      } else {
        window.localStorage.setItem(STORAGE_KEY, value);
      }
      window.dispatchEvent(new StorageEvent('storage', { key: STORAGE_KEY, newValue: value, storageArea: window.localStorage }));
    }

    afterEach(() => {
      window.localStorage.removeItem(STORAGE_KEY);
      window.sessionStorage.clear();
    });

    it('follows an override changed in another tab', async () => {
      render(
        <ThemeProvider>
          <ThemeToggle />
        </ThemeProvider>
      );
      expect(document.documentElement.dataset.theme).toBe('light');

      act(() => changeInOtherTab('dark'));
      expect(document.documentElement.dataset.theme).toBe('dark');
      expect(document.documentElement.classList.contains('dark')).toBe(true);

      act(() => changeInOtherTab(null));
      expect(document.documentElement.dataset.themePreference).toBe('system');
      expect(document.documentElement.dataset.theme).toBe('light');
    });

    it('settles on the stored override when another tab’s change event arrives after a newer one', async () => {
      render(
        <ThemeProvider>
          <ThemeToggle />
        </ThemeProvider>
      );

      // Two tabs switch the theme moments apart: tab A picks light, tab B then picks dark,
      // so storage ends at "dark". The events are queued tasks, and A's can be dispatched
      // here after B's; its newValue no longer describes what storage holds.
      window.localStorage.setItem(STORAGE_KEY, 'dark');
      act(() => {
        window.dispatchEvent(new StorageEvent('storage', { key: STORAGE_KEY, newValue: 'dark', storageArea: window.localStorage }));
      });
      act(() => {
        window.dispatchEvent(new StorageEvent('storage', { key: STORAGE_KEY, oldValue: null, newValue: 'light', storageArea: window.localStorage }));
      });

      expect(document.documentElement.dataset.themePreference).toBe('dark');
      expect(document.documentElement.dataset.theme).toBe('dark');
    });

    it('keeps the override when a frame in the same tab clears its session storage', async () => {
      window.localStorage.setItem(STORAGE_KEY, 'dark');
      render(
        <ThemeProvider>
          <ThemeToggle />
        </ThemeProvider>
      );
      expect(document.documentElement.dataset.themePreference).toBe('dark');

      // sessionStorage.clear() in a same-origin frame fires key === null here as well.
      act(() => {
        window.dispatchEvent(new StorageEvent('storage', { key: null, storageArea: window.sessionStorage }));
      });

      expect(document.documentElement.dataset.themePreference).toBe('dark');
      expect(document.documentElement.dataset.theme).toBe('dark');
    });

    it('falls back to the system scheme when another tab clears local storage', async () => {
      window.localStorage.setItem(STORAGE_KEY, 'dark');
      render(
        <ThemeProvider>
          <ThemeToggle />
        </ThemeProvider>
      );

      act(() => {
        window.localStorage.clear();
        window.dispatchEvent(new StorageEvent('storage', { key: null, storageArea: window.localStorage }));
      });

      expect(document.documentElement.dataset.themePreference).toBe('system');
      expect(document.documentElement.dataset.theme).toBe('light');
    });

    it('ignores changes to unrelated keys', async () => {
      window.localStorage.setItem(STORAGE_KEY, 'dark');
      render(
        <ThemeProvider>
          <ThemeToggle />
        </ThemeProvider>
      );

      act(() => {
        window.localStorage.removeItem(STORAGE_KEY);
        window.dispatchEvent(new StorageEvent('storage', { key: 'other-key', newValue: 'x', storageArea: window.localStorage }));
      });

      // Only an event for the theme key (or a clear) is a reason to re-read it.
      expect(document.documentElement.dataset.themePreference).toBe('dark');
    });
  });
});
