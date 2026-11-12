import { describe, expect, it, vi } from 'vitest';
import { buildRedirectUrlWithToken, buildSsoRedirectUrl } from './sso-redirect';

const variants = [
  'https://blog.laisky.com', 'https://blog.laisky.com/pages/0/',
  'http://blog.laisky.com', 'https://blog.laisky.com:444/',
  'https://user@blog.laisky.com/', 'https://blog.laisky.com./',
  'https://BLOG.LAISKY.COM/', 'https://blog.laisky.com:443/',
  'https://blog.laisky.com/?sso_token=old',
];

describe('ordered Blog issuer cutover', () => {
  it.each(variants)('rejects the reusable-bearer builder for every Blog target %s', (raw) => {
    expect(() => buildRedirectUrlWithToken(new URL(raw), 'synthetic-test-bearer')).toThrow();
  });

  it('retains another client using legacy state parameters', async () => {
    // Plain http on a production host is refused by the shared redirect policy (issue 89);
    // the legacy handoff and its state parameters are otherwise unchanged.
    await expect(buildSsoRedirectUrl(new URL('http://console.laisky.com:8080/profile?sso_state=legacy#menu'), 'synthetic-test-bearer'))
      .rejects.toThrow('Insecure redirect target');
    const target = new URL('https://console.laisky.com:8080/profile?sso_state=legacy#menu');
    const result = new URL(await buildSsoRedirectUrl(target, 'synthetic-test-bearer'));
    expect(result.searchParams.get('sso_token')).toBe('synthetic-test-bearer');
    expect(result.searchParams.get('sso_state')).toBe('legacy');
    expect(result.hash).toBe('#menu');
  });

  it('keeps another client hash route unchanged because fragment marker checks are Blog-only', async () => {
    const target = new URL('https://console.laisky.com/#/settings?tab=sso_token');
    const result = new URL(await buildSsoRedirectUrl(target, 'synthetic-test-bearer'));
    expect(result.searchParams.get('sso_token')).toBe('synthetic-test-bearer');
    expect(result.hash).toBe('#/settings?tab=sso_token');
  });

  it('continues issuing a code for the exact registered callback', async () => {
    const code = 'A'.repeat(43);
    const target = new URL('https://blog.laisky.com/?sso_flow=code&sso_state=' + code
      + '&sso_challenge=' + code + '&sso_challenge_method=S256');
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ code, expires_in: 60 }),
      { headers: { 'Content-Type': 'application/json' } })));
    try {
      const result = new URL(await buildSsoRedirectUrl(target, 'synthetic-test-bearer'));
      expect(result.searchParams.get('sso_code')).toBe(code);
      expect(result.searchParams.has('sso_token')).toBe(false);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
