import { encodeMcpHeader, headerBindings, mirrorMcpArguments, type HeaderBinding } from './mcp-headers';

/** Minimal tools-only MCP Streamable HTTP transport; no RPC is replayed implicitly. */
export interface McpCallOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
}

/** McpRequestError preserves transport/RPC status without interpreting it as tool success. */
export class McpRequestError extends Error {
  readonly status?: number;
  readonly code?: number;
  constructor(message: string, status?: number, code?: number) {
    super(message);
    this.name = 'McpRequestError';
    this.status = status;
    this.code = code;
  }
}

/** McpToolResult retains both text blocks and the protocol's optional structured content. */
export interface McpToolResult {
  content: Array<{ type: string; text?: string; [key: string]: unknown }>;
  isError?: boolean;
  structuredContent?: unknown;
}

type Envelope = Record<string, unknown>;
type Session = { id?: string; protocolVersion: string; era: 'modern' | 'legacy' };
type Fetcher = typeof fetch;
const MODERN_VERSION = '2026-07-28';
const LEGACY_VERSIONS = ['2025-11-25', '2025-06-18', '2025-03-26'];
const CLIENT_INFO = { name: 'laisky-blog-web', version: '1.0.0' };
const MAX_RESPONSE_BYTES = 64 * 1024 * 1024;
const DEFAULT_TIMEOUT_MS = 5 * 60 * 1000;
let requestSequence = 0n;

/** deadline combines caller cancellation and an absolute timeout, with explicit cleanup. */
function deadline(timeoutMs: number, signals: Array<AbortSignal | undefined>) {
  if (!Number.isSafeInteger(timeoutMs) || timeoutMs <= 0 || timeoutMs > 30 * 60 * 1000) {
    throw new McpRequestError('MCP timeout must be between 1 ms and 30 minutes');
  }
  const controller = new AbortController();
  const listeners: Array<() => void> = [];
  for (const signal of signals) {
    if (!signal) continue;
    const onAbort = () => controller.abort(signal.reason);
    if (signal.aborted) onAbort();
    else { signal.addEventListener('abort', onAbort, { once: true }); listeners.push(() => signal.removeEventListener('abort', onAbort)); }
  }
  const timer = setTimeout(() => controller.abort(new DOMException('MCP request timed out', 'TimeoutError')), timeoutMs);
  return { signal: controller.signal, dispose: () => { clearTimeout(timer); listeners.forEach((remove) => remove()); } };
}

/** waitForSignal lets one caller leave a shared handshake without cancelling other callers. */
function waitForSignal<T>(pending: Promise<T>, signal: AbortSignal): Promise<T> {
  if (signal.aborted) return Promise.reject(signal.reason);
  return new Promise<T>((resolve, reject) => {
    const aborted = () => reject(signal.reason);
    signal.addEventListener('abort', aborted, { once: true });
    pending.then(resolve, reject).finally(() => signal.removeEventListener('abort', aborted));
  });
}

/** objectEnvelope rejects batching and primitives before examining any RPC fields. */
function objectEnvelope(value: unknown): Envelope {
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new McpRequestError('Invalid MCP response envelope');
  const envelope = value as Envelope;
  if (envelope.jsonrpc !== '2.0') throw new McpRequestError('Invalid JSON-RPC version');
  return envelope;
}

/** consumeEnvelope accepts only the response for this request; notifications are not results. */
async function consumeEnvelope(value: unknown, id: string, onRequest: (message: Envelope) => Promise<void>) {
  const message = objectEnvelope(value);
  if (typeof message.method === 'string') {
    if ('id' in message) await onRequest(message);
    return { done: false as const };
  }
  if (message.id !== id || ('result' in message) === ('error' in message)) throw new McpRequestError('Mismatched or invalid MCP response');
  if ('error' in message) {
    const error = message.error as { message?: unknown; code?: unknown } | null;
    throw new McpRequestError(typeof error?.message === 'string' ? error.message : 'MCP request failed', undefined,
      typeof error?.code === 'number' ? error.code : undefined);
  }
  return { done: true as const, value: message.result };
}

