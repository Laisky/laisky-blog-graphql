import { publicApiPath } from './api-base';
import {
  classifySsoRedirect,
  isEncodedEntropy,
  parseSsoRedirectPolicy,
  type SsoRedirectPolicy,
} from './sso-redirect-policy';

export interface RedirectTarget {
  url: URL | null;
  display: string;
  error?: string;
}

const allowLegacyPolicy: SsoRedirectPolicy = Object.freeze({ legacy_token_redirect: 'allow', clients: [] });
const failClosedPolicy: SsoRedirectPolicy = Object.freeze({ legacy_token_redirect: 'deny', clients: [] });
let activePolicy: SsoRedirectPolicy = allowLegacyPolicy;
let policyUnavailable = false;

/**
 * configureSsoRedirectPolicy applies the issuer's redirect policy from runtime-config.json.
 * A missing configuration (null) or an unusable ssoRedirect value fails closed: only Blog,
 * registered code-flow clients and issuer pages remain reachable. An issuer that predates
 * the policy omits ssoRedirect entirely and keeps the legacy default.
 */
export function configureSsoRedirectPolicy(runtimeConfig: { ssoRedirect?: unknown } | null): void {
  if (runtimeConfig && runtimeConfig.ssoRedirect === undefined) {
    activePolicy = allowLegacyPolicy;
    policyUnavailable = false;
    return;
  }
  const parsed = runtimeConfig ? parseSsoRedirectPolicy(runtimeConfig.ssoRedirect) : null;
  activePolicy = parsed ?? failClosedPolicy;
  policyUnavailable = parsed === null;
}

/** currentSsoRedirectPolicy returns the policy every issuance path enforces. */
export function currentSsoRedirectPolicy(): SsoRedirectPolicy {
  return activePolicy;
}

const policyMessages: Record<string, string> = {
  missing: 'Missing redirect_to parameter. Provide a redirect target to continue.',
  invalid_character: 'Invalid redirect_to URL. It must not contain spaces, control characters, backslashes or non-ASCII characters.',
  too_long: 'Invalid redirect_to URL. It is too long.',
  unsupported_reference: 'Unsupported redirect protocol. Use an absolute https:// URL or a path starting with /.',
  userinfo: 'Unsupported redirect target. Redirect URLs must not contain user credentials.',
  invalid_host: 'Unsupported redirect host. Use the canonical host name or IP address.',
  invalid_port: 'Unsupported redirect port.',
  host_not_allowed: 'Unsupported redirect host. Only *.laisky.com domains or development hosts are allowed.',
  insecure_scheme: 'Insecure redirect target. Use https://; plain http is accepted only for development hosts.',
  invalid_code_request: 'Invalid SSO code request.',
  unregistered_client: 'Unknown SSO client. The application must be registered with the SSO issuer.',
  code_flow_required: 'This application must use the SSO code flow. Restart sign-in from the application.',
  legacy_redirect_disabled: 'This application is not registered for SSO. Ask the SSO operator to register it.',
};

/** SsoRedirectPolicyError reports a target the shared redirect policy refuses, without echoing it. */
export class SsoRedirectPolicyError extends Error {
  readonly reason: string;

  constructor(reason: string) {
    super(reason === 'legacy_redirect_disabled' && policyUnavailable
      ? 'SSO configuration could not be loaded. Reload the page and try again.'
      : policyMessages[reason] ?? 'Unsupported redirect target.');
    this.name = 'SsoRedirectPolicyError';
    this.reason = reason;
  }
}

