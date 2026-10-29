import { afterEach, expect, it, vi } from 'vitest';
import { buildRedirectUrlWithToken, buildSsoRedirectUrl, parseRedirectTarget } from './sso-redirect';

afterEach(() => vi.unstubAllGlobals());

it.each([
  ['https://blog.laisky.com', '/'],
  ['https://blog.laisky.com/p/article/?view=compact#intro', '/p/article/?view=compact#intro'],
])('continues a legitimate legacy Blog bookmark %s without a bearer', async (raw, returnPath) => {
  const transport = vi.fn(() => { throw new Error('Network forbidden'); });
  vi.stubGlobal('fetch', transport);
  const parsed = parseRedirectTarget(raw, 'https://sso.laisky.com');
  expect(parsed.error).toBeUndefined();
  expect(parsed.url).not.toBeNull();
  const bootstrap = new URL(await buildSsoRedirectUrl(parsed.url!, 'synthetic-test-bearer'));
  expect(bootstrap.origin).toBe('https://blog.laisky.com');
  expect(bootstrap.pathname).toBe('/');
  expect([...bootstrap.searchParams.keys()]).toEqual(['sso_start', 'sso_return']);
  expect(bootstrap.searchParams.get('sso_start')).toBe('1');
  expect(bootstrap.searchParams.get('sso_return')).toBe(returnPath);
  expect(bootstrap.toString()).not.toContain('synthetic-test-bearer');
  expect(() => buildRedirectUrlWithToken(new URL(raw), 'synthetic-test-bearer')).toThrow();
  expect(transport).not.toHaveBeenCalled();
});

it.each([
  ['http://blog.laisky.com/p/article/', '/p/article/'],
  ['http://blog.laisky.com:80/', '/'],
  ['https://BLOG.LAISKY.COM/', '/'],
  ['https://blog.laisky.com:443/about/site/?view=compact#intro', '/about/site/?view=compact#intro'],
])('normalizes a credential-free legacy bookmark %s to canonical HTTPS', async (raw, returnPath) => {
  const transport = vi.fn();
  vi.stubGlobal('fetch', transport);
  const parsed = parseRedirectTarget(raw, 'https://sso.laisky.com');
  expect(parsed.error).toBeUndefined();
  const bootstrap = new URL(await buildSsoRedirectUrl(parsed.url!, 'synthetic-test-bearer'));
  expect(bootstrap.origin).toBe('https://blog.laisky.com');
  expect(bootstrap.searchParams.get('sso_return')).toBe(returnPath);
  expect(bootstrap.toString()).not.toContain('synthetic-test-bearer');
  expect(transport).not.toHaveBeenCalled();
});

it.each([
  'https://user@blog.laisky.com/',
  'https://blog.laisky.com:444/',
  'http://blog.laisky.com:8080/',
  'https://blog.laisky.com./',
  'https://blog.laisky.com/?sso_token=old',
  'https://blog.laisky.com/?sso_code=old',
  'https://blog.laisky.com/?code_verifier=old',
  'https://blog.laisky.com/?sso_verifier=old',
  'https://blog.laisky.com/?sso_start=1&sso_return=/',
  'https://blog.laisky.com/?sso_challenge=old',
  'https://blog.laisky.com/#sso_token=old',
  'https://blog.laisky.com/' + 'x'.repeat(2050),
])('rejects an unsafe legacy target %s without issuance or token fallback', async (raw) => {
  const transport = vi.fn();
  vi.stubGlobal('fetch', transport);
  expect(parseRedirectTarget(raw, 'https://sso.laisky.com').url).toBeNull();
  await expect(buildSsoRedirectUrl(new URL(raw), 'synthetic-test-bearer')).rejects.toThrow();
  expect(() => buildRedirectUrlWithToken(new URL(raw), 'synthetic-test-bearer')).toThrow();
  expect(transport).not.toHaveBeenCalled();
});

it.each([
  '//blog.laisky.com/',
  'https://blog.laisky.com/\\other.invalid/',
  'https://blog.laisky.com/\narticle',
])('rejects ambiguous raw legacy spelling %s before URL normalization', (raw) => {
  expect(parseRedirectTarget(raw, 'https://sso.laisky.com').url).toBeNull();
});

it.each([
  ['https://mcp.laisky.com', 'mcp'],
  ['https://sso.laisky.com', null],
  ['http://mcp.laisky.com', null],
  ['https://mcp.laisky.com:444', null],
  ['https://other.laisky.com', null],
])('selects MCP only from actual trusted origin %s', async (origin, issuer) => {
  vi.stubGlobal('window', { location: { origin } });
  const transport = vi.fn();
  vi.stubGlobal('fetch', transport);
  const bootstrap = new URL(await buildSsoRedirectUrl(new URL('https://blog.laisky.com/p/article/'), 'synthetic-test-bearer'));
  expect(bootstrap.searchParams.get('sso_issuer')).toBe(issuer);
  expect(bootstrap.searchParams.get('sso_return')).toBe('/p/article/');
  expect(transport).not.toHaveBeenCalled();
});

it.each([
  '?SSO_TOKEN=old', '?Sso_Code=old', '?SSO_STATE=old',
  '?SSO_CHALLENGE_METHOD=S256', '?CODE_VERIFIER=old', '?SSO_ISSUER=mcp',
  '#SSO_TOKEN=old', '#Sso_Return=%2F', '?sso_issuer=https%3A%2F%2Fother.invalid',
])('rejects a known protocol marker alias or issuer input %s', async (suffix) => {
  const raw = 'https://blog.laisky.com/' + suffix;
  const transport = vi.fn();
  vi.stubGlobal('fetch', transport);
  expect(parseRedirectTarget(raw, 'https://mcp.laisky.com').url).toBeNull();
  await expect(buildSsoRedirectUrl(new URL(raw), 'synthetic-test-bearer')).rejects.toThrow();
  expect(transport).not.toHaveBeenCalled();
});
