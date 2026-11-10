/**
 * The single SSO redirect policy shared by the issuer frontend and backend. The Go
 * implementation lives in internal/library/ssoredirect; both are pinned by the shared
 * vectors in sso-redirect-policy.v1.json so the two enforcement points cannot drift.
 * Checks run on the raw string, so URL parser repairs (backslashes, tabs, numeric IPv4
 * spellings, userinfo) cannot change the outcome. See docs/manual/sso.md.
 */

/** maxRedirectTargetLength bounds a redirect target before any parsing or allocation. */
export const maxRedirectTargetLength = 2048;
const blogHostname = 'blog.laisky.com';
const codeMarkers = ['sso_flow', 'sso_client_id', 'sso_state', 'sso_challenge', 'sso_challenge_method'];

/** LegacyTokenRedirectMode selects whether unregistered targets keep the legacy bearer handoff. */
export type LegacyTokenRedirectMode = 'allow' | 'deny';

/** SsoRegisteredClient is one registered code-flow client as published in runtime-config.json. */
export interface SsoRegisteredClient {
  client_id: string;
  redirect_uris: string[];
  legacy_token_redirect?: string;
}

/** SsoRedirectPolicy is the issuer's legacy handoff mode plus its client registry. */
export interface SsoRedirectPolicy {
  legacy_token_redirect: LegacyTokenRedirectMode;
  clients: SsoRegisteredClient[];
}

/** SsoRedirectDecision is the single issuance path a redirect target may take. */
export type SsoRedirectDecision =
  | { kind: 'reject'; reason: string }
  | { kind: 'blog'; target: string }
  | { kind: 'client'; clientId: string; redirectUri: string; target: string }
  | { kind: 'self'; target: string }
  | { kind: 'legacy'; target: string };

/** formatSsoRedirectDecision renders a decision in the shared vector notation, e.g. "client:accounting". */
export function formatSsoRedirectDecision(decision: SsoRedirectDecision): string {
  if (decision.kind === 'reject') return 'reject:' + decision.reason;
  if (decision.kind === 'client') return 'client:' + decision.clientId;
  return decision.kind;
}

interface SplitTarget {
  raw: string;
  scheme: 'http' | 'https';
  userinfo: boolean;
  host: string;
  hasPort: boolean;
  port: string;
  path: string;
  query: string;
  fragment: boolean;
}

/** HostClass separates production hosts from loopback and private development hosts. */
type HostClass = 'production' | 'loopback' | 'private';

/** Inspection is a target that passed the syntax, host and transport rules. */
interface Inspection {
  value: string;
  parsed: SplitTarget;
  hostClass: HostClass;
  blog: boolean;
  issuer: SplitTarget | null;
}

/**
 * inspect applies the rules shared by every target and every registration: printable ASCII,
 * no backslash before the query or fragment, bounded length, an absolute http(s) URL or an
 * issuer-relative path, no userinfo, a canonical host and port, and https on production hosts.
 * Blog targets stop after the host is known; the Blog protocol owns them.
 */
