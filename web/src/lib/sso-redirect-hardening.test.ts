import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resolveAuthenticatedRedirect } from '@/pages/sso-session-redirect';
import {
  buildRedirectUrlWithToken,
  buildSsoRedirectUrl,
  configureSsoRedirectPolicy,
  parseRedirectTarget,
} from './sso-redirect';

// configure applies a runtime SSO redirect policy exactly as bootstrap does.
function configure(config: { ssoRedirect?: unknown } | null): void {
  configureSsoRedirectPolicy(config);
}

const issuer = 'https://sso.laisky.com';
const bearer = 'synthetic-test-bearer';
const state = 'A'.repeat(43);
const challenge = 'C'.repeat(42) + 'A';
const code = 'D'.repeat(42) + 'A';
const accountingCallback = 'https://accounting.laisky.com/api/auth/sso/callback';
const accountingCodeRequest = accountingCallback + '?sso_flow=code&sso_client_id=accounting&sso_state=' + state
  + '&sso_challenge=' + challenge + '&sso_challenge_method=S256';
const registry = { legacy_token_redirect: 'allow', clients: [{ client_id: 'accounting', redirect_uris: [accountingCallback] }] };
const originalLocation = window.location;

beforeEach(() => {
  configure({ ssoRedirect: { legacy_token_redirect: 'allow', clients: [] } });
  Object.defineProperty(window, 'location', { configurable: true, value: { origin: issuer, pathname: '/', assign: vi.fn() } });
});

afterEach(() => {
  vi.unstubAllGlobals();
  configure({ ssoRedirect: { legacy_token_redirect: 'allow', clients: [] } });
  Object.defineProperty(window, 'location', { configurable: true, value: originalLocation });
});

// expectNoBearer asserts that neither the parser nor any builder can hand the bearer to raw.
async function expectNoBearer(raw: string) {
  const transport = vi.fn();
  vi.stubGlobal('fetch', transport);
  expect(parseRedirectTarget(raw, issuer).url).toBeNull();
  const target = new URL(raw);
  await expect(buildSsoRedirectUrl(target, bearer)).rejects.toThrow();
  expect(() => buildRedirectUrlWithToken(target, bearer)).toThrow();
  expect(transport).not.toHaveBeenCalled();
}

describe('redirect transport policy for the legacy bearer handoff', () => {
  it.each([
    'http://cv.laisky.com/cv',
    'http://chat.laisky.com/gptchat?state=abc',
    'http://laisky.com/',
    'http://accounting.laisky.com:8080/api/auth/sso/callback',
  ])('refuses plain http on the production host %s', async (raw) => {
    await expectNoBearer(raw);
  });

  it.each([
    'https://user@cv.laisky.com/cv',
    'https://user:pass@cv.laisky.com/cv',
  ])('refuses userinfo in %s', async (raw) => {
    await expectNoBearer(raw);
  });

  it.each([
    'https://cv.laisky.com\\@evil.example/',
    'https://cv.laisky.com/\tcv',
    'http://0x7f.1/callback',
  ])('refuses the parser-differential spelling %j', (raw) => {
    expect(parseRedirectTarget(raw, issuer).url).toBeNull();
  });

  it.each([
    'http://localhost:5173/cb',
    'http://127.0.0.1:8080/cb',
    'http://100.75.198.70:5173/gptchat',
    'http://10.20.30.40/cb',
    'http://[fd00::1]/cb',
  ])('keeps the development-only http host %s on the legacy handoff', async (raw) => {
    const parsed = parseRedirectTarget(raw, issuer);
    expect(parsed.error).toBeUndefined();
    const result = new URL(await buildSsoRedirectUrl(parsed.url!, bearer));
    expect(result.searchParams.get('sso_token')).toBe(bearer);
  });

  it('keeps an https legacy target unchanged under the default allow mode', async () => {
    const parsed = parseRedirectTarget('https://chat.laisky.com/gptchat?state=abc#top', issuer);
    expect(parsed.error).toBeUndefined();
    expect(await buildSsoRedirectUrl(parsed.url!, bearer)).toBe('https://chat.laisky.com/gptchat?state=abc&sso_token=' + bearer + '#top');
  });
});

