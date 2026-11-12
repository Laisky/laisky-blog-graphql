import { describe, expect, it, vi } from 'vitest';

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
  it('reloads once and refuses to loop within the guard window', () => {
    const storage = memoryStorage();
    const reload = vi.fn();

    expect(reloadForStaleChunk(storage, 1_000_000, reload)).toBe(true);
    expect(reload).toHaveBeenCalledTimes(1);

    expect(reloadForStaleChunk(storage, 1_005_000, reload)).toBe(false);
    expect(reload).toHaveBeenCalledTimes(1);

    expect(reloadForStaleChunk(storage, 1_020_000, reload)).toBe(true);
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

    expect(reloadForStaleChunk(blocked, 1_000_000, reload)).toBe(false);
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
