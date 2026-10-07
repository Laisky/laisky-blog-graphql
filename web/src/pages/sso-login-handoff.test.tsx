import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchGraphQL } from '@/lib/graphql';
import { loadSsoToken } from '@/lib/sso-session';
import type { TurnstileAPI, TurnstileRenderOptions } from '@/pages/sso-turnstile';

import { SsoGithubCallbackPage } from './sso-github-callback';
import { SsoLoginPage } from './sso-login';

vi.mock('@/components/theme/theme-toggle', () => ({
  ThemeToggle: () => <div>Theme toggle</div>,
}));

vi.mock('@/lib/graphql', () => ({
  fetchGraphQL: vi.fn(),
}));

vi.mock('@/lib/passkey', () => ({
  getPasskeyCredentialJSON: vi.fn(async () => '{"id":"credential"}'),
}));

const mockNavigate = vi.fn();
vi.mock('react-router-dom', async (importOriginal) => {
  const actual = await importOriginal<typeof import('react-router-dom')>();
  return { ...actual, useNavigate: () => mockNavigate };
});

const assignMock = vi.fn();
const originalLocation = window.location;
// tokenAtNavigation records what the SSO origin had persisted at the moment the
// browser left for the external application.
let tokenAtNavigation: string | null = null;

// fakeTurnstile solves every challenge it renders; reset() clears the solved
// token like the real widget does, which forces the user to solve it again.
let turnstileSolves = 0;
const fakeTurnstile: TurnstileAPI = {
  render: (_container: HTMLElement, options: TurnstileRenderOptions) => {
    turnstileSolves += 1;
    options.callback(`turnstile-token-${turnstileSolves}`);
    return 'widget-1';
  },
  reset: vi.fn(),
  remove: vi.fn(),
};

beforeEach(() => {
  window.localStorage.clear();
  mockNavigate.mockReset();
  tokenAtNavigation = null;
  turnstileSolves = 0;
  assignMock.mockReset();
  assignMock.mockImplementation(() => {
    tokenAtNavigation = loadSsoToken();
  });
  vi.mocked(fetchGraphQL).mockReset();
  window.turnstile = fakeTurnstile;
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: {
      origin: 'https://sso.laisky.com',
      href: 'https://sso.laisky.com/',
      pathname: '/',
      search: '',
      hash: '',
      assign: assignMock,
      replace: vi.fn(),
    },
  });
});

afterEach(() => {
  delete window.turnstile;
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: originalLocation,
  });
});

const cvLoginEntry = '/?redirect_to=https%3A%2F%2Fcv.laisky.com%2F';

// renderLogin renders the standalone SSO login page.
function renderLogin(entry: string, turnstileSiteKey?: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <SsoLoginPage turnstileSiteKey={turnstileSiteKey} />
    </MemoryRouter>
  );
}

// submitPassword fills and submits the password form.
async function submitPassword() {
  fireEvent.change(screen.getByPlaceholderText('USER_IDENTIFIER'), { target: { value: 'owner@example.com' } });
  fireEvent.change(screen.getByPlaceholderText('••••••••••••'), { target: { value: 'correct-horse-battery-24' } });
  const submit = await screen.findByRole('button', { name: /^(login|verify)$/i });
  await waitFor(() => expect(submit).toBeEnabled());
  fireEvent.click(submit);
}

