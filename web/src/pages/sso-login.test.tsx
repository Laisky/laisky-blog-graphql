import '@testing-library/jest-dom/vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchGraphQL } from '@/lib/graphql';
import { loadSsoToken, storeSsoToken } from '@/lib/sso-session';

import {
  buildRedirectUrlWithToken,
  canStartGithubOAuth,
  canSubmitSsoLogin,
  isTotpRequiredError,
  isTurnstileEnabled,
  isTurnstileRequiredError,
  parseRedirectTarget,
  resolveAuthenticatedRedirect,
  SsoLoginPage,
} from './sso-login';
import { formatSsoJwtSchema } from './sso-token-schema';

// ThemeToggle depends on ThemeProvider context; stub it so the page renders standalone.
vi.mock('@/components/theme/theme-toggle', () => ({
  ThemeToggle: () => <div>Theme toggle</div>,
}));

// The session-check effect verifies a stored token via GraphQL; mock the transport
// so tests can drive valid/invalid sessions without a backend.
vi.mock('@/lib/graphql', () => ({
  fetchGraphQL: vi.fn(),
}));

// Capture navigations so profile redirects can be asserted without a real router.
const mockNavigate = vi.fn();
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => mockNavigate };
});

const origin = 'https://console.laisky.com';

// jsdom's window.location.assign is a non-configurable no-op, so swap the whole
// location object for a stub whose assign can be asserted, then restore it.
const assignMock = vi.fn();
const originalLocation = window.location;

beforeEach(() => {
  window.localStorage.clear();
  mockNavigate.mockReset();
  assignMock.mockReset();
  vi.mocked(fetchGraphQL).mockReset();
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: {
      origin: 'https://sso.laisky.com',
      href: 'https://sso.laisky.com/sso/login',
      pathname: '/sso/login',
      search: '',
      hash: '',
      assign: assignMock,
      replace: vi.fn(),
    },
  });
});

afterEach(() => {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: originalLocation,
  });
});

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
  it('blocks sign-in when a requested redirect target is invalid', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'login',
        hasRedirectTarget: false,
        redirectRequested: true,
        account: 'alice',
        password: 'secret',
        displayName: '',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('allows direct profile sign-in when no redirect target is requested', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'login',
        hasRedirectTarget: false,
        redirectRequested: false,
        account: 'alice',
        password: 'secret',
        displayName: '',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(true);
  });

  it('requires turnstile token when turnstile is enabled', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'login',
        hasRedirectTarget: true,
        account: 'alice',
        password: 'secret',
        displayName: '',
        isSubmitting: false,
        isTurnstileEnabled: true,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('allows submit when all fields are ready', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'login',
        hasRedirectTarget: true,
        account: 'alice',
        password: 'secret',
        displayName: '',
        isSubmitting: false,
        isTurnstileEnabled: true,
        turnstileToken: 'token',
      })
    ).toBe(true);
  });

  it('allows registration without redirect target when required fields are ready', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'register',
        hasRedirectTarget: false,
        account: 'alice@example.com',
        password: 'secret',
        displayName: 'Alice',
        emailCode: '123456',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(true);
  });

  it('requires display name for registration', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'register',
        hasRedirectTarget: false,
        account: 'alice@example.com',
        password: 'secret',
        displayName: '',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('requires email code for registration', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'register',
        hasRedirectTarget: false,
        account: 'alice@example.com',
        password: 'secret',
        displayName: 'Alice',
        emailCode: '   ',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('allows email-code login without a password', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'login',
        loginMethod: 'email_code',
        hasRedirectTarget: true,
        account: 'alice@example.com',
        password: '',
        displayName: '',
        emailCode: '123456',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(true);
  });

  it('requires email code for email-code login', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'login',
        loginMethod: 'email_code',
        hasRedirectTarget: true,
        account: 'alice@example.com',
        password: '',
        displayName: '',
        emailCode: '',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('blocks submit while a TOTP code is required but empty', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'login',
        hasRedirectTarget: true,
        account: 'alice',
        password: 'secret',
        displayName: '',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
        totpRequired: true,
        totpCode: '   ',
      })
    ).toBe(false);
  });

  it('allows submit once the required TOTP code is entered', () => {
    expect(
      canSubmitSsoLogin({
        mode: 'login',
        hasRedirectTarget: true,
        account: 'alice',
        password: 'secret',
        displayName: '',
        isSubmitting: false,
        isTurnstileEnabled: false,
        turnstileToken: '',
        totpRequired: true,
        totpCode: '123456',
      })
    ).toBe(true);
  });
});

