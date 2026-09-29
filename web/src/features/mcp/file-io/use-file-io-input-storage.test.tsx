import { cleanup, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fileIOInputStorageKey, sanitizeFileIOInputs, useFileIOInputDefaults, usePersistFileIOInputs,
  type FileIOPersistedInputs } from './use-file-io-input-storage';

function buildInputs(partial?: Partial<FileIOPersistedInputs>): FileIOPersistedInputs {
  return {
    project: 'demo-project', currentPath: '/docs', depth: 2, limit: 100,
    selectedPath: '/docs/readme.md', selectedContent: 'hello', writePath: '/docs/out.txt',
    writeMode: 'APPEND', writeOffset: 0, writeContent: 'new line', deletePath: '/docs/old.txt',
    deleteRecursive: false, renameFromPath: '/docs/old.txt', renameToPath: '/docs/new.txt',
    renameOverwrite: false, searchQuery: 'guide', searchPrefix: '/docs', searchLimit: 5, ...partial,
  };
}
const keyA = 'sk-storage-owner-a';
const keyB = 'sk-storage-owner-b';
beforeEach(() => window.localStorage.clear());
afterEach(() => { cleanup(); vi.restoreAllMocks(); });

describe('credential-scoped FileIO inputs', () => {
  it('returns empty defaults for empty storage, invalid JSON, and malformed stored objects', () => {
    for (const value of [null, '{bad-json', 'null', '[]', '1', '"text"']) {
      if (value !== null) window.localStorage.setItem(fileIOInputStorageKey(keyA)!, value);
      const hook = renderHook(() => useFileIOInputDefaults(keyA));
      expect(hook.result.current).toEqual({}); hook.unmount();
    }
  });
  it('persists the latest valid input values and restores them after a remount', () => {
    const initial = buildInputs({ project: 'first' });
    const next = buildInputs({ project: 'saved-project', writeMode: 'TRUNCATE', searchLimit: 9 });
    const hook = renderHook(({ inputs }) => usePersistFileIOInputs(inputs, keyA), { initialProps: { inputs: initial } });
    hook.rerender({ inputs: next }); hook.unmount();
    const defaults = renderHook(() => useFileIOInputDefaults(keyA));
    expect(defaults.result.current).toEqual(next);
  });
  it('never restores another credential\'s project or draft and retains each credential separately', () => {
    renderHook(() => usePersistFileIOInputs(buildInputs({ project: 'a-private', writeContent: 'a-draft' }), keyA));
    const defaults = renderHook(({ key }) => useFileIOInputDefaults(key), { initialProps: { key: keyB } });
    expect(defaults.result.current).toEqual({});
    renderHook(() => usePersistFileIOInputs(buildInputs({ project: 'b-private', writeContent: 'b-draft' }), keyB));
    defaults.rerender({ key: keyA });
    expect(defaults.result.current.project).toBe('a-private'); expect(defaults.result.current.writeContent).toBe('a-draft');
    defaults.rerender({ key: keyB });
    expect(defaults.result.current.project).toBe('b-private'); expect(defaults.result.current.writeContent).toBe('b-draft');
  });
  it('does not adopt legacy shared drafts under the next active key', () => {
    window.localStorage.setItem('mcp.file_io.inputs.v1', JSON.stringify(buildInputs({ project: 'previous-owner' })));
    const defaults = renderHook(() => useFileIOInputDefaults(keyB));
    expect(defaults.result.current).toEqual({});
    expect(window.localStorage.getItem('mcp.file_io.inputs.v1')).toBeNull();
    expect(window.localStorage.getItem(fileIOInputStorageKey(keyB)!)).toBeNull();
  });
  it('does not read or persist a locked or disconnected session', () => {
    window.localStorage.setItem(fileIOInputStorageKey(keyA)!, JSON.stringify(buildInputs()));
    const defaults = renderHook(() => useFileIOInputDefaults(''));
    renderHook(() => usePersistFileIOInputs(buildInputs({ project: 'locked' }), ''));
    expect(defaults.result.current).toEqual({}); expect(window.localStorage.length).toBe(1);
    expect(fileIOInputStorageKey('')).toBeNull(); expect(fileIOInputStorageKey('   ')).toBeNull();
  });
  it('does not put raw credentials, suggestions, arbitrary fields, or version tokens into draft storage', () => {
    const inputs = { ...buildInputs(), apiKey: keyA, expected_version: 'opaque:1', projects: ['other-user'], snapshot: { version: 'opaque:2' } };
    renderHook(() => usePersistFileIOInputs(inputs, keyA));
    expect(fileIOInputStorageKey(keyA)).not.toContain(keyA);
    const payload = window.localStorage.getItem(fileIOInputStorageKey(keyA)!)!;
    expect(JSON.parse(payload)).toEqual(buildInputs());
    for (const value of [keyA, 'opaque:', 'other-user', 'expected_version', 'snapshot']) expect(payload).not.toContain(value);
  });
  it('whitelists only supported scalar types and finite numbers', () => {
    expect(sanitizeFileIOInputs({ project: [], currentPath: '/safe', limit: Infinity, depth: NaN,
      searchLimit: 5, writeMode: 'INVALID', renameOverwrite: 'true', __proto__: { apiKey: keyA } })).toEqual({ currentPath: '/safe', searchLimit: 5 });
  });
  it('continues without persistence when the browser denies storage access or exceeds quota', () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('disabled'); });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('quota'); });
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('disabled'); });
    expect(renderHook(() => useFileIOInputDefaults(keyA)).result.current).toEqual({});
    expect(() => renderHook(() => usePersistFileIOInputs(buildInputs(), keyA))).not.toThrow();
  });
});
