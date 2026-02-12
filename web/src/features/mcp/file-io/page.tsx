import { FileText, FolderOpen, RefreshCw, Search, ShieldAlert, Trash2, UploadCloud } from 'lucide-react';
import { useMemo, useState } from 'react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { useApiKey } from '@/lib/api-key-context';
import { cn } from '@/lib/utils';

import { callMcpTool, type CallToolResponse } from '../shared/mcp-api';
import { useFileIOInputDefaults, usePersistFileIOInputs } from './use-file-io-input-storage';

type FileEntry = {
  name: string;
  path: string;
  type: 'FILE' | 'DIRECTORY';
  size: number;
  created_at: string;
  updated_at: string;
};

type FileListPayload = {
  entries: FileEntry[];
  has_more: boolean;
};

type FileStatPayload = {
  exists: boolean;
  type: 'FILE' | 'DIRECTORY';
  size: number;
  created_at: string;
  updated_at: string;
};

type FileReadPayload = {
  content: string;
  content_encoding: string;
};

type FileWritePayload = {
  bytes_written: number;
};

type FileDeletePayload = {
  deleted_count: number;
};

type FileSearchChunk = {
  file_path: string;
  file_seek_start_bytes: number;
  file_seek_end_bytes: number;
  chunk_content: string;
  score: number;
};

type FileSearchPayload = {
  chunks: FileSearchChunk[];
};

type StructuredToolResponse<T> = CallToolResponse & {
  structured?: T;
  structuredContent?: T;
  structured_content?: T;
};

const dateFormatter = new Intl.DateTimeFormat(undefined, {
  dateStyle: 'medium',
  timeStyle: 'medium',
});

const indentClasses = ['pl-0', 'pl-3', 'pl-6', 'pl-9', 'pl-12', 'pl-16'];
const inputLabelClass = 'text-xs font-medium uppercase tracking-wide text-muted-foreground';

function extractStructuredPayload<T>(result: CallToolResponse): T {
  const structuredResult = result as StructuredToolResponse<T>;
  const structured = structuredResult.structured ?? structuredResult.structuredContent ?? structuredResult.structured_content;
  if (structured) {
    return structured as T;
  }

  const contentText = result.content?.find((item) => typeof item.text === 'string')?.text;
  if (!contentText) {
    throw new Error('Tool response missing structured payload');
  }

  const parsed = JSON.parse(contentText) as T;
  return parsed;
}

function extractToolError(result: CallToolResponse): string {
  const contentText = result.content?.find((item) => typeof item.text === 'string')?.text;
  if (!contentText) {
    return 'Tool execution failed.';
  }
  try {
    const parsed = JSON.parse(contentText) as { message?: string };
    if (parsed?.message) {
      return parsed.message;
    }
  } catch (error) {
    if (error instanceof Error) {
      return error.message;
    }
  }
  return contentText;
}

function formatTimestamp(value: string): string {
  if (!value) return '—';
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return value;
  return dateFormatter.format(parsed);
}