describe('issuer-origin targets', () => {
  it('navigates to an issuer page without a URL bearer because the session is already stored', async () => {
    const result = await buildSsoRedirectUrl(new URL(issuer + '/profile'), bearer);
    expect(result).toBe(issuer + '/profile');
    expect(result).not.toContain(bearer);
  });
});

describe('registered SSO clients', () => {
  it('issues a single-use code for the exact registered callback and never the bearer', async () => {
    configure({ ssoRedirect: registry });
    const transport = vi.fn().mockResolvedValue(new Response(JSON.stringify({ code, expires_in: 60 }),
      { headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', transport);
    const parsed = parseRedirectTarget(accountingCodeRequest, issuer);
    expect(parsed.error).toBeUndefined();
    const result = new URL(await buildSsoRedirectUrl(parsed.url!, bearer));
    expect(result.origin + result.pathname).toBe(accountingCallback);
    expect([...result.searchParams.keys()]).toEqual(['sso_code', 'sso_state']);
    expect(result.searchParams.get('sso_code')).toBe(code);
    expect(result.searchParams.get('sso_state')).toBe(state);
    expect(result.toString()).not.toContain(bearer);
    expect(result.hash).toBe('');
    expect(transport).toHaveBeenCalledTimes(1);
    const [path, init] = transport.mock.calls[0];
    expect(path).toBe('/sso/code');
    expect(init).toMatchObject({ method: 'POST', redirect: 'error', credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer' });
    expect(JSON.parse(init.body)).toEqual({
      client_id: 'accounting', redirect_uri: accountingCallback, state, code_challenge: challenge, code_challenge_method: 'S256',
    });
  });

  it.each([
    accountingCallback + '?state=legacy',
    'https://accounting.laisky.com/',
    'https://ACCOUNTING.laisky.com/other',
  ])('refuses the legacy bearer for the registered hostname in %s', async (raw) => {
    configure({ ssoRedirect: registry });
    await expectNoBearer(raw);
  });

  it.each([
    ['another path', accountingCodeRequest.replace('/callback?', '/other?')],
    ['unknown client', accountingCodeRequest.replace('sso_client_id=accounting', 'sso_client_id=unknown')],
    ['plain PKCE', accountingCodeRequest.replace('=S256', '=plain')],
    ['extra parameter', accountingCodeRequest + '&extra=1'],
    ['fragment', accountingCodeRequest + '#x'],
  ])('rejects a malformed registered code request (%s) without fallback', async (_name, raw) => {
    configure({ ssoRedirect: registry });
    await expectNoBearer(raw);
  });
});

describe('legacy bearer cutover switch', () => {
  it('refuses an unregistered target on every frontend path when the issuer denies the legacy handoff', async () => {
    configure({ ssoRedirect: { legacy_token_redirect: 'deny', clients: [] } });
    await expectNoBearer('https://cv.laisky.com/cv');
    const redirectTarget = parseRedirectTarget('https://cv.laisky.com/cv', issuer);
    expect(resolveAuthenticatedRedirect({ token: bearer, redirectTarget, redirectRequested: true })).toEqual({ kind: 'none' });
  });

  it('fails closed when the runtime configuration could not be loaded', async () => {
    configure(null);
    await expectNoBearer('https://cv.laisky.com/cv');
  });

  it('keeps Blog code, Blog bookmark and issuer-origin targets working under deny', async () => {
    configure({ ssoRedirect: { legacy_token_redirect: 'deny', clients: [] } });
    const transport = vi.fn().mockResolvedValue(new Response(JSON.stringify({ code, expires_in: 60 }),
      { headers: { 'Content-Type': 'application/json' } }));
    vi.stubGlobal('fetch', transport);
    const blogCode = parseRedirectTarget('https://blog.laisky.com/?sso_flow=code&sso_state=' + state
      + '&sso_challenge=' + challenge + '&sso_challenge_method=S256', issuer);
    expect(new URL(await buildSsoRedirectUrl(blogCode.url!, bearer)).searchParams.get('sso_code')).toBe(code);
    const bookmark = parseRedirectTarget('https://blog.laisky.com/p/article/', issuer);
    expect(new URL(await buildSsoRedirectUrl(bookmark.url!, bearer)).searchParams.get('sso_return')).toBe('/p/article/');
    expect(await buildSsoRedirectUrl(new URL(issuer + '/profile'), bearer)).toBe(issuer + '/profile');
  });
});