/** currentIssuerOrigin is the issuer page origin that resolves relative targets and identifies issuer pages. */
function currentIssuerOrigin(): string {
  return typeof window !== 'undefined' ? window.location.origin : '';
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

  if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') {
    return {
      url: null,
      display: parsed.toString(),
      error: 'Unsupported redirect protocol. Only http and https are allowed.',
    };
  }

  // Blog rejections never echo the raw target: a legacy spelling may carry a bearer.
  try {
    const plan = planSsoRedirect(parsed);
    // URL parsing silently repairs controls, backslashes and relative spellings, so the
    // raw input is checked as well before a Blog target is trusted.
    if (plan.kind === 'blog-bootstrap' && (!/^https?:\/\//i.test(raw) || raw.length > 4096 || raw.includes('\\')
      || Array.from(raw).some((character) => character.charCodeAt(0) <= 32))) {
      throw new InvalidBlogBookmarkError();
    }
    if (plan.kind === 'blog-code' && !/^https:\/\/blog\.laisky\.com\/?\?/.test(raw)) {
      throw new Error(invalidBlogCodeRequest);
    }
    // Every other target is also classified from the raw input, so the shared policy
    // decides before any parser repair, exactly as the backend does.
    if (plan.kind !== 'blog-bootstrap' && plan.kind !== 'blog-code') {
      const decision = classifySsoRedirect(rawValue ?? '', baseOrigin, activePolicy);
      if (decision.kind === 'reject') throw new SsoRedirectPolicyError(decision.reason);
    }
  } catch (error) {
    if (error instanceof SsoRedirectPolicyError) {
      return { url: null, display: parsed.origin, error: error.message };
    }
    return error instanceof InvalidBlogBookmarkError
      ? { url: null, display: 'Invalid Blog SSO bookmark', error: error.message }
      : { url: null, display: 'Invalid Blog SSO code request', error: invalidBlogCodeRequest };
  }
  return {
    url: parsed,
    display: parsed.toString(),
  };
}

export function normalizeHostname(hostname: string): string {
  const trimmed = hostname.trim().toLowerCase();
  return trimmed.endsWith('.') ? trimmed.slice(0, -1) : trimmed;
}

/**
 * buildRedirectUrlWithToken is the legacy bearer handoff. It throws for Blog, for every
 * registered client and code request, for targets the shared policy refuses, and for every
 * target once the issuer denies the legacy handoff.
 */
export function buildRedirectUrlWithToken(target: URL, token: string): string {
  if (isBlogSsoClient(target)) throw new Error('Blog requires a single-use SSO code. Restart sign-in from Blog.');
  if (parseBlogCodeTarget(target)) throw new Error('Blog requires a single-use SSO code.');
  const plan = planSsoRedirect(target);
  if (plan.kind !== 'token' && plan.kind !== 'self') throw new Error('Registered SSO clients require a single-use SSO code.');
  if (activePolicy.legacy_token_redirect !== 'allow') throw new SsoRedirectPolicyError('legacy_redirect_disabled');
  const next = new URL(target.toString());
  next.searchParams.set('sso_token', token);
  return next.toString();
}

const blogCallback = 'https://blog.laisky.com';
const codeMarkers = ['sso_flow', 'sso_state', 'sso_challenge', 'sso_challenge_method'];
const blogNavigationMarkers = [
  ...codeMarkers, 'sso_token', 'sso_code', 'code_verifier', 'sso_verifier', 'sso_start', 'sso_return', 'sso_issuer',
];

/**
 * blogSsoProtocolMarkers lists the reserved Blog SSO parameter names. It must equal the
 * markers in blog-sso-protocol.v1.json, which the Blog consumer copies byte-identically.
 */
export const blogSsoProtocolMarkers: readonly string[] = Object.freeze([...blogNavigationMarkers]);

/**
 * fragmentParameterNames lists every parameter name a standard fragment reader can see.
 * The fragment is opaque to URL parsing, but OAuth-style fragment responses read it as
 * form data and hash routers read it as an RFC 3986 relative reference, whose query
 * follows the first "?" and whose nested fragment follows the first "#". Reading every
 * "#"-delimited segment both ways covers both encodings and nested fragments in one
 * linear pass. Names inside values stay ordinary data, matching top-level query parsing.
 */
function fragmentParameterNames(hash: string): string[] {
  const names: string[] = [];
  for (const segment of hash.slice(1).split('#')) {
    names.push(...new URLSearchParams(segment).keys());
    const queryStart = segment.indexOf('?');
    if (queryStart >= 0) names.push(...new URLSearchParams(segment.slice(queryStart + 1)).keys());
  }
  return names;
}

/** maxBlogReturnPathLength bounds the internal route; the paired consumer uses the same limit. */
const maxBlogReturnPathLength = 2048;

/**
 * maxBlogBootstrapUrlLength keeps the serialized bootstrap request line within nginx's
 * default 8 KiB large_client_header_buffers. Form encoding expands a bounded route at
 * most threefold (about 6.2 KiB), so this final check guards future limit changes.
 */
