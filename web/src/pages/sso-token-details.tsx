import { ArrowLeft, Info } from 'lucide-react';
import { Link, useLocation } from 'react-router-dom';

import { ThemeToggle } from '@/components/theme/theme-toggle';
import { Card, CardContent, CardHeader } from '@/components/ui/card';
import { LaiskyLink } from '@/components/ui/laisky-link';
import { StatusBanner } from '@/components/ui/status-banner';
import type { SsoJwtConfig } from '@/lib/runtime-config';
import { loadSsoToken, resolveSiblingSsoPath } from '@/lib/sso-session';
import { formatDecodedJwtToken } from '@/pages/sso-token-decode';
import { formatSsoJwtSchema } from '@/pages/sso-token-schema';

// SsoTokenDetailsPageProps describes the public SSO JWT metadata rendered on the standalone token page.
export interface SsoTokenDetailsPageProps {
  ssoJwt?: SsoJwtConfig | null;
}

// SsoTokenDetailsPage renders the public SSO token metadata as a standalone, directly linkable page.
// The public JWT verification metadata comes from runtime config and is shown to anonymous visitors,
// so the page works even when no session token is present. When a session token exists in local
// storage the page additionally reveals the current bearer token and its decoded JSON.
export function SsoTokenDetailsPage({ ssoJwt = null }: SsoTokenDetailsPageProps) {
  const location = useLocation();
  const trimmedToken = loadSsoToken().trim();
  // Signed-in visitors head back to their profile; anonymous visitors return to sign in. Both
  // sibling paths are resolved from the current route so the link is correct whether the SSO app
  // is served standalone ("/token" -> "/login") or mounted under the console ("/sso/token" -> "/sso/login").
  const backPath = resolveSiblingSsoPath(location.pathname, trimmedToken ? 'profile' : 'login');
  const backLabel = trimmedToken ? 'Back to profile' : 'Back to sign in';

  let decodedToken = '';
  let decodeError = '';
  if (trimmedToken) {
    try {
      decodedToken = formatDecodedJwtToken(trimmedToken);
    } catch (error) {
      decodeError = error instanceof Error ? error.message : 'Unable to decode current token.';
    }
  }

  return (
    <div className="relative flex min-h-screen items-center justify-center bg-background px-4 py-12 text-foreground transition-colors">
      <div className="absolute inset-0 z-0 bg-[linear-gradient(to_right,hsl(var(--border)/0.3)_1px,transparent_1px),linear-gradient(to_bottom,hsl(var(--border)/0.3)_1px,transparent_1px)] bg-[size:24px_24px]" />

      <div className="absolute right-4 top-4 z-10 sm:right-6 sm:top-6">
        <ThemeToggle />
      </div>

      <main className="z-10 flex w-full max-w-3xl flex-col gap-6">
        <div className="flex items-center justify-center gap-2 text-sm font-medium uppercase tracking-widest text-primary font-mono">
          <Info className="h-4 w-4" />
          <span>
            <LaiskyLink className="text-primary underline-offset-4 hover:underline">Laisky</LaiskyLink> SSO
          </span>
        </div>

        <Card className="border-border/60 bg-card/80 text-card-foreground shadow-2xl backdrop-blur-sm transition-colors">
          <CardHeader className="gap-3 border-b border-border/40 pb-6 sm:flex-row sm:items-start sm:justify-between">
            <div className="space-y-1">
              <h1 className="text-lg font-semibold tracking-tight">SSO JWT Token</h1>
              <p className="text-sm text-muted-foreground">Public verification metadata for tokens issued by this SSO service.</p>
            </div>
            <Link
              to={backPath}
              className="inline-flex shrink-0 items-center gap-1.5 text-xs font-mono uppercase tracking-widest text-muted-foreground transition-colors hover:text-foreground"
            >
              <ArrowLeft className="h-4 w-4" />
              {backLabel}
            </Link>
          </CardHeader>
          <CardContent className="min-w-0 space-y-5 pt-6 text-sm">
            {ssoJwt ? (
              <>
                <div className="grid gap-2 rounded-md border border-border bg-muted/40 p-3 sm:grid-cols-[140px_minmax(0,1fr)]">
                  <span className="text-muted-foreground">Type</span>
                  <span className="font-mono">{ssoJwt.type}</span>
                  <span className="text-muted-foreground">Algorithm</span>
                  <span className="font-mono">{ssoJwt.algorithm}</span>
                  <span className="text-muted-foreground">Issuer</span>
                  <span className="font-mono">{ssoJwt.issuer}</span>
                  <span className="text-muted-foreground">TTL seconds</span>
                  <span className="font-mono">{ssoJwt.ttl_seconds}</span>
                </div>
                <div className="space-y-2">
                  <div className="text-xs font-semibold uppercase tracking-widest text-muted-foreground">Public Key</div>
                  <pre className="max-h-60 max-w-full overflow-auto rounded-md border border-border bg-background p-3 text-xs">
                    <code>{ssoJwt.public_key_pem}</code>
                  </pre>
                </div>
                <div className="space-y-2">
                  <div className="text-xs font-semibold uppercase tracking-widest text-muted-foreground">Claims Schema</div>
                  <pre className="max-h-80 max-w-full overflow-auto rounded-md border border-border bg-background p-3 text-xs">
                    <code>{formatSsoJwtSchema(ssoJwt.claims_schema)}</code>
                  </pre>
                </div>
              </>
            ) : (
              <StatusBanner status={{ tone: 'error', message: 'SSO JWT metadata is unavailable.' }} />
            )}
            {trimmedToken && (
              <div className="space-y-2">
                <div className="text-xs font-semibold uppercase tracking-widest text-muted-foreground">Current Token</div>
                <StatusBanner status={{ tone: 'info', message: 'This bearer token authenticates your current SSO session.' }} />
                <pre className="max-h-60 max-w-full overflow-auto rounded-md border border-border bg-background p-3 text-xs">
                  <code className="break-all">{trimmedToken}</code>
                </pre>
                <div className="text-xs font-semibold uppercase tracking-widest text-muted-foreground">Decoded Token JSON</div>
                {decodedToken ? (
                  <pre className="max-h-80 max-w-full overflow-auto rounded-md border border-border bg-background p-3 text-xs">
                    <code>{decodedToken}</code>
                  </pre>
                ) : (
                  <StatusBanner status={{ tone: 'error', message: decodeError }} />
                )}
              </div>
            )}
          </CardContent>
        </Card>
      </main>
    </div>
  );
}
