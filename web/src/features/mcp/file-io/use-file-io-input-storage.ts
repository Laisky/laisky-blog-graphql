import { useEffect, useMemo } from 'react';

const fileIOInputStorageKey = 'mcp.file_io.inputs.v1';

/**
 * FileIOPersistedInputs defines all form-controlled values that should survive page refreshes.
 */
export type FileIOPersistedInputs = {
  project: string;
  currentPath: string;
  depth: number;
  limit: number;
  selectedPath: string;
  selectedContent: string;
  readOffset: number;
  readLength: number;
  writePath: string;
  writeMode: 'APPEND' | 'OVERWRITE' | 'TRUNCATE';
  writeOffset: number;
  writeContent: string;
  deletePath: string;
  deleteRecursive: boolean;
  searchQuery: string;
  searchPrefix: string;
  searchLimit: number;
};

/**
 * useFileIOInputDefaults loads previously saved FileIO input values from localStorage.
 * It does not accept parameters and returns a partial value map for state initialization.
 */
export function useFileIOInputDefaults(): Partial<FileIOPersistedInputs> {
  return useMemo(() => {
    if (typeof window === 'undefined') {
      return {};
    }

    try {
      return JSON.parse(window.localStorage.getItem(fileIOInputStorageKey) ?? '{}') as Partial<FileIOPersistedInputs>;
    } catch {
      return {};
    }
  }, []);
}

/**
 * usePersistFileIOInputs stores the latest FileIO inputs in localStorage whenever they change.
 * The inputs parameter is the full persisted snapshot, and the function returns no value.
 */
export function usePersistFileIOInputs(inputs: FileIOPersistedInputs): void {
  const {
    project,
    currentPath,
    depth,
    limit,
    selectedPath,
    selectedContent,
    readOffset,
    readLength,
    writePath,
    writeMode,
    writeOffset,
    writeContent,
    deletePath,
    deleteRecursive,
    searchQuery,
    searchPrefix,
    searchLimit,
  } = inputs;

  useEffect(() => {
    if (typeof window === 'undefined') {
      return;
    }

    const payload: FileIOPersistedInputs = {
      project,
      currentPath,
      depth,
      limit,
      selectedPath,
      selectedContent,
      readOffset,
      readLength,
      writePath,
      writeMode,
      writeOffset,
      writeContent,
      deletePath,
      deleteRecursive,
      searchQuery,
      searchPrefix,
      searchLimit,
    };

    window.localStorage.setItem(fileIOInputStorageKey, JSON.stringify(payload));
  }, [
    project,
    currentPath,
    depth,
    limit,
    selectedPath,
    selectedContent,
    readOffset,
    readLength,
    writePath,
    writeMode,
    writeOffset,
    writeContent,
    deletePath,
    deleteRecursive,
    searchQuery,
    searchPrefix,
    searchLimit,
  ]);
}
