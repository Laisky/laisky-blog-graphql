import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchGraphQL } from '@/lib/graphql';
import { storeSsoToken } from '@/lib/sso-session';
import { canSubmitPasskeyRename, canSubmitPasswordChange, canSubmitTotpCode, formatPasskeyCreatedAt, SsoProfilePage } from './sso-profile';

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
  it('links to the standalone token details page from the profile', async () => {
    const currentToken = 'header.payload.signature';
    storeSsoToken(currentToken);
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
        <SsoProfilePage />
      </MemoryRouter>
    );

    await waitFor(() => expect(fetchGraphQL).toHaveBeenCalledWith(currentToken, expect.any(String)));

    const tokenLink = await screen.findByRole('link', { name: /token details/i });
    expect(tokenLink).toHaveAttribute('href', '/sso/token');
  });
});

describe('SsoProfilePage TOTP setup', () => {
  it('shows a scannable QR code after setup starts', async () => {
    storeSsoToken('current.jwt.token');
    vi.mocked(fetchGraphQL)
      .mockResolvedValueOnce({
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
      })
      .mockResolvedValueOnce({
        UserStartTOTPSetup: {
          secret: 'JBSWY3DPEHPK3PXP',
          provisioning_uri: 'otpauth://totp/Laisky:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Laisky',
        },
      });

    render(
      <MemoryRouter initialEntries={['/sso/profile']}>
        <SsoProfilePage />
      </MemoryRouter>
    );

    fireEvent.click(await screen.findByRole('button', { name: /start totp setup/i }));

    expect(await screen.findByRole('img', { name: /totp setup qr code/i })).toBeInTheDocument();
    expect(screen.getByText('JBSWY3DPEHPK3PXP')).toBeInTheDocument();
    expect(screen.getByText(/otpauth:\/\/totp\/Laisky/)).toBeInTheDocument();
  });
});

describe('SsoProfilePage accessibility', () => {
  const baseProfile = {
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
  };

  const renderSignedIn = async (overrides: Partial<typeof baseProfile> = {}) => {
    storeSsoToken('current.jwt.token');
    vi.mocked(fetchGraphQL).mockResolvedValueOnce({ UserProfile: { ...baseProfile, ...overrides } });
    render(
      <MemoryRouter initialEntries={['/sso/profile']}>
        <SsoProfilePage />
      </MemoryRouter>
    );
    await screen.findByText('Alice');
  };

  it('outlines the page with a single h1 and an h2 per card', async () => {
    await renderSignedIn();

    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1);
    for (const name of ['Profile', 'Password', 'Passkeys', 'TOTP']) {
      expect(screen.getByRole('heading', { level: 2, name })).toBeInTheDocument();
    }
  });

  it('keeps the heading outline for signed-out visitors', async () => {
    render(
      <MemoryRouter initialEntries={['/sso/profile']}>
        <SsoProfilePage />
      </MemoryRouter>
    );

    await screen.findByRole('button', { name: /go to sign in/i });
    expect(screen.getAllByRole('heading', { level: 1 })).toHaveLength(1);
    expect(screen.getByRole('heading', { level: 2, name: 'Profile' })).toBeInTheDocument();
  });

  it('names the password and passkey forms after their card headings and labels every field', async () => {
    await renderSignedIn();

    const passwordForm = screen.getByRole('form', { name: 'Password' });
    const current = within(passwordForm).getByLabelText('Current password');
    expect(current).toHaveAttribute('name', 'currentPassword');
    expect(current).toHaveAttribute('autocomplete', 'current-password');
    const next = within(passwordForm).getByLabelText('New password');
    expect(next).toHaveAttribute('name', 'newPassword');
    expect(next).toHaveAttribute('autocomplete', 'new-password');

    const passkeyForm = screen.getByRole('form', { name: 'Passkeys' });
    expect(within(passkeyForm).getByLabelText('Passkey label')).toHaveAttribute('name', 'passkeyLabel');
  });

  it('labels the password that disables TOTP', async () => {
    await renderSignedIn({ totp_enabled: true });

    const totpForm = screen.getByRole('form', { name: 'TOTP' });
    const password = within(totpForm).getByLabelText('Current password');
    expect(password).toHaveAttribute('name', 'currentPassword');
    expect(password).toHaveAttribute('autocomplete', 'current-password');
  });

  it('labels the TOTP confirmation code and describes how to fill it', async () => {
    await renderSignedIn();
    vi.mocked(fetchGraphQL).mockResolvedValueOnce({
      UserStartTOTPSetup: {
        secret: 'JBSWY3DPEHPK3PXP',
        provisioning_uri: 'otpauth://totp/Laisky:alice@example.com?secret=JBSWY3DPEHPK3PXP&issuer=Laisky',
      },
    });

    fireEvent.click(screen.getByRole('button', { name: /start totp setup/i }));

    const code = await screen.findByLabelText('Verification code');
    expect(code).toHaveAttribute('name', 'totpCode');
    expect(code).toHaveAttribute('autocomplete', 'one-time-code');
    expect(code).toHaveAccessibleDescription('Enter the 6-digit code from your authenticator app.');
    expect(code.closest('form')).toHaveAccessibleName('TOTP');
  });

  it('renders the token details link without a button-only type attribute', async () => {
    await renderSignedIn();

    const link = screen.getByRole('link', { name: /token details/i });
    expect(link.tagName).toBe('A');
    expect(link).not.toHaveAttribute('type');
  });
});