function inspect(raw: string, issuerOrigin: string): Inspection | { reason: string } {
  let value = trimAsciiSpace(raw);
  if (!value) return { reason: 'missing' };
  // WHATWG URL parsing reads a backslash as "/" in the scheme, authority and path, where other
  // parsers keep it. Browsers leave it unencoded in the query and fragment, where it is data.
  let boundary = value.search(/[?#]/);
  if (boundary < 0) boundary = value.length;
  for (let i = 0; i < value.length; i += 1) {
    const c = value.charCodeAt(i);
    if (c < 0x21 || c > 0x7e || (c === 0x5c && i < boundary)) return { reason: 'invalid_character' };
  }
  if (value.length > maxRedirectTargetLength) return { reason: 'too_long' };

  const split = splitTarget(trimAsciiSpace(issuerOrigin));
  const issuer = split && !split.userinfo && split.query === '' && !split.fragment && (split.path === '' || split.path === '/')
    ? split : null;
  if (value.startsWith('/') && !value.startsWith('//')) {
    if (!issuer) return { reason: 'unsupported_reference' };
    value = targetOrigin(issuer) + value;
  }
  const parsed = splitTarget(value);
  if (!parsed) return { reason: 'unsupported_reference' };
  if (stripRootDot(parsed.host) === blogHostname) return { value, parsed, hostClass: 'production', blog: true, issuer };
  if (parsed.userinfo) return { reason: 'userinfo' };
  const host = classifyHost(parsed.host);
  if (host.reason) return { reason: host.reason };
  if (parsed.hasPort && !isValidPort(parsed.port)) return { reason: 'invalid_port' };
  if (host.hostClass === 'production' && parsed.scheme !== 'https') return { reason: 'insecure_scheme' };
  return { value, parsed, hostClass: host.hostClass, blog: false, issuer };
}

/** isDevelopmentIssuer reports whether the issuer page itself runs on a loopback or private host. */
function isDevelopmentIssuer(issuer: SplitTarget | null): boolean {
  if (!issuer) return false;
  const host = classifyHost(issuer.host);
  return !host.reason && host.hostClass !== 'production';
}

/**
 * classifySsoRedirect decides how the issuer may hand a session to raw. issuerOrigin is the
 * issuer page origin; it resolves root-relative paths and identifies issuer pages.
 *
 * Loopback targets keep the legacy handoff from any issuer, because the request never leaves
 * the device. A private, CGNAT or ULA target is shared by a network, so a production issuer
 * hands it the bearer only at an exact registered callback whose client allows the legacy
 * handoff; a development issuer keeps the legacy handoff.
 */
export function classifySsoRedirect(raw: string, issuerOrigin: string, policy: SsoRedirectPolicy): SsoRedirectDecision {
  const checked = inspect(raw, issuerOrigin);
  if ('reason' in checked) return reject(checked.reason);
  const { value, parsed, issuer } = checked;
  if (checked.blog) return { kind: 'blog', target: value };

  const names = queryNames(parsed.query);
  if (names.has('sso_client_id')) return classifyCodeRequest(parsed, policy);
  if (names.has('sso_flow')) return reject('invalid_code_request');
  if (issuer && targetOrigin(parsed) === targetOrigin(issuer)) return { kind: 'self', target: value };
  const gated = checked.hostClass === 'private' && !isDevelopmentIssuer(issuer);
  const hostname = stripRootDot(parsed.host);
  for (const client of policy.clients) {
    if (!client.redirect_uris.some((uri) => splitTarget(uri)?.host === hostname)) continue;
    if (client.legacy_token_redirect !== 'allow' || policy.legacy_token_redirect !== 'allow') return reject('code_flow_required');
    if (gated && !client.redirect_uris.includes(callbackOf(parsed))) return reject('development_target_requires_registration');
    return { kind: 'legacy', target: value };
  }
  if (gated) return reject('development_target_requires_registration');
  if (policy.legacy_token_redirect !== 'allow') return reject('legacy_redirect_disabled');
  return { kind: 'legacy', target: value };
}

/** callbackOf returns the raw target before its query or fragment, for exact registry comparison. */
function callbackOf(target: SplitTarget): string {
  const end = target.raw.search(/[?#]/);
  return end < 0 ? target.raw : target.raw.slice(0, end);
}

/** classifyCodeRequest admits only the exact registered callback followed by the five code markers. */
function classifyCodeRequest(parsed: SplitTarget, policy: SsoRedirectPolicy): SsoRedirectDecision {
  if (parsed.fragment) return reject('invalid_code_request');
  const values = new Map<string, string>();
  for (const piece of parsed.query.split('&')) {
    const separator = piece.indexOf('=');
    const name = separator < 0 ? piece : piece.slice(0, separator);
    if (separator < 0 || !codeMarkers.includes(name) || values.has(name)) return reject('invalid_code_request');
    values.set(name, piece.slice(separator + 1));
  }
  const clientId = values.get('sso_client_id') ?? '';
  if (values.size !== codeMarkers.length || values.get('sso_flow') !== 'code' || values.get('sso_challenge_method') !== 'S256'
    || !isEncodedEntropy(values.get('sso_state') ?? '') || !isEncodedEntropy(values.get('sso_challenge') ?? '')
    || !isValidClientId(clientId)) {
    return reject('invalid_code_request');
  }
  const client = policy.clients.find((item) => item.client_id === clientId);
  if (!client) return reject('unregistered_client');
  const redirectUri = callbackOf(parsed);
  if (!client.redirect_uris.includes(redirectUri)) return reject('invalid_code_request');
  return { kind: 'client', clientId, redirectUri, target: parsed.raw };
}

/**
 * validateRedirectUri requires a canonical callback that passes the shared redirect rules: https
 * (http only on loopback or private development hosts), lower-case host, no userinfo, default
 * port, query or fragment, an explicit path without dot segments or escapes, and not the Blog
 * host. Registering a private callback is how a production issuer may reach it. It returns an
 * error message or null.
 */
export function validateRedirectUri(uri: string): string | null {
  const checked = inspect(uri, '');
  if ('reason' in checked) return 'is not an allowed redirect target (reject:' + checked.reason + ')';
  if (checked.blog) return 'the Blog hostname is reserved for the built-in blog client';
  if (checked.value !== uri) return 'must be an absolute URL without surrounding whitespace';
  const { parsed } = checked;
  if (!uri.startsWith(parsed.scheme + '://')) return 'scheme must be lower case';
  if (parsed.query !== '' || uri.includes('?') || uri.includes('#')) return 'must not contain a query or fragment';
  if (parsed.host.endsWith('.')) return 'host must not end with a dot';
  if (parsed.hasPort && parsed.port === defaultPort(parsed.scheme)) return 'must omit the default port';
  if (!uri.startsWith(targetOrigin(parsed) + '/')) return 'host must be lower case and the path must start with /';
  if (!isCanonicalPath(parsed.path)) return 'path must use unreserved characters without dot segments or escapes';
  return null;
}

/** parseSsoRedirectPolicy validates the runtime-config policy and returns null when it is unusable. */
export function parseSsoRedirectPolicy(value: unknown): SsoRedirectPolicy | null {
  if (!value || typeof value !== 'object') return null;
  const record = value as Record<string, unknown>;
  const mode = record.legacy_token_redirect;
  if (mode !== 'allow' && mode !== 'deny') return null;
  if (!Array.isArray(record.clients) || record.clients.length > 32) return null;
  const clients: SsoRegisteredClient[] = [];
  for (const item of record.clients as unknown[]) {
    if (!item || typeof item !== 'object') return null;
    const client = item as Record<string, unknown>;
    const id = client.client_id;
    const uris = client.redirect_uris;
    const legacy = client.legacy_token_redirect ?? 'deny';
    if (typeof id !== 'string' || !isValidClientId(id) || id === 'blog' || clients.some((known) => known.client_id === id)
      || !Array.isArray(uris) || uris.length < 1 || uris.length > 8 || (legacy !== 'allow' && legacy !== 'deny')
      || uris.some((uri) => typeof uri !== 'string' || validateRedirectUri(uri) !== null)) {
      return null;
    }
    clients.push({ client_id: id, redirect_uris: [...uris as string[]], legacy_token_redirect: legacy });
  }
  // A registration reserves its hostname, so one hostname belongs to exactly one client and
  // classification never depends on the order in which clients are examined.
  const owners = new Map<string, string>();
  for (const client of clients) {
    for (const uri of client.redirect_uris) {
      const host = splitTarget(uri)?.host ?? '';
      const owner = owners.get(host);
      if (owner !== undefined && owner !== client.client_id) return null;
      owners.set(host, client.client_id);
    }
  }
  return { legacy_token_redirect: mode, clients };
}

function reject(reason: string): SsoRedirectDecision {
  return { kind: 'reject', reason };
}

/** trimAsciiSpace removes only ASCII whitespace, identically to the backend. */
function trimAsciiSpace(value: string): string {
  return value.replace(/^[ \t\n\v\f\r]+|[ \t\n\v\f\r]+$/g, '');
}

function stripRootDot(host: string): string {
  return host.endsWith('.') ? host.slice(0, -1) : host;
}

/** splitTarget splits an absolute http(s) target into its raw components without normalization. */
function splitTarget(value: string): SplitTarget | null {
  let scheme: 'http' | 'https';
  let rest: string;
  const lower = value.slice(0, 8).toLowerCase();
  if (lower === 'https://') {
    scheme = 'https';
    rest = value.slice(8);
  } else if (lower.startsWith('http://')) {
    scheme = 'http';
    rest = value.slice(7);
  } else {
    return null;
  }
  let end = rest.search(/[/?#]/);
  if (end < 0) end = rest.length;
  let authority = rest.slice(0, end);
  let tail = rest.slice(end);
  const parsed: SplitTarget = { raw: value, scheme, userinfo: false, host: '', hasPort: false, port: '', path: '', query: '', fragment: false };
  const at = authority.lastIndexOf('@');
  if (at >= 0) {
    parsed.userinfo = true;
    authority = authority.slice(at + 1);
  }
  if (authority.startsWith('[')) {
    const closing = authority.indexOf(']');
    if (closing < 0) {
      parsed.host = authority.toLowerCase();
    } else {
      parsed.host = authority.slice(0, closing + 1).toLowerCase();
      const after = authority.slice(closing + 1);
      if (after !== '') {
        if (after[0] !== ':') {
          parsed.host += after;
        } else {
          parsed.hasPort = true;
          parsed.port = after.slice(1);
        }
      }
    }
  } else {
    const colon = authority.indexOf(':');
    if (colon >= 0) {
      parsed.host = authority.slice(0, colon).toLowerCase();
      parsed.hasPort = true;
      parsed.port = authority.slice(colon + 1);
    } else {
      parsed.host = authority.toLowerCase();
    }
  }
  const hash = tail.indexOf('#');
  if (hash >= 0) {
    parsed.fragment = true;
    tail = tail.slice(0, hash);
  }
  const question = tail.indexOf('?');
  parsed.path = question >= 0 ? tail.slice(0, question) : tail;
  parsed.query = question >= 0 ? tail.slice(question + 1) : '';
  return parsed;
}

/** targetOrigin renders the comparable origin: lower-case scheme and host, without a default port. */
function targetOrigin(target: SplitTarget): string {
  const port = target.hasPort && target.port !== defaultPort(target.scheme) ? ':' + target.port : '';
  return target.scheme + '://' + target.host + port;
}

function defaultPort(scheme: string): string {
  return scheme === 'https' ? '443' : '80';
}

/** isValidPort accepts 1 to 65535 without leading zeros or signs. */
function isValidPort(port: string): boolean {
  if (!/^[1-9][0-9]{0,4}$/.test(port)) return false;
  const value = Number(port);
  return value >= 1 && value <= 65535;
}

/** classifyHost reports a production (https only), loopback or private development host, or the rejection reason. */
function classifyHost(host: string): { hostClass: HostClass; reason?: string } {
  if (host.startsWith('[')) {
    if (!host.endsWith(']')) return { hostClass: 'production', reason: 'invalid_host' };
    const address = parseIPv6(host.slice(1, -1));
    if (!address) return { hostClass: 'production', reason: 'invalid_host' };
    if (address.slice(0, 15).every((byte) => byte === 0) && address[15] === 1) return { hostClass: 'loopback' };
    if ((address[0] & 0xfe) === 0xfc) return { hostClass: 'private' };
    return { hostClass: 'production', reason: 'host_not_allowed' };
  }
  const name = stripRootDot(host);
  if (!name) return { hostClass: 'production', reason: 'invalid_host' };
  const last = name.slice(name.lastIndexOf('.') + 1);
  if (/^[0-9]+$/.test(last) || last.toLowerCase().startsWith('0x')) {
    const address = parseIPv4(host);
    if (!address) return { hostClass: 'production', reason: 'invalid_host' };
    if (address[0] === 127) return { hostClass: 'loopback' };
    return isPrivateIPv4(address) ? { hostClass: 'private' } : { hostClass: 'production', reason: 'host_not_allowed' };
  }
  if (!/^[a-z0-9-]+(\.[a-z0-9-]+)*$/.test(name)) return { hostClass: 'production', reason: 'invalid_host' };
  if (name === 'laisky.com' || name.endsWith('.laisky.com')) return { hostClass: 'production' };
  if (name === 'localhost') return { hostClass: 'loopback' };
  return { hostClass: 'production', reason: 'host_not_allowed' };
}

/** parseIPv4 accepts only canonical dotted-decimal IPv4 without leading zeros. */
function parseIPv4(host: string): number[] | null {
  const parts = host.split('.');
  if (parts.length !== 4) return null;
  const octets: number[] = [];
  for (const part of parts) {
    if (!/^(0|[1-9][0-9]{0,2})$/.test(part)) return null;
    const value = Number(part);
    if (value > 255) return null;
    octets.push(value);
  }
  return octets;
}

/** isPrivateIPv4 covers RFC 1918 private and RFC 6598 shared (CGNAT, e.g. Tailscale) ranges. */
function isPrivateIPv4([a, b]: number[]): boolean {
  return a === 10 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168) || (a === 100 && b >= 64 && b <= 127);
}

/** parseIPv6 parses lower-case hexadecimal IPv6 without embedded IPv4 or zone identifiers. */
function parseIPv6(text: string): number[] | null {
  if (!/^[0-9a-f:]+$/.test(text)) return null;
  const compressedAt = text.indexOf('::');
  const head = compressedAt >= 0 ? text.slice(0, compressedAt) : text;
  const tail = compressedAt >= 0 ? text.slice(compressedAt + 2) : null;
  if (tail !== null && tail.includes('::')) return null;
  const headGroups = ipv6Groups(head);
  const tailGroups = tail === null ? [] : ipv6Groups(tail);
  if (!headGroups || !tailGroups) return null;
  if (tail === null ? headGroups.length !== 8 : headGroups.length + tailGroups.length > 7) return null;
  const groups = [...headGroups, ...new Array<number>(8 - headGroups.length - tailGroups.length).fill(0), ...tailGroups];
  return groups.flatMap((group) => [group >> 8, group & 0xff]);
}

function ipv6Groups(text: string): number[] | null {
  if (text === '') return [];
  const groups: number[] = [];
  for (const part of text.split(':')) {
    if (!/^[0-9a-f]{1,4}$/.test(part)) return null;
    groups.push(Number.parseInt(part, 16));
  }
  return groups;
}

/** queryNames returns decoded parameter names, splitting only on "&" like the backend. */
function queryNames(query: string): Set<string> {
  const names = new Set<string>();
  if (!query) return names;
  for (const piece of query.split('&')) {
    const separator = piece.indexOf('=');
    names.add(decodeComponent(separator < 0 ? piece : piece.slice(0, separator)));
  }
  return names;
}

/** decodeComponent decodes "+" and valid %XX escapes byte-wise; invalid escapes stay literal. */
function decodeComponent(value: string): string {
  let decoded = '';
  for (let i = 0; i < value.length; i += 1) {
    const c = value[i];
    if (c === '+') {
      decoded += ' ';
    } else if (c === '%' && /^[0-9A-Fa-f]{2}$/.test(value.slice(i + 1, i + 3))) {
      decoded += String.fromCharCode(Number.parseInt(value.slice(i + 1, i + 3), 16));
      i += 2;
    } else {
      decoded += c;
    }
  }
  return decoded;
}

/** isEncodedEntropy accepts the canonical unpadded base64url encoding of exactly 32 bytes. */
export function isEncodedEntropy(value: string): boolean {
  return /^[A-Za-z0-9_-]{43}$/.test(value) && 'AEIMQUYcgkosw048'.includes(value[value.length - 1]);
}

function isValidClientId(id: string): boolean {
  return /^[a-z][a-z0-9_-]{1,63}$/.test(id);
}

/** isCanonicalPath accepts a "/"-led path whose segments browsers serialize unchanged. */
function isCanonicalPath(path: string): boolean {
  if (!path.startsWith('/')) return false;
  return path.slice(1).split('/').every((segment) => segment !== '.' && segment !== '..' && /^[A-Za-z0-9\-._~!$&'()*+,;=:@]*$/.test(segment));
}
