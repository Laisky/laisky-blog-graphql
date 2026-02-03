import { ArrowRight, Lock } from 'lucide-react';
import { useMemo, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { StatusBanner, type StatusState } from '@/components/ui/status-banner';
import { fetchGraphQL } from '@/lib/graphql';

const USER_LOGIN_MUTATION = `
  mutation SsoLogin($account: String!, $password: String!) {
    UserLogin(account: $account, password: $password) {
      token
    }
  }
`;

/**
 * SsoLoginResponse describes the GraphQL response for SSO login.
 */
interface SsoLoginResponse {
  UserLogin: {
    token: string;
  };
}

/**
 * RedirectTarget describes the parsed redirect target for the SSO flow.
 */
export interface RedirectTarget {
  url: URL | null;
  display: string;
  error?: string;
}

/**
 * parseRedirectTarget validates and normalizes the redirect_to parameter.
 */
export function parseRedirectTarget(rawValue: string | null, origin: string): RedirectTarget {
  const raw = (rawValue ?? '').trim();
  if (!raw) {
    return {
      url: null,
      display: 'Not provided',
      error: 'Missing redirect_to parameter. Provide a redirect target to continue.',
    };
  }

  const baseOrigin = origin && origin !== 'null' ? origin : 'http://localhost';

  let parsed: URL;
  try {
    parsed = new URL(raw, baseOrigin);
  } catch {
    return {
      url: null,
      display: raw,
      error: 'Invalid redirect_to URL. Please provide a valid URL or path.',
    };
  }

  if (!isAllowedRedirectProtocol(parsed)) {
    return {
      url: null,
      display: parsed.toString(),
      error: 'Unsupported redirect protocol. Only http and https are allowed.',
    };
  }

  return {
    url: parsed,
    display: parsed.toString(),
  };
}

/**
 * isAllowedRedirectProtocol checks whether a redirect URL uses an approved protocol.
 */
export function isAllowedRedirectProtocol(target: URL): boolean {
  const protocol = target.protocol.toLowerCase();
  return protocol === 'http:' || protocol === 'https:';
}

/**
 * buildRedirectUrlWithToken appends the SSO token to the redirect URL.
 */
export function buildRedirectUrlWithToken(target: URL, token: string): string {
  const next = new URL(target.toString());
  next.searchParams.set('sso_token', token);
  return next.toString();
}

/**
 * SsoLoginPage renders the SSO login form and redirects after successful login.
 */
export function SsoLoginPage() {
  const [searchParams] = useSearchParams();
  const redirectParam = searchParams.get('redirect_to');
  const origin = window.location.origin;

  const redirectTarget = useMemo(() => {
    return parseRedirectTarget(redirectParam, origin);
  }, [origin, redirectParam]);

  const [account, setAccount] = useState('');
  const [password, setPassword] = useState('');
  const [status, setStatus] = useState<StatusState | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);

  const canSubmit = Boolean(redirectTarget.url) && account.length > 0 && password.length > 0 && !isSubmitting;

  /**
   * handleSubmit handles the SSO login submit and performs the redirect.
   */
  const handleSubmit = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    if (isSubmitting) {
      return;
    }

    if (!redirectTarget.url) {
      setStatus({ tone: 'error', message: redirectTarget.error ?? 'Missing redirect_to parameter.' });
      return;
    }

    if (!account || !password) {
      setStatus({ tone: 'error', message: 'Please enter your account and password.' });
      return;
    }

    setIsSubmitting(true);
    setStatus({ tone: 'info', message: 'Signing in...' });

    try {
      const data = await fetchGraphQL<SsoLoginResponse>('', USER_LOGIN_MUTATION, {
        account,
        password,
      });

      const token = data.UserLogin?.token;
      if (!token) {
        throw new Error('Login succeeded but no token was returned.');
      }

      const redirectUrl = buildRedirectUrlWithToken(redirectTarget.url, token);
      setStatus({ tone: 'success', message: 'Login successful. Redirecting...' });
      window.location.assign(redirectUrl);
    } catch (error) {
      const message = error instanceof Error ? error.message : 'Login failed. Please try again.';
      setStatus({ tone: 'error', message });
    } finally {
      setIsSubmitting(false);
    }
  };

  const redirectBanner = redirectTarget.url
    ? { status: { tone: 'info', message: 'Redirect destination ready.' }, subtext: redirectTarget.display }
    : { status: { tone: 'error', message: redirectTarget.error ?? 'Missing redirect_to parameter.' }, subtext: redirectTarget.display };

  return (
    <div className="flex min-h-screen items-center justify-center bg-background px-4 py-12">
      <div className="flex w-full max-w-lg flex-col gap-6">
        <div className="space-y-3 text-center">
          <div className="flex items-center justify-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
            <Lock className="h-4 w-4" />
            <span>SSO Login</span>
          </div>
          <h1 className="text-3xl font-bold tracking-tight text-foreground">Sign in to continue</h1>
          <p className="text-sm text-muted-foreground">
            Authenticate with your Laisky account to issue a short-lived SSO token for the requested destination.
          </p>
        </div>

        <Card className="border border-border/60 bg-card shadow-sm">
          <CardHeader className="space-y-2">
            <CardTitle>Account Login</CardTitle>
            <CardDescription>Enter your credentials and we will redirect you with an SSO token.</CardDescription>
            <StatusBanner status={redirectBanner.status} subtext={redirectBanner.subtext} />
          </CardHeader>
          <CardContent className="space-y-4">
            {status && <StatusBanner status={status} />}

            <form className="space-y-4" onSubmit={handleSubmit}>
              <div className="space-y-2">
                <label htmlFor="sso-account" className="text-sm font-medium text-foreground">
                  Account
                </label>
                <Input
                  id="sso-account"
                  name="account"
                  autoComplete="username"
                  placeholder="Email or username"
                  value={account}
                  onChange={(event) => setAccount(event.target.value)}
                />
              </div>

              <div className="space-y-2">
                <label htmlFor="sso-password" className="text-sm font-medium text-foreground">
                  Password
                </label>
                <Input
                  id="sso-password"
                  name="password"
                  type="password"
                  autoComplete="current-password"
                  placeholder="Enter your password"
                  value={password}
                  onChange={(event) => setPassword(event.target.value)}
                />
              </div>

              <Button type="submit" className="w-full" disabled={!canSubmit}>
                {isSubmitting ? 'Signing in...' : 'Sign in and continue'}
                {!isSubmitting && <ArrowRight className="ml-2 h-4 w-4" />}
              </Button>
            </form>
          </CardContent>
        </Card>
      </div>
    </div>
  );
}
