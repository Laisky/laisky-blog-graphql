import { afterEach, describe, expect, it, vi } from 'vitest';
import { callFileAPI } from './client';
import { isProjectQuery, listProjects, parseProjectPage, PROJECT_PAGE_SIZE } from './project-options';

vi.mock('./client', () => ({ callFileAPI: vi.fn() }));
afterEach(() => vi.clearAllMocks());

describe('project discovery protocol', () => {
  it('sends only credential, query, bounded limit, cursor, and abort signal through the existing API', async () => {
    vi.mocked(callFileAPI).mockResolvedValue({ projects: ['literal_under'], has_more: false });
    const signal = new AbortController().signal;
    expect(await listProjects('owner-a', '_', 'Alpha', signal)).toEqual({ projects: ['literal_under'], has_more: false });
    expect(callFileAPI).toHaveBeenCalledWith('owner-a', 'GET', '/projects', {
      query: { q: '_', after: 'Alpha', limit: '50' }, signal,
    });
  });
  it('rejects unauthenticated and malformed requests before the transport', async () => {
    const signal = new AbortController().signal;
    await expect(listProjects('', '', '', signal)).rejects.toThrow('API key');
    for (const query of ['%', '../x', 'a'.repeat(129)]) await expect(listProjects('a', query, '', signal)).rejects.toThrow();
    await expect(listProjects('a', '', '../x', signal)).rejects.toThrow();
    expect(callFileAPI).not.toHaveBeenCalled();
  });
  it('accepts literal punctuation and all valid length boundaries', () => {
    for (const query of ['', '.', '-', '_', 'MCP', 'a'.repeat(128)]) expect(isProjectQuery(query)).toBe(true);
    for (const query of ['%', '\\', 'a b', '*', 'a'.repeat(129)]) expect(isProjectQuery(query)).toBe(false);
  });
  it('accepts an empty page and a correctly bounded continuation', () => {
    expect(parseProjectPage({ projects: [], has_more: false })).toEqual({ projects: [], has_more: false });
    expect(parseProjectPage({ projects: ['A', 'b'], has_more: true, next_cursor: 'b' }))
      .toEqual({ projects: ['A', 'b'], has_more: true, next_cursor: 'b' });
  });
  it.each([null, {}, { projects: null, has_more: false }, { projects: [123], has_more: false },
    { projects: ['<script>'], has_more: false }, { projects: ['a', 'a'], has_more: false },
    { projects: [], has_more: 'false' }, { projects: [], has_more: true, next_cursor: 'a' },
    { projects: ['a'], has_more: true }, { projects: ['a'], has_more: true, next_cursor: 'b' },
    { projects: Array.from({ length: PROJECT_PAGE_SIZE + 1 }, (_, index) => `p${index}`), has_more: false },
  ])('rejects malformed, duplicate, oversized, or ambiguous pages: %j', (value) => {
    expect(() => parseProjectPage(value)).toThrow();
  });
  it('rejects a cursor that cannot advance', () => {
    expect(() => parseProjectPage({ projects: ['a'], has_more: true, next_cursor: 'a' }, 'a')).toThrow();
  });
});
