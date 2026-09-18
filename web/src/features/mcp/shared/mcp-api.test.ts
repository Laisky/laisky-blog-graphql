import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'vitest';
import { callMcpTool } from './mcp-api';

type RequestBody = { id?: string | number; method: string; params?: Record<string, unknown> };
type CapturedRequest = { body: RequestBody; init: RequestInit };
const originalFetch = globalThis.fetch;
let sequence = 0;
let key: string;
let requests: CapturedRequest[];
let session: string | undefined;
let reply: (request: CapturedRequest) => Response | Promise<Response>;

/** jsonReply echoes the actual request ID rather than bypassing response correlation. */
function jsonReply(request: CapturedRequest, result: unknown, headers: Record<string, string> = {}) {
  return Response.json({ jsonrpc: '2.0', id: request.body.id, result }, { headers });
}
/** sseReply splits bytes at arbitrary boundaries, including within UTF-8 characters. */
function sseReply(text: string, width = 3): Response {
  const bytes = new TextEncoder().encode(text);
  return new Response(new ReadableStream<Uint8Array>({
    start(controller) {
      for (let i = 0; i < bytes.length; i += width) controller.enqueue(bytes.slice(i, i + width));
      controller.close();
    },
  }), { headers: { 'Content-Type': 'text/event-stream; charset=utf-8' } });
}
/** fixture supplies deterministic transport responses without replacing the MCP client. */
function fixture(request: CapturedRequest): Response | Promise<Response> {
  if (request.body.method === 'server/discover') return new Response('legacy endpoint', { status: 400 });
  if (request.body.method === 'initialize') {
    return jsonReply(request, { protocolVersion: '2025-06-18', capabilities: { tools: {} }, serverInfo: { name: 'fixture', version: '1' } },
      session ? { 'Mcp-Session-Id': session } : {});
  }
  if (request.body.method === 'notifications/initialized' || request.body.method === 'notifications/cancelled') return new Response(null, { status: 202 });
  return reply(request);
}

beforeEach(() => {
  key = `synthetic-lifecycle-${++sequence}`;
  requests = [];
  session = 'synthetic-session';
  reply = (request) => jsonReply(request, { content: [{ type: 'text', text: 'ok' }], isError: false });
  globalThis.fetch = async (_url, init = {}) => {
    const request = { body: JSON.parse(String(init.body)) as RequestBody, init };
    requests.push(request);
    return fixture(request);
  };
});
afterEach(() => { globalThis.fetch = originalFetch; });

