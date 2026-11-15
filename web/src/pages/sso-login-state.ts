export const TOTP_REQUIRED_TOKEN = 'totp_required';
export const TURNSTILE_REQUIRED_TOKEN = 'turnstile_required';
// TURNSTILE_FAILED_TOKEN is returned when a supplied challenge token could not be
// verified (expired, reused, or Cloudflare unreachable); it never means the
// credentials were wrong.
export const TURNSTILE_FAILED_TOKEN = 'turnstile_failed';
// LOGIN_UNAVAILABLE_TOKEN is returned when the backend could not evaluate the
// credentials at all, for example during a database outage.
export const LOGIN_UNAVAILABLE_TOKEN = 'login_unavailable';
// INVALID_EMAIL_CODE_TOKEN is returned by registration for a wrong, expired or
// exhausted email code. Each code is compared only a few times, after which even
// the right code is refused until a new one is requested.
export const INVALID_EMAIL_CODE_TOKEN = 'invalid email verification code';
// INVALID_CREDENTIALS_TOKEN is the masked rejection of every login method.
export const INVALID_CREDENTIALS_TOKEN = 'invalid credentials';
// EMAIL_CODE_REJECTED_MESSAGE explains a rejected email code without revealing
// whether the account exists.
export const EMAIL_CODE_REJECTED_MESSAGE =
  'The verification code is wrong or has expired. Each code allows only a few attempts, so request a new code if this keeps failing.';

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

// isTurnstileFailedError reports whether a supplied Turnstile token was rejected.
export function isTurnstileFailedError(message: string): boolean {
  return message.trim().toLowerCase().includes(TURNSTILE_FAILED_TOKEN);
}

// SsoAuthErrorContext describes which form produced an auth error.
export interface SsoAuthErrorContext {
  // emailCode is true when the failed request submitted an email code.
  emailCode?: boolean;
}

// describeSsoAuthError turns a backend auth error into a message for the user.
// Stable machine tokens are replaced by explanations; other messages pass through.
export function describeSsoAuthError(message: string, context: SsoAuthErrorContext = {}): string {
  const normalized = message.trim().toLowerCase();
  if (normalized.includes(LOGIN_UNAVAILABLE_TOKEN)) {
    return 'Sign-in is temporarily unavailable. Please try again in a moment.';
  }
  if (normalized.includes(INVALID_EMAIL_CODE_TOKEN) || (context.emailCode && normalized.includes(INVALID_CREDENTIALS_TOKEN))) {
    return EMAIL_CODE_REJECTED_MESSAGE;
  }
  return message;
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
