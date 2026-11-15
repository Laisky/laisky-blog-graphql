import { describe, expect, it } from 'vitest';

import { describeSsoAuthError, EMAIL_CODE_REJECTED_MESSAGE } from './sso-login-state';

describe('describeSsoAuthError', () => {
  it('explains an outage instead of echoing the machine token', () => {
    expect(describeSsoAuthError('login_unavailable')).toBe('Sign-in is temporarily unavailable. Please try again in a moment.');
  });

  it('suggests a new code when registration rejects the email code', () => {
    // A code is refused after a few attempts even when it is right, so the user
    // must be told that requesting a new one is the way forward.
    expect(describeSsoAuthError('invalid email verification code')).toBe(EMAIL_CODE_REJECTED_MESSAGE);
    expect(describeSsoAuthError('register user: Invalid Email Verification Code')).toBe(EMAIL_CODE_REJECTED_MESSAGE);
  });

  it('explains a masked email-code login rejection only for the email-code form', () => {
    expect(describeSsoAuthError('invalid credentials', { emailCode: true })).toBe(EMAIL_CODE_REJECTED_MESSAGE);
    expect(describeSsoAuthError('invalid credentials')).toBe('invalid credentials');
    expect(describeSsoAuthError('invalid credentials', { emailCode: false })).toBe('invalid credentials');
  });

  it('passes other messages through', () => {
    expect(describeSsoAuthError('account already exists', { emailCode: true })).toBe('account already exists');
  });
});
