import { afterEach, describe, expect, it, vi } from 'vitest';
import { resolveAuthenticatedRedirect } from '@/pages/sso-session-redirect';
import protocol from './blog-sso-protocol.v1.json';
import {
  blogSsoProtocolMarkers,
  buildRedirectUrlWithToken,
  buildSsoRedirectUrl,
  InvalidBlogBookmarkError,
  parseRedirectTarget,
  planSsoRedirect,
} from './sso-redirect';

afterEach(() => vi.unstubAllGlobals());

const bookmarkError = 'Invalid Blog SSO bookmark. Restart sign-in from Blog.';
const entropy = 'A'.repeat(43);
const codeTarget = 'https://blog.laisky.com/?sso_flow=code&sso_state=' + entropy
  + '&sso_challenge=' + entropy + '&sso_challenge_method=S256';

// The v1 fixture is the cross-repository contract with the Blog consumer, which
// keeps a byte-identical copy and checks its own sanitizer against the same cases.
describe('Blog SSO protocol contract v1', () => {
  it('pins the fixture version and the exact reserved marker set', () => {
    expect(protocol.version).toBe(1);
    expect(new Set(blogSsoProtocolMarkers)).toEqual(new Set(protocol.markers));
    expect(blogSsoProtocolMarkers).toHaveLength(protocol.markers.length);
  });

  it.each(protocol.markers)('rejects marker %s in the query, fragment form and hash-route query', (marker) => {
    for (const suffix of ['?' + marker + '=x', '#' + marker + '=x', '#/route?' + marker.toUpperCase() + '=x']) {
      expect(parseRedirectTarget('https://blog.laisky.com/p/' + suffix, 'https://sso.laisky.com').url).toBeNull();
    }
  });

  it.each(protocol.cases.filter((item) => 'returnPath' in item))('bootstraps $target with its exact route', async (item) => {
    const transport = vi.fn();
    vi.stubGlobal('fetch', transport);
    const parsed = parseRedirectTarget(item.target, 'https://sso.laisky.com');
    expect(parsed.error).toBeUndefined();
    const bootstrap = new URL(await buildSsoRedirectUrl(parsed.url!, 'synthetic-test-bearer'));
    expect(bootstrap.origin + bootstrap.pathname).toBe('https://blog.laisky.com/');
    expect([...bootstrap.searchParams.keys()]).toEqual(['sso_start', 'sso_return']);
    expect(bootstrap.searchParams.get('sso_return')).toBe(item.returnPath);
    expect(bootstrap.toString()).not.toContain('synthetic-test-bearer');
    expect(transport).not.toHaveBeenCalled();
  });

  it.each(protocol.cases.filter((item) => 'reject' in item))('rejects $target ($reject) on every issuer path', async (item) => {
    const transport = vi.fn();
    vi.stubGlobal('fetch', transport);
    const parsed = parseRedirectTarget(item.target, 'https://sso.laisky.com');
    expect(parsed.url).toBeNull();
    expect(parsed.error).toBe(bookmarkError);
    expect(parsed.display).not.toContain('SYNTHETIC_CANARY');
    await expect(buildSsoRedirectUrl(new URL(item.target), 'synthetic-test-bearer')).rejects.toBeInstanceOf(InvalidBlogBookmarkError);
    expect(() => buildRedirectUrlWithToken(new URL(item.target), 'synthetic-test-bearer')).toThrow();
    expect(transport).not.toHaveBeenCalled();
  });
});

describe('typed redirect plans', () => {
  it('classifies each target into exactly one issuance path', () => {
    expect(planSsoRedirect(new URL(codeTarget))).toEqual({ kind: 'blog-code', binding: { state: entropy, challenge: entropy } });
    expect(planSsoRedirect(new URL('http://blog.laisky.com/p/a/?v=1#x'))).toEqual({ kind: 'blog-bootstrap', returnPath: '/p/a/?v=1#x' });
    expect(planSsoRedirect(new URL('https://app.laisky.com/cb?state=1'))).toEqual({ kind: 'token' });
    expect(() => planSsoRedirect(new URL('https://blog.laisky.com/?sso_flow=code'))).toThrow('Invalid Blog SSO code request.');
    expect(() => planSsoRedirect(new URL('https://blog.laisky.com/#/a?sso_token=x'))).toThrow(InvalidBlogBookmarkError);
  });

  it('distinguishes an invalid bookmark from an invalid code request without echoing either', () => {
    const bookmark = parseRedirectTarget('https://blog.laisky.com/?sso_token=SYNTHETIC_CANARY', 'https://sso.laisky.com');
    expect(bookmark).toEqual({ url: null, display: 'Invalid Blog SSO bookmark', error: bookmarkError });
    const code = parseRedirectTarget('https://blog.laisky.com/?sso_flow=code&sso_state=SYNTHETIC_CANARY', 'https://sso.laisky.com');
    expect(code).toEqual({ url: null, display: 'Invalid Blog SSO code request', error: 'Invalid Blog SSO code request.' });
  });

  it('routes a stored Blog session through the asynchronous handoff, never the bearer builder', () => {
    for (const raw of [codeTarget, 'https://blog.laisky.com/p/article/']) {
      const redirectTarget = parseRedirectTarget(raw, 'https://sso.laisky.com');
      expect(resolveAuthenticatedRedirect({ token: 'jwt', redirectTarget, redirectRequested: true }))
        .toEqual({ kind: 'handoff', target: redirectTarget.url });
    }
    const other = parseRedirectTarget('https://app.laisky.com/cb', 'https://sso.laisky.com');
    expect(resolveAuthenticatedRedirect({ token: 'jwt', redirectTarget: other, redirectRequested: true }))
      .toEqual({ kind: 'external', url: 'https://app.laisky.com/cb?sso_token=jwt' });
  });
});

describe('bootstrap length bounds', () => {
  // "/" expands threefold under form encoding, the worst case for a bounded route.
  const longest = '/a' + '/'.repeat(protocol.maxReturnPathLength - 2);

  it.each([
    ['https://sso.laisky.com', ['sso_start', 'sso_return']],
    ['https://mcp.laisky.com', ['sso_start', 'sso_return', 'sso_issuer']],
  ])('serializes the longest accepted route within the request-line bound on %s', async (origin, keys) => {
    vi.stubGlobal('window', { location: { origin } });
    vi.stubGlobal('fetch', vi.fn());
    expect(longest).toHaveLength(protocol.maxReturnPathLength);
    const serialized = await buildSsoRedirectUrl(new URL('https://blog.laisky.com' + longest), 'synthetic-test-bearer');
    expect(serialized.length).toBeGreaterThan(3 * (protocol.maxReturnPathLength - 2));
    expect(serialized.length).toBeLessThanOrEqual(8000);
    const bootstrap = new URL(serialized);
    expect([...bootstrap.searchParams.keys()]).toEqual(keys);
    expect(bootstrap.searchParams.get('sso_return')).toBe(longest);
  });

  it('rejects a route one character over the shared limit', async () => {
    const raw = 'https://blog.laisky.com' + longest + '/';
    expect(parseRedirectTarget(raw, 'https://sso.laisky.com').error).toBe(bookmarkError);
    await expect(buildSsoRedirectUrl(new URL(raw), 'synthetic-test-bearer')).rejects.toBeInstanceOf(InvalidBlogBookmarkError);
  });
});
