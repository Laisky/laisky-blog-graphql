import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

const CURRENT_KEY_STORAGE = 'mcp_api_key';
const HISTORY_STORAGE = 'mcp_api_key_history';
const KEY_ENTRIES_STORAGE = 'mcp_api_key_entries';
// MAX_HISTORY caps the number of API keys preserved in local storage history.
const MAX_HISTORY = 10;

const BEARER_PREFIX = /^Bearer\s+/i;
const STATUS_STORAGE = 'mcp_api_key_status';
const QUOTA_STORAGE = 'mcp_api_key_quota';

export type ApiKeyStatus = 'none' | 'error' | 'insufficient' | 'valid' | 'validating';

/** ApiKeyEntry stores one API key and its optional user-defined alias. */
export interface ApiKeyEntry {
  key: string;
  alias: string;
}

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
  /** Validation status. */
  status: ApiKeyStatus;
  /** Remaining quota. */
  remainQuota: number | null;
  /** Recent API keys, newest first. Does not include the current key unless it was explicitly committed. */
  history: string[];
  /** API key entries with aliases, newest first. */
  keyEntries: ApiKeyEntry[];
  /** Monotonic session id that changes after key switch/disconnect. */
  sessionId: number;
  /** Set the current API key. Also pushes it to history. */
  setApiKey: (key: string) => void;
  /** Switch to an existing key and trigger validation. */
  switchApiKey: (key: string) => void;
  /** Update alias for a specific key entry. */
  setAliasForKey: (key: string, alias: string) => void;
  /** Validate the current or a new API key. */
  validateApiKey: (key?: string) => Promise<void>;
  /** Remove a specific key from history. */
  removeFromHistory: (key: string) => void;
  /** Disconnect: clear apiKey without removing history. */
  disconnect: () => void;
}

const ApiKeyContext = createContext<ApiKeyContextValue | null>(null);

/** defaultAliasForKey returns a stable fallback alias for a key. */
function defaultAliasForKey(key: string): string {
  if (key.length <= 8) {
    return key;
  }
  return `${key.slice(0, 4)}••••${key.slice(-4)}`;
}

/** normalizeAndDedupeEntries trims values, removes invalid items, and deduplicates by key. */
function normalizeAndDedupeEntries(entries: ApiKeyEntry[]): ApiKeyEntry[] {
  const unique = new Map<string, ApiKeyEntry>();
  for (const entry of entries) {
    const normalizedKey = normalizeApiKey(entry.key);
    if (!normalizedKey) {
      continue;
    }

    const normalizedAlias = (entry.alias ?? '').trim() || defaultAliasForKey(normalizedKey);
    if (!unique.has(normalizedKey)) {
      unique.set(normalizedKey, { key: normalizedKey, alias: normalizedAlias });
    }
  }

  return Array.from(unique.values()).slice(0, MAX_HISTORY);
}

/** loadKeyEntries loads alias-aware key entries and migrates old history format when needed. */
function loadKeyEntries(): ApiKeyEntry[] {
  if (typeof window === 'undefined') return [];

  try {
    const rawEntries = window.localStorage.getItem(KEY_ENTRIES_STORAGE);
    if (rawEntries) {
      const parsed = JSON.parse(rawEntries);
      if (Array.isArray(parsed)) {
        const entries: ApiKeyEntry[] = parsed
          .filter((item): item is { key?: unknown; alias?: unknown } => typeof item === 'object' && item !== null)
          .map((item) => ({ key: String(item.key ?? ''), alias: String(item.alias ?? '') }));
        return normalizeAndDedupeEntries(entries);
      }
    }
  } catch {
    // ignore
  }

  const migratedEntries: ApiKeyEntry[] = [];
  try {
    const current = normalizeApiKey(window.localStorage.getItem(CURRENT_KEY_STORAGE) ?? '');
    if (current) {
      migratedEntries.push({ key: current, alias: defaultAliasForKey(current) });
    }

    const rawHistory = window.localStorage.getItem(HISTORY_STORAGE);
    if (rawHistory) {
      const parsed = JSON.parse(rawHistory);
      if (Array.isArray(parsed)) {
        for (const item of parsed) {
          if (typeof item === 'string') {
            const normalized = normalizeApiKey(item);
            if (normalized) {
              migratedEntries.push({ key: normalized, alias: defaultAliasForKey(normalized) });
            }
          }
        }
      }
    }
  } catch {
    // ignore
  }

  return normalizeAndDedupeEntries(migratedEntries);
}

