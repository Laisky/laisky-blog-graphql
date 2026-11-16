import { afterEach, describe, expect, it, vi } from 'vitest';

import { isStaleChunkError, preloadConsoleRoute, reloadForStaleChunk } from './route-modules';

vi.mock('@/features/mcp/settings/page', () => {
  throw new TypeError('Failed to fetch dynamically imported module: /assets/page-old.js');
});

/** memoryStorage is a minimal sessionStorage stand-in. */
function memoryStorage(initial: Record<string, string> = {}) {
  const data = new Map(Object.entries(initial));
  return {
    getItem: (key: string) => data.get(key) ?? null,
    setItem: (key: string, value: string) => void data.set(key, value),
  };
}

describe('isStaleChunkError', () => {
  it.each([
    'Failed to fetch dynamically imported module: https://mcp.laisky.com/assets/page-abc.js',
    'error loading dynamically imported module: https://mcp.laisky.com/assets/page-abc.js',
    'Importing a module script failed.',
    'Unable to preload CSS for /assets/page-abc.css',
  ])('recognizes the browser message %j', (message) => {
    expect(isStaleChunkError(new TypeError(message))).toBe(true);
  });

  it('ignores ordinary failures and non-errors', () => {
    expect(isStaleChunkError(new Error('Dashboard render failed'))).toBe(false);
    expect(isStaleChunkError('Failed to fetch dynamically imported module')).toBe(false);
    expect(isStaleChunkError(undefined)).toBe(false);
  });
});

describe('reloadForStaleChunk', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('reloads once per build and refuses to reload the same build again', () => {
    const storage = memoryStorage();
    const reload = vi.fn();

    expect(reloadForStaleChunk({ storage, build: 'build-a', reload, online: true })).toBe('reloading');
    expect(reload).toHaveBeenCalledTimes(1);

    expect(reloadForStaleChunk({ storage, build: 'build-a', reload, online: true })).toBe('unavailable');
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('reloads again after a later deploy changed the build', () => {
    const storage = memoryStorage();
    const reload = vi.fn();

    expect(reloadForStaleChunk({ storage, build: 'build-a', reload, online: true })).toBe('reloading');
    // The reload fetched build B, which later lost its own chunks to another deploy.
    expect(reloadForStaleChunk({ storage, build: 'build-b', reload, online: true })).toBe('reloading');
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it('stops a rolling deploy that alternates builds from reloading forever', () => {
    // Mixed replicas can hand out old and new index.html in turn while each lacks the other's chunks.
    const storage = memoryStorage();
    const reload = vi.fn();

    const outcomes = ['build-a', 'build-b', 'build-a', 'build-b'].map((build) => reloadForStaleChunk({ storage, build, reload, online: true }));
    expect(outcomes).toEqual(['reloading', 'reloading', 'unavailable', 'unavailable']);
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it('keeps the guard bounded to the most recent builds', () => {
    const storage = memoryStorage();
    const reload = vi.fn();
    for (let i = 0; i < 20; i += 1) {
      expect(reloadForStaleChunk({ storage, build: `build-${i}`, reload, online: true })).toBe('reloading');
    }
    const remembered = JSON.parse(storage.getItem('route-chunk-recovery') ?? '[]') as string[];
    expect(remembered).toHaveLength(8);
    expect(remembered.at(-1)).toBe('build-19');
    // The newest builds are still refused.
    expect(reloadForStaleChunk({ storage, build: 'build-19', reload, online: true })).toBe('unavailable');
  });

  it.each(['not json', '{"build":"a"}', '1700000000000', '[1,null]'])('treats the malformed guard %j as no prior attempt and repairs it', (raw) => {
    const storage = memoryStorage({ 'route-chunk-recovery': raw });
    const reload = vi.fn();

    expect(reloadForStaleChunk({ storage, build: 'build-a', reload, online: true })).toBe('reloading');
    expect(JSON.parse(storage.getItem('route-chunk-recovery') ?? '')).toEqual(['build-a']);
    expect(reloadForStaleChunk({ storage, build: 'build-a', reload, online: true })).toBe('unavailable');
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('does not reload when the guard cannot be stored', () => {
    const reload = vi.fn();
    const full = {
      getItem: () => null,
      setItem: () => {
        throw new DOMException('full', 'QuotaExceededError');
      },
    };

    expect(reloadForStaleChunk({ storage: full, build: 'build-a', reload, online: true })).toBe('unavailable');
    expect(reload).not.toHaveBeenCalled();
  });

  it('never reloads twice for the same build, however far apart the failures arrive', () => {
    // A failing chunk request on a slow network can take longer than any time window
    // to fail, so the guard must bound the number of reloads, not their frequency.
    vi.useFakeTimers({ toFake: ['Date'] });
    try {
      const storage = memoryStorage();
      const reload = vi.fn();
      vi.setSystemTime(1_000_000);
      expect(reloadForStaleChunk({ storage, reload, online: true })).toBe('reloading');
      for (const elapsed of [15_000, 60_000, 3_600_000]) {
        vi.setSystemTime(1_000_000 + elapsed);
        expect(reloadForStaleChunk({ storage, reload, online: true })).toBe('unavailable');
      }
      expect(reload).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it('does not reload when session storage is unavailable', () => {
    const reload = vi.fn();
    const blocked = {
      getItem: () => {
        throw new DOMException('blocked', 'SecurityError');
      },
      setItem: () => undefined,
    };

    expect(reloadForStaleChunk({ storage: blocked, build: 'build-a', reload, online: true })).toBe('unavailable');
    expect(reload).not.toHaveBeenCalled();
  });

  it('does not throw when reading window.sessionStorage itself is refused', () => {
    // Some privacy settings make the property getter throw, before getItem is ever reached.
    vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => {
      throw new DOMException('The operation is insecure.', 'SecurityError');
    });
    const reload = vi.fn();

    expect(() => reloadForStaleChunk({ reload, online: true })).not.toThrow();
    expect(reloadForStaleChunk({ reload, online: true })).toBe('unavailable');
    expect(reload).not.toHaveBeenCalled();
  });

  it('does not reload while the browser reports being offline, and leaves the guard unarmed', () => {
    const storage = memoryStorage();
    const reload = vi.fn();

    expect(reloadForStaleChunk({ storage, build: 'build-a', reload, online: false })).toBe('offline');
    expect(reload).not.toHaveBeenCalled();
    expect(storage.getItem('route-chunk-recovery')).toBeNull();

    // Back online, the same failure may still recover through one reload.
    expect(reloadForStaleChunk({ storage, build: 'build-a', reload, online: true })).toBe('reloading');
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('reads connectivity from navigator.onLine by default', () => {
    vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false);
    const reload = vi.fn();

    expect(reloadForStaleChunk({ storage: memoryStorage(), build: 'build-a', reload })).toBe('offline');
    expect(reload).not.toHaveBeenCalled();
  });
});

describe('preloadConsoleRoute', () => {
  it('ignores paths that are not code-split console pages', () => {
    expect(() => preloadConsoleRoute('/')).not.toThrow();
    expect(() => preloadConsoleRoute('/tools/unknown')).not.toThrow();
  });

  it('swallows a failed warm-up so it never surfaces as an unhandled rejection', async () => {
    preloadConsoleRoute('/settings');
    // Let the rejected import settle; vitest fails the run on an unhandled rejection.
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
});
