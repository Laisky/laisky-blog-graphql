import assert from 'node:assert/strict';
import { describe, it } from 'vitest';
import { headerBindings, mirrorMcpArguments } from './mcp-headers';

// These are the CORS-unsafe names actually emitted by a modern tools/call.
// Accept is safelisted for the fixed application/json, text/event-stream value.
const fixedNames = ['authorization', 'content-type', 'mcp-method', 'mcp-name', 'mcp-protocol-version'];

/** schemaWithHeaders builds an ordinary reachable schema with distinct names. */
function schemaWithHeaders(names: string[]) {
  return { type: 'object', properties: Object.fromEntries(names.map((name, i) => [
    `p${i}`, { type: 'string', 'x-mcp-header': name },
  ])) };
}

/** preflightNames follows Fetch's lowercase-set, comma-without-space serialization. */
function preflightNames(names: string[]): string {
  return [...new Set([...fixedNames, ...names.map((name) => `mcp-param-${name.toLowerCase()}`)])].sort().join(',');
}

/** namesAtBytes pads only the final distinct name to an exact total request budget. */
function namesAtBytes(bytes: number, count = 1): string[] {
  const names = Array.from({ length: count }, (_, i) => `H${i}`);
  const padding = bytes - preflightNames(names).length;
  assert.ok(padding >= 0);
  names[names.length - 1] += 'x'.repeat(padding);
  assert.equal(preflightNames(names).length, bytes);
  return names;
}

describe('MCP catalog and preflight name budget', () => {
  it('rejects 101 possible mirrored values before any call chooses arguments', () => {
    const names = Array.from({ length: 101 }, (_, i) => `H${i}`);
    assert.throws(() => headerBindings(schemaWithHeaders(names)), /header.*(budget|limit)/i);
  });

  it('accepts exactly 100 mirrored values in addition to the five fixed unsafe names', () => {
    const names = Array.from({ length: 100 }, (_, i) => `H${i}`);
    const bindings = headerBindings(schemaWithHeaders(names));
    const headers = mirrorMcpArguments(bindings, Object.fromEntries(names.map((_, i) => [`p${i}`, `v${i}`])));
    assert.equal(Object.keys(headers).length, 100);
    assert.equal(preflightNames(names).split(',').length, 105);
  });

  it('accepts exactly 8192 preflight bytes including fixed names and commas', () => {
    for (const count of [1, 50, 100]) {
      const names = namesAtBytes(8192, count);
      assert.equal(headerBindings(schemaWithHeaders(names)).length, count);
    }
  });

  it('rejects 8193 preflight bytes even when dynamic names alone fit in 8192', () => {
    for (const count of [1, 50, 100]) {
      const names = namesAtBytes(8193, count);
      assert.ok(names.map((name) => `mcp-param-${name}`).join(',').length < 8192);
      assert.throws(() => headerBindings(schemaWithHeaders(names)), /header.*(budget|limit)/i);
    }
  });

  it('counts names across nested properties, not just each object separately', () => {
    const properties: Record<string, unknown> = {};
    for (let i = 0; i < 101; i++) properties[`n${i}`] = { type: 'object', properties: {
      value: { type: 'string', 'x-mcp-header': `H${i}` },
    } };
    assert.throws(() => headerBindings({ type: 'object', properties }), /header.*(budget|limit)/i);
  });

  it('skips absent/null values without reserving wire headers or changing caller data', () => {
    const names = namesAtBytes(8192, 100);
    const bindings = headerBindings(schemaWithHeaders(names));
    const args = { p0: null, p1: 'hello' };
    assert.deepEqual(mirrorMcpArguments(bindings, args), { 'Mcp-Param-H1': 'hello' });
    assert.deepEqual(args, { p0: null, p1: 'hello' });
    assert.deepEqual(mirrorMcpArguments(bindings, {}), {});
  });

  it('continues to reject invalid syntax and case-insensitive duplicate names', () => {
    for (const names of [['Tenant', 'tenant'], [''], ['bad\r\nInjected'], ['bad name']]) {
      assert.throws(() => headerBindings(schemaWithHeaders(names)), /Invalid x-mcp-header/);
    }
  });
});
