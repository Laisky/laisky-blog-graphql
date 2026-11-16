import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import * as api from './api';
import { HttpStatusError } from '@/lib/http-error';

import { describePreferenceRefusal, preferenceRetryDelay, resolveRestoredReturnMode, usePreferenceRestore } from './usePreferenceRestore';

vi.mock('./api', () => ({
  getPreferencesFromServer: vi.fn(),
}));

/** deferred returns a promise whose settlement the test controls. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

describe('resolveRestoredReturnMode', () => {
  it.each([
    // [local, server, shown] keeps the precedence the page used before restore had its own hook.
    ['all', 'first', 'first'],
    ['all', 'all', 'all'],
    ['all', undefined, 'all'],
    ['first', 'all', 'first'],
    ['first', 'first', 'first'],
    ['first', undefined, 'first'],
  ] as const)('local %j with server %j shows %j', (local, server, shown) => {
    expect(resolveRestoredReturnMode(local, server)).toBe(shown);
  });
});

describe('preferenceRetryDelay', () => {
  it('doubles from the base delay and stays capped', () => {
    expect([0, 1, 2, 3, 4, 5, 12].map(preferenceRetryDelay)).toEqual([2000, 2000, 4000, 8000, 16000, 30000, 30000]);
  });
});

describe('usePreferenceRestore', () => {
  const flush = () =>
    act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });

  beforeEach(() => {
    // Reset, not just clear: a queued mockResolvedValueOnce must not leak into the next test.
    vi.resetAllMocks();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('stays idle without a usable key and never requests preferences', () => {
    const onRestore = vi.fn();
    const { result, rerender } = renderHook(({ key, enabled }) => usePreferenceRestore(key, enabled, onRestore).status, {
      initialProps: { key: null as string | null, enabled: true },
    });
    expect(result.current).toBe('idle');

    rerender({ key: 'key-a', enabled: false });
    expect(result.current).toBe('idle');
    expect(api.getPreferencesFromServer).not.toHaveBeenCalled();
  });

  it('reports loading for a new key at once, even when the previous key was restored', async () => {
    const keyB = deferred<api.UserPreferencesResponse>();
    vi.mocked(api.getPreferencesFromServer).mockImplementation((key) => (key === 'key-a' ? Promise.resolve({ return_mode: 'all' }) : keyB.promise));
    const onRestore = vi.fn();
    const rendered: string[] = [];
    const { result, rerender } = renderHook(
      ({ key }) => {
        const { status } = usePreferenceRestore(key, true, onRestore);
        rendered.push(`${key}:${status}`);
        return status;
      },
      { initialProps: { key: 'key-a' } }
    );
    expect(result.current).toBe('loading');
    await flush();
    expect(result.current).toBe('restored');

    rendered.length = 0;
    rerender({ key: 'key-b' });
    // No render, including the one before effects run, may report key A's 'restored' for key B.
    expect(rendered.length).toBeGreaterThan(0);
    expect(rendered.every((entry) => entry === 'key-b:loading')).toBe(true);

    keyB.resolve({ return_mode: 'first' });
    await flush();
    expect(result.current).toBe('restored');
    expect(onRestore.mock.calls).toEqual([[{ return_mode: 'all' }], [{ return_mode: 'first' }]]);
  });

  it('restores again after the console unlocks instead of reusing the earlier status', async () => {
    const second = deferred<api.UserPreferencesResponse>();
    vi.mocked(api.getPreferencesFromServer).mockResolvedValueOnce({ return_mode: 'all' }).mockReturnValueOnce(second.promise);
    const onRestore = vi.fn();
    const rendered: string[] = [];
    const { result, rerender } = renderHook(
      ({ enabled }) => {
        const { status } = usePreferenceRestore('key-a', enabled, onRestore);
        rendered.push(status);
        return status;
      },
      { initialProps: { enabled: true } }
    );
    await flush();
    expect(result.current).toBe('restored');

    rerender({ enabled: false });
    expect(result.current).toBe('idle');
    rendered.length = 0;
    rerender({ enabled: true });
    // The same key after a lock is a new session: no render may reuse the earlier 'restored'.
    expect(rendered.length).toBeGreaterThan(0);
    expect(rendered.every((status) => status === 'loading')).toBe(true);

    second.resolve({ return_mode: 'first' });
    await flush();
    expect(result.current).toBe('restored');
    expect(onRestore).toHaveBeenCalledTimes(2);
  });

  it('aborts the in-flight load and never restores after unmount', async () => {
    const pending = deferred<api.UserPreferencesResponse>();
    vi.mocked(api.getPreferencesFromServer).mockReturnValue(pending.promise);
    const onRestore = vi.fn();
    const { unmount } = renderHook(() => usePreferenceRestore('key-a', true, onRestore));
    const signal = vi.mocked(api.getPreferencesFromServer).mock.calls[0][1];

    unmount();
    expect(signal?.aborted).toBe(true);
    pending.resolve({ return_mode: 'first' });
    await flush();
    expect(onRestore).not.toHaveBeenCalled();
  });

  it('stops on a final refusal, reports its status, and starts over on retry', async () => {
    vi.mocked(api.getPreferencesFromServer)
      .mockRejectedValueOnce(new HttpStatusError('forbidden', 403))
      .mockResolvedValueOnce({ return_mode: 'first' });
    const onRestore = vi.fn();
    const rendered: string[] = [];
    const { result } = renderHook(() => {
      const restore = usePreferenceRestore('key-a', true, onRestore);
      rendered.push(restore.status);
      return restore;
    });
    await flush();
    expect(result.current.status).toBe('failed');
    expect(result.current.httpStatus).toBe(403);

    await act(async () => {
      await vi.advanceTimersByTimeAsync(10 * 60_000);
    });
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(1);

    rendered.length = 0;
    act(() => result.current.retry());
    // The retry is a new attempt: no render may still report the refusal it replaces.
    expect(rendered.length).toBeGreaterThan(0);
    expect(rendered.every((status) => status === 'loading')).toBe(true);
    await flush();
    expect(result.current.status).toBe('restored');
    expect(result.current.httpStatus).toBeUndefined();
    expect(onRestore).toHaveBeenCalledExactlyOnceWith({ return_mode: 'first' });
  });

  it.each([
    ['a transport failure', new TypeError('Failed to fetch'), undefined],
    ['HTTP 429', new HttpStatusError('slow down', 429), 429],
    ['HTTP 503', new HttpStatusError('unavailable', 503), 503],
  ])('keeps retrying after %s and resets the backoff on a manual retry', async (_label, failure, httpStatus) => {
    vi.mocked(api.getPreferencesFromServer).mockRejectedValue(failure);
    const { result } = renderHook(() => usePreferenceRestore('key-a', true, vi.fn()));
    await flush();
    expect(result.current.status).toBe('retrying');
    expect(result.current.httpStatus).toBe(httpStatus);

    // Back off to the 8 s step (loads at 0, 2 and 6 s), then retry by hand.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(6000);
    });
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(3);
    act(() => result.current.retry());
    await flush();
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(4);
    // The manual retry restarted the backoff at its base delay.
    await act(async () => {
      await vi.advanceTimersByTimeAsync(2000);
    });
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(5);
  });
});

describe('describePreferenceRefusal', () => {
  it.each([
    [401, /api key was not accepted \(HTTP 401\)/i],
    [403, /not allowed to read them \(HTTP 403\)/],
    [404, /refused the request \(HTTP 404\)/],
    [undefined, /^the server refused the request\.$/],
  ])('describes HTTP %s', (status, expected) => {
    expect(describePreferenceRefusal(status)).toMatch(expected);
  });
});

