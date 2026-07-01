import { describe, expect, it } from 'vitest';

import { buildGithubCallbackRedirect } from './sso-github-callback';

describe('buildGithubCallbackRedirect', () => {
  it('adds the SSO token to the validated redirect target', () => {
    const redirectURL = buildGithubCallbackRedirect('https://app.laisky.com/callback?state=abc', 'jwt-token', 'https://sso.laisky.com');
    expect(redirectURL).toBe('https://app.laisky.com/callback?state=abc&sso_token=jwt-token');
  });

  it('falls back to the profile page when redirect target is empty', () => {
    const redirectURL = buildGithubCallbackRedirect('', 'jwt-token', 'https://sso.laisky.com');
    expect(redirectURL).toBe('https://sso.laisky.com/profile?sso_token=jwt-token');
  });
});
