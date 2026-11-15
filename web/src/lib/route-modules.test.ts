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

  it('reloads once and refuses to loop within the guard window', () => {
    const storage = memoryStorage();
    const reload = vi.fn();

    expect(reloadForStaleChunk({ storage, now: 1_000_000, reload, online: true })).toBe('reloading');
    expect(reload).toHaveBeenCalledTimes(1);

    expect(reloadForStaleChunk({ storage, now: 1_005_000, reload, online: true })).toBe('unavailable');
    expect(reload).toHaveBeenCalledTimes(1);

    expect(reloadForStaleChunk({ storage, now: 1_020_000, reload, online: true })).toBe('reloading');
    expect(reload).toHaveBeenCalledTimes(2);
  });

  it('does not reload when session storage is unavailable', () => {
    const reload = vi.fn();
    const blocked = {
      getItem: () => {
        throw new DOMException('blocked', 'SecurityError');
      },
      setItem: () => undefined,
    };

    expect(reloadForStaleChunk({ storage: blocked, now: 1_000_000, reload, online: true })).toBe('unavailable');
    expect(reload).not.toHaveBeenCalled();
  });

  it('does not throw when reading window.sessionStorage itself is refused', () => {
    // Some privacy settings make the property getter throw, before getItem is ever reached.
    vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(() => {
      throw new DOMException('The operation is insecure.', 'SecurityError');
    });
    const reload = vi.fn();

    expect(() => reloadForStaleChunk({ now: 1_000_000, reload, online: true })).not.toThrow();
    expect(reloadForStaleChunk({ now: 1_000_000, reload, online: true })).toBe('unavailable');
    expect(reload).not.toHaveBeenCalled();
  });

  it('does not reload while the browser reports being offline, and leaves the guard unarmed', () => {
    const storage = memoryStorage();
    const reload = vi.fn();

    expect(reloadForStaleChunk({ storage, now: 1_000_000, reload, online: false })).toBe('offline');
    expect(reload).not.toHaveBeenCalled();
    expect(storage.getItem('route-chunk-reload-at')).toBeNull();

    // Back online, the same failure may still recover through one reload.
    expect(reloadForStaleChunk({ storage, now: 1_001_000, reload, online: true })).toBe('reloading');
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('reads connectivity from navigator.onLine by default', () => {
    vi.spyOn(navigator, 'onLine', 'get').mockReturnValue(false);
    const reload = vi.fn();

    expect(reloadForStaleChunk({ storage: memoryStorage(), now: 1_000_000, reload })).toBe('offline');
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
