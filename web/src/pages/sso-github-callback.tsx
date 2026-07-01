/* eslint-disable react-refresh/only-export-components */
import { Github, Lock } from 'lucide-react';
import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router-dom';

import { ThemeToggle } from '@/components/theme/theme-toggle';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { LaiskyLink } from '@/components/ui/laisky-link';
import { StatusBanner, type StatusState } from '@/components/ui/status-banner';
import { fetchGraphQL } from '@/lib/graphql';
import { buildRedirectUrlWithToken } from '@/pages/sso-login';

const USER_GITHUB_OAUTH_LOGIN_MUTATION = `
  mutation GithubOAuthLogin($code: String!, $state: String!) {
    UserGithubOAuthLogin(code: $code, state: $state) {
      token
      redirect_to
    }
  }
`;

interface GithubOAuthLoginResponse {
  UserGithubOAuthLogin: {
    token: string;
    redirect_to: string;
  };
}

export function buildGithubCallbackRedirect(redirectTo: string, token: string, origin: string): string {
  const target = redirectTo.trim() ? new URL(redirectTo, origin) : new URL('/profile', origin);
  return buildRedirectUrlWithToken(target, token);
}

export function SsoGithubCallbackPage() {
  const [searchParams] = useSearchParams();
  const code = useMemo(() => searchParams.get('code') ?? '', [searchParams]);
  const state = useMemo(() => searchParams.get('state') ?? '', [searchParams]);
  const [status, setStatus] = useState<StatusState>({ tone: 'info', message: 'Completing GitHub sign in...' });

  useEffect(() => {
    let isCancelled = false;

    const completeLogin = async () => {
      if (!code || !state) {
        setStatus({ tone: 'error', message: 'GitHub callback is missing required parameters.' });
        return;
      }

      try {
        const data = await fetchGraphQL<GithubOAuthLoginResponse>('', USER_GITHUB_OAUTH_LOGIN_MUTATION, { code, state });
        const token = data.UserGithubOAuthLogin?.token;
        if (!token) {
          throw new Error('GitHub login succeeded but no token was returned.');
        }
        const redirectURL = buildGithubCallbackRedirect(data.UserGithubOAuthLogin.redirect_to, token, window.location.origin);
        if (!isCancelled) {
          setStatus({ tone: 'success', message: 'GitHub sign in complete. Redirecting...' });
          window.location.assign(redirectURL);
        }
      } catch (error) {
        if (!isCancelled) {
          setStatus({ tone: 'error', message: error instanceof Error ? error.message : 'GitHub sign in failed.' });
        }
      }
    };

    void completeLogin();
    return () => {
      isCancelled = true;
    };
  }, [code, state]);

  return (
    <div className="relative flex min-h-screen items-center justify-center bg-background px-4 py-12 text-foreground">
      <div className="absolute inset-0 z-0 bg-[linear-gradient(to_right,hsl(var(--border)/0.3)_1px,transparent_1px),linear-gradient(to_bottom,hsl(var(--border)/0.3)_1px,transparent_1px)] bg-[size:24px_24px]" />
      <div className="absolute right-4 top-4 z-10 sm:right-6 sm:top-6">
        <ThemeToggle />
      </div>
      <main className="z-10 w-full max-w-lg">
        <Card className="border-border/60 bg-card/80 text-card-foreground shadow-2xl backdrop-blur-sm">
          <CardHeader className="space-y-4 border-b border-border/40 pb-6">
            <Link to="/" className="flex items-center justify-center gap-2 font-semibold text-foreground hover:text-primary">
              <Lock className="h-5 w-5 text-primary" />
              <span>
                <LaiskyLink className="text-primary underline-offset-4 hover:underline">Laisky</LaiskyLink> SSO
              </span>
            </Link>
            <div className="flex justify-center">
              <Github className="h-8 w-8 text-primary" />
            </div>
          </CardHeader>
          <CardContent className="pt-6">
            <StatusBanner status={status} />
          </CardContent>
        </Card>
      </main>
    </div>
  );
}
