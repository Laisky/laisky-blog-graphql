/* eslint-disable react-refresh/only-export-components */
import { ArrowRight, Github, KeyRound, Lock } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

import { ThemeToggle } from '@/components/theme/theme-toggle';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { LaiskyLink } from '@/components/ui/laisky-link';
import { StatusBanner, type StatusState } from '@/components/ui/status-banner';
import { fetchGraphQL } from '@/lib/graphql';
import { getPasskeyCredentialJSON } from '@/lib/passkey';
import { buildRedirectUrlWithToken, parseRedirectTarget } from '@/lib/sso-redirect';

export {
  buildRedirectUrlWithToken,
  isAllowedLaiskyDomain,
  isAllowedRedirectHost,
  isAllowedRedirectProtocol,
  isInternalIPAddress,
  isInternalIPv4Address,
  isInternalIPv6Address,
  normalizeHostname,
  parseIPv4Address,
  parseRedirectTarget,
} from '@/lib/sso-redirect';

const USER_LOGIN_MUTATION = `
  mutation SsoLogin($account: String!, $password: String!, $turnstileToken: String, $totpCode: String) {
    UserLogin(account: $account, password: $password, turnstile_token: $turnstileToken, totp_code: $totpCode) {
      token
    }
  }
`;

const USER_REGISTER_MUTATION = `
  mutation SsoRegister($account: String!, $password: String!, $displayName: String!, $turnstileToken: String) {
    UserRegister(account: $account, password: $password, display_name: $displayName, captcha: "", turnstile_token: $turnstileToken) {
      msg
    }
  }
`;

const USER_GITHUB_OAUTH_START_MUTATION = `
  mutation StartGithubOAuth($redirectTo: String, $turnstileToken: String) {
    UserGithubOAuthStart(redirect_to: $redirectTo, turnstile_token: $turnstileToken) {
      authorize_url
    }
  }
`;

const USER_PASSKEY_LOGIN_START_MUTATION = `
  mutation StartPasskeyLogin($redirectTo: String, $turnstileToken: String) {
    UserStartPasskeyLogin(redirect_to: $redirectTo, turnstile_token: $turnstileToken) {
      options_json
      session
    }
  }
`;

const USER_PASSKEY_LOGIN_FINISH_MUTATION = `
  mutation FinishPasskeyLogin($session: String!, $credentialJSON: String!) {
    UserFinishPasskeyLogin(session: $session, credential_json: $credentialJSON) {
      token
      redirect_to
    }
  }
`;

const TURNSTILE_SCRIPT_ID = 'cloudflare-turnstile-script';
const TURNSTILE_SCRIPT_SRC = 'https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit';

interface SsoLoginResponse {
  UserLogin: {
    token: string;
  };
}

interface SsoRegisterResponse {
  UserRegister: {
    msg: string;
  };
}

interface SsoGithubOAuthStartResponse {
  UserGithubOAuthStart: {
    authorize_url: string;
  };
}

interface SsoPasskeyStartResponse {
  UserStartPasskeyLogin: {
    options_json: string;
    session: string;
  };
}

interface SsoPasskeyLoginResponse {
  UserFinishPasskeyLogin: {
    token: string;
    redirect_to: string;
  };
}

export interface SsoLoginPageProps {
  turnstileSiteKey?: string;
}

interface TurnstileRenderOptions {
  sitekey: string;
  callback: (token: string) => void;
  'expired-callback'?: () => void;
  'error-callback'?: () => void;
  'timeout-callback'?: () => void;
}

interface TurnstileAPI {
  render: (container: HTMLElement, options: TurnstileRenderOptions) => string;
  reset: (widgetId?: string) => void;
  remove: (widgetId: string) => void;
}

declare global {
  interface Window {
    turnstile?: TurnstileAPI;
  }
}

export interface SsoSubmitState {
  mode: 'login' | 'register';
  hasRedirectTarget: boolean;
  account: string;
  password: string;
  displayName: string;
  isSubmitting: boolean;
  isTurnstileEnabled: boolean;
  turnstileToken: string;
}

export interface SsoGithubStartState {
  isSubmitting: boolean;
  isTurnstileEnabled: boolean;
  turnstileToken: string;
}

export function isTurnstileEnabled(siteKey: string | undefined): boolean {
  return (siteKey ?? '').trim().length > 0;
}

