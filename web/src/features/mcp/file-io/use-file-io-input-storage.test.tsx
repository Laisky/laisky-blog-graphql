import { renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { useFileIOInputDefaults, usePersistFileIOInputs, type FileIOPersistedInputs } from './use-file-io-input-storage';

const storageKey = 'mcp.file_io.inputs.v1';

function buildInputs(partial?: Partial<FileIOPersistedInputs>): FileIOPersistedInputs {
  return {
    project: 'demo-project',
    currentPath: '/docs',
    depth: 2,
    limit: 100,
    selectedPath: '/docs/readme.md',
    selectedContent: 'hello',
    readOffset: 0,
    readLength: -1,
    writePath: '/docs/out.txt',
    writeMode: 'APPEND',
    writeOffset: 0,
    writeContent: 'new line',
    deletePath: '/docs/old.txt',
    deleteRecursive: false,
    searchQuery: 'guide',
    searchPrefix: '/docs',
    searchLimit: 5,
    ...partial,
  };
}

describe('use-file-io-input-storage', () => {
  it('returns empty defaults when localStorage is empty', () => {
    window.localStorage.clear();
    const { result } = renderHook(() => useFileIOInputDefaults());
    expect(result.current).toEqual({});
  });

  it('returns empty defaults when localStorage value is invalid JSON', () => {
    window.localStorage.setItem(storageKey, '{bad-json');
    const { result } = renderHook(() => useFileIOInputDefaults());
    expect(result.current).toEqual({});
  });

  it('hydrates saved defaults from localStorage', () => {
    const saved = buildInputs({ project: 'saved-project', deleteRecursive: true, searchLimit: 9 });
    window.localStorage.setItem(storageKey, JSON.stringify(saved));
    const { result } = renderHook(() => useFileIOInputDefaults());

    expect(result.current.project).toBe('saved-project');
    expect(result.current.deleteRecursive).toBe(true);
    expect(result.current.searchLimit).toBe(9);
  });

  it('persists latest input values into localStorage', () => {
    window.localStorage.clear();
    const initialInputs = buildInputs({ project: 'p1', writeMode: 'APPEND' });
    const nextInputs = buildInputs({ project: 'p2', writeMode: 'TRUNCATE', deleteRecursive: true });

    const { rerender } = renderHook(({ inputs }) => usePersistFileIOInputs(inputs), {
      initialProps: { inputs: initialInputs },
    });
    rerender({ inputs: nextInputs });

    const raw = window.localStorage.getItem(storageKey);
    expect(raw).not.toBeNull();
    const parsed = JSON.parse(raw ?? '{}') as FileIOPersistedInputs;
    expect(parsed.project).toBe('p2');
    expect(parsed.writeMode).toBe('TRUNCATE');
    expect(parsed.deleteRecursive).toBe(true);
  });
});
