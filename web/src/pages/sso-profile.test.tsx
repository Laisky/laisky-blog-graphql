import { describe, expect, it } from 'vitest';

import { canSubmitPasswordChange, canSubmitTotpCode } from './sso-profile';

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