export function canSubmitSsoLogin(state: SsoSubmitState): boolean {
  if (state.mode === 'login' && !state.hasRedirectTarget) {
    return false;
  }

  if (state.isSubmitting) {
    return false;
  }

  if (state.account.trim().length === 0 || state.password.trim().length === 0) {
    return false;
  }

  if (state.mode === 'register' && state.displayName.trim().length === 0) {
    return false;
  }

  if (state.isTurnstileEnabled && state.turnstileToken.trim().length === 0) {
    return false;
  }

  return true;
}

export function canStartGithubOAuth(state: SsoGithubStartState): boolean {
  if (state.isSubmitting) {
    return false;
  }

  if (state.isTurnstileEnabled && state.turnstileToken.trim().length === 0) {
    return false;
  }

  return true;
}

function getTurnstileAPI(): TurnstileAPI | undefined {
  return window.turnstile;
}

export function loadTurnstileScript(documentRef: Document): Promise<TurnstileAPI> {
  const existing = getTurnstileAPI();
  if (existing) {
    return Promise.resolve(existing);
  }

  return new Promise<TurnstileAPI>((resolve, reject) => {
    let script: HTMLScriptElement | null = null;

    const cleanup = (tid: number | undefined) => {
      if (tid !== undefined) {
        window.clearTimeout(tid);
      }
      if (script) {
        script.removeEventListener('load', resolveIfReady);
        script.removeEventListener('error', rejectWithError);
      }
    };

    const resolveIfReady = () => {
      const api = getTurnstileAPI();
      if (!api) {
        cleanup(timeoutID);
        reject(new Error('Turnstile script loaded but API is unavailable.'));
        return;
      }
      cleanup(timeoutID);
      resolve(api);
    };

    const rejectWithError = () => {
      cleanup(timeoutID);
      reject(new Error('Failed to load Cloudflare Turnstile script.'));
    };

    const timeoutID = window.setTimeout(() => {
      rejectWithError();
    }, 10_000);

    const existingScript = documentRef.getElementById(TURNSTILE_SCRIPT_ID);
    if (existingScript instanceof HTMLScriptElement) {
      script = existingScript;
      script.addEventListener('load', resolveIfReady);
      script.addEventListener('error', rejectWithError);
      const api = getTurnstileAPI();
      if (api) {
        resolveIfReady();
      }
      return;
    }

    script = documentRef.createElement('script');
    script.id = TURNSTILE_SCRIPT_ID;
    script.src = TURNSTILE_SCRIPT_SRC;
    script.async = true;
    script.defer = true;
    script.addEventListener('load', resolveIfReady);
    script.addEventListener('error', rejectWithError);
    documentRef.head.appendChild(script);
  });
}

