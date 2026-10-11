import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchGraphQL } from '@/lib/graphql';
import { getPasskeyCredentialJSON } from '@/lib/passkey';
import { loadSsoToken, storeSsoToken } from '@/lib/sso-session';
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

function loginPage(redirectTo = target) {
  render(<MemoryRouter initialEntries={['/sso/login?redirect_to=' + encodeURIComponent(redirectTo)]}>
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

describe('authenticated session survives a failed code handoff', () => {
  async function retryWithExistingSession() {
    cleanup();
    fetchCode.mockResolvedValue(new Response(JSON.stringify({ code, expires_in: 60 }),
      { headers: { 'Content-Type': 'application/json' } }));
    vi.mocked(fetchGraphQL).mockReset().mockResolvedValue({ BlogUser: {} });
    loginPage();
    await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
    expect(fetchCode).toHaveBeenCalledTimes(2);
    expect(vi.mocked(fetchGraphQL).mock.calls).toHaveLength(1);
    expect(new URL(assign.mock.calls[0][0]).searchParams.get('sso_code')).toBe(code);
  }

  it('retains authenticated session after password handoff failure and retries without login', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserLogin: { token: 'synthetic-bearer' } });
    fetchCode.mockResolvedValue(new Response('{}', { status: 503 }));
    loginPage();
    fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
    fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'inert-password' } });
    fireEvent.click(screen.getByRole('button', { name: /^Login$/ }));
    await screen.findByText(/Unable to complete Blog SSO code handoff/);
    expect(assign).not.toHaveBeenCalled();
    expect(fetchCode).toHaveBeenCalledTimes(1);
    expect(loadSsoToken() === 'synthetic-bearer').toBe(true);
    await retryWithExistingSession();
  });

  it('retains authenticated session after GitHub handoff failure and retries without OAuth', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({
      UserGithubOAuthLogin: { token: 'synthetic-bearer', redirect_to: target },
    });
    fetchCode.mockResolvedValue(new Response('{}', { status: 503 }));
    render(<MemoryRouter initialEntries={['/sso/github/callback?code=inert-code&state=inert-state']}>
      <SsoGithubCallbackPage />
    </MemoryRouter>);
    await screen.findByText(/Unable to complete Blog SSO code handoff/);
    expect(assign).not.toHaveBeenCalled();
    expect(fetchCode).toHaveBeenCalledTimes(1);
    expect(loadSsoToken() === 'synthetic-bearer').toBe(true);
    await retryWithExistingSession();
  });
});

describe('cancelled GitHub completion preserves cancellation', () => {
  it('does not persist or issue a code after the callback page unmounts', async () => {
    let resolveLogin: (value: unknown) => void = () => { throw new Error('local fixture not ready'); };
    const pending = new Promise((resolve) => { resolveLogin = resolve; });
    vi.mocked(fetchGraphQL).mockReturnValueOnce(pending);
    const page = render(<MemoryRouter initialEntries={['/sso/github/callback?code=inert-code&state=inert-state']}>
      <SsoGithubCallbackPage />
    </MemoryRouter>);
    await waitFor(() => expect(fetchGraphQL).toHaveBeenCalledTimes(1));
    page.unmount();
    resolveLogin({ UserGithubOAuthLogin: { token: 'synthetic-bearer', redirect_to: target } });
    await pending;
    await Promise.resolve();
    expect(loadSsoToken()).toBe('');
    expect(fetchCode).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });
});


/** expectBootstrapNavigation verifies the old bookmark leaves with no bearer or borrowed code binding. */
async function expectBootstrapNavigation(returnPath: string, issuer?: 'mcp') {
  await waitFor(() => expect(assign).toHaveBeenCalledTimes(1));
  const bootstrap = new URL(assign.mock.calls[0][0]);
  expect(bootstrap.origin).toBe('https://blog.laisky.com');
  expect(bootstrap.pathname).toBe('/');
  expect([...bootstrap.searchParams.keys()]).toEqual(issuer ? ['sso_start', 'sso_return', 'sso_issuer'] : ['sso_start', 'sso_return']);
  expect(bootstrap.searchParams.get('sso_issuer')).toBe(issuer ?? null);
  expect(bootstrap.searchParams.get('sso_start')).toBe('1');
  expect(bootstrap.searchParams.get('sso_return')).toBe(returnPath);
  expect(bootstrap.toString()).not.toContain('synthetic-bearer');
  expect(fetchCode).not.toHaveBeenCalled();
  expect(loadSsoToken()).toBe('synthetic-bearer');
}

