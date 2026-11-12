import '@testing-library/jest-dom/vitest';
import { cleanup, render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchGraphQL } from '@/lib/graphql';
import { loadSsoToken } from '@/lib/sso-session';
import { SsoGithubCallbackPage } from './sso-github-callback';

vi.mock('@/components/theme/theme-toggle', () => ({ ThemeToggle: () => null }));
vi.mock('@/lib/graphql', () => ({ fetchGraphQL: vi.fn() }));

const originalLocation = window.location;
const assign = vi.fn();

beforeEach(() => {
  window.localStorage.clear();
  assign.mockReset();
  vi.mocked(fetchGraphQL).mockReset();
  Object.defineProperty(window, 'location', { configurable: true, value: { origin: 'https://sso.laisky.com', pathname: '/github/callback', assign } });
});

afterEach(() => {
  cleanup();
  Object.defineProperty(window, 'location', { configurable: true, value: originalLocation });
});

function renderCallback() {
  render(<MemoryRouter initialEntries={['/github/callback?code=inert-code&state=inert-state']}><SsoGithubCallbackPage /></MemoryRouter>);
}

describe('GitHub callback hand-off', () => {
  it('adds the SSO token to a validated legacy target', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserGithubOAuthLogin: { token: 'jwt-token', redirect_to: 'https://app.laisky.com/callback?state=abc' } });
    renderCallback();
    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://app.laisky.com/callback?state=abc&sso_token=jwt-token'));
  });

  it('opens the issuer profile without a URL bearer when the redirect target is empty', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({ UserGithubOAuthLogin: { token: 'jwt-token', redirect_to: '' } });
    renderCallback();
    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://sso.laisky.com/profile'));
    expect(loadSsoToken()).toBe('jwt-token');
  });
});
