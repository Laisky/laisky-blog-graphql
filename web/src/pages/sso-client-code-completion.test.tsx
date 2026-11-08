import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchGraphQL } from '@/lib/graphql';
import { getPasskeyCredentialJSON } from '@/lib/passkey';
import { configureSsoRedirectPolicy } from '@/lib/sso-redirect';
import { loadSsoToken, storeSsoToken } from '@/lib/sso-session';
import { SsoGithubCallbackPage } from './sso-github-callback';
import { SsoLoginPage } from './sso-login';

vi.mock('@/components/theme/theme-toggle', () => ({ ThemeToggle: () => null }));
vi.mock('@/components/ui/laisky-link', () => ({ LaiskyLink: ({ children }: { children: React.ReactNode }) => <span>{children}</span> }));
vi.mock('@/lib/graphql', () => ({ fetchGraphQL: vi.fn() }));
vi.mock('@/lib/passkey', () => ({ getPasskeyCredentialJSON: vi.fn() }));

const originalLocation = window.location;
const assign = vi.fn();
const bearer = 'synthetic-bearer';
const state = 'A'.repeat(43);
const challenge = 'C'.repeat(42) + 'A';
const code = 'D'.repeat(42) + 'A';
const callback = 'https://accounting.laisky.com/api/auth/sso/callback';
const codeRequest = callback + '?sso_flow=code&sso_client_id=accounting&sso_state=' + state
  + '&sso_challenge=' + challenge + '&sso_challenge_method=S256';
const registry = { legacy_token_redirect: 'allow', clients: [{ client_id: 'accounting', redirect_uris: [callback] }] };
const fetchCode = vi.fn();

beforeEach(() => {
  window.localStorage.clear();
  assign.mockReset();
  vi.mocked(fetchGraphQL).mockReset();
  vi.mocked(getPasskeyCredentialJSON).mockReset();
  fetchCode.mockReset().mockResolvedValue(new Response(JSON.stringify({ code, expires_in: 60 }),
    { headers: { 'Content-Type': 'application/json' } }));
  vi.stubGlobal('fetch', fetchCode);
  configureSsoRedirectPolicy({ ssoRedirect: registry });
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { origin: 'https://sso.laisky.com', pathname: '/sso/login', assign },
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  configureSsoRedirectPolicy({});
  Object.defineProperty(window, 'location', { configurable: true, value: originalLocation });
});

function loginPage(redirectTo: string) {
  render(<MemoryRouter initialEntries={['/sso/login?redirect_to=' + encodeURIComponent(redirectTo)]}>
    <SsoLoginPage />
  </MemoryRouter>);
}

function submitPassword() {
  fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
  fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'inert-password' } });
  fireEvent.click(screen.getByRole('button', { name: /^Login$/ }));
}

async function expectClientCodeNavigation() {
  await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
  const url = new URL(assign.mock.calls[0][0]);
  expect(url.origin + url.pathname).toBe(callback);
  expect([...url.searchParams.keys()]).toEqual(['sso_code', 'sso_state']);
  expect(url.searchParams.get('sso_code')).toBe(code);
  expect(url.toString()).not.toContain(bearer);
  expect(fetchCode).toHaveBeenCalledTimes(1);
  expect(JSON.parse(fetchCode.mock.calls[0][1].body)).toMatchObject({ client_id: 'accounting', redirect_uri: callback });
}

describe('registered clients receive only a single-use code on every completion path', () => {
  it('after password login', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserLogin: { token: bearer } });
    loginPage(codeRequest);
    submitPassword();
    await expectClientCodeNavigation();
  });

  it('after passkey login', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValueOnce({ UserStartPasskeyLogin: { session: 'inert-session', options_json: '{}' } })
      .mockResolvedValueOnce({ UserFinishPasskeyLogin: { token: bearer, redirect_to: codeRequest } });
    vi.mocked(getPasskeyCredentialJSON).mockResolvedValue('{}');
    loginPage(codeRequest);
    fireEvent.click(screen.getByRole('button', { name: /Sign in with Passkey/ }));
    await expectClientCodeNavigation();
  });

  it('for an existing verified session', async () => {
    storeSsoToken(bearer);
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserProfile: { account: 'alice' } });
    loginPage(codeRequest);
    await expectClientCodeNavigation();
  });

  it('after the signed GitHub redirect is returned', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserGithubOAuthLogin: { token: bearer, redirect_to: codeRequest } });
    render(<MemoryRouter initialEntries={['/sso/github/callback?code=inert-code&state=inert-state']}>
      <SsoGithubCallbackPage />
    </MemoryRouter>);
    await expectClientCodeNavigation();
  });

  it('keeps the session and never navigates with a bearer when issuance fails', async () => {
    storeSsoToken(bearer);
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserProfile: { account: 'alice' } });
    fetchCode.mockResolvedValue(new Response('{}', { status: 503 }));
    loginPage(codeRequest);
    await screen.findByText(/Unable to complete SSO code handoff/);
    expect(assign).not.toHaveBeenCalled();
    expect(loadSsoToken()).toBe(bearer);
  });

  it('refuses a legacy request for the registered hostname before credentials are entered', async () => {
    loginPage(callback + '?state=legacy');
    expect(await screen.findAllByText(/must use the SSO code flow/)).not.toHaveLength(0);
    expect(screen.getByRole('button', { name: /^Login$/ })).toBeDisabled();
  });
});

describe('legacy bearer cutover on the issuer pages', () => {
  beforeEach(() => configureSsoRedirectPolicy({ ssoRedirect: { legacy_token_redirect: 'deny', clients: registry.clients } }));

  it('blocks password sign-in toward an unregistered target', async () => {
    loginPage('https://cv.laisky.com/cv');
    expect(await screen.findAllByText(/not registered for SSO/)).not.toHaveLength(0);
    expect(screen.getByRole('button', { name: /^Login$/ })).toBeDisabled();
    expect(assign).not.toHaveBeenCalled();
  });

  it('does not hand an existing session to an unregistered target', async () => {
    storeSsoToken(bearer);
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserProfile: { account: 'alice' } });
    loginPage('https://cv.laisky.com/cv');
    expect(await screen.findByRole('button', { name: /^Login$/ })).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
    expect(fetchCode).not.toHaveBeenCalled();
  });

  it('refuses a GitHub completion toward an unregistered target', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserGithubOAuthLogin: { token: bearer, redirect_to: 'https://cv.laisky.com/cv' } });
    render(<MemoryRouter initialEntries={['/sso/github/callback?code=inert-code&state=inert-state']}>
      <SsoGithubCallbackPage />
    </MemoryRouter>);
    await screen.findByText(/not registered for SSO/);
    expect(assign).not.toHaveBeenCalled();
  });

  it('opens the issuer profile after GitHub without a URL bearer', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserGithubOAuthLogin: { token: bearer, redirect_to: '' } });
    render(<MemoryRouter initialEntries={['/sso/github/callback?code=inert-code&state=inert-state']}>
      <SsoGithubCallbackPage />
    </MemoryRouter>);
    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://sso.laisky.com/profile'));
    expect(loadSsoToken()).toBe(bearer);
  });

  it('still issues a code for a registered client', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserLogin: { token: bearer } });
    loginPage(codeRequest);
    submitPassword();
    await expectClientCodeNavigation();
  });
});
