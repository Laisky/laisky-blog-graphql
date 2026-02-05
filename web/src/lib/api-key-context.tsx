import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';

const CURRENT_KEY_STORAGE = 'mcp_api_key';
const HISTORY_STORAGE = 'mcp_api_key_history';
// MAX_HISTORY caps the number of API keys preserved in local storage history.
const MAX_HISTORY = 10;

const BEARER_PREFIX = /^Bearer\s+/i;
const STATUS_STORAGE = 'mcp_api_key_status';
const QUOTA_STORAGE = 'mcp_api_key_quota';

export type ApiKeyStatus = 'none' | 'error' | 'insufficient' | 'valid' | 'validating';

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
  /** Set the current API key. Also pushes it to history. */
  setApiKey: (key: string) => void;
  /** Validate the current or a new API key. */
  validateApiKey: (key?: string) => Promise<void>;
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
  const [history, setHistory] = useState<string[]>(() => loadHistory());

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

  // Persist history when it changes
  useEffect(() => {
    saveHistory(history);
  }, [history]);

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
      setApiKeyRaw(normalised);

      if (normalised) {
        setHistory((prev) => {
          const deduped = prev.filter((k) => k !== normalised);
          return [normalised, ...deduped].slice(0, MAX_HISTORY);
        });
        validateApiKey(normalised);
      } else {
        setStatus('none');
        setRemainQuota(null);
      }
    },
    [validateApiKey]
  );

  const removeFromHistory = useCallback((key: string) => {
    const normalised = normalizeApiKey(key);
    setHistory((prev) => prev.filter((k) => k !== normalised));
  }, []);

  const disconnect = useCallback(() => {
    setApiKeyRaw('');
    setStatus('none');
    setRemainQuota(null);
  }, []);

  const value = useMemo<ApiKeyContextValue>(
    () => ({ apiKey, status, remainQuota, history, setApiKey, validateApiKey, removeFromHistory, disconnect }),
    [apiKey, status, remainQuota, history, setApiKey, validateApiKey, removeFromHistory, disconnect]
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
