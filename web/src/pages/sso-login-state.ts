export const TOTP_REQUIRED_TOKEN = 'totp_required';
export const TURNSTILE_REQUIRED_TOKEN = 'turnstile_required';

// SsoSubmitState describes the visible auth form state needed to decide whether it can submit.
export interface SsoSubmitState {
  mode: 'login' | 'register';
  loginMethod?: 'password' | 'email_code';
  hasRedirectTarget: boolean;
  redirectRequested?: boolean;
  account: string;
  password: string;
  displayName: string;
  emailCode?: string;
  isSubmitting: boolean;
  isTurnstileEnabled: boolean;
  turnstileToken: string;
  totpRequired?: boolean;
  totpCode?: string;
}

// SsoGithubStartState describes the shared gate state for starting external auth flows.
export interface SsoGithubStartState {
  isSubmitting: boolean;
  isTurnstileEnabled: boolean;
  turnstileToken: string;
}

// isTotpRequiredError reports whether a login error asks for a TOTP code as a second step.
export function isTotpRequiredError(message: string): boolean {
  return message.trim().toLowerCase().includes(TOTP_REQUIRED_TOKEN);
}

// isTurnstileRequiredError reports whether an auth error is asking for Turnstile verification.
export function isTurnstileRequiredError(message: string): boolean {
  return message.trim().toLowerCase().includes(TURNSTILE_REQUIRED_TOKEN);
}

// isTurnstileEnabled reports whether a site key is configured.
export function isTurnstileEnabled(siteKey: string | undefined): boolean {
  return (siteKey ?? '').trim().length > 0;
}

// canSubmitSsoLogin reports whether the current login/register form can submit.
export function canSubmitSsoLogin(state: SsoSubmitState): boolean {
  if (state.mode === 'login' && state.redirectRequested && !state.hasRedirectTarget) {
    return false;
  }
  if (state.isSubmitting) {
    return false;
  }
  if (state.account.trim().length === 0) {
    return false;
  }
  if (state.mode === 'register') {
    if (state.password.trim().length === 0 || state.displayName.trim().length === 0 || (state.emailCode ?? '').trim().length === 0) {
      return false;
    }
  } else if (state.loginMethod === 'email_code') {
    if ((state.emailCode ?? '').trim().length === 0) {
      return false;
    }
  } else {
    if (state.password.trim().length === 0) {
      return false;
    }
    if (state.totpRequired && (state.totpCode ?? '').trim().length === 0) {
      return false;
    }
  }
  if (state.isTurnstileEnabled && state.turnstileToken.trim().length === 0) {
    return false;
  }
  return true;
}

// canStartGithubOAuth reports whether an OAuth or passkey auth start request can be made.
export function canStartGithubOAuth(state: SsoGithubStartState): boolean {
  if (state.isSubmitting) {
    return false;
  }
  if (state.isTurnstileEnabled && state.turnstileToken.trim().length === 0) {
    return false;
  }
  return true;
}
