/* eslint-disable react-refresh/only-export-components */
import { KeyRound, Lock, LogOut, RefreshCcw, ShieldCheck, ShieldPlus, UserRound } from 'lucide-react';
import { useEffect, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';

import { ThemeToggle } from '@/components/theme/theme-toggle';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { LaiskyLink } from '@/components/ui/laisky-link';
import { StatusBanner, type StatusState } from '@/components/ui/status-banner';
import { fetchGraphQL } from '@/lib/graphql';
import { createPasskeyCredentialJSON } from '@/lib/passkey';
import { clearSsoToken, consumeSsoTokenFromLocation, resolveSiblingSsoPath } from '@/lib/sso-session';

const USER_PROFILE_QUERY = `
  query SsoProfile {
    UserProfile {
      account
      auth_methods
      password_enabled
      totp_enabled
      passkey_count
      github_bound
      user {
        id
        username
      }
    }
  }
`;

const CHANGE_PASSWORD_MUTATION = `
  mutation ChangePassword($currentPassword: String!, $newPassword: String!) {
    UserChangePassword(current_password: $currentPassword, new_password: $newPassword) {
      account
      auth_methods
      password_enabled
      totp_enabled
      passkey_count
      github_bound
      user { id username }
    }
  }
`;

const START_TOTP_MUTATION = `
  mutation StartTotp {
    UserStartTOTPSetup {
      secret
      provisioning_uri
    }
  }
`;

const CONFIRM_TOTP_MUTATION = `
  mutation ConfirmTotp($code: String!) {
    UserConfirmTOTPSetup(code: $code) {
      account
      auth_methods
      password_enabled
      totp_enabled
      passkey_count
      github_bound
      user { id username }
    }
  }
`;

const DISABLE_TOTP_MUTATION = `
  mutation DisableTotp($currentPassword: String!) {
    UserDisableTOTP(current_password: $currentPassword) {
      account
      auth_methods
      password_enabled
      totp_enabled
      passkey_count
      github_bound
      user { id username }
    }
  }
`;

const START_PASSKEY_REGISTRATION_MUTATION = `
  mutation StartPasskeyRegistration($label: String!) {
    UserStartPasskeyRegistration(label: $label) {
      options_json
      session
    }
  }
`;

const FINISH_PASSKEY_REGISTRATION_MUTATION = `
  mutation FinishPasskeyRegistration($label: String!, $session: String!, $credentialJSON: String!) {
    UserFinishPasskeyRegistration(label: $label, session: $session, credential_json: $credentialJSON) {
      account
      auth_methods
      password_enabled
      totp_enabled
      passkey_count
      github_bound
      user { id username }
    }
  }
`;

interface SsoProfile {
  account: string;
  auth_methods: string[];
  password_enabled: boolean;
  totp_enabled: boolean;
  passkey_count: number;
  github_bound: boolean;
  user: {
    id: string;
    username: string;
  };
}

interface ProfileResponse {
  UserProfile: SsoProfile;
}

interface ProfileMutationResponse {
  UserChangePassword?: SsoProfile;
  UserConfirmTOTPSetup?: SsoProfile;
  UserDisableTOTP?: SsoProfile;
  UserFinishPasskeyRegistration?: SsoProfile;
}

interface TotpSetupResponse {
  UserStartTOTPSetup: {
    secret: string;
    provisioning_uri: string;
  };
}

interface PasskeyStartResponse {
  UserStartPasskeyRegistration: {
    options_json: string;
    session: string;
  };
}

export function canSubmitPasswordChange(currentPassword: string, newPassword: string): boolean {
  return currentPassword.trim().length > 0 && newPassword.trim().length > 0 && currentPassword !== newPassword;
}

export function canSubmitTotpCode(code: string): boolean {
  return /^\d{6}$/.test(code.trim());
}

export function SsoProfilePage() {
  const navigate = useNavigate();
  const location = useLocation();
  const loginPath = resolveSiblingSsoPath(location.pathname, 'login');
  const [token, setToken] = useState('');
  const [authReady, setAuthReady] = useState(false);
  const [profile, setProfile] = useState<SsoProfile | null>(null);
  const [status, setStatus] = useState<StatusState | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const [currentPassword, setCurrentPassword] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [totpSetup, setTotpSetup] = useState<TotpSetupResponse['UserStartTOTPSetup'] | null>(null);
  const [totpCode, setTotpCode] = useState('');
  const [disablePassword, setDisablePassword] = useState('');
  const [passkeyLabel, setPasskeyLabel] = useState('My passkey');
  const [isPasskeyRegistering, setIsPasskeyRegistering] = useState(false);

  const loadProfile = async (authToken: string) => {
    setIsLoading(true);
    setStatus(null);
    try {
      const data = await fetchGraphQL<ProfileResponse>(authToken, USER_PROFILE_QUERY);
      setProfile(data.UserProfile);
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to load profile.' });
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    // Callback logins (GitHub, passkey, and cross-app redirects) hand the freshly
    // minted JWT to this page via the sso_token query parameter, while a direct
    // password sign-in stores it before navigating here. Capture whichever is
    // present so every GraphQL call below can authenticate via the Bearer header.
    const sessionToken = consumeSsoTokenFromLocation();
    setToken(sessionToken);
    setAuthReady(true);
    if (sessionToken) {
      void loadProfile(sessionToken);
    } else {
      setIsLoading(false);
    }
  }, []);

  const handleSignOut = () => {
    clearSsoToken();
    setToken('');
    setProfile(null);
    navigate(loginPath);
  };

  const updateProfile = (next: SsoProfile | undefined, message: string) => {
    if (next) {
      setProfile(next);
    }
    setStatus({ tone: 'success', message });
  };

  const handlePasswordChange = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!canSubmitPasswordChange(currentPassword, newPassword)) {
      setStatus({ tone: 'error', message: 'Enter a different new password.' });
      return;
    }

    try {
      const data = await fetchGraphQL<ProfileMutationResponse>(token, CHANGE_PASSWORD_MUTATION, { currentPassword, newPassword });
      setCurrentPassword('');
      setNewPassword('');
      updateProfile(data.UserChangePassword, 'Password updated.');
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to update password.' });
    }
  };

  const handleStartTotp = async () => {
    try {
      const data = await fetchGraphQL<TotpSetupResponse>(token, START_TOTP_MUTATION);
      setTotpSetup(data.UserStartTOTPSetup);
      setStatus({ tone: 'info', message: 'TOTP setup started.' });
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to start TOTP setup.' });
    }
  };

  const handleConfirmTotp = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (!canSubmitTotpCode(totpCode)) {
      setStatus({ tone: 'error', message: 'Enter a six-digit TOTP code.' });
      return;
    }

    try {
      const data = await fetchGraphQL<ProfileMutationResponse>(token, CONFIRM_TOTP_MUTATION, { code: totpCode });
      setTotpCode('');
      setTotpSetup(null);
      updateProfile(data.UserConfirmTOTPSetup, 'TOTP enabled.');
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to confirm TOTP.' });
    }
  };

  const handleDisableTotp = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (disablePassword.trim().length === 0) {
      setStatus({ tone: 'error', message: 'Enter your current password.' });
      return;
    }

    try {
      const data = await fetchGraphQL<ProfileMutationResponse>(token, DISABLE_TOTP_MUTATION, { currentPassword: disablePassword });
      setDisablePassword('');
      updateProfile(data.UserDisableTOTP, 'TOTP disabled.');
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to disable TOTP.' });
    }
  };

  const handleRegisterPasskey = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const label = passkeyLabel.trim();
    if (!label) {
      setStatus({ tone: 'error', message: 'Enter a passkey label.' });
      return;
    }

    setIsPasskeyRegistering(true);
    setStatus({ tone: 'info', message: 'Waiting for passkey...' });
    try {
      const start = await fetchGraphQL<PasskeyStartResponse>(token, START_PASSKEY_REGISTRATION_MUTATION, { label });
      const credentialJSON = await createPasskeyCredentialJSON(start.UserStartPasskeyRegistration.options_json);
      const finish = await fetchGraphQL<ProfileMutationResponse>(token, FINISH_PASSKEY_REGISTRATION_MUTATION, {
        label,
        session: start.UserStartPasskeyRegistration.session,
        credentialJSON,
      });
      updateProfile(finish.UserFinishPasskeyRegistration, 'Passkey registered.');
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to register passkey.' });
    } finally {
      setIsPasskeyRegistering(false);
    }
  };

  const authMethods = profile?.auth_methods.join(' / ') || 'Unknown';

  return (
    <div className="min-h-screen bg-background text-foreground">
      <header className="border-b border-border bg-card/80 backdrop-blur">
        <div className="mx-auto flex max-w-5xl items-center justify-between px-4 py-4">
          <Link to="/" className="flex items-center gap-2 font-semibold text-foreground hover:text-primary">
            <Lock className="h-5 w-5 text-primary" />
            <span>
              <LaiskyLink className="text-primary underline-offset-4 hover:underline">Laisky</LaiskyLink> SSO
            </span>
          </Link>
          <div className="flex items-center gap-2">
            {token && (
              <Button type="button" variant="outline" size="sm" className="gap-2" onClick={handleSignOut}>
                <LogOut className="h-4 w-4" />
                Sign out
              </Button>
            )}
            <ThemeToggle />
          </div>
        </div>
      </header>

      {authReady && !token ? (
        <main className="mx-auto flex max-w-md flex-col gap-6 px-4 py-16">
          {status && <StatusBanner status={status} />}
          <Card>
            <CardHeader className="border-b border-border/50">
              <div className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                <UserRound className="h-4 w-4" />
                Profile
              </div>
            </CardHeader>
            <CardContent className="space-y-4 pt-6">
              <StatusBanner status={{ tone: 'info', message: 'Please sign in to view and manage your account.' }} />
              <Button type="button" className="w-full" onClick={() => navigate(loginPath)}>
                Go to sign in
              </Button>
            </CardContent>
          </Card>
        </main>
      ) : (
        <main className="mx-auto grid max-w-5xl gap-6 px-4 py-8 lg:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
          <section className="space-y-6">
            {status && <StatusBanner status={status} />}
            <Card>
              <CardHeader className="border-b border-border/50">
                <div className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                  <UserRound className="h-4 w-4" />
                  Profile
                </div>
              </CardHeader>
              <CardContent className="space-y-4 pt-6">
                {isLoading ? (
                  <StatusBanner status={{ tone: 'info', message: 'Loading profile...' }} />
                ) : profile ? (
                  <div className="space-y-3">
                    <ProfileRow label="Display name" value={profile.user.username} />
                    <ProfileRow label="Account" value={profile.account} />
                    <ProfileRow label="Methods" value={authMethods} />
                    <ProfileRow label="Passkeys" value={String(profile.passkey_count)} />
                    <ProfileRow label="GitHub OIDC" value={profile.github_bound ? 'Bound' : 'Not bound'} />
                  </div>
                ) : (
                  <StatusBanner status={{ tone: 'error', message: 'Profile unavailable.' }} />
                )}
                <Button type="button" variant="outline" className="w-full gap-2" onClick={() => void loadProfile(token)}>
                  <RefreshCcw className="h-4 w-4" />
                  Refresh
                </Button>
              </CardContent>
            </Card>
          </section>

          <section className="space-y-6">
            <Card>
              <CardHeader className="border-b border-border/50">
                <div className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                  <KeyRound className="h-4 w-4" />
                  Password
                </div>
              </CardHeader>
              <CardContent className="pt-6">
                <form className="space-y-4" onSubmit={handlePasswordChange}>
                  <Input
                    type="password"
                    autoComplete="current-password"
                    placeholder="Current password"
                    value={currentPassword}
                    onChange={(event) => setCurrentPassword(event.target.value)}
                  />
                  <Input
                    type="password"
                    autoComplete="new-password"
                    placeholder="New password"
                    value={newPassword}
                    onChange={(event) => setNewPassword(event.target.value)}
                  />
                  <Button type="submit" className="w-full" disabled={!canSubmitPasswordChange(currentPassword, newPassword)}>
                    Update Password
                  </Button>
                </form>
              </CardContent>
            </Card>

            <Card>
              <CardHeader className="border-b border-border/50">
                <div className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                  <KeyRound className="h-4 w-4" />
                  Passkeys
                </div>
              </CardHeader>
              <CardContent className="pt-6">
                <form className="space-y-4" onSubmit={handleRegisterPasskey}>
                  <Input
                    autoComplete="off"
                    placeholder="Passkey label"
                    value={passkeyLabel}
                    onChange={(event) => setPasskeyLabel(event.target.value)}
                  />
                  <Button
                    type="submit"
                    variant="outline"
                    className="w-full"
                    disabled={isPasskeyRegistering || passkeyLabel.trim().length === 0}
                  >
                    {isPasskeyRegistering ? 'Waiting for Passkey...' : 'Register Passkey'}
                  </Button>
                </form>
              </CardContent>
            </Card>

            <Card>
              <CardHeader className="border-b border-border/50">
                <div className="flex items-center gap-2 text-sm font-semibold uppercase tracking-wide text-muted-foreground">
                  <ShieldCheck className="h-4 w-4" />
                  TOTP
                </div>
              </CardHeader>
              <CardContent className="space-y-4 pt-6">
                {profile?.totp_enabled ? (
                  <form className="space-y-4" onSubmit={handleDisableTotp}>
                    <StatusBanner status={{ tone: 'success', message: 'TOTP enabled.' }} />
                    <Input
                      type="password"
                      autoComplete="current-password"
                      placeholder="Current password"
                      value={disablePassword}
                      onChange={(event) => setDisablePassword(event.target.value)}
                    />
                    <Button type="submit" variant="outline" className="w-full">
                      Disable TOTP
                    </Button>
                  </form>
                ) : (
                  <div className="space-y-4">
                    <Button type="button" variant="outline" className="w-full gap-2" onClick={() => void handleStartTotp()}>
                      <ShieldPlus className="h-4 w-4" />
                      Start TOTP Setup
                    </Button>
                    {totpSetup && (
                      <form className="space-y-4" onSubmit={handleConfirmTotp}>
                        <div className="rounded-md border border-border bg-muted p-3 font-mono text-xs text-muted-foreground">
                          <div className="break-all">{totpSetup.secret}</div>
                          <div className="mt-2 break-all">{totpSetup.provisioning_uri}</div>
                        </div>
                        <Input
                          inputMode="numeric"
                          autoComplete="one-time-code"
                          placeholder="000000"
                          value={totpCode}
                          onChange={(event) => setTotpCode(event.target.value)}
                        />
                        <Button type="submit" className="w-full" disabled={!canSubmitTotpCode(totpCode)}>
                          Enable TOTP
                        </Button>
                      </form>
                    )}
                  </div>
                )}
              </CardContent>
            </Card>
          </section>
        </main>
      )}
    </div>
  );
}

function ProfileRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid grid-cols-[120px_minmax(0,1fr)] gap-3 text-sm">
      <span className="text-muted-foreground">{label}</span>
      <span className="min-w-0 break-words font-medium text-foreground">{value}</span>
    </div>
  );
}