const maxBlogBootstrapUrlLength = 8000;

/** InvalidBlogBookmarkError marks a rejected legacy Blog bookmark without echoing it. */
export class InvalidBlogBookmarkError extends Error {
  constructor() {
    super('Invalid Blog SSO bookmark. Restart sign-in from Blog.');
    this.name = 'InvalidBlogBookmarkError';
  }
}

const invalidBlogCodeRequest = 'Invalid Blog SSO code request.';

/**
 * parseBlogBookmarkReturnPath validates a legacy Blog bookmark and returns its bounded
 * internal route. The route, including a fragment the browser never sent, becomes a Blog
 * request-target query value, so protocol markers are rejected under every fragment reading.
 */
function parseBlogBookmarkReturnPath(target: URL): string {
  const returnPath = target.pathname + target.search + target.hash;
  if (!['http:', 'https:'].includes(target.protocol) || target.hostname !== 'blog.laisky.com'
    || target.port || target.username || target.password || returnPath.length > maxBlogReturnPathLength
    || !returnPath.startsWith('/') || returnPath.startsWith('//') || returnPath.includes('\\')
    || Array.from(returnPath).some((character) => character.charCodeAt(0) <= 32)
    || [...target.searchParams.keys(), ...fragmentParameterNames(target.hash)]
      .some((key) => blogNavigationMarkers.includes(key.toLowerCase()))) {
    throw new InvalidBlogBookmarkError();
  }
  return returnPath;
}

/** buildBlogBootstrapUrl serializes the fixed HTTPS initiation URL for a validated return route. */
function buildBlogBootstrapUrl(returnPath: string): string {
  const bootstrap = new URL(blogCallback);
  bootstrap.searchParams.set('sso_start', '1');
  bootstrap.searchParams.set('sso_return', returnPath);
  // The hint selects a fixed trusted entry only; callback and redemption bindings stay canonical.
  if (typeof window !== 'undefined' && window.location.origin === 'https://mcp.laisky.com') {
    bootstrap.searchParams.set('sso_issuer', 'mcp');
  }
  const serialized = bootstrap.toString();
  if (serialized.length > maxBlogBootstrapUrlLength) throw new InvalidBlogBookmarkError();
  return serialized;
}

/** isBlogSsoClient identifies the existing Blog hostname across old scheme, port and path variants. */
export function isBlogSsoClient(target: URL): boolean {
  return normalizeHostname(target.hostname) === 'blog.laisky.com';
}

/** BlogCodeTarget holds only the public bindings for the registered Blog callback. */
export interface BlogCodeTarget {
  state: string;
  challenge: string;
}

/** isEncodedSSOEntropy checks the canonical unpadded encoding of 32 bytes. */
const isEncodedSSOEntropy = isEncodedEntropy;

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
    throw new Error(invalidBlogCodeRequest);
  }
  return { state, challenge };
}

/**
 * SsoRedirectPlan is the single issuance path a target may take: a bound Blog code,
 * a credential-free Blog bootstrap, a bound code for a registered client, a credential-free
 * issuer page, or the legacy bearer handoff for other clients.
 */
export type SsoRedirectPlan =
  | { kind: 'blog-code'; binding: BlogCodeTarget }
  | { kind: 'blog-bootstrap'; returnPath: string }
  | { kind: 'client-code'; clientId: string; redirectUri: string; binding: BlogCodeTarget }
  | { kind: 'self' }
  | { kind: 'token' };

/**
 * planSsoRedirect classifies a target before any credential is attached. It throws
 * InvalidBlogBookmarkError for an unsafe Blog bookmark, a generic error for a malformed
 * Blog code request and SsoRedirectPolicyError for a target the shared policy refuses,
 * so no target can fall back to the bearer handoff.
 */
