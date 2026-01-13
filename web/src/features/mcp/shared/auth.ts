const BEARER_PREFIX = /^Bearer\s+/i;

export function normalizeApiKey(value: string): string {
  let output = (value ?? '').trim();
  while (output && BEARER_PREFIX.test(output)) {
    output = output.replace(BEARER_PREFIX, '').trim();
  }
  return output;
}

export function buildAuthorizationHeader(apiKey: string): string {
  const token = normalizeApiKey(apiKey);
  return token ? `Bearer ${token}` : '';
}

export function resolveCurrentApiBasePath(): string {
  if (typeof window === 'undefined') {
    return '/';
  }

  // The backend API routes follow patterns like /tools/{name}/api/...
  // This helper should return the base path for the current module.
  // We use the first two segments of the path to determine the module base.
  // e.g. /tools/ask_user/something -> /tools/ask_user/
  const path = window.location.pathname || '/';
  const segments = path.split('/').filter(Boolean);
  if (segments.length >= 2) {
    return `/${segments[0]}/${segments[1]}/`;
  }

  return path.endsWith('/') ? path : `${path}/`;
}

/**
 * Resolves a tool-specific API base path regardless of current page.
 * e.g. resolveToolApiBase('call_log') -> /tools/call_log/
 */
export function resolveToolApiBase(toolName: string): string {
  return `/tools/${toolName}/`;
}

/**
 * Resolves the global MCP JSON-RPC endpoint.
 */
export function resolveMcpEndpoint(): string {
  return '/mcp';
}
