/* eslint-disable react-refresh/only-export-components */
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';

const STORAGE_KEY = 'ui-theme-preference';

export type ThemeSetting = 'light' | 'dark' | 'system';

interface ThemeContextValue {
  theme: ThemeSetting;
  resolvedTheme: 'light' | 'dark';
  setTheme: (theme: ThemeSetting) => void;
}

const ThemeContext = createContext<ThemeContextValue | undefined>(undefined);

function getStoredTheme(): ThemeSetting {
  if (typeof window === 'undefined') {
    return 'system';
  }
  const stored = window.localStorage.getItem(STORAGE_KEY);
  if (stored === 'light' || stored === 'dark' || stored === 'system') {
    return stored;
  }
  return 'system';
}

function getSystemTheme(): 'light' | 'dark' {
  if (typeof window === 'undefined') {
    return 'light';
  }
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
}

function applyTheme(setting: ThemeSetting, fallback: 'light' | 'dark') {
  if (typeof window === 'undefined') {
    return;
  }
  const root = window.document.documentElement;
  const active = setting === 'system' ? fallback : setting;
  root.dataset.theme = active;
  root.dataset.themePreference = setting;
  root.style.colorScheme = active;
  if (active === 'dark') {
    root.classList.add('dark');
  } else {
    root.classList.remove('dark');
  }
}

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

    return () => {
      media.removeEventListener('change', listener);
    };
  }, []);

  useEffect(() => {
    applyTheme(theme, systemTheme);
    if (typeof window !== 'undefined') {
      window.localStorage.setItem(STORAGE_KEY, theme);
    }
  }, [theme, systemTheme]);

  const setThemePreference = useCallback((next: ThemeSetting) => {
    setThemeState(next);
  }, []);

  const value = useMemo<ThemeContextValue>(() => {
    const resolved = theme === 'system' ? systemTheme : theme;
    return {
      theme,
      resolvedTheme: resolved,
      setTheme: setThemePreference,
    };
  }, [theme, systemTheme, setThemePreference]);

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>;
}

export function useTheme() {
  const ctx = useContext(ThemeContext);
  if (!ctx) {
    throw new Error('useTheme must be used within a ThemeProvider');
  }
  return ctx;
}
