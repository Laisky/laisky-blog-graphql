import { describe, expect, it } from 'vitest';

import { buildRedirectUrlWithToken, canSubmitSsoLogin, isTurnstileEnabled, parseRedirectTarget } from './sso-login';

const origin = 'https://console.laisky.com';

describe('parseRedirectTarget', () => {
  it('returns error when redirect_to is missing', () => {
    const result = parseRedirectTarget(null, origin);
    expect(result.url).toBeNull();
    expect(result.error).toMatch(/missing redirect_to/i);
  });

  it('accepts absolute https URLs', () => {
    const result = parseRedirectTarget('https://app.laisky.com/callback?foo=bar', origin);
    expect(result.url?.toString()).toBe('https://app.laisky.com/callback?foo=bar');
    expect(result.error).toBeUndefined();
  });

  it('accepts relative paths', () => {
    const result = parseRedirectTarget('/sso/callback?state=abc', origin);
    expect(result.url?.toString()).toBe('https://console.laisky.com/sso/callback?state=abc');
  });

  it('accepts internal IPv4 addresses', () => {
    const result = parseRedirectTarget('http://10.20.30.40/sso/callback', origin);
    expect(result.url?.toString()).toBe('http://10.20.30.40/sso/callback');
    expect(result.error).toBeUndefined();
  });

  it('accepts carrier-grade NAT IPv4 addresses', () => {
    const result = parseRedirectTarget('http://100.75.198.70/sso/callback', origin);
    expect(result.url?.toString()).toBe('http://100.75.198.70/sso/callback');
    expect(result.error).toBeUndefined();
  });

  it('accepts internal IPv6 addresses', () => {
    const result = parseRedirectTarget('http://[fd00::1]/sso/callback', origin);
    expect(result.url?.toString()).toBe('http://[fd00::1]/sso/callback');
    expect(result.error).toBeUndefined();
  });

  it('rejects unsupported protocols', () => {
    const result = parseRedirectTarget('javascript:alert(1)', origin);
    expect(result.url).toBeNull();
    expect(result.error).toMatch(/unsupported redirect protocol/i);
  });

  it('rejects non-laisky domains', () => {
    const result = parseRedirectTarget('https://evil.com/callback', origin);
    expect(result.url).toBeNull();
    expect(result.error).toMatch(/unsupported redirect host/i);
  });

  it('rejects lookalike laisky domains', () => {
    const result = parseRedirectTarget('https://laisky.com.evil.com/callback', origin);
    expect(result.url).toBeNull();
    expect(result.error).toMatch(/unsupported redirect host/i);
  });
});

describe('buildRedirectUrlWithToken', () => {
  it('adds sso_token to the redirect URL', () => {
    const target = new URL('https://app.laisky.com/callback?foo=bar');
    const redirect = buildRedirectUrlWithToken(target, 'jwt-token');
    expect(redirect).toBe('https://app.laisky.com/callback?foo=bar&sso_token=jwt-token');
  });

  it('overwrites existing sso_token parameter', () => {
    const target = new URL('https://app.laisky.com/callback?sso_token=old');
    const redirect = buildRedirectUrlWithToken(target, 'new-token');
    expect(redirect).toBe('https://app.laisky.com/callback?sso_token=new-token');
  });
});

describe('isTurnstileEnabled', () => {
  it('returns false for empty values', () => {
    expect(isTurnstileEnabled(undefined)).toBe(false);
    expect(isTurnstileEnabled('')).toBe(false);
    expect(isTurnstileEnabled('   ')).toBe(false);
  });

  it('returns true for non-empty values', () => {
    expect(isTurnstileEnabled('turnstile-site-key')).toBe(true);
  });
});

describe('canSubmitSsoLogin', () => {
  it('requires redirect target and credentials', () => {
    expect(
      canSubmitSsoLogin({
        hasRedirectTarget: false,
        account: 'alice',
        password: 'secret',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('requires turnstile token when turnstile is enabled', () => {
    expect(
      canSubmitSsoLogin({
        hasRedirectTarget: true,
        account: 'alice',
        password: 'secret',
        isSubmitting: false,
        isTurnstileEnabled: true,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('allows submit when all fields are ready', () => {
    expect(
      canSubmitSsoLogin({
        hasRedirectTarget: true,
        account: 'alice',
        password: 'secret',
        isSubmitting: false,
        isTurnstileEnabled: true,
        turnstileToken: 'token',
      })
    ).toBe(true);
  });
});
