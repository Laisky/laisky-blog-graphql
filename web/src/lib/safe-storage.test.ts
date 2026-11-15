import { afterEach, describe, expect, it, vi } from 'vitest';

import { readStorage, removeStorage, resolveStorage, subscribeStorageKey, writeStorage } from './safe-storage';

const refused = () => {
  throw new DOMException('The operation is insecure.', 'SecurityError');
};

describe('safe-storage', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    localStorage.clear();
    sessionStorage.clear();
  });

  it('round-trips values in both storage areas', () => {
    expect(writeStorage('k', 'local-value')).toBe(true);
    expect(writeStorage('k', 'session-value', 'session')).toBe(true);
    expect(readStorage('k')).toBe('local-value');
    expect(readStorage('k', 'session')).toBe('session-value');
    expect(removeStorage('k')).toBe(true);
    expect(readStorage('k')).toBeNull();
    expect(readStorage('k', 'session')).toBe('session-value');
  });

  it('degrades to "not remembered" when the storage property getter itself throws', () => {
    vi.spyOn(window, 'localStorage', 'get').mockImplementation(refused);
    vi.spyOn(window, 'sessionStorage', 'get').mockImplementation(refused);

    expect(resolveStorage()).toBeNull();
    expect(resolveStorage('session')).toBeNull();
    expect(readStorage('k')).toBeNull();
    expect(writeStorage('k', 'v')).toBe(false);
    expect(removeStorage('k', 'session')).toBe(false);
  });

  it('degrades when individual operations throw, such as a full quota', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(refused);
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('quota', 'QuotaExceededError');
    });
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(refused);

    expect(readStorage('k')).toBeNull();
    expect(writeStorage('k', 'v')).toBe(false);
    expect(removeStorage('k')).toBe(false);
  });
});

describe('subscribeStorageKey', () => {
  afterEach(() => {
    vi.restoreAllMocks();
    localStorage.clear();
    sessionStorage.clear();
  });

  /** fire dispatches a storage event the way another document's write would. */
  const fire = (init: StorageEventInit) => window.dispatchEvent(new StorageEvent('storage', init));

  it('reports the stored value rather than the event payload', () => {
    const onChange = vi.fn();
    const unsubscribe = subscribeStorageKey('k', onChange);
    localStorage.setItem('k', 'newest');

    // An event for an older write, dispatched after a newer one already landed.
    fire({ key: 'k', newValue: 'older', storageArea: localStorage });

    expect(onChange).toHaveBeenCalledExactlyOnceWith('newest');
    unsubscribe();
  });

  it('re-reads on a clear of either area and ignores unrelated keys', () => {
    const onChange = vi.fn();
    const unsubscribe = subscribeStorageKey('k', onChange);
    localStorage.setItem('k', 'kept');

    fire({ key: 'other', newValue: 'x', storageArea: localStorage });
    expect(onChange).not.toHaveBeenCalled();

    // A same-tab frame clearing sessionStorage leaves the localStorage value in place.
    fire({ key: null, storageArea: sessionStorage });
    expect(onChange).toHaveBeenLastCalledWith('kept');

    localStorage.clear();
    fire({ key: null, storageArea: localStorage });
    expect(onChange).toHaveBeenLastCalledWith(null);
    unsubscribe();
  });

  it('reads the requested area', () => {
    const onChange = vi.fn();
    const unsubscribe = subscribeStorageKey('k', onChange, 'session');
    localStorage.setItem('k', 'local-value');
    sessionStorage.setItem('k', 'session-value');

    fire({ key: 'k', newValue: 'session-value', storageArea: sessionStorage });

    expect(onChange).toHaveBeenCalledExactlyOnceWith('session-value');
    unsubscribe();
  });

  it('reports null instead of throwing when storage is refused', () => {
    vi.spyOn(window, 'localStorage', 'get').mockImplementation(refused);
    const onChange = vi.fn();
    const unsubscribe = subscribeStorageKey('k', onChange);

    expect(() => fire({ key: 'k', newValue: 'v' })).not.toThrow();
    expect(onChange).toHaveBeenCalledExactlyOnceWith(null);
    unsubscribe();
  });

  it('stops notifying once unsubscribed', () => {
    const onChange = vi.fn();
    subscribeStorageKey('k', onChange)();

    fire({ key: 'k', newValue: 'v', storageArea: localStorage });

    expect(onChange).not.toHaveBeenCalled();
  });
});
