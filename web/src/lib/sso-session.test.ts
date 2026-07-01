import { afterEach, describe, expect, it } from 'vitest';

import {
  clearSsoToken,
  consumeSsoTokenFromLocation,
  extractSsoTokenFromSearch,
  loadSsoToken,
  resolveSiblingSsoPath,
  storeSsoToken,
} from './sso-session';

afterEach(() => {
  window.localStorage.clear();
  window.history.replaceState(null, '', '/');
});

describe('storeSsoToken / loadSsoToken / clearSsoToken', () => {
  it('persists and reads back a trimmed token', () => {
    storeSsoToken('  jwt-token  ');
    expect(loadSsoToken()).toBe('jwt-token');
  });

  it('clears the token when an empty value is stored', () => {
    storeSsoToken('jwt-token');
    storeSsoToken('   ');
    expect(loadSsoToken()).toBe('');
  });

  it('removes the token on clear', () => {
    storeSsoToken('jwt-token');
    clearSsoToken();
    expect(loadSsoToken()).toBe('');
  });
});

describe('extractSsoTokenFromSearch', () => {
  it('returns an empty token when sso_token is absent', () => {
    const result = extractSsoTokenFromSearch('?state=abc');
    expect(result.token).toBe('');
    expect(result.nextSearch).toBe('?state=abc');
  });

  it('extracts the token and drops it from the query string', () => {
    const result = extractSsoTokenFromSearch('?sso_token=jwt&state=abc');
    expect(result.token).toBe('jwt');
    expect(result.nextSearch).toBe('?state=abc');
  });

  it('returns an empty query string when sso_token was the only parameter', () => {
    const result = extractSsoTokenFromSearch('?sso_token=jwt');
    expect(result.token).toBe('jwt');
    expect(result.nextSearch).toBe('');
  });
});

describe('consumeSsoTokenFromLocation', () => {
  it('persists the URL token and scrubs it from the address bar', () => {
    window.history.replaceState(null, '', '/profile?sso_token=jwt&state=abc');
    const token = consumeSsoTokenFromLocation();
    expect(token).toBe('jwt');
    expect(loadSsoToken()).toBe('jwt');
    expect(window.location.pathname).toBe('/profile');
    expect(window.location.search).toBe('?state=abc');
  });

  it('falls back to the stored token when the URL has none', () => {
    storeSsoToken('stored-jwt');
    window.history.replaceState(null, '', '/profile');
    expect(consumeSsoTokenFromLocation()).toBe('stored-jwt');
  });
});

describe('resolveSiblingSsoPath', () => {
  it('resolves siblings for the standalone SSO router', () => {
    expect(resolveSiblingSsoPath('/login', 'profile')).toBe('/profile');
    expect(resolveSiblingSsoPath('/profile', 'login')).toBe('/login');
    expect(resolveSiblingSsoPath('/', 'profile')).toBe('/profile');
  });

  it('resolves siblings for the MCP-mounted SSO router', () => {
    expect(resolveSiblingSsoPath('/sso/login', 'profile')).toBe('/sso/profile');
    expect(resolveSiblingSsoPath('/sso/profile', 'login')).toBe('/sso/login');
  });

  it('ignores a trailing slash on the current path', () => {
    expect(resolveSiblingSsoPath('/sso/login/', 'profile')).toBe('/sso/profile');
  });
});