describe('SSO session hand-off to external applications', () => {
  it('persists the password-login session before leaving for the application', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValueOnce({ UserLogin: { token: 'password-jwt' } });
    renderLogin(cvLoginEntry);

    await submitPassword();

    await waitFor(() => expect(assignMock).toHaveBeenCalledWith('https://cv.laisky.com/?sso_token=password-jwt'));
    expect(tokenAtNavigation).toBe('password-jwt');
  });

  it('persists the passkey session before leaving for the application', async () => {
    vi.mocked(fetchGraphQL)
      .mockResolvedValueOnce({ UserStartPasskeyLogin: { options_json: '{}', session: 'signed-session' } })
      .mockResolvedValueOnce({ UserFinishPasskeyLogin: { token: 'passkey-jwt', redirect_to: 'https://cv.laisky.com/' } });
    renderLogin(cvLoginEntry);

    fireEvent.click(await screen.findByRole('button', { name: /sign in with passkey/i }));

    await waitFor(() => expect(assignMock).toHaveBeenCalledWith('https://cv.laisky.com/?sso_token=passkey-jwt'));
    expect(tokenAtNavigation).toBe('passkey-jwt');
  });

  it('persists the GitHub session before leaving for the application', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValueOnce({
      UserGithubOAuthLogin: { token: 'github-jwt', redirect_to: 'https://cv.laisky.com/' },
    });
    render(
      <MemoryRouter initialEntries={['/github/callback?code=abc&state=signed']}>
        <SsoGithubCallbackPage />
      </MemoryRouter>
    );

    await waitFor(() => expect(assignMock).toHaveBeenCalledWith('https://cv.laisky.com/?sso_token=github-jwt'));
    expect(tokenAtNavigation).toBe('github-jwt');
  });

  it('lets a returning visitor reuse the persisted session without signing in again', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValueOnce({ UserLogin: { token: 'password-jwt' } });
    const first = renderLogin(cvLoginEntry);
    await submitPassword();
    await waitFor(() => expect(assignMock).toHaveBeenCalledTimes(1));
    first.unmount();

    vi.mocked(fetchGraphQL).mockResolvedValueOnce({ UserProfile: { account: 'owner@example.com' } });
    renderLogin(cvLoginEntry);

    await waitFor(() => expect(assignMock).toHaveBeenCalledTimes(2));
    expect(assignMock).toHaveBeenLastCalledWith('https://cv.laisky.com/?sso_token=password-jwt');
    expect(vi.mocked(fetchGraphQL).mock.calls[1][0]).toBe('password-jwt');
  });
});

describe('SSO challenge and failure handling', () => {
  it('does not demand a second challenge for the TOTP step after one was solved', async () => {
    vi.mocked(fetchGraphQL)
      .mockRejectedValueOnce(new Error('turnstile_required'))
      .mockRejectedValueOnce(new Error('totp_required'))
      .mockResolvedValueOnce({ UserLogin: { token: 'totp-jwt' } });
    renderLogin(cvLoginEntry, 'site-key');

    await submitPassword();
    await waitFor(() => expect(turnstileSolves).toBe(1));
    await submitPassword();
    const totp = await screen.findByPlaceholderText('000000');
    fireEvent.change(totp, { target: { value: '123456' } });
    const verify = await screen.findByRole('button', { name: /^verify$/i });
    await waitFor(() => expect(verify).toBeEnabled());
    fireEvent.click(verify);

    await waitFor(() => expect(assignMock).toHaveBeenCalledWith('https://cv.laisky.com/?sso_token=totp-jwt'));
    expect(turnstileSolves).toBe(1);
    const totpCall = vi.mocked(fetchGraphQL).mock.calls[2][2] as Record<string, unknown>;
    expect(totpCall.totpCode).toBe('123456');
    expect(totpCall.turnstileToken).toBeNull();
  });

  it('asks for a fresh challenge when the supplied one could not be verified', async () => {
    vi.mocked(fetchGraphQL)
      .mockRejectedValueOnce(new Error('turnstile_required'))
      .mockRejectedValueOnce(new Error('turnstile_failed'));
    renderLogin(cvLoginEntry, 'site-key');

    await submitPassword();
    await waitFor(() => expect(turnstileSolves).toBe(1));
    await submitPassword();

    expect(await screen.findByText(/security verification could not be confirmed/i)).toBeInTheDocument();
    expect(screen.queryByText(/invalid credentials/i)).not.toBeInTheDocument();
  });

  it('explains an unavailable sign-in service instead of blaming the password', async () => {
    vi.mocked(fetchGraphQL).mockRejectedValueOnce(new Error('login_unavailable'));
    renderLogin(cvLoginEntry);

    await submitPassword();

    expect(await screen.findByText(/sign-in is temporarily unavailable/i)).toBeInTheDocument();
    expect(loadSsoToken()).toBe('');
    expect(assignMock).not.toHaveBeenCalled();
  });
});
