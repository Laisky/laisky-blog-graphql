import { useCallback, useEffect, useRef, useState } from 'react';

import { getQuota, type QuotaResponse } from './api';

/**
 * useQuota polls /api/quota on mount and after explicit refresh() calls,
 * returning the last-known usage plus the computed percent used.
 */
export function useQuota(apiKey: string | null) {
  const [quota, setQuota] = useState<QuotaResponse | null>(null);
  const [isLoading, setIsLoading] = useState(false);
  const [error, setError] = useState<Error | null>(null);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);

  const refresh = useCallback(async () => {
    if (!apiKey) {
      setQuota(null);
      return;
    }
    setIsLoading(true);
    try {
      const data = await getQuota(apiKey);
      if (mountedRef.current) {
        setQuota(data);
        setError(null);
      }
    } catch (err) {
      if (mountedRef.current) {
        setError(err instanceof Error ? err : new Error(String(err)));
      }
    } finally {
      if (mountedRef.current) {
        setIsLoading(false);
      }
    }
  }, [apiKey]);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const percent = quota && quota.quota_bytes > 0 ? Math.min(100, (quota.used_bytes / quota.quota_bytes) * 100) : 0;

  return { quota, percent, refresh, isLoading, error };
}
