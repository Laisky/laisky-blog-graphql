import { afterEach, describe, expect, it } from 'vitest';
import { configurePublicApiBasePath, normalizePublicApiBasePath, publicApiPath } from './api-base';

afterEach(() => configurePublicApiBasePath(undefined));
describe('shared public API prefix', () => {
  it.each(['', '/', '/mcp', '/gateway/v1'])('joins GraphQL and HTTP for %s', (prefix) => {
    configurePublicApiBasePath(prefix);
    const normalized = normalizePublicApiBasePath(prefix);
    expect(publicApiPath('/query')).toBe(`${normalized}/query`);
    expect(publicApiPath('/tools/file_io/api/file')).toBe(`${normalized}/tools/file_io/api/file`);
  });
  it('distinguishes explicit root from prefix inference', () => {
    expect(publicApiPath('/query', '/mcp/tools/web_search')).toBe('/mcp/query');
    configurePublicApiBasePath('');
    expect(publicApiPath('/query', '/mcp/tools/web_search')).toBe('/query');
  });
  it.each(['https://other.example', '//other.example', '/a/../b', '/a?key=x', '/a\\b', '/%2fother', '/a//b'])('rejects unsafe prefix %s', (prefix) => {
    expect(() => configurePublicApiBasePath(prefix)).toThrow();
  });
});
