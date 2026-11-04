import { buildRedirectUrlWithToken, planSsoRedirect, type RedirectTarget } from '@/lib/sso-redirect';

// SsoSessionRedirect describes where an already-authenticated visitor should be sent.
// 'handoff' targets (Blog code or bootstrap) must go through the asynchronous
// buildSsoRedirectUrl; only 'external' targets carry the legacy bearer handoff.
export type SsoSessionRedirect =
  | { kind: 'external'; url: string }
  | { kind: 'handoff'; target: URL }
  | { kind: 'profile' }
  | { kind: 'none' };

// resolveAuthenticatedRedirect decides where a visitor with a confirmed-valid session should go.
// It accepts a token, redirect target, and redirect request flag, returning the redirect decision.
export function resolveAuthenticatedRedirect(params: {
  token: string;
  redirectTarget: RedirectTarget;
  redirectRequested: boolean;
}): SsoSessionRedirect {
  if (params.redirectTarget.url) {
    let kind: ReturnType<typeof planSsoRedirect>['kind'];
    try {
      kind = planSsoRedirect(params.redirectTarget.url).kind;
    } catch {
      // A target that cannot be planned never receives the session.
      return { kind: 'none' };
    }
    if (kind !== 'token') return { kind: 'handoff', target: params.redirectTarget.url };
    return { kind: 'external', url: buildRedirectUrlWithToken(params.redirectTarget.url, params.token) };
  }
  if (params.redirectRequested) {
    return { kind: 'none' };
  }
  return { kind: 'profile' };
}