/** readRPC handles JSON or incremental SSE, bounded in bytes and correlated by exact ID. */
async function readRPC(response: Response, id: string, signal: AbortSignal, onRequest: (message: Envelope) => Promise<void>): Promise<unknown> {
  const type = response.headers.get('Content-Type')?.split(';')[0].trim().toLowerCase();
  if (type !== 'application/json' && type !== 'text/event-stream') {
    await response.body?.cancel();
    throw new McpRequestError('MCP server returned neither JSON nor SSE');
  }
  if (!response.body) throw new McpRequestError('MCP response has no body');
  const reader = response.body.getReader();
  const decoder = new TextDecoder('utf-8', { fatal: true });
  let buffer = '', total = 0;
  let data: string[] = [];
  let eventType = '';
  try {
    while (true) {
      const chunk = await waitForSignal(reader.read(), signal);
      total += chunk.value?.byteLength ?? 0;
      if (total > MAX_RESPONSE_BYTES) throw new McpRequestError('MCP response exceeded the 64 MiB limit');
      buffer += decoder.decode(chunk.value, { stream: !chunk.done });
      if (type === 'application/json') {
        if (!chunk.done) continue;
        const result = await consumeEnvelope(JSON.parse(buffer), id, onRequest);
        if (!result.done) throw new McpRequestError('MCP response did not contain a result');
        return result.value;
      }
      while (true) {
        const offset = buffer.search(/[\r\n]/);
        if (offset < 0 || (!chunk.done && buffer[offset] === '\r' && offset === buffer.length - 1)) break;
        const line = buffer.slice(0, offset);
        buffer = buffer.slice(offset + (buffer[offset] === '\r' && buffer[offset + 1] === '\n' ? 2 : 1));
        if (line === '') {
          if (data.join('\n').trim() && (eventType === '' || eventType === 'message')) {
            const result = await consumeEnvelope(JSON.parse(data.join('\n')), id, onRequest);
            if (result.done) return result.value;
          }
          data = []; eventType = '';
        } else if (!line.startsWith(':')) {
          const colon = line.indexOf(':');
          const field = colon < 0 ? line : line.slice(0, colon);
          const value = colon < 0 ? '' : line.slice(colon + 1).replace(/^ /, '');
          if (field === 'data') data.push(value);
          else if (field === 'event') eventType = value;
          // Event IDs/retry hints are not permission to re-POST a possibly committed mutation.
        }
      }
      if (chunk.done) throw new McpRequestError('MCP stream ended before the matching response; the request was not replayed');
    }
  } finally {
    // A successful response may arrive before the server closes the stream.
    // Cancellation here releases transport resources; it does not undo server effects.
    await reader.cancel().catch(() => undefined);
    reader.releaseLock();
  }
}

/** httpFailure bounds diagnostic reads; arbitrary response bodies are never echoed. */
async function httpFailure(response: Response, signal: AbortSignal): Promise<{ code?: number; legacyExpired: boolean; truncated: boolean }> {
  const reader = response.body?.getReader();
  if (!reader) return { legacyExpired: false, truncated: false };
  let bytes = 0, text = '', truncated = false;
  const decoder = new TextDecoder();
  try {
    while (bytes <= 4096) {
      const chunk = await waitForSignal(reader.read(), signal);
      const piece = chunk.value?.slice(0, 4097 - bytes);
      bytes += piece?.length ?? 0;
      if (bytes > 4096) { truncated = true; break; }
      text += decoder.decode(piece, { stream: !chunk.done });
      if (chunk.done) break;
    }
    if (truncated) return { legacyExpired: false, truncated: true };
    try {
      const error = JSON.parse(text)?.error;
      if (typeof error?.code === 'number') return { code: error.code, legacyExpired: false, truncated: false };
    } catch { /* A legacy server may send a plain HTTP diagnostic. */ }
    return { legacyExpired: /^(invalid session id|session terminated)[.!\s]*$/i.test(text.trim()), truncated: false };
  } finally { await reader.cancel().catch(() => undefined); reader.releaseLock(); }
}

type Catalog = { entries: Map<string, HeaderBinding[]>; rejected: Set<string>; expires: number };

/** StreamableMcpClient supports modern per-request metadata and legacy handshakes.
 * It advertises no sampling, elicitation, roots, tasks or subscription capability.
 * A side-effecting tools/call is sent at most once per explicit call().
 */
export class StreamableMcpClient {
  private session?: Session;
  private initializing?: Promise<Session>;
  private catalog?: Catalog;
  private catalogLoading?: Promise<Catalog>;
  private readonly lifetime = new AbortController();
  private readonly endpoint: string;
  private readonly authorization: string;
  private readonly fetcher: Fetcher;

