import { afterEach, describe, expect, it, vi } from 'vitest';

import { readStorage, removeStorage, resolveStorage, writeStorage } from './safe-storage';

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