describe('isTotpRequiredError', () => {
  it('detects the backend totp_required signal', () => {
    expect(isTotpRequiredError('totp_required')).toBe(true);
    expect(isTotpRequiredError('  TOTP_REQUIRED  ')).toBe(true);
    expect(isTotpRequiredError('graphql: totp_required')).toBe(true);
  });

  it('ignores unrelated login errors', () => {
    expect(isTotpRequiredError('invalid credentials')).toBe(false);
    expect(isTotpRequiredError('login failed')).toBe(false);
    expect(isTotpRequiredError('')).toBe(false);
  });
});

describe('isTurnstileRequiredError', () => {
  it('detects the backend turnstile_required signal', () => {
    expect(isTurnstileRequiredError('turnstile_required')).toBe(true);
    expect(isTurnstileRequiredError('  TURNSTILE_REQUIRED  ')).toBe(true);
    expect(isTurnstileRequiredError('login: turnstile_required')).toBe(true);
  });

  it('ignores unrelated auth errors', () => {
    expect(isTurnstileRequiredError('invalid credentials')).toBe(false);
    expect(isTurnstileRequiredError('totp_required')).toBe(false);
    expect(isTurnstileRequiredError('')).toBe(false);
  });
});

describe('canSubmitSsoLogin turnstile gating', () => {
  const baseState = {
    mode: 'login' as const,
    hasRedirectTarget: true,
    redirectRequested: true,
    account: 'alice',
    password: 'secret',
    displayName: '',
    isSubmitting: false,
  };

  it('does not require a turnstile token until a challenge is active', () => {
    expect(
      canSubmitSsoLogin({
        ...baseState,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(true);
  });

  it('blocks submit while an active challenge has no token', () => {
    expect(
      canSubmitSsoLogin({
        ...baseState,
        isTurnstileEnabled: true,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('allows submit once the active challenge is solved', () => {
    expect(
      canSubmitSsoLogin({
        ...baseState,
        isTurnstileEnabled: true,
        turnstileToken: 'token',
      })
    ).toBe(true);
  });
});

describe('canStartGithubOAuth', () => {
  it('requires turnstile token when turnstile is enabled', () => {
    expect(
      canStartGithubOAuth({
        isSubmitting: false,
        isTurnstileEnabled: true,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('disables while a request is already submitting', () => {
    expect(
      canStartGithubOAuth({
        isSubmitting: true,
        isTurnstileEnabled: false,
        turnstileToken: '',
      })
    ).toBe(false);
  });

  it('allows start when security requirements are ready', () => {
    expect(
      canStartGithubOAuth({
        isSubmitting: false,
        isTurnstileEnabled: true,
        turnstileToken: 'turnstile-token',
      })
    ).toBe(true);
  });
});

describe('SsoLoginPage GitHub option visibility', () => {
  const renderLoginPage = (githubOAuthEnabled: boolean | undefined) =>
    render(
      <MemoryRouter initialEntries={['/sso/login']}>
        <SsoLoginPage githubOAuthEnabled={githubOAuthEnabled} />
      </MemoryRouter>
    );

  const githubButton = () => screen.queryByRole('button', { name: /continue with github/i });

  it('hides the GitHub button when GitHub OAuth is not configured', () => {
    renderLoginPage(false);
    expect(githubButton()).not.toBeInTheDocument();
    // Passkey and password sign-in stay available regardless of GitHub config.
    expect(screen.getByRole('button', { name: /sign in with passkey/i })).toBeInTheDocument();
  });

  it('hides the GitHub button when the flag is omitted', () => {
    renderLoginPage(undefined);
    expect(githubButton()).not.toBeInTheDocument();
  });

  it('shows the GitHub button when GitHub OAuth is configured', () => {
    renderLoginPage(true);
    expect(githubButton()).toBeInTheDocument();
  });
});

describe('SsoLoginPage token details', () => {
  const ssoJwt = {
    algorithm: 'EdDSA',
    type: 'JWT',
    issuer: 'laisky-sso',
    ttl_seconds: 7_776_000,
    public_key_pem: '-----BEGIN PUBLIC KEY-----\npublic-key\n-----END PUBLIC KEY-----\n',
    claims_schema: {
      type: 'object',
      required: ['iss', 'sub', 'uid'],
    },
  };

  it('formats missing and present claims schemas', () => {
    expect(formatSsoJwtSchema(undefined)).toBe('{}');
    expect(formatSsoJwtSchema(ssoJwt.claims_schema)).toContain('"required": [');
  });

  it('links to the standalone token details page from the login page', () => {
    render(
      <MemoryRouter initialEntries={['/sso/login']}>
        <SsoLoginPage />
      </MemoryRouter>
    );

    const tokenLink = screen.getByRole('link', { name: /token details/i });
    expect(tokenLink).toHaveAttribute('href', '/sso/token');
  });
});

describe('resolveAuthenticatedRedirect', () => {
  it('sends a signed-in visitor to the requested target with the token attached', () => {
    const redirectTarget = parseRedirectTarget('https://app.laisky.com/cb?foo=bar', origin);
    expect(resolveAuthenticatedRedirect({ token: 'jwt', redirectTarget, redirectRequested: true })).toEqual({
      kind: 'external',
      url: 'https://app.laisky.com/cb?foo=bar&sso_token=jwt',
    });
  });

  it('sends a signed-in visitor to their profile when no redirect was requested', () => {
    const redirectTarget = parseRedirectTarget(null, origin);
    expect(resolveAuthenticatedRedirect({ token: 'jwt', redirectTarget, redirectRequested: false })).toEqual({
      kind: 'profile',
    });
  });

  it('does not auto-redirect when a requested redirect target is invalid', () => {
    const redirectTarget = parseRedirectTarget('https://evil.com/cb', origin);
    expect(resolveAuthenticatedRedirect({ token: 'jwt', redirectTarget, redirectRequested: true })).toEqual({
      kind: 'none',
    });
  });
});

describe('SsoLoginPage session detection', () => {
  const renderAt = (entry: string) =>
    render(
      <MemoryRouter initialEntries={[entry]}>
        <SsoLoginPage />
      </MemoryRouter>
    );

  const loginButton = () => screen.findByRole('button', { name: /^login$/i });

  it('shows the login form without probing the backend when no session is stored', async () => {
    renderAt('/sso/login');
    expect(await loginButton()).toBeInTheDocument();
    expect(fetchGraphQL).not.toHaveBeenCalled();
  });

  it('clears an invalid stored token and reveals the login form', async () => {
    storeSsoToken('stale-token');
    vi.mocked(fetchGraphQL).mockRejectedValue(new Error('unauthorized'));

    renderAt('/sso/login');

    expect(await loginButton()).toBeInTheDocument();
    expect(loadSsoToken()).toBe('');
    expect(mockNavigate).not.toHaveBeenCalled();
    expect(assignMock).not.toHaveBeenCalled();
  });

  it('redirects a signed-in visitor to their profile when no redirect was requested', async () => {
    storeSsoToken('valid-jwt');
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserProfile: { account: 'alice' } });

    renderAt('/sso/login');

    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/sso/profile'));
    expect(assignMock).not.toHaveBeenCalled();
    // The valid session is preserved, not cleared.
    expect(loadSsoToken()).toBe('valid-jwt');
  });

  it('redirects a signed-in visitor to the requested target with the token attached', async () => {
    storeSsoToken('valid-jwt');
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserProfile: { account: 'alice' } });

    renderAt('/sso/login?redirect_to=https://app.laisky.com/cb');

    await waitFor(() => expect(assignMock).toHaveBeenCalledWith('https://app.laisky.com/cb?sso_token=valid-jwt'));
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('keeps a signed-in visitor on the form when the requested target is invalid', async () => {
    storeSsoToken('valid-jwt');
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserProfile: { account: 'alice' } });

    renderAt('/sso/login?redirect_to=https://evil.com/cb');

    expect(await loginButton()).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
    expect(assignMock).not.toHaveBeenCalled();
  });
});
