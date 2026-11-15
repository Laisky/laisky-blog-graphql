/* eslint-disable react-refresh/only-export-components */
import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';

import { readStorage, removeStorage, writeStorage } from './safe-storage';
import { createVisibilityPoller } from './visibility-poller';

const CURRENT_KEY_STORAGE = 'mcp_api_key';
const HISTORY_STORAGE = 'mcp_api_key_history';
const KEY_ENTRIES_STORAGE = 'mcp_api_key_entries';
// MAX_HISTORY caps the number of API keys preserved in local storage history.
const MAX_HISTORY = 10;

const BEARER_PREFIX = /^Bearer\s+/i;
const STATUS_STORAGE = 'mcp_api_key_status';
const QUOTA_STORAGE = 'mcp_api_key_quota';

export type ApiKeyStatus = 'none' | 'error' | 'insufficient' | 'valid' | 'validating';
const TOOL_CONSOLE_LOCKED_STATUSES = new Set<ApiKeyStatus>(['none', 'error', 'validating']);
// MIN_VALID_QUOTA is the remaining quota (0.5 USD) above which a key counts as valid rather than insufficient.
const MIN_VALID_QUOTA = 250000;
// QUOTA_RECHECK_INTERVAL_MS is the background re-check cadence while the quota is insufficient.
const QUOTA_RECHECK_INTERVAL_MS = 10000;

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

/**
 * isToolConsoleLockedStatus returns whether MCP tool consoles should be disabled for the given API key status.
 * It accepts the current API key validation status and returns true when interaction must be blocked.
 */
export function isToolConsoleLockedStatus(status: ApiKeyStatus): boolean {
  return TOOL_CONSOLE_LOCKED_STATUSES.has(status);
}

interface ApiKeyContextValue {
  /** The active API key (already normalised). */
  apiKey: string;
  /** Validation status. */
  status: ApiKeyStatus;
  /** Whether MCP tool console interactions should be disabled for the current API key status. */
  isToolConsoleLocked: boolean;
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
  try {
    const rawEntries = readStorage(KEY_ENTRIES_STORAGE);
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
    // Malformed JSON: fall back to the next source.
  }

  const migratedEntries: ApiKeyEntry[] = [];
  try {
    const current = loadCurrentKey();
    if (current) {
      migratedEntries.push({ key: current, alias: defaultAliasForKey(current) });
    }

    const rawHistory = readStorage(HISTORY_STORAGE);
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
    // Malformed JSON: fall back to the next source.
  }

  return normalizeAndDedupeEntries(migratedEntries);
}

/** saveKeyEntries persists alias-aware entries and mirrors legacy history for compatibility. */
function saveKeyEntries(entries: ApiKeyEntry[]) {
  const normalized = normalizeAndDedupeEntries(entries);
  writeStorage(KEY_ENTRIES_STORAGE, JSON.stringify(normalized));
  writeStorage(HISTORY_STORAGE, JSON.stringify(normalized.map((entry) => entry.key)));
}

/** loadCurrentKey reads the active key; blocked site data means no remembered key. */
function loadCurrentKey(): string {
  return normalizeApiKey(readStorage(CURRENT_KEY_STORAGE) ?? '');
}

/** saveCurrentKey remembers the active key, or forgets it when the key is empty. */
function saveCurrentKey(key: string) {
  const normalised = normalizeApiKey(key);
  if (normalised) {
    writeStorage(CURRENT_KEY_STORAGE, normalised);
  } else {
    removeStorage(CURRENT_KEY_STORAGE);
  }
}

/**
 * loadStatus restores the last validation verdict. Only settled verdicts are trusted:
 * "validating" describes a check that was still running when the page closed, and any
 * other value is unknown, so both load as "none", which revalidates the key on mount.
 */
function loadStatus(): ApiKeyStatus {
  const stored = readStorage(STATUS_STORAGE);
  return stored === 'valid' || stored === 'insufficient' || stored === 'error' ? stored : 'none';
}

/** saveStatus remembers the status, including "validating", so an interrupted check is redone. */
function saveStatus(status: ApiKeyStatus) {
  writeStorage(STATUS_STORAGE, status);
}

/** loadQuota restores the last remaining quota, ignoring a missing or non-numeric value. */
function loadQuota(): number | null {
  const stored = readStorage(QUOTA_STORAGE);
  if (stored === null || stored.trim() === '') return null;
  const quota = Number(stored);
  return Number.isFinite(quota) ? quota : null;
}

/** saveQuota remembers the remaining quota, or forgets it when unknown. */
function saveQuota(quota: number | null) {
  if (quota === null) {
    removeStorage(QUOTA_STORAGE);
  } else {
    writeStorage(QUOTA_STORAGE, quota.toString());
  }
}

/**
 * parseRemainQuota extracts the remaining quota from a ValidateOneapiApiKey response body.
 * It returns null when the body carries GraphQL errors, no data, or a non-numeric quota.
 */
function parseRemainQuota(body: unknown): number | null {
  if (!body || typeof body !== 'object') return null;
  const { data, errors } = body as { data?: { ValidateOneapiApiKey?: { remain_quota?: unknown } | null } | null; errors?: unknown };
  if (Array.isArray(errors) ? errors.length > 0 : Boolean(errors)) return null;
  const quota = data?.ValidateOneapiApiKey?.remain_quota;
  return typeof quota === 'number' && Number.isFinite(quota) ? quota : null;
}

