/** A version belongs to one observed snapshot, never to a path-only cache. */
export type FileSnapshot = {
  path: string;
  content: string;
  content_encoding: 'utf-8';
  version: string;
};

export type ReadPayload = {
  content: string;
  content_encoding: string;
  version: string;
};

/** History row IDs select immutable bytes; never convert them to JS numbers. */
export function requireHistoryID(id: unknown): string {
  if (typeof id !== 'string' || !/^[1-9][0-9]{0,19}$/.test(id)
      || (id.length === 20 && id > '18446744073709551615')) {
    throw new Error('The server must return history IDs as exact positive decimal strings.');
  }
  return id;
}

export function canonicalFilePath(path: string): string {
  const value = path.trim();
  return value && !value.startsWith('/') ? `/${value}` : value;
}

export function requireFileVersion(version: unknown): string {
  if (typeof version !== 'string' || !/^[0-9a-f]{32}:[1-9][0-9]{0,18}$/.test(version)) {
    throw new Error('The server did not return a valid file version. Reload from an upgraded FileIO server.');
  }
  const counter = version.split(':')[1];
  if (counter.length === 19 && counter > '9223372036854775807') {
    throw new Error('The server returned a file revision outside the supported range.');
  }
  // Keep the counter as a string, including values above Number.MAX_SAFE_INTEGER.
  return version;
}

export function fileSnapshot(path: string, payload: ReadPayload): FileSnapshot {
  if (typeof payload.content !== 'string' || payload.content_encoding !== 'utf-8') {
    throw new Error('A complete UTF-8 file snapshot is required before editing.');
  }
  return { ...payload, path: canonicalFilePath(path), content_encoding: 'utf-8', version: requireFileVersion(payload.version) };
}

export function expectedVersion(snapshot: FileSnapshot | null, path: string): string {
  if (!snapshot || snapshot.path !== canonicalFilePath(path)) {
    throw new Error('Read and review this file before changing it. No matching snapshot is loaded.');
  }
  return requireFileVersion(snapshot.version);
}

export function writeCondition(snapshot: FileSnapshot | null, path: string, createOnly: boolean): Record<string, unknown> {
  return createOnly ? { create_only: true } : { expected_version: expectedVersion(snapshot, path) };
}

export function renameCondition(
  source: FileSnapshot | null,
  from: string,
  to: string,
  overwrite: boolean,
  destination: FileSnapshot | null,
): Record<string, unknown> {
  const condition = { expected_version: expectedVersion(source, from) };
  if (!canonicalFilePath(to) || canonicalFilePath(from) === canonicalFilePath(to)) {
    throw new Error('Choose a different, non-empty destination file path.');
  }
  return overwrite
    ? { ...condition, overwrite: true, expected_destination_version: expectedVersion(destination, to) }
    : { ...condition, overwrite: false, destination_must_not_exist: true };
}

export function ifMatch(snapshot: FileSnapshot | null, path: string): Record<string, string> {
  return { 'If-Match': `"${expectedVersion(snapshot, path)}"` };
}

/** Invalidates late responses after another selection, a close, or unmount. */
export class RequestEpoch {
  private epoch = 0;
  next(): number { return ++this.epoch; }
  current(ticket: number): boolean { return ticket === this.epoch; }
  invalidate(): void { ++this.epoch; }
}

export class FileIORequestError extends Error {
  readonly code?: string;
  readonly status?: number;
  constructor(message: string, code?: string, status?: number) {
    super(message);
    this.name = 'FileIORequestError';
    this.code = code;
    this.status = status;
  }
}

export function fileIOErrorMessage(error: unknown): string {
  if (error instanceof FileIORequestError) {
    if (error.code === 'VERSION_CONFLICT' || error.status === 412) {
      return 'Version conflict: another client changed, moved, or deleted this file. Your draft is kept. Copy it before reloading, then review the latest content and recompute your edit. Nothing was retried automatically.';
    }
    if (error.code === 'PRECONDITION_REQUIRED' || error.status === 428) {
      return 'A file version is required. Read the current file first, or explicitly choose Create only for a new file. Your draft is kept.';
    }
  }
  return error instanceof Error ? error.message : 'FileIO request failed. Your draft is kept.';
}
