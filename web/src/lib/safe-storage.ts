/**
 * Web Storage access that never throws.
 *
 * Browsers can refuse site data in several places: reading the
 * `window.localStorage` / `window.sessionStorage` property itself throws a
 * SecurityError when cookies and site data are blocked, and `setItem` throws a
 * QuotaExceededError when storage is full. Preferences and caches stored here
 * are conveniences, so a refusal must degrade to "not remembered" instead of
 * breaking the page. Every helper resolves the storage area inside its own
 * try/catch for that reason.
 */

/** StorageArea names the Web Storage area to use. */
export type StorageArea = 'local' | 'session';

/** StorageLike is the subset of the Storage interface these helpers need. */
export type StorageLike = Pick<Storage, 'getItem' | 'setItem' | 'removeItem'>;

/**
 * resolveStorage returns the requested storage area, or null when the page has
 * no window or the browser refuses access to it.
 */
export function resolveStorage(area: StorageArea = 'local'): StorageLike | null {
  if (typeof window === 'undefined') {
    return null;
  }
  try {
    return area === 'session' ? window.sessionStorage : window.localStorage;
  } catch {
    return null;
  }
}

/** readStorage returns the stored string, or null when it is missing or storage is unavailable. */
export function readStorage(key: string, area: StorageArea = 'local'): string | null {
  try {
    return resolveStorage(area)?.getItem(key) ?? null;
  } catch {
    return null;
  }
}

/** writeStorage stores a string and reports whether the browser accepted it. */
export function writeStorage(key: string, value: string, area: StorageArea = 'local'): boolean {
  try {
    const storage = resolveStorage(area);
    if (!storage) {
      return false;
    }
    storage.setItem(key, value);
    return true;
  } catch {
    return false;
  }
}

/** removeStorage deletes a key and reports whether the browser accepted the removal. */
export function removeStorage(key: string, area: StorageArea = 'local'): boolean {
  try {
    const storage = resolveStorage(area);
    if (!storage) {
      return false;
    }
    storage.removeItem(key);
    return true;
  } catch {
    return false;
  }
}
