import { describe, expect, it } from 'vitest';

import { buildRedirectUrlWithToken, parseRedirectTarget } from './sso-login';

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

  it('rejects unsupported protocols', () => {
    const result = parseRedirectTarget('javascript:alert(1)', origin);
    expect(result.url).toBeNull();
    expect(result.error).toMatch(/unsupported redirect protocol/i);
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
