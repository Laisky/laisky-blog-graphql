import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

const CURRENT_KEY_STORAGE = 'mcp_api_key';
const HISTORY_STORAGE = 'mcp_api_key_history';
const MAX_HISTORY = 10;

const BEARER_PREFIX = /^Bearer\s+/i;

/** Strip the optional "Bearer " prefix and trim whitespace. */
export function normalizeApiKey(value: string): string {
  let output = (value ?? '').trim();
  while (output && BEARER_PREFIX.test(output)) {
    output = output.replace(BEARER_PREFIX, '').trim();
  }
  return output;
}

interface ApiKeyContextValue {
  /** The active API key (already normalised). */
  apiKey: string;
  /** Recent API keys, newest first. Does not include the current key unless it was explicitly committed. */
  history: string[];
  /** Set the current API key. Also pushes it to history. */
  setApiKey: (key: string) => void;
  /** Remove a specific key from history. */
  removeFromHistory: (key: string) => void;
  /** Disconnect: clear apiKey without removing history. */
  disconnect: () => void;
}

const ApiKeyContext = createContext<ApiKeyContextValue | null>(null);

function loadHistory(): string[] {
  if (typeof window === 'undefined') return [];
  try {
    const raw = window.localStorage.getItem(HISTORY_STORAGE);
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed.filter((v): v is string => typeof v === 'string' && v.length > 0);
  } catch {
    return [];
  }
}

function saveHistory(history: string[]) {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(HISTORY_STORAGE, JSON.stringify(history.slice(0, MAX_HISTORY)));
  } catch {
    // ignore
  }
}

function loadCurrentKey(): string {
  if (typeof window === 'undefined') return '';
  try {
    return normalizeApiKey(window.localStorage.getItem(CURRENT_KEY_STORAGE) ?? '');
  } catch {
    return '';
  }
}

function saveCurrentKey(key: string) {
  if (typeof window === 'undefined') return;
  try {
    const normalised = normalizeApiKey(key);
    if (normalised) {
      window.localStorage.setItem(CURRENT_KEY_STORAGE, normalised);
    } else {
      window.localStorage.removeItem(CURRENT_KEY_STORAGE);
    }
  } catch {
    // ignore
  }
}

export function ApiKeyProvider({ children }: { children: ReactNode }) {
  const [apiKey, setApiKeyRaw] = useState<string>(() => loadCurrentKey());
  const [history, setHistory] = useState<string[]>(() => loadHistory());

  // Persist current key when it changes
  useEffect(() => {
    saveCurrentKey(apiKey);
  }, [apiKey]);

  // Persist history when it changes
  useEffect(() => {
    saveHistory(history);
  }, [history]);

  const setApiKey = useCallback((key: string) => {
    const normalised = normalizeApiKey(key);
    setApiKeyRaw(normalised);

    if (normalised) {
      setHistory((prev) => {
        const deduped = prev.filter((k) => k !== normalised);
        return [normalised, ...deduped].slice(0, MAX_HISTORY);
      });
    }
  }, []);

  const removeFromHistory = useCallback((key: string) => {
    const normalised = normalizeApiKey(key);
    setHistory((prev) => prev.filter((k) => k !== normalised));
  }, []);

  const disconnect = useCallback(() => {
    setApiKeyRaw('');
  }, []);

  const value = useMemo<ApiKeyContextValue>(
    () => ({ apiKey, history, setApiKey, removeFromHistory, disconnect }),
    [apiKey, history, setApiKey, removeFromHistory, disconnect]
  );

  return <ApiKeyContext.Provider value={value}>{children}</ApiKeyContext.Provider>;
}

export function useApiKey(): ApiKeyContextValue {
  const ctx = useContext(ApiKeyContext);
  if (!ctx) {
    throw new Error('useApiKey must be used within ApiKeyProvider');
  }
  return ctx;
}
