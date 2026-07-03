import { Info } from 'lucide-react';

import { Button } from '@/components/ui/button';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from '@/components/ui/confirm-dialog';
import { StatusBanner } from '@/components/ui/status-banner';
import type { SsoJwtConfig } from '@/lib/runtime-config';
import { formatDecodedJwtToken } from '@/pages/sso-token-decode';
import { formatSsoJwtSchema } from '@/pages/sso-token-schema';

// SsoTokenDetailsDialogProps describes the public SSO JWT metadata rendered in the login page modal.
export interface SsoTokenDetailsDialogProps {
  ssoJwt?: SsoJwtConfig | null;
  currentToken?: string;
}

// SsoTokenDetailsDialog renders the public SSO token metadata modal from runtime config.
// It accepts optional SSO JWT metadata and returns a trigger button plus dialog content.
export function SsoTokenDetailsDialog({ ssoJwt, currentToken = '' }: SsoTokenDetailsDialogProps) {
  const trimmedToken = currentToken.trim();
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
    <Dialog>
      <DialogTrigger asChild>
        <Button type="button" variant="ghost" size="sm" className="gap-2 text-xs font-mono uppercase tracking-widest">
          <Info className="h-4 w-4" />
          Token Details
        </Button>
      </DialogTrigger>
      <DialogContent className="max-h-[90vh] max-w-3xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>SSO JWT Token</DialogTitle>
          <DialogDescription>Public verification metadata for tokens issued by this SSO service.</DialogDescription>
        </DialogHeader>
        <div className="space-y-5 text-sm">
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
                <pre className="max-h-60 overflow-auto rounded-md border border-border bg-background p-3 text-xs">
                  <code>{ssoJwt.public_key_pem}</code>
                </pre>
              </div>
              <div className="space-y-2">
                <div className="text-xs font-semibold uppercase tracking-widest text-muted-foreground">Claims Schema</div>
                <pre className="max-h-80 overflow-auto rounded-md border border-border bg-background p-3 text-xs">
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
              <pre className="max-h-60 overflow-auto rounded-md border border-border bg-background p-3 text-xs">
                <code className="break-all">{trimmedToken}</code>
              </pre>
              <div className="text-xs font-semibold uppercase tracking-widest text-muted-foreground">Decoded Token JSON</div>
              {decodedToken ? (
                <pre className="max-h-80 overflow-auto rounded-md border border-border bg-background p-3 text-xs">
                  <code>{decodedToken}</code>
                </pre>
              ) : (
                <StatusBanner status={{ tone: 'error', message: decodeError }} />
              )}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}
