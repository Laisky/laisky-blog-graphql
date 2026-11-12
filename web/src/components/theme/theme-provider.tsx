/* eslint-disable react-refresh/only-export-components */
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';

const STORAGE_KEY = 'ui-theme-preference';

/** THEME_COLORS are the browser-UI tints matching the page header in each scheme (see index.html). */
export const THEME_COLORS = { light: '#fefefe', dark: '#0a0f1a' } as const;

/** ThemeSetting is the user's color-scheme preference; "system" follows the OS. */
export type ThemeSetting = 'light' | 'dark' | 'system';

interface ThemeContextValue {
  theme: ThemeSetting;
  resolvedTheme: 'light' | 'dark';
  systemTheme: 'light' | 'dark';
  setTheme: (theme: ThemeSetting) => void;
}

const ThemeContext = createContext<ThemeContextValue | undefined>(undefined);

/** parseStoredTheme maps a raw storage value to a theme override; anything else means "follow the system". */
function parseStoredTheme(stored: string | null): ThemeSetting {
  return stored === 'light' || stored === 'dark' ? stored : 'system';
}

/** getStoredTheme reads the override, treating blocked site data as "follow the system". */
function getStoredTheme(): ThemeSetting {
  if (typeof window === 'undefined') {
    return 'system';
  }
  try {
    return parseStoredTheme(window.localStorage.getItem(STORAGE_KEY));
  } catch {
    return 'system';
  }
}

/**
 * persistTheme stores an explicit override and removes the entry for "system", so a
 * returning visitor follows later OS scheme changes. Storage failures are ignored: the
 * theme still applies to the current page, it just is not remembered.
 */
function persistTheme(setting: ThemeSetting) {
  try {
    if (setting === 'system') {
      window.localStorage.removeItem(STORAGE_KEY);
    } else {
      window.localStorage.setItem(STORAGE_KEY, setting);
    }
  } catch {
    /* Site data blocked: keep the in-memory preference only. */
  }
}

function getSystemTheme(): 'light' | 'dark' {
  if (typeof window === 'undefined') {
    return 'light';
  }
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

/**
 * applyTheme reflects the preference on the document: the data attributes and `.dark`
 * class drive the CSS tokens, `color-scheme` drives native UI, and the color-scheme and
 * theme-color meta tags tell the browser which scheme to use for its own chrome.
 * Keep in sync with the pre-paint inline script in index.html.
 */
function applyTheme(setting: ThemeSetting, fallback: 'light' | 'dark') {
  if (typeof window === 'undefined') {
    return;
  }
  const root = window.document.documentElement;
  const active = setting === 'system' ? fallback : setting;
  root.dataset.theme = active;
  root.dataset.themePreference = setting;
  root.style.colorScheme = active;
  root.classList.toggle('dark', active === 'dark');

  window.document.querySelector('meta[name="color-scheme"]')?.setAttribute('content', setting === 'system' ? 'light dark' : setting);
  window.document.querySelectorAll('meta[name="theme-color"]').forEach((meta) => {
    // With no override each tag keeps the color of the scheme its media query selects.
    const scheme = setting === 'system' ? (meta.getAttribute('media')?.includes('dark') ? 'dark' : 'light') : active;
    meta.setAttribute('content', THEME_COLORS[scheme]);
  });
}

/** ThemeProvider owns the light/dark/system preference and keeps the document, storage and other tabs in sync. */
export function ThemeProvider({ children }: { children: React.ReactNode }) {
  const [systemTheme, setSystemTheme] = useState<'light' | 'dark'>(getSystemTheme);
  const [theme, setThemeState] = useState<ThemeSetting>(getStoredTheme);

  useEffect(() => {
    if (typeof window === 'undefined') {
      return;
    }

    const media = window.matchMedia('(prefers-color-scheme: dark)');
    const update = (matches: boolean) => {
      setSystemTheme(matches ? 'dark' : 'light');
    };

    update(media.matches);

    const listener = (event: MediaQueryListEvent) => update(event.matches);
    media.addEventListener('change', listener);

    // Another tab changed the override: follow it so every open tab shows the same scheme.
    const handleStorage = (event: StorageEvent) => {
      if (event.key === STORAGE_KEY || event.key === null) {
        setThemeState(parseStoredTheme(event.key === null ? null : event.newValue));
      }
    };
    window.addEventListener('storage', handleStorage);

    return () => {
      media.removeEventListener('change', listener);
      window.removeEventListener('storage', handleStorage);
    };
  }, []);

  useEffect(() => {
    applyTheme(theme, systemTheme);
  }, [theme, systemTheme]);

  const setThemePreference = useCallback((next: ThemeSetting) => {
    setThemeState(next);
    persistTheme(next);
  }, []);

  const value = useMemo<ThemeContextValue>(() => {
    const resolved = theme === 'system' ? systemTheme : theme;
    return {
      theme,
      resolvedTheme: resolved,
      systemTheme,
      setTheme: setThemePreference,
    };
  }, [theme, systemTheme, setThemePreference]);

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

/** useTheme returns the current theme preference, its resolved scheme and the setter. */
export function useTheme() {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    throw new Error('useTheme must be used within a ThemeProvider');
  }
  return ctx;
}
