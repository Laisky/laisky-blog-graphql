// SSO_TOKEN_PARAM is the query parameter the SSO callback flows use to carry the
// freshly minted JWT back to the profile page. See buildRedirectUrlWithToken in
// sso-redirect.ts, which appends it to the redirect target.
export const SSO_TOKEN_PARAM = 'sso_token';

// SSO_TOKEN_STORAGE_KEY is the localStorage slot that persists the SSO session
// token so authenticated profile requests can attach it as a Bearer credential.
// The backend authenticates GraphQL calls from the Authorization header only, so
// the browser has to keep the token itself rather than relying on a cookie.
const SSO_TOKEN_STORAGE_KEY = 'laisky_sso_token';

/** storeSsoToken persists the SSO session token, clearing it when the value is empty. */
export function storeSsoToken(token: string): void {
  if (typeof window === 'undefined') {
    return;
  }
  const trimmed = token.trim();
  if (!trimmed) {
    window.localStorage.removeItem(SSO_TOKEN_STORAGE_KEY);
    return;
  }
  window.localStorage.setItem(SSO_TOKEN_STORAGE_KEY, trimmed);
}

/** loadSsoToken returns the persisted SSO session token, or an empty string when absent. */
export function loadSsoToken(): string {
  if (typeof window === 'undefined') {
    return '';
  }
  return (window.localStorage.getItem(SSO_TOKEN_STORAGE_KEY) ?? '').trim();
}

/** clearSsoToken removes any persisted SSO session token. */
export function clearSsoToken(): void {
  if (typeof window === 'undefined') {
    return;
  }
  window.localStorage.removeItem(SSO_TOKEN_STORAGE_KEY);
}

export interface SsoTokenFromSearch {
  token: string;
  // nextSearch is the original query string with the sso_token parameter removed.
  // It keeps a leading "?" when other parameters remain and is empty otherwise.
  nextSearch: string;
}

/**
 * extractSsoTokenFromSearch pulls the sso_token parameter out of a URL query string.
 * It accepts the raw location search (with or without a leading "?") and returns the
 * token plus the remaining query string so callers can scrub the token from the URL.
 */
export function extractSsoTokenFromSearch(search: string): SsoTokenFromSearch {
  const params = new URLSearchParams(search);
  const token = (params.get(SSO_TOKEN_PARAM) ?? '').trim();
  if (!token) {
    return { token: '', nextSearch: search };
  }

  params.delete(SSO_TOKEN_PARAM);
  const rest = params.toString();
  return { token, nextSearch: rest ? `?${rest}` : '' };
}

/**
 * consumeSsoTokenFromLocation extracts an sso_token from the current URL, persists it,
 * scrubs it from the address bar, and returns the effective session token. When no
 * token is present in the URL it falls back to the previously stored token.
 */
export function consumeSsoTokenFromLocation(): string {
  if (typeof window === 'undefined') {
    return '';
  }

  const { token, nextSearch } = extractSsoTokenFromSearch(window.location.search);
  if (token) {
    storeSsoToken(token);
    const cleaned = `${window.location.pathname}${nextSearch}${window.location.hash}`;
    window.history.replaceState(window.history.state, '', cleaned);
  }

  return loadSsoToken();
}

/**
 * resolveSiblingSsoPath computes the router path of a sibling SSO route.
 * It accepts the router-relative pathname (basename already stripped by React Router)
 * and a sibling route name, returning an absolute in-router path. This keeps the
 * login/profile links correct whether the SSO app is served standalone ("/login",
 * "/profile") or mounted under the MCP console ("/sso/login", "/sso/profile").
 */
export function resolveSiblingSsoPath(pathname: string, name: string): string {
  const trimmed = pathname.replace(/\/+$/, '');
  const idx = trimmed.lastIndexOf('/');
  const base = idx > 0 ? trimmed.slice(0, idx) : '';
  return `${base}/${name}`;
}