/** saveKeyEntries persists alias-aware entries and mirrors legacy history for compatibility. */
function saveKeyEntries(entries: ApiKeyEntry[]) {
  if (typeof window === 'undefined') return;
  try {
    const normalized = normalizeAndDedupeEntries(entries);
    window.localStorage.setItem(KEY_ENTRIES_STORAGE, JSON.stringify(normalized));
    window.localStorage.setItem(HISTORY_STORAGE, JSON.stringify(normalized.map((entry) => entry.key)));
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

function loadStatus(): ApiKeyStatus {
  if (typeof window === 'undefined') return 'none';
  return (window.localStorage.getItem(STATUS_STORAGE) as ApiKeyStatus) ?? 'none';
}

function saveStatus(status: ApiKeyStatus) {
  if (typeof window === 'undefined') return;
  window.localStorage.setItem(STATUS_STORAGE, status);
}

function loadQuota(): number | null {
  if (typeof window === 'undefined') return null;
  const raw = window.localStorage.getItem(QUOTA_STORAGE);
  return raw ? Number(raw) : null;
}

function saveQuota(quota: number | null) {
  if (typeof window === 'undefined') return;
  if (quota === null) {
    window.localStorage.removeItem(QUOTA_STORAGE);
  } else {
    window.localStorage.setItem(QUOTA_STORAGE, quota.toString());
  }
}

const VALIDATE_API_KEY_QUERY = `
  query ValidateApiKey($apiKey: String!) {
    ValidateOneapiApiKey(api_key: $apiKey) {
      remain_quota
      used_quota
    }
  }
`;

export function ApiKeyProvider({ children }: { children: ReactNode }) {
  const [apiKey, setApiKeyRaw] = useState<string>(() => loadCurrentKey());
  const [status, setStatus] = useState<ApiKeyStatus>(() => (apiKey ? loadStatus() : 'none'));
  const [remainQuota, setRemainQuota] = useState<number | null>(() => (apiKey ? loadQuota() : null));
  const [keyEntries, setKeyEntries] = useState<ApiKeyEntry[]>(() => loadKeyEntries());
  const [sessionId, setSessionId] = useState(0);

  // Persist current key when it changes
  useEffect(() => {
    saveCurrentKey(apiKey);
    if (!apiKey) {
      setStatus('none');
      setRemainQuota(null);
    }
  }, [apiKey]);

  useEffect(() => {
    saveStatus(status);
  }, [status]);

  useEffect(() => {
    saveQuota(remainQuota);
  }, [remainQuota]);

  // Persist key entries when they change
  useEffect(() => {
    saveKeyEntries(keyEntries);
  }, [keyEntries]);

  const validateApiKey = useCallback(
    async (keyToValidate?: string) => {
      const key = normalizeApiKey(keyToValidate ?? apiKey);
      if (!key) {
        setStatus('none');
        setRemainQuota(null);
        return;
      }

      setStatus('validating');
      try {
        const resp = await fetch('/query/', {
          method: 'POST',
          headers: {
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({
            query: VALIDATE_API_KEY_QUERY,
            variables: { apiKey: key },
          }),
        });

        if (!resp.ok) {
          setStatus('error');
          setRemainQuota(null);
          return;
        }

        const body = await resp.json();
        if (body.errors) {
          console.error('graphql errors', body.errors);
          setStatus('error');
          setRemainQuota(null);
          return;
        }

        const data = body.data?.ValidateOneapiApiKey;
        if (data) {
          const quota = data.remain_quota;
          setRemainQuota(quota);
          setStatus(quota > 250000 ? 'valid' : 'insufficient'); // greater than $0.5
        } else {
          setStatus('error');
          setRemainQuota(null);
        }
      } catch (err) {
        console.error('failed to validate api key', err);
        setStatus('error');
        setRemainQuota(null);
      }
    },
    [apiKey]
  );

  // Poll for quota if insufficient
  useEffect(() => {
    if (status !== 'insufficient' || !apiKey) {
      return;
    }

    const timer = setInterval(() => {
      validateApiKey();
    }, 10000); // 10s polling

    return () => clearInterval(timer);
  }, [status, apiKey, validateApiKey]);

  // Validate on mount if status is unknown or needs check
  useEffect(() => {
    if (apiKey && (status === 'none' || status === 'error')) {
      validateApiKey();
    }
    // Only on mount
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const setApiKey = useCallback(
    (key: string) => {
      const normalised = normalizeApiKey(key);
      if (normalised !== apiKey) {
        setSessionId((prev) => prev + 1);
      }
      setApiKeyRaw(normalised);

      if (normalised) {
        setKeyEntries((prev) => {
          const existing = prev.find((entry) => entry.key === normalised);
          const deduped = prev.filter((entry) => entry.key !== normalised);
          return [
            {
              key: normalised,
              alias: existing?.alias || defaultAliasForKey(normalised),
            },
            ...deduped,
          ].slice(0, MAX_HISTORY);
        });
        validateApiKey(normalised);
      } else {
        setStatus('none');
        setRemainQuota(null);
      }
    },
    [apiKey, validateApiKey]
  );

  const switchApiKey = useCallback(
    (key: string) => {
      setApiKey(key);
    },
    [setApiKey]
  );

  const setAliasForKey = useCallback((key: string, alias: string) => {
    const normalisedKey = normalizeApiKey(key);
    if (!normalisedKey) {
      return;
    }

    const normalisedAlias = alias.trim() || defaultAliasForKey(normalisedKey);
    setKeyEntries((prev) => {
      const targetIndex = prev.findIndex((entry) => entry.key === normalisedKey);
      if (targetIndex === -1) {
        return [{ key: normalisedKey, alias: normalisedAlias }, ...prev].slice(0, MAX_HISTORY);
      }

      const updated = [...prev];
      updated[targetIndex] = { ...updated[targetIndex], alias: normalisedAlias };
      return updated;
    });
  }, []);

  const removeFromHistory = useCallback((key: string) => {
    const normalised = normalizeApiKey(key);
    setKeyEntries((prev) => prev.filter((entry) => entry.key !== normalised));
  }, []);

  const disconnect = useCallback(() => {
    setApiKeyRaw('');
    setStatus('none');
    setRemainQuota(null);
    setSessionId((prev) => prev + 1);
  }, []);

  const history = useMemo(() => keyEntries.map((entry) => entry.key), [keyEntries]);

  const value = useMemo<ApiKeyContextValue>(
    () => ({
      apiKey,
      status,
      remainQuota,
      history,
      keyEntries,
      sessionId,
      setApiKey,
      switchApiKey,
      setAliasForKey,
      validateApiKey,
      removeFromHistory,
      disconnect,
    }),
    [
      apiKey,
      status,
      remainQuota,
      history,
      keyEntries,
      sessionId,
      setApiKey,
      switchApiKey,
      setAliasForKey,
      validateApiKey,
      removeFromHistory,
      disconnect,
    ]
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
