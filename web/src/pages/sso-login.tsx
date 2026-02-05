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
 * Parameters: The interface fields map the UserLogin token payload returned by the API.
 * Returns: The interface is used to type the GraphQL response shape.
 */
interface SsoLoginResponse {
  UserLogin: {
    token: string;
  };
}

/**
 * RedirectTarget describes the parsed redirect target for the SSO flow.
 * Parameters: The interface fields describe the parsed URL, display string, and optional error message.
 * Returns: The interface is used as a shape for parse results.
 */
export interface RedirectTarget {
  url: URL | null;
  display: string;
  error?: string;
}

/**
 * parseRedirectTarget validates and normalizes the redirect_to parameter.
 * Parameters: rawValue is the redirect_to query value, origin is the current page origin for resolving relative URLs.
 * Returns: A RedirectTarget containing the resolved URL, display string, and optional error message.
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

  if (!isAllowedRedirectHost(parsed)) {
    return {
      url: null,
      display: parsed.toString(),
      error: 'Unsupported redirect host. Only *.laisky.com domains or internal IPs are allowed.',
    };
  }

  return {
    url: parsed,
    display: parsed.toString(),
  };
}

/**
 * isAllowedRedirectProtocol checks whether a redirect URL uses an approved protocol.
 * Parameters: target is the parsed redirect URL to inspect.
 * Returns: True when the protocol is http or https.
 */
export function isAllowedRedirectProtocol(target: URL): boolean {
  const protocol = target.protocol.toLowerCase();
  return protocol === 'http:' || protocol === 'https:';
}

/**
 * isAllowedRedirectHost checks whether a redirect URL hostname is permitted.
 * Parameters: target is the parsed redirect URL to inspect.
 * Returns: True when the hostname is a laisky.com domain or an internal IP.
 */
export function isAllowedRedirectHost(target: URL): boolean {
  const hostname = normalizeHostname(target.hostname);
  return isAllowedLaiskyDomain(hostname) || isInternalIPAddress(hostname);
}

/**
 * normalizeHostname lowercases and trims the hostname for comparison.
 * Parameters: hostname is the raw hostname string from the URL.
 * Returns: The normalized hostname without trailing dots.
 */
export function normalizeHostname(hostname: string): string {
  const trimmed = hostname.trim().toLowerCase();
  return trimmed.endsWith('.') ? trimmed.slice(0, -1) : trimmed;
}

/**
 * isAllowedLaiskyDomain verifies that the hostname is laisky.com or a subdomain.
 * Parameters: hostname is the normalized hostname string.
 * Returns: True when hostname matches laisky.com or ends with .laisky.com.
 */
export function isAllowedLaiskyDomain(hostname: string): boolean {
  return hostname === 'laisky.com' || hostname.endsWith('.laisky.com');
}

/**
 * isInternalIPAddress verifies that the hostname is an internal IP address.
 * Parameters: hostname is the normalized hostname string.
 * Returns: True when hostname is an internal IPv4 or IPv6 address.
 */
export function isInternalIPAddress(hostname: string): boolean {
  const ipv4 = parseIPv4Address(hostname);
  if (ipv4) {
    return isInternalIPv4Address(ipv4);
  }

  if (hostname.includes(':')) {
    return isInternalIPv6Address(hostname);
  }

  return false;
}

/**
 * parseIPv4Address parses a dotted IPv4 string into octets.
 * Parameters: hostname is the candidate IPv4 string.
 * Returns: A tuple of octets when valid, otherwise null.
 */
export function parseIPv4Address(hostname: string): [number, number, number, number] | null {
  const parts = hostname.split('.');
  if (parts.length !== 4) {
    return null;
  }

  const octets: number[] = [];
  for (const part of parts) {
    if (!/^\d+$/.test(part)) {
      return null;
    }
    const value = Number(part);
    if (Number.isNaN(value) || value < 0 || value > 255) {
      return null;
    }
    octets.push(value);
  }

  return [octets[0], octets[1], octets[2], octets[3]];
}

/**
 * isInternalIPv4Address checks whether an IPv4 address is in a private range.
 * Parameters: octets is the IPv4 address represented as four octets.
 * Returns: True when the address is within RFC1918 or loopback ranges.
 */
export function isInternalIPv4Address(octets: [number, number, number, number]): boolean {
  const [first, second] = octets;
  if (first === 10) {
    return true;
  }
  if (first === 172 && second >= 16 && second <= 31) {
    return true;
  }
  if (first === 192 && second === 168) {
    return true;
  }
  if (first === 127) {
    return true;
  }
  // 100.64.0.0/10 (Carrier-Grade NAT)
  if (first === 100 && second >= 64 && second <= 127) {
    return true;
  }
  return false;
}

/**
 * isInternalIPv6Address checks whether an IPv6 address is private or loopback.
 * Parameters: hostname is the IPv6 hostname string.
 * Returns: True when the address is within unique-local or loopback ranges.
 */
export function isInternalIPv6Address(hostname: string): boolean {
  let normalized = hostname.toLowerCase();

  // Strip brackets if present (e.g., [fd00::1] -> fd00::1)
  if (normalized.startsWith('[') && normalized.endsWith(']')) {
    normalized = normalized.slice(1, -1);
  }

  if (normalized === '::1') {
    return true;
  }

  if (normalized.includes('.')) {
    const ipv4Part = normalized.slice(normalized.lastIndexOf(':') + 1);
    const ipv4 = parseIPv4Address(ipv4Part);
    if (ipv4) {
      return isInternalIPv4Address(ipv4);
    }
  }

  const firstHextet = normalized.split(':').find((part) => part.length > 0);
  if (!firstHextet) {
    return false;
  }

  const value = Number.parseInt(firstHextet, 16);
  if (Number.isNaN(value)) {
    return false;
  }

  return (value & 0xfe00) === 0xfc00;
}

/**
 * buildRedirectUrlWithToken appends the SSO token to the redirect URL.
 * Parameters: target is the redirect URL to update, token is the SSO token to append.
 * Returns: The redirect URL string containing the sso_token query parameter.
 */
export function buildRedirectUrlWithToken(target: URL, token: string): string {
  const next = new URL(target.toString());
  next.searchParams.set('sso_token', token);
  return next.toString();
}

/**
 * SsoLoginPage renders the SSO login form and redirects after successful login.
 * Parameters: This component does not accept props.
 * Returns: A JSX element containing the SSO login form.
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
   * Parameters: event is the form submit event to prevent default navigation.
   * Returns: A Promise that resolves after handling the login attempt.
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

  const redirectStatus: StatusState = redirectTarget.url
    ? { tone: 'info', message: 'Redirect destination ready.' }
    : { tone: 'error', message: redirectTarget.error ?? 'Missing redirect_to parameter.' };
  const redirectBanner = { status: redirectStatus, subtext: redirectTarget.display };

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
