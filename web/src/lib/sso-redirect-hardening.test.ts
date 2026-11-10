import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { resolveAuthenticatedRedirect } from '@/pages/sso-session-redirect';
import {
  buildRedirectUrlWithToken,
  buildSsoRedirectUrl,
  configureSsoRedirectPolicy,
  parseRedirectTarget,
} from './sso-redirect';
import { parseSsoRedirectPolicy } from './sso-redirect-policy';

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
    'http://[::1]:8080/cb',
  ])('keeps the loopback http host %s on the legacy handoff', async (raw) => {
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

// Review follow-ups for PR 93: development targets, shared hostnames, backslashes in the
// query or fragment, and the bearer builder for issuer pages.
describe('review follow-ups', () => {
  function useIssuerOrigin(origin: string) {
    Object.defineProperty(window, 'location', { configurable: true, value: { origin, pathname: '/', assign: vi.fn() } });
  }

  it.each([
    'http://192.168.1.66/x',
    'https://10.0.0.5/x',
    'http://100.64.0.10:5173/gptchat',
    'http://172.16.4.2/cb',
    'http://[fd00::1]/cb',
  ])('a production issuer never hands the bearer to the unregistered private target %s', async (raw) => {
    await expectNoBearer(raw);
  });

  it.each(['http://localhost:5173/cb', 'http://127.0.0.1:8080/cb', 'http://[::1]:8080/cb'])(
    'a production issuer keeps the loopback target %s on the legacy handoff', async (raw) => {
      expect(new URL(await buildSsoRedirectUrl(parseRedirectTarget(raw, issuer).url!, bearer)).searchParams.get('sso_token')).toBe(bearer);
    });

  it('a development issuer keeps private development targets on the legacy handoff', async () => {
    useIssuerOrigin('http://192.168.1.10:5173');
    const parsed = parseRedirectTarget('http://192.168.1.66/x', 'http://192.168.1.10:5173');
    expect(parsed.error).toBeUndefined();
    expect(new URL(await buildSsoRedirectUrl(parsed.url!, bearer)).searchParams.get('sso_token')).toBe(bearer);
  });

  it('a production issuer hands the bearer only to the exact registered development callback', async () => {
    configure({ ssoRedirect: { legacy_token_redirect: 'allow', clients: [
      { client_id: 'devapp', redirect_uris: ['http://192.168.1.66:8080/sso/callback'], legacy_token_redirect: 'allow' },
    ] } });
    const parsed = parseRedirectTarget('http://192.168.1.66:8080/sso/callback?state=x', issuer);
    expect(parsed.error).toBeUndefined();
    expect(new URL(await buildSsoRedirectUrl(parsed.url!, bearer)).searchParams.get('sso_token')).toBe(bearer);
    await expectNoBearer('http://192.168.1.66:8080/other');
  });

  it('refuses a registry in which two clients share one hostname', () => {
    expect(parseSsoRedirectPolicy({ legacy_token_redirect: 'allow', clients: [
      { client_id: 'first', redirect_uris: ['https://shared.laisky.com/a'] },
      { client_id: 'second', redirect_uris: ['https://shared.laisky.com/b'] },
    ] })).toBeNull();
  });

  it('keeps backslashes that browsers leave in the query and fragment of location.href', async () => {
    const parsed = parseRedirectTarget('https://chat.laisky.com/p?q=a\\b#f\\g', issuer);
    expect(parsed.error).toBeUndefined();
    const result = new URL(await buildSsoRedirectUrl(parsed.url!, bearer));
    expect(result.searchParams.get('sso_token')).toBe(bearer);
    expect(result.hash).toBe('#f\\g');
    await expectNoBearer('https://evil.example\\@sso.laisky.com/');
    expect(parseRedirectTarget('https://chat.laisky.com/a\\b?x=1', issuer).url).toBeNull();
  });

  it('never builds a bearer URL for an issuer page', () => {
    expect(() => buildRedirectUrlWithToken(new URL(issuer + '/profile'), bearer)).toThrow();
  });
});