  constructor(endpoint: string, authorization: string, fetcher: Fetcher = (...args) => fetch(...args)) {
    this.endpoint = endpoint;
    this.authorization = authorization;
    this.fetcher = fetcher;
  }

  /** close cancels local requests and forgets negotiation without issuing a mutation. */
  close(): void {
    this.session = undefined; this.catalog = undefined;
    this.lifetime.abort(new DOMException('MCP client closed', 'AbortError'));
  }

  /** post carries identical routing metadata in headers/body; credentials never follow redirects. */
  private post(message: Envelope, session: Session | undefined, signal: AbortSignal, extra: Record<string, string> = {}) {
    signal.throwIfAborted();
    const headers: Record<string, string> = {
      'Content-Type': 'application/json', Accept: 'application/json, text/event-stream', Authorization: this.authorization,
      ...(session ? { 'MCP-Protocol-Version': session.protocolVersion } : {}),
      ...(session?.era === 'legacy' && session.id ? { 'Mcp-Session-Id': session.id } : {}),
      ...extra,
    };
    let outgoing = message;
    if (session?.era === 'modern') {
      if (typeof message.method !== 'string') throw new McpRequestError('Modern MCP does not accept client response POSTs');
      const params = (message.params ?? {}) as Record<string, unknown>;
      outgoing = { ...message, params: { ...params, _meta: {
        'io.modelcontextprotocol/protocolVersion': session.protocolVersion,
        'io.modelcontextprotocol/clientInfo': CLIENT_INFO,
        'io.modelcontextprotocol/clientCapabilities': {},
      } } };
      headers['Mcp-Method'] = message.method;
      if (message.method === 'tools/call') headers['Mcp-Name'] = encodeMcpHeader(String(params.name));
    }
    return this.fetcher(this.endpoint, { method: 'POST', headers, body: JSON.stringify(outgoing), signal, redirect: 'error' });
  }

  /** notify is used only for legacy lifecycle/responses, never on modern HTTP. */
  private async notify(message: Envelope, session: Session, signal: AbortSignal): Promise<void> {
    const response = await this.post(message, session, signal);
    await response.body?.cancel();
    if (response.status !== 202) throw new McpRequestError('MCP notification was not accepted', response.status);
  }

  /** respond handles legacy pings; unadvertised work and modern server RPCs are rejected. */
  private async respond(message: Envelope, session: Session, signal: AbortSignal): Promise<void> {
    if (session.era === 'modern') throw new McpRequestError('Modern MCP stream contains an independent server request');
    if (typeof message.id !== 'string' && typeof message.id !== 'number') throw new McpRequestError('Invalid server request ID');
    const payload = message.method === 'ping' ? { result: {} } : { error: { code: -32601, message: 'Client capability is not supported' } };
    await this.notify({ jsonrpc: '2.0', id: message.id, ...payload }, session, signal);
  }

