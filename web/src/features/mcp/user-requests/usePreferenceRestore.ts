import { useCallback, useEffect, useRef, useState } from 'react';

import { backoffDelay } from '@/lib/backoff';
import { HttpStatusError, isRetryableRequestFailure } from '@/lib/http-error';
import { createVisibilityPoller } from '@/lib/visibility-poller';

import { getPreferencesFromServer, type ReturnMode, type UserPreferencesResponse } from './api';

/** PREFERENCE_RETRY_BASE_MS is the wait after the first failed preference load. */
export const PREFERENCE_RETRY_BASE_MS = 2000;
/** PREFERENCE_RETRY_MAX_MS caps the exponential backoff between preference loads. */
export const PREFERENCE_RETRY_MAX_MS = 30_000;

/**
 * PreferenceRestoreStatus reports where restoring the server-saved preferences stands:
 * - 'idle': there is no usable API key (none configured, or the console is locked).
 * - 'loading': the first load for the current key has not answered yet.
 * - 'retrying': a load failed for a reason that may pass (a transport failure, a 5xx or a
 *   temporary 4xx such as 429) and another load is scheduled.
 * - 'failed': the server refused the load with a final status (401, 403, 404, …); nothing
 *   is retried on a timer until the user retries or the key changes.
 * - 'restored': the preferences for the current key were loaded and handed to onRestore.
 */
export type PreferenceRestoreStatus = 'idle' | 'loading' | 'retrying' | 'failed' | 'restored';

/** PreferenceRestore is what usePreferenceRestore reports to the page. */
export interface PreferenceRestore {
  status: PreferenceRestoreStatus;
  /** httpStatus is the HTTP status of the last refused load while 'retrying' or 'failed', if the server answered. */
  httpStatus?: number;
  /** retry loads the preferences again at once with a fresh backoff; it does nothing without a usable key. */
  retry: () => void;
}

/** preferenceRetryDelay returns the backoff before the next load after `failures` consecutive failures. */
export function preferenceRetryDelay(failures: number): number {
  return backoffDelay(failures, { baseMs: PREFERENCE_RETRY_BASE_MS, maxMs: PREFERENCE_RETRY_MAX_MS });
}

/**
 * describePreferenceRefusal explains a final refusal of the preference load in one
 * sentence, naming what the user can do about it.
 */
export function describePreferenceRefusal(httpStatus: number | undefined): string {
  switch (httpStatus) {
    case 401:
      return 'your API key was not accepted (HTTP 401). Check or switch the key, then retry.';
    case 403:
      return 'this API key is not allowed to read them (HTTP 403).';
    case undefined:
      return 'the server refused the request.';
    default:
      return `the server refused the request (HTTP ${httpStatus}).`;
  }
}

/**
 * resolveRestoredReturnMode decides which return mode the page shows once the server
 * preference arrives. A non-default choice remembered in this browser wins; the default
 * ('all') adopts a non-default server value. The page applies it only when the user has
 * not picked a mode since the load started, because that pick is newer than the read.
 */
export function resolveRestoredReturnMode(local: ReturnMode, server: ReturnMode | undefined): ReturnMode {
  return local === 'all' && server !== undefined && server !== 'all' ? server : local;
}

interface RestoreState {
  /** session is the API key the status belongs to, or null after the session ended. */
  session: string | null;
  /** attempt is the manual-retry generation the status belongs to. */
  attempt: number;
  status: PreferenceRestoreStatus;
  httpStatus?: number;
}

const endedSession: RestoreState = { session: null, attempt: -1, status: 'idle' };

/**
 * usePreferenceRestore loads the server-saved preferences for an API key once, on its own
 * lifecycle: a failure of any other request on the page cannot discard them. A load that
 * failed for a reason that may pass is retried with capped exponential backoff (paused
 * while the tab is hidden) until it succeeds; a final HTTP refusal stops the retries and
 * reports 'failed', because repeating it cannot succeed. retry() starts over at once in
 * either case. onRestore runs exactly once per successful load, and never for a key the
 * page has since switched away from or after the console was locked.
 */
export function usePreferenceRestore(
  apiKey: string | null | undefined,
  enabled: boolean,
  onRestore: (preferences: UserPreferencesResponse) => void
): PreferenceRestore {
  const session = enabled && apiKey ? apiKey : null;
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<RestoreState>(endedSession);
  const onRestoreRef = useRef(onRestore);
  useEffect(() => {
    onRestoreRef.current = onRestore;
  }, [onRestore]);
  const retry = useCallback(() => setAttempt((current) => current + 1), []);

  useEffect(() => {
    if (!session) {
      return;
    }

    let failures = 0;
    const poller = createVisibilityPoller(async ({ signal }) => {
      try {
        const preferences = await getPreferencesFromServer(session, signal);
        if (signal.aborted) return null;
        onRestoreRef.current(preferences);
        setState({ session, attempt, status: 'restored' });
        return null;
      } catch (error) {
        if (signal.aborted) return null;
        const httpStatus = error instanceof HttpStatusError ? error.status : undefined;
        if (!isRetryableRequestFailure(error)) {
          setState({ session, attempt, status: 'failed', httpStatus });
          return null;
        }
        failures += 1;
        setState({ session, attempt, status: 'retrying', httpStatus });
        return preferenceRetryDelay(failures);
      }
    });
    poller.start();

    return () => {
      poller.dispose();
      // Forget this session's status so a later session for the same key starts as 'loading'.
      setState(endedSession);
    };
  }, [session, attempt]);

  if (!session) {
    return { status: 'idle', retry };
  }
  // Until this session (and retry) reports, its preferences are still loading, whatever an
  // earlier session (another key, this key before a lock, or a superseded retry) reached.
  if (state.session !== session || state.attempt !== attempt) {
    return { status: 'loading', retry };
  }
  return { status: state.status, httpStatus: state.httpStatus, retry };
}
