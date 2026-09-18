import assert from 'node:assert/strict';
import { describe, it } from 'vitest';
import { encodeMcpHeader, headerBindings, mirrorMcpArguments } from './mcp-headers';

describe('MCP 2026 request header contract', () => {
  it('encodes only values unsafe or ambiguous as plain HTTP field values', () => {
    for (const plain of ['', 'us-west1', 'a b', 'a\tb', '42', 'false']) assert.equal(encodeMcpHeader(plain), plain);
    for (const value of [' padded ', 'a\nb', '漢🙂', '=?base64?literal?=', '\x00x', '\tx']) {
      const encoded = encodeMcpHeader(value);
      assert.match(encoded, /^=\?base64\?.*\?=$/);
      const decoded = new TextDecoder().decode(Uint8Array.from(atob(encoded.slice(9, -2)), (c) => c.charCodeAt(0)));
      assert.equal(decoded, value);
    }
    assert.throws(() => encodeMcpHeader('\ud800'));
  });
  it('rejects empty, duplicate, unsafe, nonprimitive or dynamically reachable annotations', () => {
    for (const node of [{ type: 'string', 'x-mcp-header': '' }, { type: 'string', 'x-mcp-header': 'A\r\nInjected' },
      { type: 'number', 'x-mcp-header': 'A' }, { type: ['string'], 'x-mcp-header': 'A' }, { type: 'object', 'x-mcp-header': 'A' }]) {
      assert.throws(() => headerBindings({ properties: { p: node } }));
    }
    assert.throws(() => headerBindings({ properties: { a: { type: 'string', 'x-mcp-header': 'A' }, b: { type: 'string', 'x-mcp-header': 'a' } } }));
    for (const keyword of ['oneOf', 'allOf', 'anyOf', 'items', 'prefixItems']) {
      assert.throws(() => headerBindings({ [keyword]: [{ properties: { x: { type: 'string', 'x-mcp-header': 'X' } } }] }));
    }
    assert.throws(() => headerBindings({ $defs: { s: { type: 'string', 'x-mcp-header': 'X' } } }));
    assert.throws(() => headerBindings({ type: 'string', 'x-mcp-header': 'Root' }));
  });
  it('does not interpret annotations inside data examples as schema annotations', () => {
    assert.deepEqual(headerBindings({ type: 'object', examples: [{ 'x-mcp-header': 'data' }], default: { 'x-mcp-header': 'data' } }), []);
  });
  it('omits absent/null fields and refuses coercion, unsafe integers and inherited fields', () => {
    const b = headerBindings({ properties: { nested: { properties: { n: { type: 'integer', 'x-mcp-header': 'N' } } } } });
    for (const args of [{}, { nested: null }, { nested: {} }, { nested: { n: null } }, { nested: Object.create({ n: 7 }) }]) assert.deepEqual(mirrorMcpArguments(b, args), {});
    for (const n of ['1', 1.5, true, 2 ** 53, NaN]) assert.throws(() => mirrorMcpArguments(b, { nested: { n } }));
    assert.deepEqual(mirrorMcpArguments(b, { nested: { n: Number.MAX_SAFE_INTEGER } }), { 'Mcp-Param-N': '9007199254740991' });
  });
});