function entryDepth(basePath: string, entryPath: string): number {
  const prefix = basePath ? `${basePath.replace(/\/$/, '')}/` : '/';
  const relative = entryPath.startsWith(prefix) ? entryPath.slice(prefix.length) : entryPath.replace(/^\//, '');
  if (!relative) {
    return 0;
  }
  return Math.max(0, relative.split('/').length - 1);
}

function entryIndentClass(depth: number): string {
  const index = Math.min(depth, indentClasses.length - 1);
  return indentClasses[index];
}

export function FileIOPage() {
  const { apiKey } = useApiKey();
  const persistedInputs = useFileIOInputDefaults();
  const [project, setProject] = useState(persistedInputs.project ?? '');
  const [currentPath, setCurrentPath] = useState(persistedInputs.currentPath ?? '');
  const [depth, setDepth] = useState(persistedInputs.depth ?? 2);
  const [limit, setLimit] = useState(persistedInputs.limit ?? 200);
  const [entries, setEntries] = useState<FileEntry[]>([]);
  const [hasMore, setHasMore] = useState(false);
  const [browserError, setBrowserError] = useState<string | null>(null);
  const [isListing, setIsListing] = useState(false);

  const [selectedPath, setSelectedPath] = useState(persistedInputs.selectedPath ?? '');
  const [selectedStat, setSelectedStat] = useState<FileStatPayload | null>(null);
  const [selectedContent, setSelectedContent] = useState(persistedInputs.selectedContent ?? '');
  const [readOffset, setReadOffset] = useState(persistedInputs.readOffset ?? 0);
  const [readLength, setReadLength] = useState(persistedInputs.readLength ?? -1);
  const [readError, setReadError] = useState<string | null>(null);
  const [isReading, setIsReading] = useState(false);

  const [writePath, setWritePath] = useState(persistedInputs.writePath ?? '');
  const [writeMode, setWriteMode] = useState<'APPEND' | 'OVERWRITE' | 'TRUNCATE'>(
    persistedInputs.writeMode === 'OVERWRITE' || persistedInputs.writeMode === 'TRUNCATE' ? persistedInputs.writeMode : 'APPEND',
  );
  const [writeOffset, setWriteOffset] = useState(persistedInputs.writeOffset ?? 0);
  const [writeContent, setWriteContent] = useState(persistedInputs.writeContent ?? '');
  const [writeError, setWriteError] = useState<string | null>(null);
  const [writeInfo, setWriteInfo] = useState<string | null>(null);
  const [isWriting, setIsWriting] = useState(false);

  const [deletePath, setDeletePath] = useState(persistedInputs.deletePath ?? '');
  const [deleteRecursive, setDeleteRecursive] = useState(persistedInputs.deleteRecursive ?? false);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [deleteInfo, setDeleteInfo] = useState<string | null>(null);
  const [isDeleting, setIsDeleting] = useState(false);

  const [searchQuery, setSearchQuery] = useState(persistedInputs.searchQuery ?? '');
  const [searchPrefix, setSearchPrefix] = useState(persistedInputs.searchPrefix ?? '');
  const [searchLimit, setSearchLimit] = useState(persistedInputs.searchLimit ?? 5);
  const [searchResults, setSearchResults] = useState<FileSearchChunk[]>([]);
  const [searchError, setSearchError] = useState<string | null>(null);
  const [isSearching, setIsSearching] = useState(false);

  usePersistFileIOInputs({
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
  });

  const entryRows = useMemo(() => {
    return entries.map((entry) => ({
      ...entry,
      depth: entryDepth(currentPath, entry.path),
    }));
  }, [entries, currentPath]);

  async function callTool<T>(toolName: string, args: Record<string, unknown>) {
    if (!apiKey) {
      throw new Error('API key is required');
    }
    const result = await callMcpTool(apiKey, toolName, args);
    if (result.isError) {
      throw new Error(extractToolError(result));
    }
    return extractStructuredPayload<T>(result);
  }

  async function loadList(pathOverride?: string) {
    const listPath = pathOverride ?? currentPath;
    setBrowserError(null);
    setIsListing(true);
    try {
      const payload = await callTool<FileListPayload>('file_list', {
        project,
        path: listPath,
        depth,
        limit,
      });
      setCurrentPath(listPath);
      setEntries(payload.entries ?? []);
      setHasMore(Boolean(payload.has_more));
    } catch (error) {
      setBrowserError(error instanceof Error ? error.message : 'Failed to list files.');
      setEntries([]);
      setHasMore(false);
    } finally {
      setIsListing(false);
    }
  }

  async function loadFile(targetPath: string) {
    setSelectedPath(targetPath);
    setReadError(null);
    setIsReading(true);
    try {
      const statPayload = await callTool<FileStatPayload>('file_stat', { project, path: targetPath });
      setSelectedStat(statPayload);
      if (statPayload.type === 'FILE') {
        const readPayload = await callTool<FileReadPayload>('file_read', {
          project,
          path: targetPath,
          offset: readOffset,
          length: readLength,
        });
        setSelectedContent(readPayload.content ?? '');
      } else {
        setSelectedContent('');
      }
    } catch (error) {
      setReadError(error instanceof Error ? error.message : 'Failed to read file.');
    } finally {
      setIsReading(false);
    }
  }

  async function submitWrite() {
    setWriteError(null);
    setWriteInfo(null);
    setIsWriting(true);
    try {
      const payload = await callTool<FileWritePayload>('file_write', {
        project,
        path: writePath,
        content: writeContent,
        content_encoding: 'utf-8',
        offset: writeOffset,
        mode: writeMode,
      });
      setWriteInfo(`Wrote ${payload.bytes_written} bytes.`);
      if (writePath) {
        setSelectedPath(writePath);
        await loadFile(writePath);
      }
      await loadList();
    } catch (error) {
      setWriteError(error instanceof Error ? error.message : 'Failed to write file.');
    } finally {
      setIsWriting(false);
    }
  }

  async function submitDelete() {
    setDeleteError(null);
    setDeleteInfo(null);
    setIsDeleting(true);
    try {
      const payload = await callTool<FileDeletePayload>('file_delete', {
        project,
        path: deletePath,
        recursive: deleteRecursive,
      });
      setDeleteInfo(`Deleted ${payload.deleted_count} item(s).`);
      if (deletePath === selectedPath) {
        setSelectedPath('');
        setSelectedContent('');
        setSelectedStat(null);
      }
      await loadList();
    } catch (error) {
      setDeleteError(error instanceof Error ? error.message : 'Failed to delete path.');
    } finally {
      setIsDeleting(false);
    }
  }

  async function submitSearch() {
    setSearchError(null);
    setIsSearching(true);
    setSearchResults([]);
    try {
      const payload = await callTool<FileSearchPayload>('file_search', {
        project,
        query: searchQuery,
        path_prefix: searchPrefix,
        limit: searchLimit,
      });
      setSearchResults(payload.chunks ?? []);
    } catch (error) {
      setSearchError(error instanceof Error ? error.message : 'Failed to search.');
    } finally {
      setIsSearching(false);
    }
  }

  const selectedIsDirectory = selectedStat?.type === 'DIRECTORY';
  const isProjectMissing = project.trim().length === 0;

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <FolderOpen className="h-4 w-4" />
          <span>FileIO Console</span>
        </div>
        <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">file_io</h1>
        <p className="max-w-3xl text-lg text-muted-foreground">
          Manage project-scoped files, inspect metadata, and search content with the unified FileIO toolset.
        </p>
      </section>

      <div className="grid gap-6 lg:grid-cols-[1.2fr_0.8fr]">
        <Card className="border border-border/60 bg-card">
          <CardHeader>
            <CardTitle className="text-xl">Workspace Browser</CardTitle>
            <CardDescription>Browse project paths and load file content.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="grid gap-3 md:grid-cols-2">
              <div className="space-y-1">
                <label htmlFor="file-io-project" className={cn(inputLabelClass, isProjectMissing && 'text-destructive')}>
                  Project <span className={cn('font-semibold', isProjectMissing ? 'text-destructive' : 'text-muted-foreground')}>*</span>
                </label>
                <Input
                  id="file-io-project"
                  placeholder="Required"
                  required
                  aria-required="true"
                  className={cn(isProjectMissing && 'border-destructive/70 focus-visible:ring-destructive/40')}
                  value={project}
                  onChange={(event) => setProject(event.target.value)}
                />
                {isProjectMissing && <p className="text-xs font-medium text-destructive">Required field</p>}
              </div>
              <div className="space-y-1">
                <label htmlFor="file-io-current-path" className={inputLabelClass}>
                  Path Prefix
                </label>
                <Input
                  id="file-io-current-path"
                  placeholder="Empty means root (/)"
                  value={currentPath}
                  onChange={(event) => setCurrentPath(event.target.value)}
                />
              </div>
              <div className="space-y-1">
                <label htmlFor="file-io-depth" className={inputLabelClass}>
                  Browse Depth
                </label>
                <Input
                  id="file-io-depth"
                  placeholder="0 or greater"
                  type="number"
                  value={depth}
                  onChange={(event) => setDepth(Number(event.target.value))}
                  min={0}
                />
              </div>
              <div className="space-y-1">
                <label htmlFor="file-io-list-limit" className={inputLabelClass}>
                  List Limit
                </label>
                <Input
                  id="file-io-list-limit"
                  placeholder="1 or greater"
                  type="number"
                  value={limit}
                  onChange={(event) => setLimit(Number(event.target.value))}
                  min={1}
                />
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button type="button" onClick={() => loadList()} disabled={!project || isListing}>
                <RefreshCw className={cn('mr-2 h-4 w-4', isListing && 'animate-spin')} />
                Refresh list
              </Button>
              {hasMore && <Badge variant="secondary">List truncated</Badge>}
              {browserError && <span className="text-sm text-destructive">{browserError}</span>}
            </div>
            <div className="rounded-lg border border-border/60 bg-muted/30">
              <div className="flex items-center justify-between border-b border-border/60 px-4 py-2 text-xs font-semibold uppercase tracking-widest text-muted-foreground">
                <span>Entries</span>
                <span>{entries.length} item(s)</span>
              </div>
              <div className="max-h-[320px] overflow-y-auto px-4 py-3">
                {entryRows.length === 0 ? (
                  <div className="text-sm text-muted-foreground">No entries yet.</div>
                ) : (
                  <ul className="space-y-2 text-sm">
                    {entryRows.map((entry) => (
                      <li key={entry.path} className="flex items-center justify-between gap-3">
                        <button
                          type="button"
                          className={cn('flex flex-1 items-center gap-2 text-left hover:text-primary', entryIndentClass(entry.depth))}
                          onClick={() => {
                            if (entry.type === 'DIRECTORY') {
                              loadList(entry.path);
                            } else {
                              loadFile(entry.path);
                              setWritePath(entry.path);
                              setDeletePath(entry.path);
                            }
                          }}
                        >
                          {entry.type === 'DIRECTORY' ? <FolderOpen className="h-4 w-4" /> : <FileText className="h-4 w-4" />}
                          <span className="font-medium">{entry.name || entry.path}</span>
                        </button>
                        <span className="text-xs text-muted-foreground">{entry.type === 'FILE' ? `${entry.size} bytes` : 'DIR'}</span>
                      </li>
                    ))}
                  </ul>
                )}
              </div>
            </div>
          </CardContent>
        </Card>

        <Card className="border border-border/60 bg-card">
          <CardHeader>
            <CardTitle className="text-xl">Selection</CardTitle>
            <CardDescription>Read, inspect, and copy file content.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-1">
              <label htmlFor="file-io-selected-path" className={inputLabelClass}>
                Selected Path
              </label>
              <Input
                id="file-io-selected-path"
                placeholder="/path/to/file"
                value={selectedPath}
                onChange={(event) => setSelectedPath(event.target.value)}
              />
            </div>
            <div className="grid gap-3 md:grid-cols-2">
              <div className="space-y-1">
                <label htmlFor="file-io-read-offset" className={inputLabelClass}>
                  Read Offset (bytes)
                </label>
                <Input
                  id="file-io-read-offset"
                  type="number"
                  placeholder="0"
                  value={readOffset}
                  onChange={(event) => setReadOffset(Number(event.target.value))}
                  min={0}
                />
              </div>
              <div className="space-y-1">
                <label htmlFor="file-io-read-length" className={inputLabelClass}>
                  Read Length (bytes)
                </label>
                <Input
                  id="file-io-read-length"
                  type="number"
                  placeholder="-1 reads to EOF"
                  value={readLength}
                  onChange={(event) => setReadLength(Number(event.target.value))}
                />
              </div>
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button
                type="button"
                variant="secondary"
                onClick={() => selectedPath && loadFile(selectedPath)}
                disabled={!project || !selectedPath || isReading}
              >
                <RefreshCw className={cn('mr-2 h-4 w-4', isReading && 'animate-spin')} />
                Read
              </Button>
              {readError && <span className="text-sm text-destructive">{readError}</span>}
            </div>
            {selectedStat && (
              <div className="rounded-lg border border-border/60 bg-muted/40 p-3 text-xs text-muted-foreground">
                <div className="flex items-center justify-between">
                  <span>Type</span>
                  <span className="font-medium text-foreground">{selectedStat.type}</span>
                </div>
                <div className="flex items-center justify-between">
                  <span>Size</span>
                  <span className="font-medium text-foreground">{selectedStat.size} bytes</span>
                </div>
                <div className="flex items-center justify-between">
                  <span>Created</span>
                  <span className="font-medium text-foreground">{formatTimestamp(selectedStat.created_at)}</span>
                </div>
                <div className="flex items-center justify-between">
                  <span>Updated</span>
                  <span className="font-medium text-foreground">{formatTimestamp(selectedStat.updated_at)}</span>
                </div>
              </div>
            )}
            <div className="space-y-1">
              <label htmlFor="file-io-selected-content" className={inputLabelClass}>
                File Content
              </label>
              <Textarea
                id="file-io-selected-content"
                rows={10}
                value={selectedContent}
                onChange={(event) => setSelectedContent(event.target.value)}
                placeholder={selectedIsDirectory ? 'Directory selected.' : 'File content will appear here.'}
              />
            </div>
          </CardContent>
        </Card>
      </div>

      <div className="grid gap-6 lg:grid-cols-2">
        <Card className="border border-border/60 bg-card">
          <CardHeader>
            <CardTitle className="text-xl">Write</CardTitle>
            <CardDescription>Append or overwrite file content.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-1">
              <label htmlFor="file-io-write-path" className={inputLabelClass}>
                Target Path
              </label>
              <Input
                id="file-io-write-path"
                placeholder="/path/to/file"
                value={writePath}
                onChange={(event) => setWritePath(event.target.value)}
              />
            </div>
            <div className="grid gap-3 md:grid-cols-2">
              <div className="space-y-1">
                <label htmlFor="file-io-write-mode" className={inputLabelClass}>
                  Write Mode
                </label>
                <select
                  id="file-io-write-mode"
                  value={writeMode}
                  onChange={(event) => setWriteMode(event.target.value as 'APPEND' | 'OVERWRITE' | 'TRUNCATE')}
                  className="h-10 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground shadow-sm focus:border-ring focus:outline-none"
                >
                  <option value="APPEND">APPEND</option>
                  <option value="OVERWRITE">OVERWRITE</option>
                  <option value="TRUNCATE">TRUNCATE</option>
                </select>
              </div>
              <div className="space-y-1">
                <label htmlFor="file-io-write-offset" className={inputLabelClass}>
                  Write Offset (bytes)
                </label>
                <Input
                  id="file-io-write-offset"
                  type="number"
                  placeholder="0"
                  value={writeOffset}
                  onChange={(event) => setWriteOffset(Number(event.target.value))}
                  min={0}
                />
              </div>
            </div>
            <div className="space-y-1">
              <label htmlFor="file-io-write-content" className={inputLabelClass}>
                Write Content (UTF-8)
              </label>
              <Textarea
                id="file-io-write-content"
                rows={8}
                value={writeContent}
                onChange={(event) => setWriteContent(event.target.value)}
                placeholder="Enter UTF-8 content to write."
              />
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button type="button" onClick={submitWrite} disabled={!project || !writePath || isWriting}>
                <UploadCloud className={cn('mr-2 h-4 w-4', isWriting && 'animate-bounce')} />
                Write
              </Button>
              {writeInfo && <span className="text-sm text-emerald-600">{writeInfo}</span>}
              {writeError && <span className="text-sm text-destructive">{writeError}</span>}
            </div>
          </CardContent>
        </Card>

        <Card className="border border-border/60 bg-card">
          <CardHeader>
            <CardTitle className="text-xl">Delete</CardTitle>
            <CardDescription>Remove files or directories.</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-1">
              <label htmlFor="file-io-delete-path" className={inputLabelClass}>
                Path To Delete
              </label>
              <Input
                id="file-io-delete-path"
                placeholder="/path/to/file-or-directory"
                value={deletePath}
                onChange={(event) => setDeletePath(event.target.value)}
              />
            </div>
            <div className="space-y-2">
              <span className={inputLabelClass}>Delete Options</span>
              <label htmlFor="file-io-delete-recursive" className="flex items-center gap-2 text-sm text-muted-foreground">
                <input
                  id="file-io-delete-recursive"
                  type="checkbox"
                  checked={deleteRecursive}
                  onChange={(event) => setDeleteRecursive(event.target.checked)}
                  className="h-4 w-4 rounded border-border"
                />
                Recursive delete
              </label>
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button type="button" variant="destructive" onClick={submitDelete} disabled={!project || !deletePath || isDeleting}>
                <Trash2 className="mr-2 h-4 w-4" />
                Delete
              </Button>
              {deleteInfo && <span className="text-sm text-emerald-600">{deleteInfo}</span>}
              {deleteError && <span className="text-sm text-destructive">{deleteError}</span>}
            </div>
          </CardContent>
        </Card>
      </div>

      <Card className="border border-border/60 bg-card">
        <CardHeader>
          <CardTitle className="text-xl">Search</CardTitle>
          <CardDescription>Run hybrid file_search across indexed content.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-3 md:grid-cols-3">
            <div className="space-y-1">
              <label htmlFor="file-io-search-query" className={inputLabelClass}>
                Search Query
              </label>
              <Input
                id="file-io-search-query"
                placeholder="Keywords or question"
                value={searchQuery}
                onChange={(event) => setSearchQuery(event.target.value)}
              />
            </div>
            <div className="space-y-1">
              <label htmlFor="file-io-search-prefix" className={inputLabelClass}>
                Path Prefix
              </label>
              <Input
                id="file-io-search-prefix"
                placeholder="Optional directory filter"
                value={searchPrefix}
                onChange={(event) => setSearchPrefix(event.target.value)}
              />
            </div>
            <div className="space-y-1">
              <label htmlFor="file-io-search-limit" className={inputLabelClass}>
                Result Limit
              </label>
              <Input
                id="file-io-search-limit"
                type="number"
                placeholder="1 - 20"
                min={1}
                max={20}
                value={searchLimit}
                onChange={(event) => setSearchLimit(Number(event.target.value))}
              />
            </div>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <Button type="button" onClick={submitSearch} disabled={!project || !searchQuery || isSearching}>
              <Search className={cn('mr-2 h-4 w-4', isSearching && 'animate-pulse')} />
              Search
            </Button>
            {searchError && (
              <span className="inline-flex items-center gap-2 text-sm text-destructive">
                <ShieldAlert className="h-4 w-4" />
                {searchError}
              </span>
            )}
          </div>
          <div className="rounded-lg border border-border/60 bg-muted/30">
            <div className="flex items-center justify-between border-b border-border/60 px-4 py-2 text-xs font-semibold uppercase tracking-widest text-muted-foreground">
              <span>Results</span>
              <span>{searchResults.length} chunk(s)</span>
            </div>
            <div className="space-y-4 px-4 py-3">
              {searchResults.length === 0 ? (
                <div className="text-sm text-muted-foreground">No results yet.</div>
              ) : (
                searchResults.map((chunk) => (
                  <div key={`${chunk.file_path}-${chunk.file_seek_start_bytes}`} className="rounded-md border border-border/60 bg-card p-3">
                    <div className="flex flex-wrap items-center justify-between gap-2">
                      <span className="text-sm font-medium text-foreground">{chunk.file_path}</span>
                      <Badge variant="secondary">Score {chunk.score.toFixed(3)}</Badge>
                    </div>
                    <div className="mt-2 text-xs text-muted-foreground">
                      Bytes {chunk.file_seek_start_bytes} - {chunk.file_seek_end_bytes}
                    </div>
                    <p className="mt-2 whitespace-pre-wrap text-sm text-foreground/90">{chunk.chunk_content}</p>
                  </div>
                ))
              )}
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  );
}
