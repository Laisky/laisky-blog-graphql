import { getPrivateErrorMessage, setUnexpectedRequestReporter } from '@/lib/private-error';
import assert from 'node:assert/strict';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { StreamableMcpClient } from './mcp-transport';

const fixedNames = ['authorization', 'content-type', 'mcp-method', 'mcp-name', 'mcp-protocol-version'];

/** headerNames creates distinct names at either a count or serialized-byte boundary. */
function headerNames(count: number, bytes?: number): string[] {
  const names = Array.from({ length: count }, (_, i) => `H${i}`);
  if (bytes !== undefined) {
    const size = [...fixedNames, ...names.map((name) => `mcp-param-${name}`)].join(',').length;
    assert.ok(bytes >= size);
    names[count - 1] += 'x'.repeat(bytes - size);
  }
  return names;
}

/** fixture drives the complete production client with real Response objects and a controlled server. */
function fixture(names: string[]) {
  const operations: Array<{ headers: Headers; arguments: unknown }> = [];
  const schema = { type: 'object', properties: Object.fromEntries(names.map((name, i) => [
    `p${i}`, { type: 'string', 'x-mcp-header': name },
  ])) };
  const fetcher: typeof fetch = async (_url, init = {}) => {
    const message = JSON.parse(String(init.body));
    const headers = new Headers(init.headers);
    assert.equal(headers.get('Authorization'), 'Bearer synthetic');
    assert.equal(headers.get('Mcp-Method'), message.method);
    const reply = (result: unknown) => Response.json({ jsonrpc: '2.0', id: message.id, result });
    if (message.method === 'server/discover') return reply({ resultType: 'complete', supportedVersions: ['2026-07-28'] });
    if (message.method === 'tools/list') return reply({ resultType: 'complete', ttlMs: 60_000, tools: [
      { name: 'candidate', inputSchema: schema }, { name: 'healthy', inputSchema: { type: 'object' } },
    ] });
    assert.equal(message.method, 'tools/call');
    operations.push({ headers, arguments: message.params.arguments });
    return reply({ resultType: 'complete', content: [] });
  };
  return { client: new StreamableMcpClient('https://fixture.invalid/mcp', 'Bearer synthetic', fetcher), operations };
}

/** unsafeNames checks the actual emitted request, not an inferred catalog-only estimate. */
function unsafeNames(headers: Headers): string[] {
  assert.equal(headers.get('Accept'), 'application/json, text/event-stream');
  return [...headers.keys()].filter((name) => name !== 'accept').sort();
}

describe('MCP preflight rejection before mutation dispatch', () => {
  for (const [label, names] of [
    ['101 headers', headerNames(101)], ['8193 bytes', headerNames(1, 8193)],
    ['100 headers / 8193 bytes', headerNames(100, 8193)],
  ] as const) {
    it(`does not POST an operation from a catalog exceeding ${label}`, async () => {
      const { client, operations } = fixture(names);
      try {
        const args = Object.fromEntries(names.map((_, i) => [`p${i}`, `value-${i}`]));
        await assert.rejects(client.call('candidate', args), /invalid x-mcp-header/);
        assert.equal(operations.length, 0);
        await client.call('healthy');
        assert.equal(operations.length, 1, 'reject only the oversized tool, not the whole catalog');
      } finally { client.close(); }
    });
  }
  for (const count of [1, 50, 100]) {
    it(`dispatches exactly once at 8192 bytes with ${count} mirrored values`, async () => {
      const names = headerNames(count, 8192);
      const { client, operations } = fixture(names);
      const args = Object.fromEntries(names.map((_, i) => [`p${i}`, `value-${i}`]));
      try {
        await client.call('candidate', args);
        assert.equal(operations.length, 1);
        assert.equal(unsafeNames(operations[0].headers).join(',').length, 8192);
        assert.equal(unsafeNames(operations[0].headers).length, count + fixedNames.length);
        assert.deepEqual(operations[0].arguments, args);
      } finally { client.close(); }
    });
  }
  it('validates the possible header set even when this call supplies only null values', async () => {
    const names = headerNames(101);
    const { client, operations } = fixture(names);
    try {
      await assert.rejects(client.call('candidate', Object.fromEntries(names.map((_, i) => [`p${i}`, null]))), /invalid x-mcp-header/);
      assert.equal(operations.length, 0);
    } finally { client.close(); }
  });
  it('skips absent/null values from an admissible catalog without mutating arguments', async () => {
    const names = headerNames(100, 8192);
    const { client, operations } = fixture(names);
    const args = { p0: null, p1: 'present' };
    try {
      await client.call('candidate', args);
      assert.deepEqual(unsafeNames(operations[0].headers), [...fixedNames, 'mcp-param-h1'].sort());
      assert.deepEqual(operations[0].arguments, args);
      assert.deepEqual(args, { p0: null, p1: 'present' });
    } finally { client.close(); }
  });
});

afterEach(() => { setUnexpectedRequestReporter(); });

describe('private MCP transport errors', () => {
  it.each(['json', 'sse'])('retains remote RPC messages and codes from %s', async (format) => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    const fetcher: typeof fetch = async (_url, init) => {
      const request = JSON.parse(String(init?.body));
      const message = { jsonrpc: '2.0', id: request.id, error: { code: -32000, message: 'synthetic private tool arguments' } };
      return format === 'json' ? Response.json(message)
        : new Response(`data: ${JSON.stringify(message)}\n\n`, { headers: { 'Content-Type': 'text/event-stream' } });
    };
    const client = new StreamableMcpClient('https://fixture.invalid/mcp', 'Bearer synthetic', fetcher);
    try {
      const error = await client.call('candidate').catch((failure: unknown) => failure);
      expect(error).toMatchObject({ code: -32000, message: 'synthetic private tool arguments' });
      expect(getPrivateErrorMessage(error)).toBe('MCP request failed');
      expect(observer).not.toHaveBeenCalled();
    } finally { client.close(); }
  });
  it.each(['application/json', 'text/event-stream'])('annotates native parse failures in %s', async (type) => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    const body = type === 'application/json' ? 'synthetic private tool result' : 'data: synthetic private tool result\n\n';
    const fetcher: typeof fetch = async () => new Response(body, { headers: { 'Content-Type': type } });
    const client = new StreamableMcpClient('https://fixture.invalid/mcp', 'Bearer synthetic', fetcher);
    try {
      const error = await client.call('candidate').catch((failure: unknown) => failure);
      expect(error).toBeInstanceOf(SyntaxError);
      expect(getPrivateErrorMessage(error)).toBe('Invalid MCP JSON response');
      expect(observer).toHaveBeenCalledExactlyOnceWith(error, { operation: 'mcp' });
    } finally { client.close(); }
  });
});

describe('actionable malformed MCP protocol replies', () => {
  it('observes invalid protocol shape once without replaying a request', async () => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    const fetcher = vi.fn().mockResolvedValue(Response.json({ jsonrpc: 'bad', id: 'unknown' }));
    const client = new StreamableMcpClient('https://fixture.invalid/mcp', 'Bearer synthetic', fetcher);
    try {
      const error = await client.call('candidate').catch((failure: unknown) => failure);
      expect(error).toMatchObject({ message: 'Invalid JSON-RPC version' });
      expect(observer).toHaveBeenCalledExactlyOnceWith(error, { operation: 'mcp' });
      expect(fetcher).toHaveBeenCalledTimes(1);
    } finally { client.close(); }
  });
});
