import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchGraphQL } from '@/lib/graphql';
import { storeSsoToken } from '@/lib/sso-session';
import { canSubmitPasskeyRename, canSubmitPasswordChange, canSubmitTotpCode, formatPasskeyCreatedAt } from './sso-profile';
import { SsoProfilePage } from './sso-profile';

vi.mock('@/components/theme/theme-toggle', () => ({
  ThemeToggle: () => <div>Theme toggle</div>,
}));

vi.mock('@/components/ui/laisky-link', () => ({
  LaiskyLink: ({ children, className }: { children: ReactNode; className?: string }) => <span className={className}>{children}</span>,
}));

vi.mock('@/lib/graphql', () => ({
  fetchGraphQL: vi.fn(),
}));

beforeEach(() => {
  window.localStorage.clear();
  window.history.replaceState(null, '', '/sso/profile');
  vi.mocked(fetchGraphQL).mockReset();
});

describe('canSubmitPasswordChange', () => {
  it('requires both passwords', () => {
    expect(canSubmitPasswordChange('', 'new-password')).toBe(false);
    expect(canSubmitPasswordChange('old-password', '')).toBe(false);
  });

  it('requires a different new password', () => {
    expect(canSubmitPasswordChange('same-password', 'same-password')).toBe(false);
  });

  it('allows distinct non-empty passwords', () => {
    expect(canSubmitPasswordChange('old-password', 'new-password')).toBe(true);
  });
});

describe('canSubmitTotpCode', () => {
  it('requires six digits', () => {
    expect(canSubmitTotpCode('12345')).toBe(false);
    expect(canSubmitTotpCode('1234567')).toBe(false);
    expect(canSubmitTotpCode('abcdef')).toBe(false);
  });

  it('allows a six-digit code', () => {
    expect(canSubmitTotpCode('123456')).toBe(true);
  });
});

describe('canSubmitPasskeyRename', () => {
  it('requires a non-empty changed name', () => {
    expect(canSubmitPasskeyRename('Laptop', '')).toBe(false);
    expect(canSubmitPasskeyRename('Laptop', 'Laptop')).toBe(false);
    expect(canSubmitPasskeyRename('Laptop', '  Laptop  ')).toBe(false);
  });

  it('allows a changed name within the limit', () => {
    expect(canSubmitPasskeyRename('Laptop', 'Desktop')).toBe(true);
  });

  it('rejects names over one hundred characters', () => {
    expect(canSubmitPasskeyRename('Laptop', 'a'.repeat(101))).toBe(false);
  });
});

describe('formatPasskeyCreatedAt', () => {
  it('formats valid timestamps in UTC', () => {
    expect(formatPasskeyCreatedAt('2026-01-02T03:04:05.000Z')).toBe('2026-01-02 03:04 UTC');
  });

  it('returns invalid timestamps unchanged', () => {
    expect(formatPasskeyCreatedAt('not-a-date')).toBe('not-a-date');
  });
});

describe('SsoProfilePage token details', () => {
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

  it('shows the current SSO token in the profile token details modal', async () => {
    storeSsoToken('current.jwt.token');
    vi.mocked(fetchGraphQL).mockResolvedValue({
      UserProfile: {
        uid: '0194d5f8-19f7-7f7b-a8d3-421a60f8d8ab',
        account: 'alice@example.com',
        auth_methods: ['password'],
        password_enabled: true,
        totp_enabled: false,
        passkey_count: 0,
        passkeys: [],
        github_bound: false,
        user: {
          id: '0194d5f8-19f7-7f7b-a8d3-421a60f8d8ab',
          username: 'Alice',
        },
      },
    });

    render(
      <MemoryRouter initialEntries={['/sso/profile']}>
        <SsoProfilePage ssoJwt={ssoJwt} />
      </MemoryRouter>
    );

    await waitFor(() => expect(fetchGraphQL).toHaveBeenCalledWith('current.jwt.token', expect.any(String)));
    fireEvent.click(screen.getByRole('button', { name: /token details/i }));

    expect(screen.getByText('Current Token')).toBeInTheDocument();
    expect(screen.getByText('current.jwt.token')).toBeInTheDocument();
    expect(screen.getByText('EdDSA')).toBeInTheDocument();
  });
});