  /** initialize probes safely with discovery; no tool request is used for era detection. */
  private async initialize(): Promise<Session> {
    const timeout = deadline(30_000, [this.lifetime.signal]);
    try {
      const modern: Session = { era: 'modern', protocolVersion: MODERN_VERSION };
      const discoverID = `web-mcp-${++requestSequence}`;
      const discover = await this.post({ jsonrpc: '2.0', id: discoverID, method: 'server/discover', params: {} }, modern, timeout.signal);
      if (discover.ok) {
        const value = await readRPC(discover, discoverID, timeout.signal, (msg) => this.respond(msg, modern, timeout.signal));
        const result = value as { resultType?: unknown; supportedVersions?: unknown } | null;
        if (result?.resultType !== 'complete' || !Array.isArray(result.supportedVersions) || !result.supportedVersions.includes(MODERN_VERSION)) {
          throw new McpRequestError('MCP discovery did not confirm the requested modern protocol');
        }
        this.lifetime.signal.throwIfAborted();
        this.session = modern;
        return modern;
      }
      const failure = await httpFailure(discover, timeout.signal);
      // Recognized modern errors must not cause a downgrade. Some legacy
      // implementations send generic JSON-RPC errors rather than plain HTTP text.
      // Accept only the known generic legacy 400 shapes; never truncated evidence.
      const legacyRejection = failure.code === undefined
        || (discover.status === 400 && [-32600, -32601, -32000, -32001].includes(failure.code));
      if (failure.truncated || ![400, 404, 405].includes(discover.status) || !legacyRejection) {
        throw new McpRequestError('MCP discovery failed; protocol was not downgraded', discover.status, failure.code);
      }
      const id = `web-mcp-${++requestSequence}`;
      const response = await this.post({ jsonrpc: '2.0', id, method: 'initialize', params: {
        protocolVersion: LEGACY_VERSIONS[0], capabilities: {}, clientInfo: CLIENT_INFO,
      } }, undefined, timeout.signal);
      if (!response.ok) { await response.body?.cancel(); throw new McpRequestError('MCP initialization failed', response.status); }
      const token = response.headers.get('Mcp-Session-Id') ?? undefined;
      if (token !== undefined && (token.length === 0 || token.length > 1024 || !/^[\x21-\x7e]+$/.test(token))) {
        await response.body?.cancel(); throw new McpRequestError('MCP server returned an invalid session ID');
      }
      const negotiated: Session = { id: token, protocolVersion: LEGACY_VERSIONS[0], era: 'legacy' };
      const raw = await readRPC(response, id, timeout.signal, (message) => this.respond(message, negotiated, timeout.signal));
      const version = (raw as { protocolVersion?: unknown } | null)?.protocolVersion;
      if (typeof version !== 'string' || !LEGACY_VERSIONS.includes(version)) throw new McpRequestError('MCP server selected an unsupported protocol version');
      negotiated.protocolVersion = version;
      await this.notify({ jsonrpc: '2.0', method: 'notifications/initialized' }, negotiated, timeout.signal);
      this.lifetime.signal.throwIfAborted();
      this.session = negotiated;
      return negotiated;
    } finally { timeout.dispose(); }
  }

  /** ready deduplicates negotiation without tying it to any individual caller's abort. */
  private ready(): Promise<Session> {
    if (this.session) return Promise.resolve(this.session);
    if (!this.initializing) {
      const pending = this.initialize().finally(() => { if (this.initializing === pending) this.initializing = undefined; });
      this.initializing = pending;
    }
    return this.initializing;
  }

  /** loadCatalog validates every modern tool's mirrored headers, with bounded pagination. */
  private async loadCatalog(session: Session): Promise<Catalog> {
    const timeout = deadline(30_000, [this.lifetime.signal]);
    const entries = new Map<string, HeaderBinding[]>(), rejected = new Set<string>(), cursors = new Set<string>();
    let cursor: string | undefined, ttl = 300_000, count = 0;
    try {
      do {
        const id = `web-mcp-${++requestSequence}`;
        const response = await this.post({ jsonrpc: '2.0', id, method: 'tools/list', params: cursor ? { cursor } : {} }, session, timeout.signal);
        if (!response.ok) { await response.body?.cancel(); throw new McpRequestError('MCP tool discovery failed', response.status); }
        const raw = await readRPC(response, id, timeout.signal, (msg) => this.respond(msg, session, timeout.signal));
        const result = raw as { tools?: unknown; nextCursor?: unknown; ttlMs?: unknown; resultType?: unknown } | null;
        if (result?.resultType !== 'complete' || !Array.isArray(result.tools)) throw new McpRequestError('Invalid modern MCP tool catalog');
        ttl = Math.min(ttl, typeof result.ttlMs === 'number' && Number.isFinite(result.ttlMs) ? Math.max(0, result.ttlMs) : 0);
        for (const item of result.tools) {
          if (++count > 10_000) throw new McpRequestError('MCP catalog exceeds the tool limit');
          if (!item || typeof item !== 'object' || typeof item.name !== 'string' || entries.has(item.name) || rejected.has(item.name)) {
            throw new McpRequestError('Invalid or duplicate MCP tool name');
          }
          try { entries.set(item.name, headerBindings(item.inputSchema)); }
          catch { rejected.add(item.name); } // Invalid annotations exclude only this tool, not the valid catalog.
        }
        const next = result.nextCursor;
        if (next !== undefined && (typeof next !== 'string' || !next || cursors.has(next) || cursors.size >= 100)) throw new McpRequestError('Invalid MCP catalog cursor');
        cursor = next as string | undefined;
        if (cursor) cursors.add(cursor);
      } while (cursor);
      return { entries, rejected, expires: Date.now() + ttl };
    } finally { timeout.dispose(); }
  }

