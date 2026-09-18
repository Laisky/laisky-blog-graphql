import assert from 'node:assert/strict';
import { afterEach, beforeEach, describe, it } from 'vitest';
import { callMcpTool } from './mcp-api';

type Message = { id?: string; method: string; params?: Record<string, unknown> };
type Request = { body: Message; init: RequestInit };
const originalFetch = globalThis.fetch;
let key: string, serial = 0;
let requests: Request[];
let tools: Array<{ name: string; inputSchema: unknown }>;
let answer: (request: Request) => Response | Promise<Response>;
const result = (r: Request, value: unknown, status = 200) => Response.json({ jsonrpc: '2.0', id: r.body.id, result: value }, { status });
const error = (r: Request, code: number, status: number) => Response.json({ jsonrpc: '2.0', id: r.body.id, error: { code, message: 'synthetic failure' } }, { status });

/** modernFixture rejects handshakes, sessions and messages missing per-request routing metadata. */
function modernFixture(r: Request): Response | Promise<Response> {
  if (r.body.method === 'initialize' || r.body.method.startsWith('notifications/')) return error(r, -32601, 404);
  const h = new Headers(r.init.headers);
  assert.equal(h.get('MCP-Protocol-Version'), '2026-07-28');
  assert.equal(h.get('Mcp-Method'), r.body.method);
  assert.equal(h.get('Mcp-Session-Id'), null);
  assert.equal(h.get('Authorization'), `Bearer ${key}`);
  assert.equal(r.init.redirect, 'error');
  const meta = r.body.params?._meta as Record<string, unknown>;
  assert.equal(meta['io.modelcontextprotocol/protocolVersion'], '2026-07-28');
  assert.deepEqual(meta['io.modelcontextprotocol/clientCapabilities'], {});
  if (r.body.method === 'server/discover') return result(r, { resultType: 'complete', supportedVersions: ['2026-07-28', '2025-11-25'] });
  if (r.body.method === 'tools/list') return result(r, { resultType: 'complete', tools, ttlMs: 60_000, cacheScope: 'private' });
  return answer(r);
}

beforeEach(() => {
  key = `synthetic-modern-${++serial}`;
  requests = [];
  tools = [{ name: 'file_read', inputSchema: { type: 'object' } }, { name: 'file_write', inputSchema: { type: 'object' } }];
  answer = (r) => result(r, { resultType: 'complete', content: [{ type: 'text', text: 'ok' }] });
  globalThis.fetch = async (_url, init = {}) => {
    const request = { body: JSON.parse(String(init.body)) as Message, init }; requests.push(request);
    return modernFixture(request);
  };
});
afterEach(() => { globalThis.fetch = originalFetch; });