/** ValidationOptions selects how a validation result may change the shared key status. */
interface ValidationOptions {
  /**
   * background marks a periodic re-check. It never shows "validating" and only applies a
   * successful quota reading; see runValidation.
   */
  background: boolean;
  /** signal cancels a background re-check that its poller no longer wants. */
  signal?: AbortSignal;
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
  const isToolConsoleLocked = isToolConsoleLockedStatus(status);
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

  // apiKeyRef is the active key. setApiKey and disconnect update it synchronously, so a
  // validation that settles before the next render already sees the switch.
  const apiKeyRef = useRef(apiKey);
  useEffect(() => {
    apiKeyRef.current = apiKey;
  }, [apiKey]);

  // validationRef identifies the newest explicit validation. Its generation advances
  // synchronously whenever a newer explicit validation starts, the key changes or the key
  // is disconnected; a result is applied only while its generation is still the current
  // one, and the superseded explicit request is aborted.
  const validationRef = useRef<{ generation: number; controller: AbortController | null }>({ generation: 0, controller: null });
  const supersedeValidations = useCallback((): number => {
    const current = validationRef.current;
    current.controller?.abort();
    current.controller = null;
    current.generation += 1;
    return current.generation;
  }, []);
  useEffect(() => () => void supersedeValidations(), [supersedeValidations]);

  /**
   * runValidation checks a key against the quota endpoint and records the verdict.
   *
   * An explicit validation reports every failure as "error". A background re-check is
   * upgrade-only: it applies a successful quota reading and otherwise keeps the last
   * known status, because none of its failures is a verdict on the key. Transport and
   * HTTP failures say nothing about the key; the resolver reports an unreachable or
   * failing balance endpoint as a GraphQL error over HTTP 200; and the upstream 401 it
   * relays also covers a valid key whose quota is exhausted, which is exactly what an
   * "insufficient" key is expected to reach. Turning any of these into "error" would lock
   * every tool console and stop the re-check that unlocks it after a top-up.
   *
   * Either kind of result is dropped once it is superseded: by a newer explicit
   * validation, a key switch or a disconnect (see validationRef), and for a background
   * re-check also by its poller's signal or a different active key.
   */
  const runValidation = useCallback(
    async (keyToValidate: string | undefined, { background, signal }: ValidationOptions) => {
      const key = normalizeApiKey(keyToValidate ?? apiKeyRef.current);
      if (!key) {
        if (!background) {
          supersedeValidations();
        }
        setStatus('none');
        setRemainQuota(null);
        return;
      }

      let generation = validationRef.current.generation;
      let requestSignal = signal;
      // A background re-check must not flip the status to "validating": that status locks every
      // tool console, which would disable inputs and drop keyboard focus on each poll tick.
      if (!background) {
        generation = supersedeValidations();
        const controller = new AbortController();
        validationRef.current.controller = controller;
        requestSignal = controller.signal;
        setStatus('validating');
      }
      const isCurrent = () =>
        generation === validationRef.current.generation &&
        !requestSignal?.aborted &&
        // Background results are only applied while the checked key is still the active one.
        (!background || apiKeyRef.current === key);
      const fail = (reason: string, detail?: unknown) => {
        if (background) {
          console.debug('background api key check kept the last status', reason, detail);
          return;
        }
        console.error('failed to validate api key', reason, detail);
        setStatus('error');
        setRemainQuota(null);
      };
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
          signal: requestSignal,
          ...(background ? { priority: 'low' as const } : {}),
        });
        if (!isCurrent()) {
          return;
        }
        if (!resp.ok) {
          fail(`http ${resp.status}`);
          return;
        }

        const body: unknown = await resp.json();
        if (!isCurrent()) {
          return;
        }
        const quota = parseRemainQuota(body);
        if (quota === null) {
          fail('no quota in response', (body as { errors?: unknown } | null)?.errors);
          return;
        }
        setRemainQuota(quota);
        setStatus(quota > MIN_VALID_QUOTA ? 'valid' : 'insufficient');
      } catch (err) {
        if (!isCurrent()) {
          return;
        }
        fail('request failed', err);
      }
    },
    [supersedeValidations]
  );

  const validateApiKey = useCallback((keyToValidate?: string) => runValidation(keyToValidate, { background: false }), [runValidation]);

  // Re-check the quota in the background while it is insufficient, so the console unlocks
  // once the balance is topped up. The shared poller never overlaps checks, pauses while the
  // tab is hidden and catches up once it is shown again.
  useEffect(() => {
    if (status !== 'insufficient' || !apiKey) {
      return;
    }

    const poller = createVisibilityPoller(async ({ signal }) => {
      await runValidation(undefined, { background: true, signal });
      return QUOTA_RECHECK_INTERVAL_MS;
    });
    poller.schedule(QUOTA_RECHECK_INTERVAL_MS);
    return () => poller.dispose();
  }, [status, apiKey, runValidation]);

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
      if (normalised !== apiKeyRef.current) {
        setSessionId((prev) => prev + 1);
        apiKeyRef.current = normalised;
        supersedeValidations();
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
    [supersedeValidations, validateApiKey]
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
    apiKeyRef.current = '';
    supersedeValidations();
    setApiKeyRaw('');
    setStatus('none');
    setRemainQuota(null);
    setSessionId((prev) => prev + 1);
  }, [supersedeValidations]);

  const history = useMemo(() => keyEntries.map((entry) => entry.key), [keyEntries]);

  const value = useMemo<ApiKeyContextValue>(
    () => ({
      apiKey,
      status,
      isToolConsoleLocked,
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
      isToolConsoleLocked,
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
