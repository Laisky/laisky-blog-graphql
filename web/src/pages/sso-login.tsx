import { ArrowRight, Lock } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';

import { ThemeToggle } from '@/components/theme/theme-toggle';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { StatusBanner, type StatusState } from '@/components/ui/status-banner';
import { fetchGraphQL } from '@/lib/graphql';

const USER_LOGIN_MUTATION = `
  mutation SsoLogin($account: String!, $password: String!, $turnstileToken: String) {
    UserLogin(account: $account, password: $password, turnstile_token: $turnstileToken) {
      token
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
  hasRedirectTarget: boolean;
  account: string;
  password: string;
  isSubmitting: boolean;
  isTurnstileEnabled: boolean;
  turnstileToken: string;
}

export function isTurnstileEnabled(siteKey: string | undefined): boolean {
  return (siteKey ?? '').trim().length > 0;
}

export function canSubmitSsoLogin(state: SsoSubmitState): boolean {
  if (!state.hasRedirectTarget || state.isSubmitting) {
    return false;
  }

  if (state.account.trim().length === 0 || state.password.trim().length === 0) {
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
    let timeoutID: number | undefined;
    let script: HTMLScriptElement | null = null;

    const cleanup = () => {
      if (timeoutID !== undefined) {
        window.clearTimeout(timeoutID);
      }
      if (script) {
        script.removeEventListener('load', resolveIfReady);
        script.removeEventListener('error', rejectWithError);
      }
    };

    const resolveIfReady = () => {
      const api = getTurnstileAPI();
      if (!api) {
        cleanup();
        reject(new Error('Turnstile script loaded but API is unavailable.'));
        return;
      }
      cleanup();
      resolve(api);
    };

    const rejectWithError = () => {
      cleanup();
      reject(new Error('Failed to load Cloudflare Turnstile script.'));
    };

    timeoutID = window.setTimeout(() => {
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

export interface RedirectTarget {
  url: URL | null;
  display: string;
  error?: string;
}

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

export function isAllowedRedirectProtocol(target: URL): boolean {
  const protocol = target.protocol.toLowerCase();
  return protocol === 'http:' || protocol === 'https:';
}

export function isAllowedRedirectHost(target: URL): boolean {
  const hostname = normalizeHostname(target.hostname);
  return isAllowedLaiskyDomain(hostname) || isInternalIPAddress(hostname);
}

export function normalizeHostname(hostname: string): string {
  const trimmed = hostname.trim().toLowerCase();
  return trimmed.endsWith('.') ? trimmed.slice(0, -1) : trimmed;
}

export function isAllowedLaiskyDomain(hostname: string): boolean {
  return hostname === 'laisky.com' || hostname.endsWith('.laisky.com');
}

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

export function buildRedirectUrlWithToken(target: URL, token: string): string {
  const next = new URL(target.toString());
  next.searchParams.set('sso_token', token);
  return next.toString();
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

  const [account, setAccount] = useState('');
  const [password, setPassword] = useState('');
  const [status, setStatus] = useState<StatusState | null>(null);
  const [isSubmitting, setIsSubmitting] = useState(false);
  const [turnstileToken, setTurnstileToken] = useState('');
  const [turnstileMessage, setTurnstileMessage] = useState<string | null>(null);
  const turnstileContainerRef = useRef<HTMLDivElement | null>(null);
  const turnstileWidgetIDRef = useRef<string | null>(null);

  const canSubmit = canSubmitSsoLogin({
    hasRedirectTarget: Boolean(redirectTarget.url),
    account,
    password,
    isSubmitting,
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

    if (!redirectTarget.url) {
      setStatus({ tone: 'error', message: redirectTarget.error ?? 'Missing redirect_to parameter.' });
      return;
    }

    if (!account || !password) {
      setStatus({ tone: 'error', message: 'Please enter your account and password.' });
      return;
    }

    if (turnstileEnabled && !turnstileToken) {
      setStatus({ tone: 'error', message: 'Please complete the security verification challenge.' });
      return;
    }

    setIsSubmitting(true);
    setStatus({ tone: 'info', message: 'Authenticating...' });

    try {
      const data = await fetchGraphQL<SsoLoginResponse>('', USER_LOGIN_MUTATION, {
        account,
        password,
        turnstileToken: turnstileEnabled ? turnstileToken : null,
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
      if (turnstileEnabled) {
        resetTurnstile();
        setTurnstileMessage('Please complete the security verification again.');
      }
    } finally {
      setIsSubmitting(false);
    }
  };

  const redirectStatus: StatusState = redirectTarget.url
    ? { tone: 'info', message: 'Redirect destination ready.' }
    : { tone: 'error', message: redirectTarget.error ?? 'Missing redirect_to parameter.' };
  const redirectBanner = { status: redirectStatus, subtext: redirectTarget.display };

  // Hut 8-inspired Color Scheme applied to Original Layout
  return (
    <div className="relative flex min-h-screen items-center justify-center bg-gray-50 dark:bg-[#0d0d0d] px-4 py-12 text-zinc-900 dark:text-slate-200 selection:bg-yellow-500 selection:text-black transition-colors duration-300">
      {/* Background patterns/grid for "Hut 8" vibe without changing layout structure */}
      <div className="absolute inset-0 z-0 bg-[linear-gradient(to_right,#0000000a_1px,transparent_1px),linear-gradient(to_bottom,#0000000a_1px,transparent_1px)] dark:bg-[linear-gradient(to_right,#80808012_1px,transparent_1px),linear-gradient(to_bottom,#80808012_1px,transparent_1px)] bg-[size:24px_24px]"></div>

      <div className="absolute right-4 top-4 z-10 sm:right-6 sm:top-6">
        <ThemeToggle />
      </div>

      <div className="z-10 flex w-full max-w-lg flex-col gap-6">
        <div className="space-y-3 text-center">
          <div className="flex items-center justify-center gap-2 text-sm font-medium uppercase tracking-widest text-blue-600 dark:text-[#d8ff00] font-mono">
            <Lock className="h-4 w-4" />
            <span>Laisky SSO</span>
          </div>
        </div>

        <Card className="border-0 bg-white/80 dark:bg-zinc-900/40 text-zinc-900 dark:text-slate-200 shadow-2xl backdrop-blur-sm ring-1 ring-black/5 dark:ring-white/10 transition-colors duration-300">
          <CardHeader className="space-y-2 border-b border-black/5 dark:border-white/5 pb-6">
            <StatusBanner status={redirectBanner.status} subtext={redirectBanner.subtext} />
          </CardHeader>
          <CardContent className="space-y-6 pt-6">
            {status && <StatusBanner status={status} />}

            <form className="space-y-6" onSubmit={handleSubmit}>
              <div className="space-y-2">
                <label
                  htmlFor="sso-account"
                  className="text-xs font-bold text-zinc-500 dark:text-zinc-400 font-mono uppercase tracking-widest pl-1"
                >
                  Account ID
                </label>
                <Input
                  id="sso-account"
                  name="account"
                  autoComplete="username"
                  placeholder="USER_IDENTIFIER"
                  value={account}
                  onChange={(event) => setAccount(event.target.value)}
                  className="bg-gray-50 dark:bg-black/40 border-zinc-200 dark:border-zinc-800 text-zinc-900 dark:text-zinc-100 placeholder:text-zinc-400 dark:placeholder:text-zinc-700 h-12 font-mono focus-visible:ring-blue-600/30 dark:focus-visible:ring-[#d8ff00]/30 focus-visible:border-blue-600/50 dark:focus-visible:border-[#d8ff00]/50 transition-colors"
                />
              </div>

              <div className="space-y-2">
                <label
                  htmlFor="sso-password"
                  className="text-xs font-bold text-zinc-500 dark:text-zinc-400 font-mono uppercase tracking-widest pl-1"
                >
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
                  className="bg-gray-50 dark:bg-black/40 border-zinc-200 dark:border-zinc-800 text-zinc-900 dark:text-zinc-100 placeholder:text-zinc-400 dark:placeholder:text-zinc-700 h-12 font-mono focus-visible:ring-blue-600/30 dark:focus-visible:ring-[#d8ff00]/30 focus-visible:border-blue-600/50 dark:focus-visible:border-[#d8ff00]/50 transition-colors"
                />
              </div>

              {turnstileEnabled && (
                <div className="space-y-2">
                  <label className="text-xs font-bold text-zinc-500 dark:text-zinc-400 font-mono uppercase tracking-widest pl-1">
                    Security Check
                  </label>
                  <div
                    ref={turnstileContainerRef}
                    className="flex min-h-20 items-center justify-center rounded-md border border-zinc-200 dark:border-zinc-800 bg-gray-50 dark:bg-black/20 px-2 py-3 transition-colors"
                  />
                  {turnstileMessage && <p className="text-xs text-zinc-500 font-mono">{turnstileMessage}</p>}
                </div>
              )}

              <Button
                type="submit"
                className="w-full h-12 bg-zinc-900 dark:bg-[#d8ff00] hover:bg-zinc-800 dark:hover:bg-[#c5e600] text-white dark:text-black font-bold uppercase tracking-widest font-mono transition-all duration-300 transform active:scale-95"
                disabled={!canSubmit}
              >
                {isSubmitting ? 'Authenticating...' : 'Login'}
                {!isSubmitting && <ArrowRight className="ml-2 h-4 w-4" />}
              </Button>
            </form>
          </CardContent>
        </Card>

        <div className="flex justify-between text-[10px] text-zinc-400 dark:text-zinc-600 font-mono uppercase">
          <span>Sys.Status: Online</span>
          <span>Encrypted: TLS 1.3</span>
        </div>
      </div>
    </div>
  );
}