describe('Modern stateless MCP HTTP behavior', () => {
  it('discovers once and sends tools/call without initialize, notifications or a session', async () => {
    await callMcpTool(key, 'file_read'); await callMcpTool(key, 'file_write');
    assert.deepEqual(requests.map((r) => r.body.method), ['server/discover', 'tools/list', 'tools/call', 'tools/call']);
    assert.equal(new Headers(requests.at(-1)!.init.headers).get('Mcp-Name'), 'file_write');
  });
  it('does not call tools excluded by the authenticated catalog', async () => {
    await assert.rejects(callMcpTool(key, 'unknown_tool'));
    assert.equal(requests.some((r) => r.body.method === 'tools/call'), false);
  });
  it('mirrors nested annotated arguments and safely encodes non-ASCII names and values', async () => {
    tools = [{ name: '讀取', inputSchema: { type: 'object', properties: { tenant: { type: 'object', properties: {
      region: { type: 'string', 'x-mcp-header': 'Region' }, count: { type: 'integer', 'x-mcp-header': 'Count' },
      enabled: { type: 'boolean', 'x-mcp-header': 'Enabled' }, absent: { type: 'string', 'x-mcp-header': 'Absent' },
    } } } } }];
    await callMcpTool(key, '讀取', { tenant: { region: ' Montréal\n', count: 9, enabled: false, absent: null } });
    const headers = new Headers(requests.at(-1)!.init.headers);
    assert.match(headers.get('Mcp-Name')!, /^=\?base64\?/);
    assert.match(headers.get('Mcp-Param-Region')!, /^=\?base64\?/);
    assert.equal(headers.get('Mcp-Param-Count'), '9'); assert.equal(headers.get('Mcp-Param-Enabled'), 'false');
    assert.equal(headers.has('Mcp-Param-Absent'), false);
  });
  it('rejects an invalid annotation without preventing a different valid tool from working', async () => {
    tools.push({ name: 'bad', inputSchema: { type: 'object', oneOf: [{ properties: { x: { type: 'string', 'x-mcp-header': 'X' } } }] } });
    await assert.rejects(callMcpTool(key, 'bad'));
    await callMcpTool(key, 'file_read');
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 1);
  });
  it('does not downgrade recognized protocol, header, method or capability errors', async () => {
    for (const code of [-32020, -32021, -32022, -32601]) {
      globalThis.fetch = async (_url, init = {}) => {
        const r = { body: JSON.parse(String(init.body)) as Message, init }; requests.push(r); return error(r, code, code === -32601 ? 404 : 400);
      };
      await assert.rejects(callMcpTool(`${key}-${code}`, 'file_write'));
    }
    assert.equal(requests.some((r) => r.body.method === 'initialize' || r.body.method === 'tools/call'), false);
  });
  it('rejects truncated discovery diagnostics instead of treating them as a legacy endpoint', async () => {
    globalThis.fetch = async (_url, init = {}) => {
      const r = { body: JSON.parse(String(init.body)) as Message, init }; requests.push(r);
      return new Response(' '.repeat(5000) + JSON.stringify({ error: { code: -32022 } }), { status: 400 });
    };
    await assert.rejects(callMcpTool(key, 'file_write'));
    assert.deepEqual(requests.map((r) => r.body.method), ['server/discover']);
  });
  it('rejects malformed tool result blocks and non-boolean tool status', async () => {
    for (const payload of [{ content: [null] }, { content: ['not a block'] }, { content: [{}] },
      { content: [{ type: 'text', text: 7 }] }, { content: [], isError: 'false' }]) {
      answer = (r) => result(r, { resultType: 'complete', ...payload });
      await assert.rejects(callMcpTool(key, 'file_read'));
    }
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 5);
  });
  it('does not downgrade authentication, rate-limit or service errors', async () => {
    for (const status of [401, 403, 429, 500, 503]) {
      globalThis.fetch = async (_url, init = {}) => {
        const r = { body: JSON.parse(String(init.body)) as Message, init }; requests.push(r); return new Response('rejected', { status });
      };
      await assert.rejects(callMcpTool(`${key}-${status}`, 'file_write'));
    }
    assert.equal(requests.every((r) => r.body.method === 'server/discover'), true);
  });
  it('does not replay a modern mutation on 404 or invalidate its protocol as a legacy session', async () => {
    answer = (r) => error(r, -32601, 404);
    await assert.rejects(callMcpTool(key, 'file_write'));
    answer = (r) => result(r, { resultType: 'complete', content: [] });
    await callMcpTool(key, 'file_read');
    assert.equal(requests.filter((r) => r.body.method === 'server/discover').length, 1);
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 2);
  });
  it('refreshes a stale mirrored schema on the next explicit call, never by replaying the failed call', async () => {
    answer = (r) => error(r, -32020, 400);
    await assert.rejects(callMcpTool(key, 'file_write'));
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 1);
    answer = (r) => result(r, { resultType: 'complete', content: [] });
    await callMcpTool(key, 'file_write');
    assert.equal(requests.filter((r) => r.body.method === 'tools/list').length, 2);
  });
  it('rejects input-required results without submitting an automatic continuation', async () => {
    answer = (r) => result(r, { resultType: 'input_required', inputRequests: [{ method: 'sampling/createMessage' }] });
    await assert.rejects(callMcpTool(key, 'file_write'));
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 1);
  });
  it('ignores empty SSE priming events and consumes the exact final response', async () => {
    answer = (r) => new Response(`id: stream-1\ndata:\n\n: ping\n\ndata: ${JSON.stringify({ jsonrpc: '2.0', id: r.body.id, result: { resultType: 'complete', content: [] } })}\n\n`,
      { headers: { 'Content-Type': 'text/event-stream' } });
    assert.deepEqual((await callMcpTool(key, 'file_read')).content, []);
  });
  it('cancels a pending modern stream without sending legacy cancellation notifications', async () => {
    let closed = false;
    let started!: () => void;
    const ready = new Promise<void>((resolve) => { started = resolve; });
    answer = () => new Response(new ReadableStream({ start() { started(); }, cancel() { closed = true; } }), { headers: { 'Content-Type': 'text/event-stream' } });
    const controller = new AbortController();
    const pending = callMcpTool(key, 'file_write', {}, { signal: controller.signal });
    await Promise.race([ready, pending.then(() => { throw new Error('No stream was opened'); })]); controller.abort();
    await assert.rejects(pending);
    assert.equal(closed, true);
    assert.equal(requests.some((r) => r.body.method.startsWith('notifications/')), false);
  });
  it('rejects independent server requests on a modern stream rather than running unadvertised work', async () => {
    answer = () => new Response('data: {"jsonrpc":"2.0","id":"server","method":"ping"}\n\n', { headers: { 'Content-Type': 'text/event-stream' } });
    await assert.rejects(callMcpTool(key, 'file_read'));
    assert.equal(requests.length, 3);
  });
  it('honors paginated tool catalogs', async () => {
    const normal = globalThis.fetch;
    globalThis.fetch = async (url, init = {}) => {
      const r = { body: JSON.parse(String(init.body)) as Message, init };
      if (r.body.method !== 'tools/list') return normal(url, init);
      requests.push(r);
      return result(r, r.body.params?.cursor ? { resultType: 'complete', tools: [tools[1]], ttlMs: 1000 }
        : { resultType: 'complete', tools: [tools[0]], nextCursor: 'page-2', ttlMs: 1000 });
    };
    await callMcpTool(key, 'file_write');
    assert.equal(requests.filter((r) => r.body.method === 'tools/list').length, 2);
  });
  it('bounds repeated catalog cursors before a tool is invoked', async () => {
    const normal = globalThis.fetch;
    globalThis.fetch = async (url, init = {}) => {
      const r = { body: JSON.parse(String(init.body)) as Message, init };
      if (r.body.method !== 'tools/list') return normal(url, init);
      requests.push(r);
      return result(r, { resultType: 'complete', tools: [], nextCursor: 'same' });
    };
    await assert.rejects(callMcpTool(key, 'file_write'));
    assert.equal(requests.filter((r) => r.body.method === 'tools/list').length, 2);
    assert.equal(requests.some((r) => r.body.method === 'tools/call'), false);
  });
  it('snapshots caller-owned arguments before asynchronous discovery', async () => {
    let release!: () => void;
    const gate = new Promise<void>((resolve) => { release = resolve; });
    const normal = globalThis.fetch;
    globalThis.fetch = async (url, init = {}) => { await gate; return normal(url, init); };
    const args = { content: 'original', expected_version: 'original-token', nested: { value: 'before' } };
    const pending = callMcpTool(key, 'file_write', args);
    args.content = 'different'; args.expected_version = 'different-token'; args.nested.value = 'after';
    release(); await pending;
    assert.deepEqual(requests.at(-1)!.body.params?.arguments, { content: 'original', expected_version: 'original-token', nested: { value: 'before' } });
  });
  it('times out a pending modern response and does not replay it', async () => {
    let closed = false;
    answer = () => new Response(new ReadableStream({ cancel() { closed = true; } }), { headers: { 'Content-Type': 'text/event-stream' } });
    await assert.rejects(callMcpTool(key, 'file_write', {}, { timeoutMs: 30 }), (e: unknown) => e instanceof DOMException && e.name === 'TimeoutError');
    assert.equal(closed, true);
    assert.equal(requests.filter((r) => r.body.method === 'tools/call').length, 1);
  });

});
