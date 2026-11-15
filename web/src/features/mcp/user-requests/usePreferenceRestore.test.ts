import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import * as api from './api';
import { preferenceRetryDelay, resolveRestoredReturnMode, usePreferenceRestore } from './usePreferenceRestore';

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
    vi.clearAllMocks();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('stays idle without a usable key and never requests preferences', () => {
    const onRestore = vi.fn();
    const { result, rerender } = renderHook(({ key, enabled }) => usePreferenceRestore(key, enabled, onRestore), {
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
        const status = usePreferenceRestore(key, true, onRestore);
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
        const status = usePreferenceRestore('key-a', enabled, onRestore);
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
});
