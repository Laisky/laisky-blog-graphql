import { publicApiPath } from './api-base';

export interface RedirectTarget {
  url: URL | null;
  display: string;
  error?: string;
}

export function parseRedirectTarget(rawValue: string | null, origin: string): RedirectTarget {
  const raw = (rawValue ?? '').trim();
  if (!raw) {
    return {
      url: null,
      display: 'Not provided',
      error: 'Missing redirect_to parameter. Provide a redirect target to continue.',
    };
  }

  const baseOrigin = origin && origin !== 'null' ? origin : 'http://localhost';

  let parsed: URL;
  try {
    parsed = new URL(raw, baseOrigin);
  } catch {
    return {
      url: null,
      display: raw,
      error: 'Invalid redirect_to URL. Please provide a valid URL or path.',
    };
  }

  if (!isAllowedRedirectProtocol(parsed)) {
    return {
      url: null,
      display: parsed.toString(),
      error: 'Unsupported redirect protocol. Only http and https are allowed.',
    };
  }

  if (!isAllowedRedirectHost(parsed)) {
    return {
      url: null,
      display: parsed.toString(),
      error: 'Unsupported redirect host. Only *.laisky.com domains or internal IPs are allowed.',
    };
  }

  try {
    if (parseBlogCodeTarget(parsed) && !/^https:\/\/blog\.laisky\.com\/?\?/.test(raw)) {
      throw new Error('Invalid Blog SSO callback spelling.');
    }
  } catch {
    return { url: null, display: 'Invalid Blog SSO code request', error: 'Invalid Blog SSO code request.' };
  }
  return {
    url: parsed,
    display: parsed.toString(),
  };
}

export function isAllowedRedirectProtocol(target: URL): boolean {
  const protocol = target.protocol.toLowerCase();
  return protocol === 'http:' || protocol === 'https:';
}

export function isAllowedRedirectHost(target: URL): boolean {
  const hostname = normalizeHostname(target.hostname);
  return isAllowedLaiskyDomain(hostname) || isInternalIPAddress(hostname);
}

export function normalizeHostname(hostname: string): string {
  const trimmed = hostname.trim().toLowerCase();
  return trimmed.endsWith('.') ? trimmed.slice(0, -1) : trimmed;
}

export function isAllowedLaiskyDomain(hostname: string): boolean {
  return hostname === 'laisky.com' || hostname.endsWith('.laisky.com');
}

export function isInternalIPAddress(hostname: string): boolean {
  const ipv4 = parseIPv4Address(hostname);
  if (ipv4) {
    return isInternalIPv4Address(ipv4);
  }

  if (hostname.includes(':')) {
    return isInternalIPv6Address(hostname);
  }

  return false;
}

export function parseIPv4Address(hostname: string): [number, number, number, number] | null {
  const parts = hostname.split('.');
  if (parts.length !== 4) {
    return null;
  }

  const octets: number[] = [];
  for (const part of parts) {
    if (!/^\d+$/.test(part)) {
      return null;
    }
    const value = Number(part);
    if (Number.isNaN(value) || value < 0 || value > 255) {
      return null;
    }
    octets.push(value);
  }

  return [octets[0], octets[1], octets[2], octets[3]];
}

export function isInternalIPv4Address(octets: [number, number, number, number]): boolean {
  const [first, second] = octets;
  if (first === 10) {
    return true;
  }
  if (first === 172 && second >= 16 && second <= 31) {
    return true;
  }
  if (first === 192 && second === 168) {
    return true;
  }
  if (first === 127) {
    return true;
  }
  if (first === 100 && second >= 64 && second <= 127) {
    return true;
  }
  return false;
}

export function isInternalIPv6Address(hostname: string): boolean {
  let normalized = hostname.toLowerCase();

  if (normalized.startsWith('[') && normalized.endsWith(']')) {
    normalized = normalized.slice(1, -1);
  }

  if (normalized === '::1') {
    return true;
  }

  if (normalized.includes('.')) {
    const ipv4Part = normalized.slice(normalized.lastIndexOf(':') + 1);
    const ipv4 = parseIPv4Address(ipv4Part);
    if (ipv4) {
      return isInternalIPv4Address(ipv4);
    }
  }

  const firstHextet = normalized.split(':').find((part) => part.length > 0);
  if (!firstHextet) {
    return false;
  }

  const value = Number.parseInt(firstHextet, 16);
  if (Number.isNaN(value)) {
    return false;
  }

  return (value & 0xfe00) === 0xfc00;
}

