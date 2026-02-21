import { useEffect, useMemo } from 'react';

const memoryInputStorageKey = 'mcp.memory.inputs.v1';

/**
 * MemoryPersistedInputs defines memory console form values persisted across refreshes.
 */
export type MemoryPersistedInputs = {
  project: string;
  sessionId: string;
  userId: string;
  turnId: string;
  maxInputTok: number;
  baseInstructions: string;
  currentInputText: string;
  inputItemsText: string;
  outputItemsText: string;
  listPath: string;
  listDepth: number;
  listLimit: number;
};

/**
 * useMemoryInputDefaults reads persisted memory console inputs from localStorage.
 * It does not accept parameters and returns partial defaults for state initialization.
 */
export function useMemoryInputDefaults(): Partial<MemoryPersistedInputs> {
  return useMemo(() => {
    if (typeof window === 'undefined') {
      return {};
    }

    try {
      return JSON.parse(window.localStorage.getItem(memoryInputStorageKey) ?? '{}') as Partial<MemoryPersistedInputs>;
    } catch {
      return {};
    }
  }, []);
}

/**
 * usePersistMemoryInputs writes the latest memory console form values to localStorage.
 * The inputs parameter is the full persisted payload and the function returns no value.
 */
export function usePersistMemoryInputs(inputs: MemoryPersistedInputs): void {
  const {
    project,
    sessionId,
    userId,
    turnId,
    maxInputTok,
    baseInstructions,
    currentInputText,
    inputItemsText,
    outputItemsText,
    listPath,
    listDepth,
    listLimit,
  } = inputs;

  useEffect(() => {
    if (typeof window === 'undefined') {
      return;
    }

    const payload: MemoryPersistedInputs = {
      project,
      sessionId,
      userId,
      turnId,
      maxInputTok,
      baseInstructions,
      currentInputText,
      inputItemsText,
      outputItemsText,
      listPath,
      listDepth,
      listLimit,
    };

    window.localStorage.setItem(memoryInputStorageKey, JSON.stringify(payload));
  }, [
    project,
    sessionId,
    userId,
    turnId,
    maxInputTok,
    baseInstructions,
    currentInputText,
    inputItemsText,
    outputItemsText,
    listPath,
    listDepth,
    listLimit,
  ]);
}