describe('legacy Blog bookmarks restart safely through the current consumer', () => {
  const bookmark = 'https://blog.laisky.com/p/article/?view=compact#intro';
  const returnPath = '/p/article/?view=compact#intro';

  it('continues password login without placing the session in a URL', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserLogin: { token: 'synthetic-bearer' } });
    loginPage(bookmark);
    fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
    fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'inert-password' } });
    fireEvent.click(screen.getByRole('button', { name: /^Login$/ }));
    await expectBootstrapNavigation(returnPath);
  });

  it('continues password/TOTP login without weakening the second step', async () => {
    vi.mocked(fetchGraphQL).mockRejectedValueOnce(new Error('totp_required'))
      .mockResolvedValueOnce({ UserLogin: { token: 'synthetic-bearer' } });
    loginPage(bookmark);
    fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
    fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'inert-password' } });
    fireEvent.click(screen.getByRole('button', { name: /^Login$/ }));
    fireEvent.change(await screen.findByLabelText('TOTP Code'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: /^Verify$/ }));
    await expectBootstrapNavigation(returnPath);
  });

  it('continues email-code login without sending a live email', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserLoginWithEmailCode: { token: 'synthetic-bearer' } });
    loginPage(bookmark);
    fireEvent.change(screen.getByLabelText('Account ID'), { target: { value: 'alice' } });
    fireEvent.click(screen.getByRole('button', { name: /^Email Code$/ }));
    fireEvent.change(screen.getByLabelText('Email Code'), { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: /^Login$/ }));
    await expectBootstrapNavigation(returnPath);
  });

  it('continues an already-verified issuer session without a new login', async () => {
    storeSsoToken('synthetic-bearer');
    vi.mocked(fetchGraphQL).mockResolvedValue({ BlogUser: {} });
    loginPage(bookmark);
    await expectBootstrapNavigation(returnPath);
    expect(fetchGraphQL).toHaveBeenCalledTimes(1);
  });

  it('keeps a verified MCP-origin session on the fixed MCP login entry', async () => {
    Object.defineProperty(window, 'location', {
      configurable: true, value: { origin: 'https://mcp.laisky.com', pathname: '/sso/login', assign },
    });
    storeSsoToken('synthetic-bearer');
    vi.mocked(fetchGraphQL).mockResolvedValue({ BlogUser: {} });
    loginPage(bookmark);
    await expectBootstrapNavigation(returnPath, 'mcp');
    expect(fetchGraphQL).toHaveBeenCalledTimes(1);
  });

  it('upgrades an old HTTP bookmark after passkey completion', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValueOnce({ UserStartPasskeyLogin: { session: 'inert-session', options_json: '{}' } })
      .mockResolvedValueOnce({ UserFinishPasskeyLogin: { token: 'synthetic-bearer', redirect_to: 'http://blog.laisky.com/p/article/' } });
    vi.mocked(getPasskeyCredentialJSON).mockResolvedValue('{}');
    loginPage(bookmark);
    fireEvent.click(screen.getByRole('button', { name: /Sign in with Passkey/ }));
    await expectBootstrapNavigation('/p/article/');
  });

  it('continues a valid target returned by signed GitHub completion', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({
      UserGithubOAuthLogin: { token: 'synthetic-bearer', redirect_to: bookmark },
    });
    render(<MemoryRouter initialEntries={['/sso/github/callback?code=inert-code&state=inert-state']}>
      <SsoGithubCallbackPage />
    </MemoryRouter>);
    await expectBootstrapNavigation(returnPath);
  });

  it('rejects a non-default port returned by signed GitHub completion', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({
      UserGithubOAuthLogin: { token: 'synthetic-bearer', redirect_to: 'https://blog.laisky.com:444/' },
    });
    render(<MemoryRouter initialEntries={['/sso/github/callback?code=inert-code&state=inert-state']}>
      <SsoGithubCallbackPage />
    </MemoryRouter>);
    await screen.findByText(/Invalid Blog SSO bookmark/);
    expect(fetchCode).not.toHaveBeenCalled();
    expect(assign).not.toHaveBeenCalled();
  });
});
