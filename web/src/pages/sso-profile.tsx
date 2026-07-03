/* eslint-disable react-refresh/only-export-components */
import { Github, KeyRound, Lock, LogOut, RefreshCcw, Save, ShieldCheck, ShieldPlus, Trash2, UserRound } from 'lucide-react';
import { QRCodeSVG } from 'qrcode.react';
import { useCallback, useEffect, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router-dom';

import { ThemeToggle } from '@/components/theme/theme-toggle';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { LaiskyLink } from '@/components/ui/laisky-link';
import { StatusBanner, type StatusState } from '@/components/ui/status-banner';
import { fetchGraphQL } from '@/lib/graphql';
import { createPasskeyCredentialJSON } from '@/lib/passkey';
import type { SsoJwtConfig } from '@/lib/runtime-config';
import { clearSsoToken, consumeSsoTokenFromLocation, resolveSiblingSsoPath } from '@/lib/sso-session';
import { SsoTokenDetailsDialog } from '@/pages/sso-token-details';

const SSO_PROFILE_FIELDS = `
  uid
  account
  auth_methods
  password_enabled
  totp_enabled
  passkey_count
  passkeys {
    id
    name
    created_at
  }
  github_bound
  user {
    id
    username
  }
`;

const USER_PROFILE_QUERY = `
  query SsoProfile {
    UserProfile {
      ${SSO_PROFILE_FIELDS}
    }
  }
`;

const CHANGE_PASSWORD_MUTATION = `
  mutation ChangePassword($currentPassword: String!, $newPassword: String!) {
    UserChangePassword(current_password: $currentPassword, new_password: $newPassword) {
      ${SSO_PROFILE_FIELDS}
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
      ${SSO_PROFILE_FIELDS}
    }
  }
`;

const DISABLE_TOTP_MUTATION = `
  mutation DisableTotp($currentPassword: String!) {
    UserDisableTOTP(current_password: $currentPassword) {
      ${SSO_PROFILE_FIELDS}
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
      ${SSO_PROFILE_FIELDS}
    }
  }
`;

const RENAME_PASSKEY_MUTATION = `
  mutation RenamePasskey($passkeyID: String!, $name: String!) {
    UserRenamePasskey(passkey_id: $passkeyID, name: $name) {
      ${SSO_PROFILE_FIELDS}
    }
  }
`;

const DELETE_PASSKEY_MUTATION = `
  mutation DeletePasskey($passkeyID: String!) {
    UserDeletePasskey(passkey_id: $passkeyID) {
      ${SSO_PROFILE_FIELDS}
    }
  }
`;

const START_GITHUB_BIND_MUTATION = `
  mutation StartGithubBind {
    UserGithubOAuthBindStart {
      authorize_url
    }
  }
`;

interface PasskeyInfo {
  id: string;
  name: string;
  created_at: string;
}

interface SsoProfile {
  uid: string;
  account: string;
  auth_methods: string[];
  password_enabled: boolean;
  totp_enabled: boolean;
  passkey_count: number;
  passkeys: PasskeyInfo[];
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
  UserRenamePasskey?: SsoProfile;
  UserDeletePasskey?: SsoProfile;
}

interface GithubBindStartResponse {
  UserGithubOAuthBindStart: {
    authorize_url: string;
  };
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

export function canSubmitPasskeyRename(currentName: string, nextName: string): boolean {
  const normalizedCurrent = currentName.trim();
  const normalizedNext = nextName.trim();
  return normalizedNext.length > 0 && normalizedNext.length <= 100 && normalizedNext !== normalizedCurrent;
}

export function formatPasskeyCreatedAt(createdAt: string): string {
  const date = new Date(createdAt);
  if (Number.isNaN(date.getTime())) {
    return createdAt;
  }

  const iso = date.toISOString();
  return `${iso.slice(0, 10)} ${iso.slice(11, 16)} UTC`;
}

function buildPasskeyDraftNames(passkeys: PasskeyInfo[]): Record<string, string> {
  return Object.fromEntries(passkeys.map((passkey) => [passkey.id, passkey.name]));
}

interface SsoProfilePageProps {
  githubOAuthEnabled?: boolean;
  ssoJwt?: SsoJwtConfig | null;
}

export function SsoProfilePage({ githubOAuthEnabled = false, ssoJwt = null }: SsoProfilePageProps) {
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
  const [passkeyDraftNames, setPasskeyDraftNames] = useState<Record<string, string>>({});
  const [passkeyBusyID, setPasskeyBusyID] = useState('');
  const [isGithubBinding, setIsGithubBinding] = useState(false);

  const applyProfile = useCallback((next: SsoProfile) => {
    setProfile(next);
    setPasskeyDraftNames(buildPasskeyDraftNames(next.passkeys));
  }, []);

  const loadProfile = useCallback(async (authToken: string) => {
    setIsLoading(true);
    setStatus(null);
    try {
      const data = await fetchGraphQL<ProfileResponse>(authToken, USER_PROFILE_QUERY);
      applyProfile(data.UserProfile);
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to load profile.' });
    } finally {
      setIsLoading(false);
    }
  }, [applyProfile]);

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
  }, [loadProfile]);

  const handleSignOut = () => {
    clearSsoToken();
    setToken('');
    setProfile(null);
    navigate(loginPath);
  };

  const updateProfile = (next: SsoProfile | undefined, message: string) => {
    if (next) {
      applyProfile(next);
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
      setPasskeyLabel('My passkey');
      updateProfile(finish.UserFinishPasskeyRegistration, 'Passkey registered.');
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to register passkey.' });
    } finally {
      setIsPasskeyRegistering(false);
    }
  };

  const handlePasskeyNameChange = (passkeyID: string, name: string) => {
    setPasskeyDraftNames((current) => ({
      ...current,
      [passkeyID]: name,
    }));
  };

  const handleRenamePasskey = async (event: React.FormEvent<HTMLFormElement>, passkey: PasskeyInfo) => {
    event.preventDefault();
    const nextName = (passkeyDraftNames[passkey.id] || '').trim();
    if (!canSubmitPasskeyRename(passkey.name, nextName)) {
      setStatus({ tone: 'error', message: 'Enter a different passkey name.' });
      return;
    }

    setPasskeyBusyID(passkey.id);
    try {
      const data = await fetchGraphQL<ProfileMutationResponse>(token, RENAME_PASSKEY_MUTATION, {
        passkeyID: passkey.id,
        name: nextName,
      });
      updateProfile(data.UserRenamePasskey, 'Passkey renamed.');
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to rename passkey.' });
    } finally {
      setPasskeyBusyID('');
    }
  };

  const handleDeletePasskey = async (passkey: PasskeyInfo) => {
    if (!window.confirm(`Delete passkey "${passkey.name}"?`)) {
      return;
    }

    setPasskeyBusyID(passkey.id);
    try {
      const data = await fetchGraphQL<ProfileMutationResponse>(token, DELETE_PASSKEY_MUTATION, { passkeyID: passkey.id });
      updateProfile(data.UserDeletePasskey, 'Passkey deleted.');
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to delete passkey.' });
    } finally {
      setPasskeyBusyID('');
    }
  };

  const handleBindGithub = async () => {
    if (!token || isGithubBinding || profile?.github_bound) {
      return;
    }

    setIsGithubBinding(true);
    setStatus({ tone: 'info', message: 'Opening GitHub...' });
    try {
      const data = await fetchGraphQL<GithubBindStartResponse>(token, START_GITHUB_BIND_MUTATION);
      const authorizeURL = data.UserGithubOAuthBindStart?.authorize_url;
      if (!authorizeURL) {
        throw new Error('GitHub authorization URL was not returned.');
      }
      window.location.assign(authorizeURL);
    } catch (error) {
      setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'Failed to start GitHub binding.' });
      setIsGithubBinding(false);
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
                    <ProfileRow label="UID" value={profile.uid} />
                    <ProfileRow label="Account" value={profile.account} />
                    <ProfileRow label="Methods" value={authMethods} />
                    <ProfileRow label="Passkeys" value={String(profile.passkey_count)} />
                    <ProfileRow label="GitHub OIDC" value={profile.github_bound ? 'Bound' : 'Not bound'} />
                    <SsoTokenDetailsDialog ssoJwt={ssoJwt} currentToken={token} />
                    {githubOAuthEnabled && !profile.github_bound && (
                      <Button type="button" variant="outline" className="w-full gap-2" disabled={isGithubBinding} onClick={() => void handleBindGithub()}>
                        <Github className="h-4 w-4" />
                        {isGithubBinding ? 'Opening GitHub...' : 'Bind GitHub'}
                      </Button>
                    )}
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
              <CardContent className="space-y-5 pt-6">
                {profile && profile.passkeys.length > 0 && (
                  <div className="space-y-3">
                    {profile.passkeys.map((passkey) => {
                      const draftName = passkeyDraftNames[passkey.id] || '';
                      const isBusy = passkeyBusyID === passkey.id;
                      return (
                        <form
                          key={passkey.id}
                          className="space-y-3 rounded-md border border-border bg-muted/30 p-3"
                          onSubmit={(event) => void handleRenamePasskey(event, passkey)}
                        >
                          <div className="flex flex-col gap-2 sm:flex-row">
                            <Input
                              autoComplete="off"
                              value={draftName}
                              onChange={(event) => handlePasskeyNameChange(passkey.id, event.target.value)}
                              aria-label={`Name for passkey ${passkey.name}`}
                            />
                            <div className="flex shrink-0 gap-2">
                              <Button
                                type="submit"
                                variant="outline"
                                size="icon"
                                disabled={isBusy || !canSubmitPasskeyRename(passkey.name, draftName)}
                                aria-label={`Rename passkey ${passkey.name}`}
                                title="Rename passkey"
                              >
                                <Save className="h-4 w-4" />
                              </Button>
                              <Button
                                type="button"
                                variant="destructive"
                                size="icon"
                                disabled={isBusy}
                                aria-label={`Delete passkey ${passkey.name}`}
                                title="Delete passkey"
                                onClick={() => void handleDeletePasskey(passkey)}
                              >
                                <Trash2 className="h-4 w-4" />
                              </Button>
                            </div>
                          </div>
                          <div className="grid gap-1 text-xs text-muted-foreground sm:grid-cols-[80px_minmax(0,1fr)]">
                            <span>Created</span>
                            <span className="min-w-0 break-words">{formatPasskeyCreatedAt(passkey.created_at)}</span>
                            <span>ID</span>
                            <span className="min-w-0 break-all font-mono">{passkey.id.slice(0, 24)}</span>
                          </div>
                        </form>
                      );
                    })}
                  </div>
                )}
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
                        <div className="grid gap-4 rounded-md border border-border bg-muted p-3 sm:grid-cols-[180px_minmax(0,1fr)]">
                          <div className="flex justify-center rounded-md bg-white p-3">
                            <QRCodeSVG
                              value={totpSetup.provisioning_uri}
                              size={156}
                              level="M"
                              marginSize={2}
                              title="TOTP setup QR code"
                              role="img"
                              aria-label="TOTP setup QR code"
                            />
                          </div>
                          <div className="min-w-0 space-y-2 font-mono text-xs text-muted-foreground">
                            <div className="break-all">{totpSetup.secret}</div>
                            <div className="break-all">{totpSetup.provisioning_uri}</div>
                          </div>
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
