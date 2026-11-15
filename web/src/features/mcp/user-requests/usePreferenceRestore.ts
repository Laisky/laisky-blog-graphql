import { useEffect, useRef, useState } from 'react';

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
 * - 'retrying': a load failed and another is scheduled.
 * - 'restored': the preferences for the current key were loaded and handed to onRestore.
 */
export type PreferenceRestoreStatus = 'idle' | 'loading' | 'retrying' | 'restored';

/** preferenceRetryDelay returns the backoff before the next load after `failures` consecutive failures. */
export function preferenceRetryDelay(failures: number): number {
  return Math.min(PREFERENCE_RETRY_BASE_MS * 2 ** Math.max(0, failures - 1), PREFERENCE_RETRY_MAX_MS);
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
  status: PreferenceRestoreStatus;
}

const endedSession: RestoreState = { session: null, status: 'idle' };

/**
 * usePreferenceRestore loads the server-saved preferences for an API key once, on its own
 * lifecycle: a failure of any other request on the page cannot discard them, and a failed
 * load is retried with capped exponential backoff (paused while the tab is hidden) until it
 * succeeds. It calls onRestore exactly once per key with the loaded preferences, and never
 * for a key the page has since switched away from or after the console was locked.
 */
export function usePreferenceRestore(
  apiKey: string | null | undefined,
  enabled: boolean,
  onRestore: (preferences: UserPreferencesResponse) => void
): PreferenceRestoreStatus {
  const session = enabled && apiKey ? apiKey : null;
  const [state, setState] = useState<RestoreState>(endedSession);
  const onRestoreRef = useRef(onRestore);
  useEffect(() => {
    onRestoreRef.current = onRestore;
  }, [onRestore]);

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
        setState({ session, status: 'restored' });
        return null;
      } catch {
        if (signal.aborted) return null;
        failures += 1;
        setState({ session, status: 'retrying' });
        return preferenceRetryDelay(failures);
      }
    });
    poller.start();

    return () => {
      poller.dispose();
      // Forget this session's status so a later session for the same key starts as 'loading'.
      setState(endedSession);
    };
  }, [session]);

  if (!session) {
    return 'idle';
  }
  // Until this session reports, its preferences are still loading, whatever an earlier
  // session (another key, or this key before a lock) reached.
  return state.session === session ? state.status : 'loading';
}
