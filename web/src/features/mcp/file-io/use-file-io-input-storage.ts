import { sha256 } from 'js-sha256';
import { useEffect, useMemo } from 'react';

const legacyStorageKey = 'mcp.file_io.inputs.v1';

/** FileIOPersistedInputs contains drafts and paths, never authorization or version tokens. */
export type FileIOPersistedInputs = {
  project: string;
  currentPath: string;
  depth: number;
  limit: number;
  selectedPath: string;
  selectedContent: string;
  writePath: string;
  writeMode: 'APPEND' | 'OVERWRITE' | 'TRUNCATE';
  writeOffset: number;
  writeContent: string;
  deletePath: string;
  deleteRecursive: boolean;
  renameFromPath: string;
  renameToPath: string;
  renameOverwrite: boolean;
  searchQuery: string;
  searchPrefix: string;
  searchLimit: number;
};

/** fileIOInputStorageKey scopes browser drafts to the provider's normalized API key. */
export function fileIOInputStorageKey(apiKey: string): string | null {
  return apiKey.trim() ? `mcp.file_io.inputs.v2.${sha256(apiKey)}` : null;
}

/** sanitizeFileIOInputs whitelists supported scalar fields, excluding credentials and versions. */
export function sanitizeFileIOInputs(value: unknown): Partial<FileIOPersistedInputs> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {};
  const source = value as Record<string, unknown>;
  const result: Partial<FileIOPersistedInputs> = {};
  for (const field of ['project', 'currentPath', 'selectedPath', 'selectedContent', 'writePath', 'writeContent',
    'deletePath', 'renameFromPath', 'renameToPath', 'searchQuery', 'searchPrefix'] as const) {
    if (typeof source[field] === 'string') result[field] = source[field];
  }
  for (const field of ['depth', 'limit', 'writeOffset', 'searchLimit'] as const) {
    if (typeof source[field] === 'number' && Number.isFinite(source[field])) result[field] = source[field];
  }
  for (const field of ['deleteRecursive', 'renameOverwrite'] as const) {
    if (typeof source[field] === 'boolean') result[field] = source[field];
  }
  if (source.writeMode === 'APPEND' || source.writeMode === 'OVERWRITE' || source.writeMode === 'TRUNCATE') {
    result.writeMode = source.writeMode;
  }
  return result;
}

/** useFileIOInputDefaults restores only the active credential's whitelisted draft fields. */
export function useFileIOInputDefaults(apiKey: string): Partial<FileIOPersistedInputs> {
  const storageKey = useMemo(() => fileIOInputStorageKey(apiKey), [apiKey]);
  useEffect(() => {
    // Legacy data has no trustworthy owner. Remove it, never migrate it to the next key.
    try { window.localStorage.removeItem(legacyStorageKey); } catch { /* Storage can be disabled. */ }
  }, []);
  return useMemo(() => {
    if (!storageKey || typeof window === 'undefined') return {};
    try { return sanitizeFileIOInputs(JSON.parse(window.localStorage.getItem(storageKey) ?? '{}')); }
    catch { return {}; }
  }, [storageKey]);
}

/** usePersistFileIOInputs persists only active-credential drafts; locked callers pass an empty key. */
export function usePersistFileIOInputs(inputs: FileIOPersistedInputs, apiKey: string): void {
  const storageKey = useMemo(() => fileIOInputStorageKey(apiKey), [apiKey]);
  const payload = JSON.stringify(sanitizeFileIOInputs(inputs));
  useEffect(() => {
    if (!storageKey || typeof window === 'undefined') return;
    try { window.localStorage.setItem(storageKey, payload); }
    catch { /* Quota and browser privacy settings must not break the editable console. */ }
  }, [storageKey, payload]);
}
