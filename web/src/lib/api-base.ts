let configuredBase: string | undefined;

/** hasUnsafePathChars rejects escaping, encoding and whitespace characters that would
 * let a configured prefix change the request target. Control characters are compared
 * by code unit because a regular-expression literal containing them reads as a typo
 * and is rejected by lint. */
function hasUnsafePathChars(text: string): boolean {
  for (let index = 0; index < text.length; index += 1) {
    if (text.charCodeAt(index) <= 0x20) return true;
    const char = text[index];
    if (char === '\\' || char === '%' || char === '?' || char === '#') return true;
  }
  return false;
}

/** Normalize a same-origin deployment prefix without accepting a credential sink. */
export function normalizePublicApiBasePath(raw: string): string {
  const trimmed = raw.trim();
  if (trimmed.startsWith('//')) throw new Error('Invalid public API base path');
  const base = trimmed.replace(/\/+$/, '');
  if (!base) return '';
  if (!base.startsWith('/') || base.startsWith('//') || hasUnsafePathChars(base)
      || base.includes('//') || base.split('/').some((part) => part === '.' || part === '..')) {
    throw new Error('Invalid public API base path');
  }
  return base;
}

/** Called once by bootstrap, using the API mount, never the site/SPA router prefix. */
export function configurePublicApiBasePath(raw: string | undefined): void {
  configuredBase = raw === undefined ? undefined : normalizePublicApiBasePath(raw);
}

/** Distinguishes explicitly configured root ('') from an absent runtime setting. */
export function configuredPublicApiBasePath(): string | undefined { return configuredBase; }

/** Resolve same-origin HTTP/GraphQL routes, independently of a remote MCP override. */
export function publicApiPath(path: string, pathname = typeof window === 'undefined' ? '/' : window.location.pathname): string {
  if (!path.startsWith('/') || path.startsWith('//') || hasUnsafePathChars(path)
      || path.split('/').some((part) => part === '.' || part === '..')) {
    throw new Error('Invalid API path');
  }
  const fallback = pathname === '/mcp' || pathname.startsWith('/mcp/') ? '/mcp' : '';
  return (configuredBase ?? fallback) + path;
}