export function SsoLoginPage(props: SsoLoginPageProps) {
  const resolvedTurnstileSiteKey = (props.turnstileSiteKey ?? '').trim();
  const turnstileEnabled = isTurnstileEnabled(resolvedTurnstileSiteKey);
  const [searchParams] = useSearchParams();
  const redirectParam = searchParams.get('redirect_to');
  const origin = window.location.origin;

  const redirectTarget = useMemo(() => {
    return parseRedirectTarget(redirectParam, origin);
  }, [origin, redirectParam]);

  const [mode, setMode] = useState<'login' | 'register'>('login');
  const [account, setAccount] = useState('');
  const [displayName, setDisplayName] = useState('');
  const [password, setPassword] = useState('');
  const [totpCode, setTotpCode] = useState('');
  const [status, setStatus] = useState<StatusState | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [isGithubSubmitting, setIsGithubSubmitting] = useState(false);
  const [isPasskeySubmitting, setIsPasskeySubmitting] = useState(false);
  const [turnstileToken, setTurnstileToken] = useState('');
  const [turnstileMessage, setTurnstileMessage] = useState<string | null>(null);
  const turnstileContainerRef = useRef<HTMLDivElement | null>(null);
  const turnstileWidgetIDRef = useRef<string | null>(null);

  const canSubmit = canSubmitSsoLogin({
    mode,
    hasRedirectTarget: Boolean(redirectTarget.url),
    account,
    password,
    displayName,
    isSubmitting,
    isTurnstileEnabled: turnstileEnabled,
    turnstileToken,
  });
  const canStartGithub = canStartGithubOAuth({
    isSubmitting: isSubmitting || isGithubSubmitting || isPasskeySubmitting,
    isTurnstileEnabled: turnstileEnabled,
    turnstileToken,
  });
  const canStartPasskey = canStartGithubOAuth({
    isSubmitting: isSubmitting || isGithubSubmitting || isPasskeySubmitting,
    isTurnstileEnabled: turnstileEnabled,
    turnstileToken,
  });

  const resetTurnstile = () => {
    const widgetID = turnstileWidgetIDRef.current;
    const api = getTurnstileAPI();
    if (widgetID && api) {
      api.reset(widgetID);
    }
    setTurnstileToken('');
  };

  useEffect(() => {
    if (!turnstileEnabled) {
      setTurnstileToken('');
      setTurnstileMessage(null);
      return;
    }

    let isCancelled = false;
    setTurnstileMessage('Loading security challenge...');
    setTurnstileToken('');

    loadTurnstileScript(window.document)
      .then((turnstile) => {
        if (isCancelled || !turnstileContainerRef.current) {
          return;
        }

        const widgetID = turnstile.render(turnstileContainerRef.current, {
          sitekey: resolvedTurnstileSiteKey,
          callback: (token: string) => {
            setTurnstileToken(token);
            setTurnstileMessage(null);
          },
          'expired-callback': () => {
            setTurnstileToken('');
            setTurnstileMessage('Security verification expired. Please complete it again.');
          },
          'error-callback': () => {
            setTurnstileToken('');
            setTurnstileMessage('Security verification failed. Please retry.');
          },
          'timeout-callback': () => {
            setTurnstileToken('');
            setTurnstileMessage('Security verification timed out. Please retry.');
          },
        });

        turnstileWidgetIDRef.current = widgetID;
        setTurnstileMessage('Please complete the security verification.');
      })
      .catch(() => {
        if (!isCancelled) {
          setTurnstileToken('');
          setTurnstileMessage('Unable to load security verification. Refresh and try again.');
        }
      });

    return () => {
      isCancelled = true;
      const widgetID = turnstileWidgetIDRef.current;
      const api = getTurnstileAPI();
      if (widgetID && api) {
        api.remove(widgetID);
      }
      turnstileWidgetIDRef.current = null;
    };
  }, [resolvedTurnstileSiteKey, turnstileEnabled]);

  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (isSubmitting) {
      return;
    }

    if (mode === 'login' && !redirectTarget.url) {
      setStatus({ tone: 'error', message: redirectTarget.error ?? 'Missing redirect_to parameter.' });
      return;
    }

    if (!account || !password) {
      setStatus({ tone: 'error', message: 'Please enter your account and password.' });
      return;
    }

    if (mode === 'register' && !displayName) {
      setStatus({ tone: 'error', message: 'Please enter your display name.' });
      return;
    }

    if (turnstileEnabled && !turnstileToken) {
      setStatus({ tone: 'error', message: 'Please complete the security verification challenge.' });
      return;
    }

    setIsSubmitting(true);
    setStatus({ tone: 'info', message: mode === 'login' ? 'Authenticating...' : 'Creating account...' });

    try {
      if (mode === 'register') {
        const data = await fetchGraphQL<SsoRegisterResponse>('', USER_REGISTER_MUTATION, {
          account,
          password,
          displayName,
          turnstileToken: turnstileEnabled ? turnstileToken : null,
        });
        setStatus({ tone: 'success', message: data.UserRegister?.msg ?? 'Registration complete. You can sign in now.' });
        setMode('login');
        setPassword('');
        if (turnstileEnabled) {
          resetTurnstile();
          setTurnstileMessage('Please complete the security verification again.');
        }
        return;
      }

      const data = await fetchGraphQL<SsoLoginResponse>('', USER_LOGIN_MUTATION, {
        account,
        password,
        totpCode: totpCode.trim() ? totpCode : null,
        turnstileToken: turnstileEnabled ? turnstileToken : null,
      });

      const token = data.UserLogin?.token;
      if (!token) {
        throw new Error('Login succeeded but no token was returned.');
      }

      const target = redirectTarget.url;
      if (!target) {
        throw new Error('Missing redirect target.');
      }

      const redirectUrl = buildRedirectUrlWithToken(target, token);
      setStatus({ tone: 'success', message: 'Login successful. Redirecting...' });
      window.location.assign(redirectUrl);
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Login failed. Please try again.';
      setStatus({ tone: 'error', message });
      if (turnstileEnabled) {
        resetTurnstile();
        setTurnstileMessage('Please complete the security verification again.');
      }
    } finally {
      setIsSubmitting(false);
    }
  };

  const handleGithubOAuth = async () => {
    if (isSubmitting || isGithubSubmitting) {
      return;
    }

    if (redirectParam && !redirectTarget.url) {
      setStatus({ tone: 'error', message: redirectTarget.error ?? 'Invalid redirect_to parameter.' });
      return;
    }

    if (turnstileEnabled && !turnstileToken) {
      setStatus({ tone: 'error', message: 'Please complete the security verification challenge.' });
      return;
    }

    setIsGithubSubmitting(true);
    setStatus({ tone: 'info', message: 'Opening GitHub...' });
    try {
      const data = await fetchGraphQL<SsoGithubOAuthStartResponse>('', USER_GITHUB_OAUTH_START_MUTATION, {
        redirectTo: redirectTarget.url ? redirectTarget.url.toString() : null,
        turnstileToken: turnstileEnabled ? turnstileToken : null,
      });
      const authorizeURL = data.UserGithubOAuthStart?.authorize_url;
      if (!authorizeURL) {
        throw new Error('GitHub authorization URL was not returned.');
      }
      window.location.assign(authorizeURL);
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Failed to start GitHub sign in.';
      setStatus({ tone: 'error', message });
      if (turnstileEnabled) {
        resetTurnstile();
        setTurnstileMessage('Please complete the security verification again.');
      }
    } finally {
      setIsGithubSubmitting(false);
    }
  };

  const handlePasskeyLogin = async () => {
    if (isSubmitting || isGithubSubmitting || isPasskeySubmitting) {
      return;
    }

    if (redirectParam && !redirectTarget.url) {
      setStatus({ tone: 'error', message: redirectTarget.error ?? 'Invalid redirect_to parameter.' });
      return;
    }

    if (turnstileEnabled && !turnstileToken) {
      setStatus({ tone: 'error', message: 'Please complete the security verification challenge.' });
      return;
    }

    setIsPasskeySubmitting(true);
    setStatus({ tone: 'info', message: 'Waiting for passkey...' });
    try {
      const start = await fetchGraphQL<SsoPasskeyStartResponse>('', USER_PASSKEY_LOGIN_START_MUTATION, {
        redirectTo: redirectTarget.url ? redirectTarget.url.toString() : null,
        turnstileToken: turnstileEnabled ? turnstileToken : null,
      });
      const credentialJSON = await getPasskeyCredentialJSON(start.UserStartPasskeyLogin.options_json);
      const finish = await fetchGraphQL<SsoPasskeyLoginResponse>('', USER_PASSKEY_LOGIN_FINISH_MUTATION, {
        session: start.UserStartPasskeyLogin.session,
        credentialJSON,
      });
      const token = finish.UserFinishPasskeyLogin?.token;
      if (!token) {
        throw new Error('Passkey login succeeded but no token was returned.');
      }
      const target = new URL(finish.UserFinishPasskeyLogin.redirect_to || '/profile', window.location.origin);
      setStatus({ tone: 'success', message: 'Passkey accepted. Redirecting...' });
      window.location.assign(buildRedirectUrlWithToken(target, token));
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Passkey login failed.';
      setStatus({ tone: 'error', message });
      if (turnstileEnabled) {
        resetTurnstile();
        setTurnstileMessage('Please complete the security verification again.');
      }
    } finally {
      setIsPasskeySubmitting(false);
    }
  };

  const redirectStatus: StatusState =
    mode === 'register'
      ? { tone: 'info', message: 'Registration mode ready.' }
      : redirectTarget.url
        ? { tone: 'info', message: 'Redirect destination ready.' }
        : { tone: 'error', message: redirectTarget.error ?? 'Missing redirect_to parameter.' };
  const redirectBanner = { status: redirectStatus, subtext: mode === 'register' ? account || 'New account' : redirectTarget.display };

  return (
    <div className="relative flex min-h-screen items-center justify-center bg-background px-4 py-12 text-foreground transition-colors">
      <div className="absolute inset-0 z-0 bg-[linear-gradient(to_right,hsl(var(--border)/0.3)_1px,transparent_1px),linear-gradient(to_bottom,hsl(var(--border)/0.3)_1px,transparent_1px)] bg-[size:24px_24px]" />

      <div className="absolute right-4 top-4 z-10 sm:right-6 sm:top-6">
        <ThemeToggle />
      </div>

      <main className="z-10 flex w-full max-w-lg flex-col gap-6">
        <div className="space-y-3 text-center">
          <div className="flex items-center justify-center gap-2 text-sm font-medium uppercase tracking-widest text-primary font-mono">
            <Lock className="h-4 w-4" />
            <span>
              <LaiskyLink className="text-primary underline-offset-4 hover:underline">Laisky</LaiskyLink> SSO
            </span>
          </div>
        </div>

        <Card className="border-border/60 bg-card/80 text-card-foreground shadow-2xl backdrop-blur-sm transition-colors">
          <CardHeader className="space-y-2 border-b border-border/40 pb-6">
            <StatusBanner status={redirectBanner.status} subtext={redirectBanner.subtext} />
          </CardHeader>
          <CardContent className="space-y-6 pt-6">
            {status && <StatusBanner status={status} />}

            <div className="grid grid-cols-2 gap-2 rounded-lg bg-muted p-1">
              <button
                type="button"
                className={`rounded-md px-3 py-2 text-sm font-semibold transition-colors ${mode === 'login' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'}`}
                onClick={() => setMode('login')}
              >
                Sign in
              </button>
              <button
                type="button"
                className={`rounded-md px-3 py-2 text-sm font-semibold transition-colors ${mode === 'register' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground hover:text-foreground'}`}
                onClick={() => setMode('register')}
              >
                Register
              </button>
            </div>

            <Button
              type="button"
              variant="outline"
              className="w-full h-12 font-bold uppercase tracking-widest font-mono transition-[color,background-color,transform] active:scale-95"
              disabled={!canStartGithub}
              onClick={handleGithubOAuth}
            >
              <Github className="mr-2 h-4 w-4" />
              {isGithubSubmitting ? 'Opening GitHub...' : 'Continue with GitHub'}
            </Button>

            <Button
              type="button"
              variant="outline"
              className="w-full h-12 font-bold uppercase tracking-widest font-mono transition-[color,background-color,transform] active:scale-95"
              disabled={!canStartPasskey}
              onClick={handlePasskeyLogin}
            >
              <KeyRound className="mr-2 h-4 w-4" />
              {isPasskeySubmitting ? 'Waiting for Passkey...' : 'Sign in with Passkey'}
            </Button>

            <form className="space-y-6" onSubmit={handleSubmit}>
              <div className="space-y-2">
                <label htmlFor="sso-account" className="text-xs font-bold text-muted-foreground font-mono uppercase tracking-widest pl-1">
                  Account ID
                </label>
                <Input
                  id="sso-account"
                  name="account"
                  autoComplete="username"
                  placeholder="USER_IDENTIFIER"
                  value={account}
                  onChange={(event) => setAccount(event.target.value)}
                  className="h-12 font-mono transition-colors"
                />
              </div>

              {mode === 'register' && (
                <div className="space-y-2">
                  <label
                    htmlFor="sso-display-name"
                    className="text-xs font-bold text-muted-foreground font-mono uppercase tracking-widest pl-1"
                  >
                    Display Name
                  </label>
                  <Input
                    id="sso-display-name"
                    name="displayName"
                    autoComplete="name"
                    placeholder="Alice"
                    value={displayName}
                    onChange={(event) => setDisplayName(event.target.value)}
                    className="h-12 transition-colors"
                  />
                </div>
              )}

              <div className="space-y-2">
                <label htmlFor="sso-password" className="text-xs font-bold text-muted-foreground font-mono uppercase tracking-widest pl-1">
                  Passphrase
                </label>
                <Input
                  id="sso-password"
                  name="password"
                  type="password"
                  autoComplete="current-password"
                  placeholder="••••••••••••"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                  className="h-12 font-mono transition-colors"
                />
              </div>

              {mode === 'login' && (
                <div className="space-y-2">
                  <label htmlFor="sso-totp" className="text-xs font-bold text-muted-foreground font-mono uppercase tracking-widest pl-1">
                    TOTP Code
                  </label>
                  <Input
                    id="sso-totp"
                    name="totp"
                    inputMode="numeric"
                    autoComplete="one-time-code"
                    placeholder="000000"
                    value={totpCode}
                    onChange={(event) => setTotpCode(event.target.value)}
                    className="h-12 font-mono transition-colors"
                  />
                </div>
              )}

              {turnstileEnabled && (
                <div className="space-y-2">
                  <label className="text-xs font-bold text-muted-foreground font-mono uppercase tracking-widest pl-1">Security Check</label>
                  <div
                    ref={turnstileContainerRef}
                    className="flex min-h-20 items-center justify-center rounded-md border border-input bg-background px-2 py-3 transition-colors"
                  />
                  {turnstileMessage && <p className="text-xs text-muted-foreground font-mono">{turnstileMessage}</p>}
                </div>
              )}

              <Button
                type="submit"
                className="w-full h-12 font-bold uppercase tracking-widest font-mono transition-[color,background-color,transform] active:scale-95"
                disabled={!canSubmit}
              >
                {isSubmitting ? (mode === 'login' ? 'Authenticating...' : 'Creating...') : mode === 'login' ? 'Login' : 'Create Account'}
                {!isSubmitting && <ArrowRight className="ml-2 h-4 w-4" />}
              </Button>
            </form>
          </CardContent>
        </Card>
      </main>
    </div>
  );
}
