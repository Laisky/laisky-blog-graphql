import { afterEach, describe, expect, it, vi } from 'vitest';
import { buildRedirectUrlWithToken, buildSsoRedirectUrl, parseRedirectTarget } from './sso-redirect';

describe('Blog code handoff does not expose a reusable bearer', () => {
  it('refuses the legacy token URL builder for an opted-in Blog callback', () => {
    const target = new URL('https://blog.laisky.com/?sso_flow=code&sso_state=' + 'A'.repeat(43)
      + '&sso_challenge=' + ('B'.repeat(42) + 'A') + '&sso_challenge_method=S256');
    expect(() => buildRedirectUrlWithToken(target, 'synthetic-test-bearer')).toThrow();
  });
});

const state = 'A'.repeat(43);
const challenge = 'C'.repeat(42) + 'A';
const code = 'D'.repeat(42) + 'A';
const validTarget = () => new URL('https://blog.laisky.com/?sso_flow=code&sso_state=' + state
  + '&sso_challenge=' + challenge + '&sso_challenge_method=S256');

afterEach(() => vi.unstubAllGlobals());

describe('opt-in Blog code completion', () => {
  it('posts the bearer only to the same-origin issuer and navigates with a code', async () => {
    const transport = vi.fn().mockResolvedValue(new Response(JSON.stringify({ code, expires_in: 60 }), { headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', transport);
    const redirect = new URL(await buildSsoRedirectUrl(validTarget(), 'synthetic-test-bearer'));
    expect(redirect.origin).toBe('https://blog.laisky.com');
    expect([...redirect.searchParams.keys()]).toEqual(['sso_code', 'sso_state']);
    expect(redirect.searchParams.get('sso_code')).toBe(code);
    expect(redirect.searchParams.get('sso_state')).toBe(state);
    expect(redirect.toString()).not.toContain('synthetic-test-bearer');
    expect(transport).toHaveBeenCalledTimes(1);
    const [path, init] = transport.mock.calls[0];
    expect(path).toBe('/sso/code');
    expect(init).toMatchObject({
      method: 'POST', redirect: 'error', credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer',
      headers: { Authorization: 'Bearer synthetic-test-bearer' },
    });
    expect(JSON.parse(init.body)).toEqual({
      client_id: 'blog', redirect_uri: 'https://blog.laisky.com', state,
      code_challenge: challenge, code_challenge_method: 'S256',
    });
  });

  it.each([
    ['unknown flow', (url: URL) => url.searchParams.set('sso_flow', 'unknown')],
    ['wrong origin', (url: URL) => { url.hostname = 'other.laisky.com'; }],
    ['wrong path', (url: URL) => { url.pathname = '/profile'; }],
    ['userinfo', (url: URL) => { url.username = 'user'; }],
    ['non-default port', (url: URL) => { url.port = '444'; }],
    ['hash', (url: URL) => { url.hash = 'fragment'; }],
    ['query extras', (url: URL) => url.searchParams.set('extra', 'value')],
    ['duplicate', (url: URL) => url.searchParams.append('sso_state', state)],
    ['missing', (url: URL) => url.searchParams.delete('sso_challenge')],
    ['plain PKCE', (url: URL) => url.searchParams.set('sso_challenge_method', 'plain')],
    ['malformed entropy', (url: URL) => url.searchParams.set('sso_state', 'short')],
    ['noncanonical entropy', (url: URL) => url.searchParams.set('sso_state', 'B'.repeat(43))],
  ] as const)('rejects %s without legacy fallback or network access', async (_name, change) => {
    const transport = vi.fn();
    vi.stubGlobal('fetch', transport);
    const target = validTarget();
    change(target);
    await expect(buildSsoRedirectUrl(target, 'synthetic-test-bearer')).rejects.toThrow('Invalid Blog SSO code request');
    expect(() => buildRedirectUrlWithToken(target, 'synthetic-test-bearer')).toThrow();
    expect(parseRedirectTarget(target.toString(), 'https://sso.laisky.com').url).toBeNull();
    expect(transport).not.toHaveBeenCalled();
  });

  it('retains legacy clients without a code request', async () => {
    const transport = vi.fn();
    vi.stubGlobal('fetch', transport);
    const target = new URL('https://console.laisky.com/profile?existing=1#menu');
    const url = new URL(await buildSsoRedirectUrl(target, 'synthetic-test-bearer'));
    expect(url.searchParams.get('sso_token')).toBe('synthetic-test-bearer');
    expect(url.searchParams.get('existing')).toBe('1');
    expect(url.hash).toBe('#menu');
    expect(transport).not.toHaveBeenCalled();
  });

  it('fails closed when code issuance fails or returns malformed code', async () => {
    const transport = vi.fn().mockResolvedValueOnce(new Response('{}', { status: 503 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ code: 'short' }), { headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', transport);
    await expect(buildSsoRedirectUrl(validTarget(), 'synthetic-test-bearer')).rejects.toThrow('Unable to complete');
    await expect(buildSsoRedirectUrl(validTarget(), 'synthetic-test-bearer')).rejects.toThrow('Invalid Blog SSO code response');
  });
});

it('rejects explicit default ports in the raw opt-in callback', () => {
  const raw = validTarget().toString().replace('blog.laisky.com/', 'blog.laisky.com:443/');
  expect(parseRedirectTarget(raw, 'https://sso.laisky.com').url).toBeNull();
});
it.each([
  [JSON.stringify({ code, expires_in: 61 }), 'application/json'],
  [JSON.stringify({ code, expires_in: 0 }), 'application/json'],
  [JSON.stringify({ code, expires_in: 60 }), 'text/html'],
  ['x'.repeat(8193), 'application/json'],
  ['invalid json', 'application/json'],
])('rejects malformed, oversized or non-JSON responses', async (body, contentType) => {
  vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(body, { headers: { 'Content-Type': contentType } })));
  await expect(buildSsoRedirectUrl(validTarget(), 'synthetic-test-bearer')).rejects.toThrow('Invalid Blog SSO code response');
});

it('preserves another legacy client using sso_state without explicit code opt-in', async () => {
  const transport = vi.fn();
  vi.stubGlobal('fetch', transport);
  const target = new URL('https://console.laisky.com/profile?sso_state=legacy-state&sso_challenge=legacy-custom');
  const redirect = new URL(await buildSsoRedirectUrl(target, 'synthetic-test-bearer'));
  expect(redirect.searchParams.get('sso_state')).toBe('legacy-state');
  expect(redirect.searchParams.get('sso_challenge')).toBe('legacy-custom');
  expect(redirect.searchParams.get('sso_token')).toBe('synthetic-test-bearer');
  expect(transport).not.toHaveBeenCalled();
});