export function buildRedirectUrlWithToken(target: URL, token: string): string {
  if (parseBlogCodeTarget(target)) throw new Error('Blog requires a single-use SSO code.');
  const next = new URL(target.toString());
  next.searchParams.set('sso_token', token);
  return next.toString();
}

const blogCallback = 'https://blog.laisky.com';
const codeMarkers = ['sso_flow', 'sso_state', 'sso_challenge', 'sso_challenge_method'];

/** BlogCodeTarget holds only the public bindings for the registered Blog callback. */
export interface BlogCodeTarget {
  state: string;
  challenge: string;
}

/** isEncodedSSOEntropy checks the canonical unpadded encoding of 32 bytes. */
function isEncodedSSOEntropy(value: string): boolean {
  if (!/^[A-Za-z0-9_-]{43}$/.test(value)) return false;
  // The final base64 character has only four significant bits for 32 bytes.
  return 'AEIMQUYcgkosw048'.includes(value[value.length - 1]);
}

/** parseBlogCodeTarget recognizes opt-in markers and rejects malformed flows without legacy fallback. */
export function parseBlogCodeTarget(target: URL): BlogCodeTarget | null {
  const explicitFlow = target.searchParams.has('sso_flow');
  const partialBlogFlow = target.origin === blogCallback && codeMarkers.some((key) => target.searchParams.has(key));
  if (!explicitFlow && !partialBlogFlow) return null;
  const state = target.searchParams.get('sso_state') ?? '';
  const challenge = target.searchParams.get('sso_challenge') ?? '';
  if (target.origin !== blogCallback || target.username || target.password || target.pathname !== '/'
    || target.hash || [...target.searchParams.keys()].some((key) => !codeMarkers.includes(key))
    || codeMarkers.some((key) => target.searchParams.getAll(key).length !== 1)
    || target.searchParams.get('sso_flow') !== 'code'
    || target.searchParams.get('sso_challenge_method') !== 'S256'
    || !isEncodedSSOEntropy(state) || !isEncodedSSOEntropy(challenge)) {
    throw new Error('Invalid Blog SSO code request.');
  }
  return { state, challenge };
}

/** buildSsoRedirectUrl issues an opt-in Blog code or preserves the legacy handoff for other clients. */
export async function buildSsoRedirectUrl(target: URL, token: string): Promise<string> {
  const binding = parseBlogCodeTarget(target);
  if (!binding) return buildRedirectUrlWithToken(target, token);
  const response = await fetch(publicApiPath('/sso/code'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({
      client_id: 'blog', redirect_uri: blogCallback, state: binding.state,
      code_challenge: binding.challenge, code_challenge_method: 'S256',
    }),
    redirect: 'error', credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer',
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) throw new Error('Unable to complete Blog SSO code handoff.');
  const data: unknown = await readSsoCodeResponse(response);
  if (!data || typeof data !== 'object' || !('code' in data)
    || typeof data.code !== 'string' || !isEncodedSSOEntropy(data.code)
    || !('expires_in' in data) || typeof data.expires_in !== 'number'
    || !Number.isInteger(data.expires_in) || data.expires_in < 1 || data.expires_in > 60) {
    throw new Error('Invalid Blog SSO code response.');
  }
  const callback = new URL(blogCallback);
  callback.searchParams.set('sso_code', data.code);
  callback.searchParams.set('sso_state', binding.state);
  return callback.toString();
}

/** readSsoCodeResponse bounds streamed JSON and returns only a generic failure for invalid responses. */
async function readSsoCodeResponse(response: Response): Promise<unknown> {
  const contentType = response.headers.get('Content-Type')?.split(';', 1)[0].trim().toLowerCase();
  if (contentType !== 'application/json' || !response.body) {
    throw new Error('Invalid Blog SSO code response.');
  }
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      total += value.byteLength;
      if (total > 8192) {
        await reader.cancel();
        throw new Error('Invalid Blog SSO code response.');
      }
      chunks.push(value);
    }
    const bytes = new Uint8Array(total);
    let offset = 0;
    for (const chunk of chunks) {
      bytes.set(chunk, offset);
      offset += chunk.byteLength;
    }
    return JSON.parse(new TextDecoder().decode(bytes)) as unknown;
  } catch {
    throw new Error('Invalid Blog SSO code response.');
  } finally {
    reader.releaseLock();
  }
}
