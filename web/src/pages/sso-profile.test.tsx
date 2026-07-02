import { describe, expect, it } from 'vitest';

import { canSubmitPasskeyRename, canSubmitPasswordChange, canSubmitTotpCode, formatPasskeyCreatedAt } from './sso-profile';

describe('canSubmitPasswordChange', () => {
  it('requires both passwords', () => {
    expect(canSubmitPasswordChange('', 'new-password')).toBe(false);
    expect(canSubmitPasswordChange('old-password', '')).toBe(false);
  });

  it('requires a different new password', () => {
    expect(canSubmitPasswordChange('same-password', 'same-password')).toBe(false);
  });

  it('allows distinct non-empty passwords', () => {
    expect(canSubmitPasswordChange('old-password', 'new-password')).toBe(true);
  });
});

describe('canSubmitTotpCode', () => {
  it('requires six digits', () => {
    expect(canSubmitTotpCode('12345')).toBe(false);
    expect(canSubmitTotpCode('1234567')).toBe(false);
    expect(canSubmitTotpCode('abcdef')).toBe(false);
  });

  it('allows a six-digit code', () => {
    expect(canSubmitTotpCode('123456')).toBe(true);
  });
});

describe('canSubmitPasskeyRename', () => {
  it('requires a non-empty changed name', () => {
    expect(canSubmitPasskeyRename('Laptop', '')).toBe(false);
    expect(canSubmitPasskeyRename('Laptop', 'Laptop')).toBe(false);
    expect(canSubmitPasskeyRename('Laptop', '  Laptop  ')).toBe(false);
  });

  it('allows a changed name within the limit', () => {
    expect(canSubmitPasskeyRename('Laptop', 'Desktop')).toBe(true);
  });

  it('rejects names over one hundred characters', () => {
    expect(canSubmitPasskeyRename('Laptop', 'a'.repeat(101))).toBe(false);
  });
});

describe('formatPasskeyCreatedAt', () => {
  it('formats valid timestamps in UTC', () => {
    expect(formatPasskeyCreatedAt('2026-01-02T03:04:05.000Z')).toBe('2026-01-02 03:04 UTC');
  });

  it('returns invalid timestamps unchanged', () => {
    expect(formatPasskeyCreatedAt('not-a-date')).toBe('not-a-date');
  });
});
