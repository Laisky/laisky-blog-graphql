import { renderHook } from '@testing-library/react';
import { describe, expect, it } from 'vitest';

import { useMemoryInputDefaults, usePersistMemoryInputs, type MemoryPersistedInputs } from './use-memory-input-storage';

const storageKey = 'mcp.memory.inputs.v1';

function buildInputs(partial?: Partial<MemoryPersistedInputs>): MemoryPersistedInputs {
  return {
    memoryPlugin: 'rag',
    project: 'demo-project',
    sessionId: 'session-1',
    userId: 'user-1',
    turnId: 'turn-1',
    maxInputTok: 120000,
    baseInstructions: 'remember context',
    currentInputText: '[]',
    inputItemsText: '[]',
    outputItemsText: '[]',
    listPath: '/memory',
    listDepth: 8,
    listLimit: 200,
    ...partial,
  };
}

describe('use-memory-input-storage', () => {
  it('returns empty defaults when localStorage is empty', () => {
    window.localStorage.clear();
    const { result } = renderHook(() => useMemoryInputDefaults());
    expect(result.current).toEqual({});
  });

  it('returns empty defaults when localStorage value is invalid JSON', () => {
    window.localStorage.setItem(storageKey, '{bad-json');
    const { result } = renderHook(() => useMemoryInputDefaults());
    expect(result.current).toEqual({});
  });

  it('hydrates saved defaults from localStorage', () => {
    const saved = buildInputs({ memoryPlugin: 'pageindex', project: 'saved-project', listLimit: 9 });
    window.localStorage.setItem(storageKey, JSON.stringify(saved));
    const { result } = renderHook(() => useMemoryInputDefaults());

    expect(result.current.memoryPlugin).toBe('pageindex');
    expect(result.current.project).toBe('saved-project');
    expect(result.current.listLimit).toBe(9);
  });

  it('persists latest input values into localStorage', () => {
    window.localStorage.clear();
    const initialInputs = buildInputs({ memoryPlugin: 'rag', project: 'p1' });
    const nextInputs = buildInputs({ memoryPlugin: 'pageindex', project: 'p2', listDepth: 4 });

    const { rerender } = renderHook(({ inputs }) => usePersistMemoryInputs(inputs), {
      initialProps: { inputs: initialInputs },
    });
    rerender({ inputs: nextInputs });

    const raw = window.localStorage.getItem(storageKey);
    expect(raw).not.toBeNull();
    const parsed = JSON.parse(raw ?? '{}') as MemoryPersistedInputs;
    expect(parsed.memoryPlugin).toBe('pageindex');
    expect(parsed.project).toBe('p2');
    expect(parsed.listDepth).toBe(4);
  });
});
