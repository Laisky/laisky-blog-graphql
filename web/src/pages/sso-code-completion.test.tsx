import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchGraphQL } from '@/lib/graphql';
import { getPasskeyCredentialJSON } from '@/lib/passkey';
import { storeSsoToken } from '@/lib/sso-session';
import { SsoGithubCallbackPage } from './sso-github-callback';
import { SsoLoginPage } from './sso-login';

vi.mock('@/components/theme/theme-toggle', () => ({ ThemeToggle: () => null }));
vi.mock('@/components/ui/laisky-link', () => ({ LaiskyLink: ({ children }: { children: React.ReactNode }) => <span>{children}</span> }));
vi.mock('@/lib/graphql', () => ({ fetchGraphQL: vi.fn() }));
vi.mock('@/lib/passkey', () => ({ getPasskeyCredentialJSON: vi.fn() }));
const originalLocation = window.location;
const assign = vi.fn();
const state = 'A'.repeat(43);
const challenge = 'C'.repeat(42) + 'A';
const code = 'D'.repeat(42) + 'A';
const target = 'https://blog.laisky.com/?sso_flow=code&sso_state=' + state
  + '&sso_challenge=' + challenge + '&sso_challenge_method=S256';
const fetchCode = vi.fn();

beforeEach(() => {
  window.localStorage.clear();
  assign.mockReset();
  vi.mocked(fetchGraphQL).mockReset();
  vi.mocked(getPasskeyCredentialJSON).mockReset();
  fetchCode.mockReset().mockResolvedValue(new Response(JSON.stringify({ code, expires_in: 60 }),
    { headers: { 'Content-Type': 'application/json' } }));
  vi.stubGlobal('fetch', fetchCode);
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { origin: 'https://sso.laisky.com', pathname: '/sso/login', assign },
  });
});
afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  Object.defineProperty(window, 'location', { configurable: true, value: originalLocation });
});

function loginPage() {
  render(<MemoryRouter initialEntries={['/sso/login?redirect_to=' + encodeURIComponent(target)]}>
    <SsoLoginPage />
  </MemoryRouter>);
}

async function expectCodeNavigation() {
  await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
  const url = new URL(assign.mock.calls[0][0]);
  expect([...url.searchParams.keys()]).toEqual(['sso_code', 'sso_state']);
  expect(url.searchParams.get('sso_code')).toBe(code);
  expect(url.searchParams.get('sso_state')).toBe(state);
  expect(url.toString()).not.toContain('synthetic-bearer');
  expect(fetchCode).toHaveBeenCalledTimes(1);
}

describe('all existing auth completions use the opt-in code handoff', () => {
  it('uses a code after password login', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserLogin: { token: 'synthetic-bearer' } });
    loginPage();
    fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
    fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'inert-password' } });
    fireEvent.click(screen.getByRole('button', { name: /^Login$/ }));
    await expectCodeNavigation();
  });

  it('uses a code after the password/TOTP second step', async () => {
    vi.mocked(fetchGraphQL).mockRejectedValueOnce(new Error('totp_required'))
      .mockResolvedValueOnce({ UserLogin: { token: 'synthetic-bearer' } });
    loginPage();
    fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
    fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'inert-password' } });
    fireEvent.click(screen.getByRole('button', { name: /^Login$/ }));
    fireEvent.change(await screen.findByLabelText('TOTP Code'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: /^Verify$/ }));
    await expectCodeNavigation();
  });

  it('uses a code after email-code login', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserLoginWithEmailCode: { token: 'synthetic-bearer' } });
    loginPage();
    fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
    fireEvent.click(screen.getByRole('button', { name: /^Email Code$/ }));
    fireEvent.change(screen.getByLabelText('Email Code'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: /^Login$/ }));
    await expectCodeNavigation();
  });

  it('uses a code after passkey login', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValueOnce({ UserStartPasskeyLogin: { session: 'inert-session', options_json: '{}' } })
      .mockResolvedValueOnce({ UserFinishPasskeyLogin: { token: 'synthetic-bearer', redirect_to: target } });
    vi.mocked(getPasskeyCredentialJSON).mockResolvedValue('{}');
    loginPage();
    fireEvent.click(screen.getByRole('button', { name: /Sign in with Passkey/ }));
    await expectCodeNavigation();
  });

  it('uses a code for an existing verified session without constructing a bearer URL', async () => {
    storeSsoToken('synthetic-bearer');
    vi.mocked(fetchGraphQL).mockResolvedValue({ BlogUser: {} });
    loginPage();
    await expectCodeNavigation();
  });

  it('uses a code after the signed GitHub redirect is returned', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({
      UserGithubOAuthLogin: { token: 'synthetic-bearer', redirect_to: target },
    });
    render(<MemoryRouter initialEntries={['/sso/github/callback?code=inert-code&state=inert-state']}>
      <SsoGithubCallbackPage />
    </MemoryRouter>);
    await expectCodeNavigation();
  });

  it('never navigates with a bearer if issuance fails', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserLogin: { token: 'synthetic-bearer' } });
    fetchCode.mockResolvedValue(new Response('{}', { status: 503 }));
    loginPage();
    fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
    fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'inert-password' } });
    fireEvent.click(screen.getByRole('button', { name: /^Login$/ }));
    await screen.findByText(/Unable to complete Blog SSO code handoff/);
    expect(assign).not.toHaveBeenCalled();
  });
});

describe('ordered Blog cutover prevents authentication-method downgrades', () => {
  it('blocks direct password login with an old unmarked Blog redirect', () => {
    render(<MemoryRouter initialEntries={['/sso/login?redirect_to=' + encodeURIComponent('https://blog.laisky.com/pages/0/')]}>
      <SsoLoginPage />
    </MemoryRouter>);
    fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
    fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'inert-password' } });
    expect(screen.getByRole('button', { name: /^Login$/ })).toBeDisabled();
    expect(fetchGraphQL).not.toHaveBeenCalled();
    expect(fetchCode).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });

  it('does not downgrade an existing session for an old Blog bookmark', async () => {
    storeSsoToken('synthetic-bearer');
    vi.mocked(fetchGraphQL).mockResolvedValue({ BlogUser: {} });
    render(<MemoryRouter initialEntries={['/sso/login?redirect_to=' + encodeURIComponent('https://blog.laisky.com')]}>
      <SsoLoginPage />
    </MemoryRouter>);
    await screen.findByText(/Invalid Blog SSO code request/);
    expect(fetchCode).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });

  it('rejects an unmarked Blog target returned by passkey completion', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValueOnce({ UserStartPasskeyLogin: { session: 'inert-session', options_json: '{}' } })
      .mockResolvedValueOnce({ UserFinishPasskeyLogin: { token: 'synthetic-bearer', redirect_to: 'http://blog.laisky.com/article' } });
    vi.mocked(getPasskeyCredentialJSON).mockResolvedValue('{}');
    loginPage();
    fireEvent.click(screen.getByRole('button', { name: /Sign in with Passkey/ }));
    await screen.findByText(/Blog requires a single-use SSO code/);
    expect(fetchCode).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });

  it('rejects an unmarked Blog target returned by signed GitHub completion', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({
      UserGithubOAuthLogin: { token: 'synthetic-bearer', redirect_to: 'https://blog.laisky.com:444/' },
    });
    render(<MemoryRouter initialEntries={['/sso/github/callback?code=inert-code&state=inert-state']}>
      <SsoGithubCallbackPage />
    </MemoryRouter>);
    await screen.findByText(/Blog requires a single-use SSO code/);
    expect(fetchCode).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });
});
