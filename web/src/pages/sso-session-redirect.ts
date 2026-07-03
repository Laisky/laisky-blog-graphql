import { buildRedirectUrlWithToken, type RedirectTarget } from '@/lib/sso-redirect';

// SsoSessionRedirect describes where an already-authenticated visitor should be sent.
export type SsoSessionRedirect = { kind: 'external'; url: string } | { kind: 'profile' } | { kind: 'none' };

// resolveAuthenticatedRedirect decides where a visitor with a confirmed-valid session should go.
// It accepts a token, redirect target, and redirect request flag, returning the redirect decision.
export function resolveAuthenticatedRedirect(params: {
  token: string;
  redirectTarget: RedirectTarget;
  redirectRequested: boolean;
}): SsoSessionRedirect {
  if (params.redirectTarget.url) {
    return { kind: 'external', url: buildRedirectUrlWithToken(params.redirectTarget.url, params.token) };
  }
  if (params.redirectRequested) {
    return { kind: 'none' };
  }
  return { kind: 'profile' };
}