  /** toolHeaders uses only the current credential's validated tool schema. */
  private async toolHeaders(session: Session, name: string, args: Record<string, unknown>, signal: AbortSignal): Promise<Record<string, string>> {
    if (session.era !== 'modern') return {};
    let catalog = this.catalog;
    if (!catalog || catalog.expires <= Date.now()) {
      if (!this.catalogLoading) {
        const pending = this.loadCatalog(session).then((next) => { this.catalog = next; return next; })
          .finally(() => { if (this.catalogLoading === pending) this.catalogLoading = undefined; });
        this.catalogLoading = pending;
      }
      catalog = await waitForSignal(this.catalogLoading, signal);
    }
    if (catalog.rejected.has(name)) throw new McpRequestError('MCP tool has invalid x-mcp-header annotations');
    const bindings = catalog.entries.get(name);
    if (!bindings) throw new McpRequestError('MCP tool is not in the current authorized catalog');
    return mirrorMcpArguments(bindings, args);
  }

  /** call sends one operation; expired sessions and stale schemas only affect the next explicit call. */
  async call(name: string, args: Record<string, unknown> = {}, options: McpCallOptions = {}): Promise<McpToolResult> {
    options.signal?.throwIfAborted();
    this.lifetime.signal.throwIfAborted();
    // Snapshot caller-owned input before any handshake await. In particular, a late
    // form edit must not change the bytes or precondition of an in-flight request.
    const input = JSON.parse(JSON.stringify(args)) as Record<string, unknown>;
    const timeout = deadline(options.timeoutMs ?? DEFAULT_TIMEOUT_MS, [options.signal, this.lifetime.signal]);
    let sent = false;
    let observed: Session | undefined;
    const id = `web-mcp-${++requestSequence}`;
    try {
      observed = await waitForSignal(this.ready(), timeout.signal);
      const mirrored = await this.toolHeaders(observed, name, input, timeout.signal);
      timeout.signal.throwIfAborted();
      sent = true;
      const response = await this.post({ jsonrpc: '2.0', id, method: 'tools/call', params: { name, arguments: input } }, observed, timeout.signal, mirrored);
      if (!response.ok) {
        const failure = await httpFailure(response, timeout.signal);
        const expired = observed.era === 'legacy' && observed.id && (response.status === 404 || (response.status === 400 && failure.legacyExpired));
        if (expired && this.session === observed) this.session = undefined;
        if (failure.code === -32020) this.catalog = undefined;
        throw new McpRequestError(expired ? 'MCP session expired. Re-read state before retrying; this request was not replayed.'
          : `MCP request failed (HTTP ${response.status}); it was not replayed`, response.status, failure.code);
      }
      const result = await readRPC(response, id, timeout.signal, (message) => this.respond(message, observed!, timeout.signal));
      if (!result || typeof result !== 'object' || Array.isArray(result)) throw new McpRequestError('Invalid MCP tool result');
      if (observed.era === 'modern' && (!('resultType' in result) || result.resultType !== 'complete')) {
        throw new McpRequestError('MCP requires an unsupported interaction; the operation was not replayed');
      }
      if (!('content' in result) || !Array.isArray(result.content)) throw new McpRequestError('MCP tool response is missing content');
      if ('isError' in result && typeof result.isError !== 'boolean') throw new McpRequestError('Invalid MCP tool error status');
      for (const block of result.content) {
        if (!block || typeof block !== 'object' || Array.isArray(block) || typeof block.type !== 'string' || !block.type
            || (block.type === 'text' && typeof block.text !== 'string')) throw new McpRequestError('Invalid MCP content block');
      }
      return result as McpToolResult;
    } catch (error) {
      if (sent && observed?.era === 'legacy' && timeout.signal.aborted && !this.lifetime.signal.aborted) {
        const cancel = deadline(1_000, [this.lifetime.signal]);
        void this.notify({ jsonrpc: '2.0', method: 'notifications/cancelled', params: { requestId: id, reason: 'Caller cancelled or timed out' } }, observed, cancel.signal)
          .catch(() => undefined).finally(cancel.dispose);
      }
      // In modern HTTP, closing the response stream is the cancellation signal.
      throw error;
    } finally { timeout.dispose(); }
  }
}
