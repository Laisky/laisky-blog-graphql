import '@testing-library/jest-dom/vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, expect, it, vi } from 'vitest';

import {
  buildRedirectUrlWithToken,
  canStartGithubOAuth,
  canSubmitSsoLogin,
  isTotpRequiredError,
  isTurnstileEnabled,
  isTurnstileRequiredError,
  parseRedirectTarget,
  SsoLoginPage,
} from './sso-login';

// ThemeToggle depends on ThemeProvider context; stub it so the page renders standalone.
vi.mock('@/components/theme/theme-toggle', () => ({
  ThemeToggle: () => <div>Theme toggle</div>,
}));

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
