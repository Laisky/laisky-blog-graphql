import { describe, expect, it, vi } from 'vitest';
import { buildRedirectUrlWithToken, buildSsoRedirectUrl, parseRedirectTarget } from './sso-redirect';

const variants = [
  'https://blog.laisky.com', 'https://blog.laisky.com/pages/0/',
  'http://blog.laisky.com', 'https://blog.laisky.com:444/',
  'https://user@blog.laisky.com/', 'https://blog.laisky.com./',
  'https://BLOG.LAISKY.COM/', 'https://blog.laisky.com:443/',
  'https://blog.laisky.com/?sso_token=old',
];

describe('ordered Blog issuer cutover', () => {
  it.each(variants)('rejects a reusable bearer for legacy Blog target %s', async (raw) => {
    const target = new URL(raw);
    expect(() => buildRedirectUrlWithToken(target, 'synthetic-test-bearer')).toThrow();
    await expect(buildSsoRedirectUrl(target, 'synthetic-test-bearer')).rejects.toThrow();
    expect(parseRedirectTarget(raw, 'https://sso.laisky.com').url).toBeNull();
  });

  it('retains another client using legacy state parameters', async () => {
    const target = new URL('http://console.laisky.com:8080/profile?sso_state=legacy#menu');
    const result = new URL(await buildSsoRedirectUrl(target, 'synthetic-test-bearer'));
    expect(result.searchParams.get('sso_token')).toBe('synthetic-test-bearer');
    expect(result.searchParams.get('sso_state')).toBe('legacy');
    expect(result.hash).toBe('#menu');
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