export function planSsoRedirect(target: URL): SsoRedirectPlan {
  // The Blog wire format is fixed by blog-sso-protocol.v1.json; sso_client_id marks the
  // generic registered-client format, which the Blog hostname never accepts.
  if (isBlogSsoClient(target) || !target.searchParams.has('sso_client_id')) {
    const binding = parseBlogCodeTarget(target);
    if (binding) return { kind: 'blog-code', binding };
    if (isBlogSsoClient(target)) return { kind: 'blog-bootstrap', returnPath: parseBlogBookmarkReturnPath(target) };
  }
  const decision = classifySsoRedirect(target.href, currentIssuerOrigin(), activePolicy);
  switch (decision.kind) {
    case 'client':
      return {
        kind: 'client-code', clientId: decision.clientId, redirectUri: decision.redirectUri,
        binding: { state: target.searchParams.get('sso_state') ?? '', challenge: target.searchParams.get('sso_challenge') ?? '' },
      };
    case 'self':
      return { kind: 'self' };
    case 'legacy':
      return { kind: 'token' };
    case 'reject':
      throw new SsoRedirectPolicyError(decision.reason);
    default:
      throw new Error(invalidBlogCodeRequest);
  }
}

/** SsoCodeRequest names the public bindings sent to /sso/code and the messages for its failures. */
interface SsoCodeRequest {
  clientId: string;
  redirectUri: string;
  binding: BlogCodeTarget;
  unavailable: string;
  invalid: string;
}

/** requestSsoCode exchanges the session for a single-use code without ever exposing the bearer in a URL. */
async function requestSsoCode(request: SsoCodeRequest, token: string): Promise<string> {
  const response = await fetch(publicApiPath('/sso/code'), {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` },
    body: JSON.stringify({
      client_id: request.clientId, redirect_uri: request.redirectUri, state: request.binding.state,
      code_challenge: request.binding.challenge, code_challenge_method: 'S256',
    }),
    redirect: 'error', credentials: 'omit', cache: 'no-store', referrerPolicy: 'no-referrer',
    signal: AbortSignal.timeout(10_000),
  });
  if (!response.ok) throw new Error(request.unavailable);
  const data: unknown = await readSsoCodeResponse(response, request.invalid);
  if (!data || typeof data !== 'object' || !('code' in data)
    || typeof data.code !== 'string' || !isEncodedSSOEntropy(data.code)
    || !('expires_in' in data) || typeof data.expires_in !== 'number'
    || !Number.isInteger(data.expires_in) || data.expires_in < 1 || data.expires_in > 60) {
    throw new Error(request.invalid);
  }
  const callback = new URL(request.redirectUri);
  callback.searchParams.set('sso_code', data.code);
  callback.searchParams.set('sso_state', request.binding.state);
  return callback.toString();
}

/** buildSelfRedirectUrl opens an issuer page; the session is already stored on this origin. */
function buildSelfRedirectUrl(target: URL): string {
  const next = new URL(target.toString());
  // A stale bearer in the target must not replace the session the issuer just stored.
  if (next.searchParams.has('sso_token')) next.searchParams.delete('sso_token');
  return next.toString();
}

/**
 * buildSsoRedirectUrl issues a bound code (Blog or a registered client), bootstraps an old
 * Blog bookmark, opens an issuer page without a URL bearer, or preserves the legacy handoff.
 */
export async function buildSsoRedirectUrl(target: URL, token: string): Promise<string> {
  const plan = planSsoRedirect(target);
  if (plan.kind === 'token') return buildRedirectUrlWithToken(target, token);
  if (plan.kind === 'self') return buildSelfRedirectUrl(target);
  if (plan.kind === 'blog-bootstrap') return buildBlogBootstrapUrl(plan.returnPath);
  if (plan.kind === 'client-code') {
    return requestSsoCode({
      clientId: plan.clientId, redirectUri: plan.redirectUri, binding: plan.binding,
      unavailable: 'Unable to complete SSO code handoff.', invalid: 'Invalid SSO code response.',
    }, token);
  }
  return requestSsoCode({
    clientId: 'blog', redirectUri: blogCallback, binding: plan.binding,
    unavailable: 'Unable to complete Blog SSO code handoff.', invalid: 'Invalid Blog SSO code response.',
  }, token);
}

/** readSsoCodeResponse bounds streamed JSON and returns only a generic failure for invalid responses. */
async function readSsoCodeResponse(response: Response, invalid: string): Promise<unknown> {
  const contentType = response.headers.get('Content-Type')?.split(';', 1)[0].trim().toLowerCase();
  if (contentType !== 'application/json' || !response.body) {
    throw new Error(invalid);
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
        throw new Error(invalid);
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
    throw new Error(invalid);
  } finally {
    reader.releaseLock();
  }
}
