const BEARER_PREFIX = /^Bearer\s+/i;
const LOCAL_HOSTS = new Set(['localhost', '127.0.0.1', '::1']);
const MCP_SITE_PREFIX = 'mcp';
const DEFAULT_LOCAL_MCP_ENDPOINT = '/mcp';
const DEFAULT_DOMAIN_MCP_ENDPOINT = '/';

/**
 * normalizeApiKey trims API key input and strips any duplicated Bearer prefix.
 * It accepts a raw API key string and returns the normalized token body.
 */
export function normalizeApiKey(value: string): string {
  let output = (value ?? '').trim();
  while (output && BEARER_PREFIX.test(output)) {
    output = output.replace(BEARER_PREFIX, '').trim();
  }
  return output;
}

/**
 * buildAuthorizationHeader builds the Authorization header value from an API key.
 * It accepts an API key string and returns either "Bearer <token>" or an empty string.
 */
export function buildAuthorizationHeader(apiKey: string): string {
  const token = normalizeApiKey(apiKey);
  return token ? `Bearer ${token}` : '';
}

/**
 * resolveCurrentApiBasePath resolves the current tool API base path from window location.
 * It reads the active pathname and returns a normalized base path ending with "/".
 */
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
 * resolveToolApiBase resolves a tool-specific API base path.
 * It accepts a tool name and returns a path formatted as "/tools/{toolName}/".
 */
export function resolveToolApiBase(toolName: string): string {
  return `/tools/${toolName}/`;
}

/**
 * resolveMcpEndpoint resolves the MCP JSON-RPC endpoint for both domain and prefix deployments.
 * It uses an optional VITE_MCP_ENDPOINT_PATH override, then infers from pathname/hostname, and returns the endpoint path.
 */
export function resolveMcpEndpoint(): string {
  if (typeof window === 'undefined') {
    return DEFAULT_LOCAL_MCP_ENDPOINT;
  }

  return resolveMcpEndpointByLocation(
    window.location.pathname,
    window.location.hostname,
    import.meta.env.VITE_MCP_ENDPOINT_PATH as string | undefined
  );
}

/**
 * resolveMcpEndpointByLocation resolves the MCP endpoint using explicit location inputs.
 * It accepts pathname, hostname, and optional configured endpoint, then returns the selected endpoint path or URL.
 */
export function resolveMcpEndpointByLocation(pathname: string, hostname: string, configuredEndpoint?: string): string {
  const configured = normalizeConfiguredEndpoint(configuredEndpoint);
  if (configured) {
    return configured;
  }

  const prefixFromPath = inferSitePrefixFromPathname(pathname);
  if (prefixFromPath) {
    return prefixFromPath;
  }

  if (isLocalHost(hostname)) {
    return DEFAULT_LOCAL_MCP_ENDPOINT;
  }

  return DEFAULT_DOMAIN_MCP_ENDPOINT;
}

/**
 * normalizeConfiguredEndpoint normalizes a configured MCP endpoint string.
 * It accepts an optional endpoint string and returns a normalized endpoint path or URL, or undefined when empty.
 */
function normalizeConfiguredEndpoint(input: string | undefined): string | undefined {
  const trimmed = (input ?? '').trim();
  if (!trimmed) {
    return undefined;
  }

  if (/^https?:\/\//i.test(trimmed)) {
    return trimmed.replace(/\/$/, '') || trimmed;
  }

  const withLeadingSlash = trimmed.startsWith('/') ? trimmed : `/${trimmed}`;
  if (withLeadingSlash === '/') {
    return '/';
  }

  return withLeadingSlash.replace(/\/$/, '');
}

/**
 * inferSitePrefixFromPathname infers a site prefix from the first pathname segment.
 * It accepts a pathname and returns "/mcp" when present, otherwise undefined.
 */
function inferSitePrefixFromPathname(pathname: string): string | undefined {
  const [firstSegment] = (pathname || '/').split('/').filter(Boolean);
  if (!firstSegment) {
    return undefined;
  }

  const normalized = firstSegment.trim().toLowerCase();
  if (normalized !== MCP_SITE_PREFIX) {
    return undefined;
  }

  return `/${MCP_SITE_PREFIX}`;
}

/**
 * isLocalHost determines whether the current hostname is local development/test host.
 * It accepts a hostname and returns true for localhost, loopback, or LAN/private IP addresses.
 */
function isLocalHost(hostname: string): boolean {
  const normalized = (hostname ?? '').trim().toLowerCase();
  if (!normalized) {
    return false;
  }

  if (LOCAL_HOSTS.has(normalized)) {
    return true;
  }

  // IPv4 private/local ranges: 10/8, 127/8, 172.16/12, 192.168/16, 100.64/10.
  if (
    /^10\./.test(normalized) ||
    /^127\./.test(normalized) ||
    /^192\.168\./.test(normalized) ||
    /^100\.(6[4-9]|[7-9]\d|1[01]\d|12[0-7])\./.test(normalized) ||
    /^172\.(1[6-9]|2\d|3[01])\./.test(normalized)
  ) {
    return true;
  }

  return false;
}
