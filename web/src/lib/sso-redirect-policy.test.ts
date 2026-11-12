import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import vectors from './sso-redirect-policy.v1.json';
import { configureSsoRedirectPolicy, parseRedirectTarget, planSsoRedirect } from './sso-redirect';
import {
  classifySsoRedirect,
  formatSsoRedirectDecision,
  parseSsoRedirectPolicy,
  validateRedirectUri,
  type SsoRedirectPolicy,
} from './sso-redirect-policy';

const modes = ['allow', 'deny'] as const;
const originalLocation = window.location;

// vectorPolicy parses the fixture registry through the production runtime-config parser.
function vectorPolicy(mode: (typeof modes)[number]): SsoRedirectPolicy {
  const policy = parseSsoRedirectPolicy({ legacy_token_redirect: mode, clients: vectors.clients });
  expect(policy).not.toBeNull();
  return policy!;
}

const cases = vectors.cases.map((item) => ({
  ...item, raw: item.target + 'a'.repeat(item.pad ?? 0), issuer: item.issuer ?? vectors.issuerOrigin,
}));

function useIssuerOrigin(origin: string) {
  Object.defineProperty(window, 'location', { configurable: true, value: { origin, pathname: '/' } });
}

beforeEach(() => useIssuerOrigin(vectors.issuerOrigin));

afterEach(() => {
  configureSsoRedirectPolicy({ ssoRedirect: undefined });
  Object.defineProperty(window, 'location', { configurable: true, value: originalLocation });
});

// The v1 vectors are consumed unchanged by the Go policy test (internal/library/ssoredirect),
// so the frontend and backend enforcement points cannot drift.
describe('shared SSO redirect policy vectors v1', () => {
  it('pins the fixture version', () => {
    expect(vectors.version).toBe(1);
    expect(cases.length).toBeGreaterThan(50);
  });

  for (const mode of modes) {
    it.each(cases)(`classifies %j under ${mode}`, (item) => {
      expect(formatSsoRedirectDecision(classifySsoRedirect(item.raw, item.issuer, vectorPolicy(mode))))
        .toBe(mode === 'allow' ? item.allow : item.deny);
    });

    it.each(cases)(`enforces %j identically on the issuer entry points under ${mode}`, (item) => {
      configureSsoRedirectPolicy({ ssoRedirect: { legacy_token_redirect: mode, clients: vectors.clients } });
      useIssuerOrigin(item.issuer);
      const expected = mode === 'allow' ? item.allow : item.deny;
      const parsed = parseRedirectTarget(item.raw, item.issuer);
      if (expected === 'blog') {
        // Blog targets follow blog-sso-protocol.v1.json and never take the bearer path.
        if (parsed.url) expect(planSsoRedirect(parsed.url).kind).toMatch(/^blog-/);
        return;
      }
      if (expected.startsWith('reject:')) {
        expect(parsed.url).toBeNull();
        expect(parsed.error).toBeTruthy();
        return;
      }
      expect(parsed.error).toBeUndefined();
      const kind = planSsoRedirect(parsed.url!).kind;
      expect({ legacy: 'token', self: 'self' }[expected] ?? 'client-code').toBe(kind);
    });
  }

  it.each(vectors.registrations)('validates the registration %j', (item) => {
    expect(validateRedirectUri(item.uri) === null).toBe(item.valid);
  });

  it.each(vectors.registries)('validates the registry $note', (item) => {
    expect(parseSsoRedirectPolicy({ legacy_token_redirect: 'allow', clients: item.clients }) !== null).toBe(item.valid);
  });
});

describe('runtime policy parsing', () => {
  it('rejects malformed registries so the frontend fails closed', () => {
    for (const value of [
      null, 'allow', { legacy_token_redirect: 'maybe', clients: [] }, { legacy_token_redirect: 'allow' },
      { legacy_token_redirect: 'allow', clients: [{ client_id: 'blog', redirect_uris: ['https://x.laisky.com/cb'] }] },
      { legacy_token_redirect: 'allow', clients: [{ client_id: 'x1', redirect_uris: ['http://x.laisky.com/cb'] }] },
      { legacy_token_redirect: 'allow', clients: [{ client_id: 'x1', redirect_uris: [] }] },
      { legacy_token_redirect: 'allow', clients: [{ client_id: 'x1', redirect_uris: ['https://x.laisky.com/cb'], legacy_token_redirect: 'x' }] },
    ]) {
      expect(parseSsoRedirectPolicy(value)).toBeNull();
    }
    expect(parseSsoRedirectPolicy({ legacy_token_redirect: 'deny', clients: [{ client_id: 'x1', redirect_uris: ['https://x.laisky.com/cb'] }] }))
      .toEqual({ legacy_token_redirect: 'deny', clients: [{ client_id: 'x1', redirect_uris: ['https://x.laisky.com/cb'], legacy_token_redirect: 'deny' }] });
  });

  it('keeps the legacy default only when the issuer predates the policy', () => {
    configureSsoRedirectPolicy({});
    expect(parseRedirectTarget('https://chat.laisky.com/', vectors.issuerOrigin).url).not.toBeNull();
    configureSsoRedirectPolicy(null);
    expect(parseRedirectTarget('https://chat.laisky.com/', vectors.issuerOrigin).error).toMatch(/could not be loaded/);
    configureSsoRedirectPolicy({ ssoRedirect: { legacy_token_redirect: 'deny', clients: [] } });
    expect(parseRedirectTarget('https://chat.laisky.com/', vectors.issuerOrigin).error).toMatch(/not registered for SSO/);
  });
});
