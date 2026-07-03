import '@testing-library/jest-dom/vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { storeSsoToken } from '@/lib/sso-session';
import { SsoTokenDetailsPage } from './sso-token-details';

// ThemeToggle depends on ThemeProvider context; stub it so the page renders standalone.
vi.mock('@/components/theme/theme-toggle', () => ({
  ThemeToggle: () => <div>Theme toggle</div>,
}));

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

// encodeJwtJSON serializes a JWT segment for tests.
// It accepts a JSON value and returns a base64url-encoded string.
function encodeJwtJSON(value: unknown): string {
  return window.btoa(JSON.stringify(value)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/g, '');
}

function renderTokenPage(pathname = '/sso/token') {
  return render(
    <MemoryRouter initialEntries={[pathname]}>
      <SsoTokenDetailsPage ssoJwt={ssoJwt} />
    </MemoryRouter>
  );
}

beforeEach(() => {
  window.localStorage.clear();
});

describe('SsoTokenDetailsPage', () => {
  it('shows the public JWT metadata to anonymous visitors without a stored token', () => {
    renderTokenPage();

    expect(screen.getByRole('heading', { name: 'SSO JWT Token' })).toBeInTheDocument();
    expect(screen.getByText('EdDSA')).toBeInTheDocument();
    expect(screen.getByText(/BEGIN PUBLIC KEY/)).toBeInTheDocument();
    expect(screen.getByText(/"uid"/)).toBeInTheDocument();

    // Anonymous visitors have no session token, so the current-token section stays hidden
    // and the back link returns to sign in.
    expect(screen.queryByText('Current Token')).not.toBeInTheDocument();
    const backLink = screen.getByRole('link', { name: /back to sign in/i });
    expect(backLink).toHaveAttribute('href', '/sso/login');
  });

  it('reveals the current token and decoded JSON when a session token is stored', () => {
    const currentToken = `${encodeJwtJSON({ alg: 'EdDSA', typ: 'JWT' })}.${encodeJwtJSON({
      iss: 'laisky-sso',
      uid: '0194d5f8-19f7-7f7b-a8d3-421a60f8d8ab',
      username: 'alice@example.com',
    })}.signature`;
    storeSsoToken(currentToken);

    renderTokenPage();

    expect(screen.getByText('Current Token')).toBeInTheDocument();
    expect(screen.getByText(currentToken)).toBeInTheDocument();
    expect(screen.getByText('Decoded Token JSON')).toBeInTheDocument();
    expect(screen.getByText(/"alg": "EdDSA"/)).toBeInTheDocument();
    expect(screen.getByText(/"username": "alice@example.com"/)).toBeInTheDocument();

    // A signed-in visitor is offered a link back to their profile instead of sign in.
    const backLink = screen.getByRole('link', { name: /back to profile/i });
    expect(backLink).toHaveAttribute('href', '/sso/profile');
  });

  it('surfaces a decode error banner when the stored token is not a compact JWT', () => {
    storeSsoToken('not-a-jwt');

    renderTokenPage();

    expect(screen.getByText('Current Token')).toBeInTheDocument();
    expect(screen.getByText(/Token is not a compact JWT/i)).toBeInTheDocument();
  });

  it('keeps the token blocks bounded so long values scroll instead of overflowing', () => {
    storeSsoToken('header.payload.signature');

    renderTokenPage();

    expect(screen.getByText('header.payload.signature').closest('pre')).toHaveClass('max-w-full');
  });
});