describe('MCP Streamable HTTP lifecycle regressions', () => {
  it('accepts stateless servers and initializes only once without inventing a session ID', async () => {
    session = undefined;
    await callMcpTool(key, 'file_read');
    await callMcpTool(key, 'file_stat');
    assert.deepEqual(requests.map((r) => r.body.method), ['server/discover', 'initialize', 'notifications/initialized', 'tools/call', 'tools/call']);
    for (const request of requests) assert.equal(new Headers(request.init.headers).has('Mcp-Session-Id'), false);
  });
  it('negotiates before operations and includes required Accept/version/session headers', async () => {
    await callMcpTool(key, 'file_read');
    assert.deepEqual(requests.map((r) => r.body.method), ['server/discover', 'initialize', 'notifications/initialized', 'tools/call']);
    assert.equal(requests[2].body.id, undefined);
    for (const [index, request] of requests.entries()) {
      const headers = new Headers(request.init.headers);
      assert.equal(headers.get('Authorization'), `Bearer ${key}`);
      assert.equal(headers.get('Accept'), 'application/json, text/event-stream');
      if (index > 1) {
        assert.equal(headers.get('MCP-Protocol-Version'), '2025-06-18');
        assert.equal(headers.get('Mcp-Session-Id'), session);
      }
    }
  });
  it('falls back on a generic legacy protocol rejection without replaying a tool operation', async () => {
    for (const code of [-32600, -32000, -32001]) {
      globalThis.fetch = async (_url, init = {}) => {
        const r = { body: JSON.parse(String(init.body)) as RequestBody, init }; requests.push(r);
        if (r.body.method === 'server/discover') return Response.json({ jsonrpc: '2.0', id: r.body.id,
          error: { code, message: 'legacy protocol rejection' } }, { status: 400 });
        return fixture(r);
      };
      await callMcpTool(`${key}-${code}`, 'file_read');
    }
    assert.equal(requests.filter((r) => r.body.method === 'initialize').length, 3);
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 3);
  });
  it('shares one in-flight initialization for concurrent callers', async () => {
    await Promise.all(Array.from({ length: 12 }, () => callMcpTool(key, 'file_read')));
    assert.equal(requests.filter((r) => r.body.method === 'initialize').length, 1);
    assert.equal(requests.filter((r) => r.body.method === 'notifications/initialized').length, 1);
    const calls = requests.filter((r) => r.body.method === 'tools/call');
    assert.equal(calls.length, 12);
    assert.equal(new Set(calls.map((r) => r.body.id)).size, 12);
  });
  it('accepts SSE initialization as well as JSON initialization', async () => {
    globalThis.fetch = async (_url, init = {}) => {
      const request = { body: JSON.parse(String(init.body)) as RequestBody, init };
      requests.push(request);
      if (request.body.method === 'initialize') return sseReply(`data: ${JSON.stringify({ jsonrpc: '2.0', id: request.body.id, result: { protocolVersion: '2025-06-18', capabilities: { tools: {} } } })}\n\n`);
      return fixture(request);
    };
    assert.equal((await callMcpTool(key, 'file_read')).isError, false);
  });
  it('reads split UTF-8 SSE events, comments, notifications, CRLF and multiline data', async () => {
    reply = (request) => {
      const result = JSON.stringify({ jsonrpc: '2.0', id: request.body.id, result: { content: [{ type: 'text', text: '漢🙂 café' }] } });
      return sseReply(`: heartbeat\r\nevent: message\r\ndata: {"jsonrpc":"2.0","method":"notifications/progress","params":{}}\r\n\r\ndata: ${result.slice(0, 1)}\r\ndata: ${result.slice(1)}\r\n\r\n`, 1);
    };
    const result = await callMcpTool(key, 'file_read');
    assert.equal(result.content[0].text, '漢🙂 café');
  });
  it('closes a long-lived response stream once the matching result arrives', async () => {
    let cancelled = false;
    reply = (request) => new Response(new ReadableStream<Uint8Array>({
      start(controller) { controller.enqueue(new TextEncoder().encode(`data: ${JSON.stringify({ jsonrpc: '2.0', id: request.body.id, result: { content: [] } })}\n\n`)); },
      cancel() { cancelled = true; },
    }), { headers: { 'Content-Type': 'text/event-stream' } });
    let timer: ReturnType<typeof setTimeout> | undefined;
    try {
      await Promise.race([callMcpTool(key, 'file_read'), new Promise<never>((_, reject) => { timer = setTimeout(() => reject(new Error('matching SSE result was not consumed')), 200); })]);
    } finally { clearTimeout(timer); }
    assert.equal(cancelled, true);
  });
  it('does not silently replay mutations after an expired session; the next explicit request renegotiates', async () => {
    reply = () => new Response('Not Found', { status: 404 });
    await assert.rejects(callMcpTool(key, 'file_write', { expected_version: 'original', content: 'draft' }));
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 1);
    session = 'replacement-session';
    reply = (request) => jsonReply(request, { content: [] });
    await callMcpTool(key, 'file_read');
    assert.equal(requests.filter((r) => r.body.method === 'initialize').length, 2);
    assert.equal(new Headers(requests.at(-1)!.init.headers).get('Mcp-Session-Id'), 'replacement-session');
  });
  it('does not replay mutations for the legacy explicit invalid-session response either', async () => {
    reply = () => new Response('Invalid session ID\n', { status: 400 });
    await assert.rejects(callMcpTool(key, 'file_write', { create_only: true }));
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 1);
  });
  it('never retries network errors, 5xx errors or incomplete SSE after a mutation may have committed', async () => {
    for (const failure of [
      () => Promise.reject(new TypeError('synthetic connection lost')),
      () => new Response('temporary failure', { status: 503 }),
      () => sseReply('data: {"jsonrpc":"2.0","method":"notifications/progress"}\n\n'),
    ]) {
      reply = failure;
      const before = requests.filter((r) => r.body.method === 'tools/call').length;
      await assert.rejects(callMcpTool(key, 'file_write'));
      assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, before + 1);
    }
  });
  it('rejects mismatched response IDs instead of attributing another request result', async () => {
    reply = () => Response.json({ jsonrpc: '2.0', id: 'another-request', result: { content: [] } });
    await assert.rejects(callMcpTool(key, 'file_read'));
  });
  it('rejects malformed response envelopes and unsupported content types', async () => {
    for (const response of [Response.json({ result: { content: [] } }), new Response('<html>gateway</html>', { headers: { 'Content-Type': 'text/html' } })]) {
      reply = () => response;
      await assert.rejects(callMcpTool(key, 'file_read'));
    }
  });
  it('preserves structured tool errors without retrying or converting them to success', async () => {
    reply = (request) => jsonReply(request, { content: [{ type: 'text', text: 'conflict' }], isError: true, structuredContent: { code: 'VERSION_CONFLICT' } });
    const result = await callMcpTool(key, 'file_write');
    assert.equal(result.isError, true);
    assert.deepEqual(result.structuredContent, { code: 'VERSION_CONFLICT' });
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 1);
  });
  it('isolates session state for different credentials', async () => {
    await callMcpTool(key, 'file_read');
    session = 'other-account-session';
    await callMcpTool(`${key}-other`, 'file_read');
    await callMcpTool(key, 'file_read');
    assert.equal(requests.filter((r) => r.body.method === 'initialize').length, 2);
    assert.equal(new Headers(requests.at(-1)!.init.headers).get('Mcp-Session-Id'), 'synthetic-session');
  });
  it('rejects an unsupported negotiated protocol before any tool execution', async () => {
    globalThis.fetch = async (_url, init = {}) => {
      const request = { body: JSON.parse(String(init.body)) as RequestBody, init };
      requests.push(request);
      if (request.body.method === 'server/discover') return new Response('legacy', { status: 400 });
      return jsonReply(request, { protocolVersion: 'unsupported', capabilities: { tools: {} } }, { 'Mcp-Session-Id': 'unsupported-session' });
    };
    await assert.rejects(callMcpTool(key, 'file_read'));
    assert.equal(requests.length, 2);
  });
  it('does not execute tools when the initialized notification is rejected', async () => {
    globalThis.fetch = async (_url, init = {}) => {
      const request = { body: JSON.parse(String(init.body)) as RequestBody, init };
      requests.push(request);
      if (request.body.method === 'notifications/initialized') return new Response(null, { status: 503 });
      return fixture(request);
    };
    await assert.rejects(callMcpTool(key, 'file_read'));
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 0);
  });
  it('does not submit anything for an already-aborted caller', async () => {
    const controller = new AbortController(); controller.abort();
    await assert.rejects(callMcpTool(key, 'file_write', {}, { signal: controller.signal }));
    assert.equal(requests.length, 0);
  });
  it('propagates a rejected legacy initialization without invoking any tool', async () => {
    globalThis.fetch = async (_url, init = {}) => {
      const request = { body: JSON.parse(String(init.body)) as RequestBody, init }; requests.push(request);
      if (request.body.method === 'initialize') return new Response(null, { status: 503 });
      return fixture(request);
    };
    await assert.rejects(callMcpTool(key, 'file_write'));
    assert.equal(requests.some((r) => r.body.method === 'tools/call'), false);
  });
  it('allows another caller to finish a shared handshake after the first caller aborts', async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const normal = globalThis.fetch;
    globalThis.fetch = async (url, init = {}) => { await gate; return normal(url, init); };
    const controller = new AbortController();
    const first = callMcpTool(key, 'file_write', {}, { signal: controller.signal });
    const rejected = assert.rejects(first);
    const second = callMcpTool(key, 'file_read');
    controller.abort(); release();
    await rejected; await second;
    assert.equal(requests.filter((r) => r.body.method === 'initialize').length, 1);
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 1);
    assert.equal((requests.at(-1)!.body.params as { name: string }).name, 'file_read');
  });
  it('recognizes a split legacy expired-session diagnostic without replay', async () => {
    reply = () => new Response(new ReadableStream<Uint8Array>({ start(c) {
      c.enqueue(new TextEncoder().encode('Invalid ')); c.enqueue(new TextEncoder().encode('session ID\n')); c.close();
    } }), { status: 400 });
    await assert.rejects(callMcpTool(key, 'file_write'));
    reply = (r) => jsonReply(r, { content: [] }); await callMcpTool(key, 'file_read');
    assert.equal(requests.filter((r) => r.body.method === 'initialize').length, 2);
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 2);
  });

});
