import {
  ChevronDown,
  ChevronRight,
  ChevronUp,
  FileText,
  Folder,
  FolderOpen,
  RefreshCw,
  Search,
  ShieldAlert,
  Trash2,
  UploadCloud,
} from 'lucide-react';
import { useState } from 'react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/confirm-dialog';
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

function FileTreeNode({
  entry,
  level,
  expandedPaths,
  dirCache,
  loadingPaths,
  onToggle,
  onSelect,
  selectedPath,
}: {
  entry: FileEntry;
  level: number;
  expandedPaths: Set<string>;
  dirCache: Record<string, { entries: FileEntry[]; hasMore: boolean }>;
  loadingPaths: Set<string>;
  onToggle: (entry: FileEntry) => void;
  onSelect: (entry: FileEntry) => void;
  selectedPath: string;
}) {
  const isExpanded = expandedPaths.has(entry.path);
  const isDirectory = entry.type === 'DIRECTORY';
  const childrenData = dirCache[entry.path];
  const isLoading = loadingPaths.has(entry.path);
  const isSelected = selectedPath === entry.path;
  const displayName = entry.name || entry.path.split('/').pop() || entry.path;

  return (
    <div className="select-none">
      <div
        className={cn(
          'flex cursor-pointer items-center gap-2 rounded-sm py-1 pr-2 text-sm transition-colors hover:bg-accent/50',
          isSelected && 'bg-accent font-medium text-accent-foreground'
        )}
        style={{ paddingLeft: `${Math.max(4, level * 16)}px` }}
        onClick={(e) => {
          e.stopPropagation();
          if (isDirectory) {
            onToggle(entry);
          } else {
            onSelect(entry);
          }
        }}
      >
        <div
          className="flex h-6 w-6 shrink-0 items-center justify-center text-muted-foreground hover:text-foreground"
          onClick={(e) => {
            if (isDirectory) {
              e.stopPropagation();
              onToggle(entry);
            }
          }}
        >
          {isDirectory ? (
            isLoading ? (
              <RefreshCw className="h-3 w-3 animate-spin" />
            ) : isExpanded ? (
              <ChevronDown className="h-4 w-4" />
            ) : (
              <ChevronRight className="h-4 w-4" />
            )
          ) : (
            <span className="w-4" />
          )}
        </div>

        <div className={cn('flex shrink-0 items-center', isDirectory ? 'text-primary' : 'text-muted-foreground')}>
          {isDirectory ? (
            isExpanded ? (
              <FolderOpen className="h-4 w-4" />
            ) : (
              <Folder className="h-4 w-4" />
            )
          ) : (
            <FileText className="h-4 w-4" />
          )}
        </div>

        <span className="truncate">{displayName}</span>
        {/* <span className="ml-auto text-xs text-muted-foreground">{isDirectory ? '' : `${entry.size} B`}</span> */}
      </div>

      {isExpanded && isDirectory && childrenData && (
        <div>
          {childrenData.entries.map((child) => (
            <FileTreeNode
              key={child.path}
              entry={child}
              level={level + 1}
              expandedPaths={expandedPaths}
              dirCache={dirCache}
              loadingPaths={loadingPaths}
              onToggle={onToggle}
              onSelect={onSelect}
              selectedPath={selectedPath}
            />
          ))}
          {childrenData.hasMore && (
            <div className="py-1 text-xs italic text-muted-foreground" style={{ paddingLeft: `${(level + 1) * 16 + 32}px` }}>
              ... truncated ...
            </div>
          )}
        </div>
      )}
      {isExpanded && isDirectory && !childrenData && !isLoading && (
        <div className="py-1 text-xs italic text-muted-foreground" style={{ paddingLeft: `${(level + 1) * 16 + 32}px` }}>
          (Empty)
        </div>
      )}
    </div>
  );
}

export function FileIOPage() {
  const { apiKey } = useApiKey();
  const persistedInputs = useFileIOInputDefaults();
  const [project, setProject] = useState(persistedInputs.project ?? '');
  const [currentPath, setCurrentPath] = useState(persistedInputs.currentPath ?? '');
  const [depth, setDepth] = useState(persistedInputs.depth ?? 1);
  const [limit, setLimit] = useState(persistedInputs.limit ?? 200);

  // Tree State
  const [rootEntries, setRootEntries] = useState<FileEntry[]>([]);
  const [dirCache, setDirCache] = useState<Record<string, { entries: FileEntry[]; hasMore: boolean }>>({});
  const [expandedPaths, setExpandedPaths] = useState<Set<string>>(new Set());
  const [loadingPaths, setLoadingPaths] = useState<Set<string>>(new Set());

  const [hasMore, setHasMore] = useState(false);
  const [browserError, setBrowserError] = useState<string | null>(null);
  const [isListing, setIsListing] = useState(false);

  const [selectedPath, setSelectedPath] = useState(persistedInputs.selectedPath ?? '');
  const [selectedStat, setSelectedStat] = useState<FileStatPayload | null>(null);
  const [selectedContent, setSelectedContent] = useState(persistedInputs.selectedContent ?? '');
  const [readError, setReadError] = useState<string | null>(null);
  const [isReading, setIsReading] = useState(false);
  const [isFilePreviewOpen, setIsFilePreviewOpen] = useState(false);

  const [writePath, setWritePath] = useState(persistedInputs.writePath ?? '');
  const [writeMode, setWriteMode] = useState<'APPEND' | 'OVERWRITE' | 'TRUNCATE'>(
    persistedInputs.writeMode === 'OVERWRITE' || persistedInputs.writeMode === 'TRUNCATE' ? persistedInputs.writeMode : 'APPEND'
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

  const [isDescriptionCollapsed, setIsDescriptionCollapsed] = useState(() => {
    if (typeof localStorage === 'undefined') return false;
    return localStorage.getItem('mcp_file_io_description_collapsed') === 'true';
  });

  usePersistFileIOInputs({
    project,
    currentPath,
    depth,
    limit,
    selectedPath,
    selectedContent,
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

  // Helper to process flat list into tree structure
  function processEntries(basePath: string, rawEntries: FileEntry[]) {
    const normBase = basePath === '/' ? '' : basePath.replace(/\/$/, '');
    const roots: FileEntry[] = [];
    const newCache: Record<string, { entries: FileEntry[]; hasMore: boolean }> = {};
    const newExpanded = new Set<string>();

    const sorter = (a: FileEntry, b: FileEntry) => {
      if (a.type !== b.type) return a.type === 'DIRECTORY' ? -1 : 1;
      return (a.name || a.path).localeCompare(b.name || b.path);
    };

    rawEntries.forEach((entry) => {
      // Determine parent
      // Assuming Unix paths
      const parts = entry.path.split('/');
      const parent = parts.length > 1 ? parts.slice(0, parts.length - 1).join('/') : '/';
      // Fix for root entries usually having parent '' or '/' depending directly on path str

      // Heuristic: If entry.path starts with normBase + '/', it is inside.
      // The parent is the directory containing it.

      if (parent === normBase || (normBase === '' && parent === '') || (normBase === '' && parent === '/')) {
        roots.push(entry);
      } else {
        if (!newCache[parent]) {
          newCache[parent] = { entries: [], hasMore: false };
        }
        newCache[parent].entries.push(entry);
        // Auto-expand if we receive explicit children
        newExpanded.add(parent);
      }
    });

    roots.sort(sorter);
    Object.values(newCache).forEach((val) => {
      val.entries.sort(sorter);
    });

    return { roots, newCache, newExpanded };
  }

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
      // Force depth 1 if loading root for tree view?
      // User might want deep load. Let's respect state.depth for ROOT refresh,
      // but toggle relies on fetchChildren (depth 1).

      const payload = await callTool<FileListPayload>('file_list', {
        project,
        path: listPath,
        depth,
        limit,
      });

      const { roots, newCache, newExpanded } = processEntries(listPath, payload.entries ?? []);

      setCurrentPath(listPath);
      setRootEntries(roots);
      // Merge cache instead of replace? No, root load implies reset of view usually?
      // Actually, if we refresh root, we might want to keep existing known subdirs if valid?
      // For safety, let's reset cache for consistency with "Refresh".
      setDirCache((prev) => ({ ...prev, ...newCache }));
      setExpandedPaths((prev) => {
        const next = new Set(prev);
        newExpanded.forEach((p) => next.add(p));
        return next;
      });
      setHasMore(Boolean(payload.has_more));
    } catch (error) {
      setBrowserError(error instanceof Error ? error.message : 'Failed to list files.');
      setRootEntries([]);
      setHasMore(false);
    } finally {
      setIsListing(false);
    }
  }

  async function toggleFolder(entry: FileEntry) {
    if (expandedPaths.has(entry.path)) {
      const next = new Set(expandedPaths);
      next.delete(entry.path);
      setExpandedPaths(next);
      return;
    }

    // Expand
    setExpandedPaths((prev) => new Set(prev).add(entry.path));

    // Check if loaded
    if (dirCache[entry.path]) {
      return;
    }

    // Fetch
    setLoadingPaths((prev) => new Set(prev).add(entry.path));
    try {
      const payload = await callTool<FileListPayload>('file_list', {
        project,
        path: entry.path,
        depth: 1, // Always shallow fetch for expand
        limit: 200, // Reasonable limit
      });

      // Process just this folder
      // The entries returned are children of entry.path
      const sorted = (payload.entries ?? []).sort((a, b) => {
        if (a.type !== b.type) return a.type === 'DIRECTORY' ? -1 : 1;
        return (a.name || a.path).localeCompare(b.name || b.path);
      });

      setDirCache((prev) => ({
        ...prev,
        [entry.path]: { entries: sorted, hasMore: payload.has_more },
      }));
    } catch (error) {
      // Show error? For now just console or toggle back?
      console.error('Failed to load folder', error);
      setExpandedPaths((prev) => {
        const next = new Set(prev);
        next.delete(entry.path);
        return next;
      });
      setBrowserError(`Failed to load ${entry.name}`);
    } finally {
      setLoadingPaths((prev) => {
        const next = new Set(prev);
        next.delete(entry.path);
        return next;
      });
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
          offset: 0,
          length: -1,
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

  function openFilePreview(targetPath: string) {
    setIsFilePreviewOpen(true);
    void loadFile(targetPath);
  }

  const selectedIsDirectory = selectedStat?.type === 'DIRECTORY';
  const isProjectMissing = project.trim().length === 0;

  return (
    <div className="space-y-8">
      <section className="space-y-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
            <FolderOpen className="h-4 w-4" />
            <span>FileIO Console</span>
          </div>
          <Button
            variant="ghost"
            size="sm"
            onClick={() => {
              const next = !isDescriptionCollapsed;
              setIsDescriptionCollapsed(next);
              localStorage.setItem('mcp_file_io_description_collapsed', String(next));
            }}
            className="text-muted-foreground hover:text-foreground"
          >
            {isDescriptionCollapsed ? (
              <>
                Show Introduction <ChevronDown className="ml-2 h-4 w-4" />
              </>
            ) : (
              <>
                Hide Introduction <ChevronUp className="ml-2 h-4 w-4" />
              </>
            )}
          </Button>
        </div>

        {!isDescriptionCollapsed && (
          <div className="space-y-6 animate-in fade-in slide-in-from-top-2 duration-300">
            <div className="space-y-2">
              <h1 className="text-3xl font-bold tracking-tight text-foreground sm:text-4xl">file_io</h1>
              <p className="max-w-4xl text-lg leading-relaxed text-muted-foreground">
                The <strong>FileIO</strong> system provides a centralized, remote filesystem via Remote MCP, designed to function as shared
                context and persistent memory for distributed AI agents. By offering project-scoped file access, it enables different agents
                to collaborate, read, and maintain a consistent state across tasks.
              </p>
            </div>

            <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
              <div className="rounded-lg border border-border/50 bg-card/50 p-4 transition-colors hover:border-border hover:bg-card">
                <div className="mb-2 flex items-center gap-2 font-semibold text-foreground">
                  <span className="font-mono text-sm text-primary">file_write</span>
                </div>
                <p className="text-sm text-muted-foreground">
                  Create or update files with support for append, overwrite, and truncate modes.
                </p>
              </div>

              <div className="rounded-lg border border-border/50 bg-card/50 p-4 transition-colors hover:border-border hover:bg-card">
                <div className="mb-2 flex items-center gap-2 font-semibold text-foreground">
                  <span className="font-mono text-sm text-primary">file_read</span>
                </div>
                <p className="text-sm text-muted-foreground">Read file contents with optional byte-range support for partial reads.</p>
              </div>

              <div className="rounded-lg border border-border/50 bg-card/50 p-4 transition-colors hover:border-border hover:bg-card">
                <div className="mb-2 flex items-center gap-2 font-semibold text-foreground">
                  <span className="font-mono text-sm text-primary">file_list</span>
                </div>
                <p className="text-sm text-muted-foreground">Browse directory structures and discover files within the project scope.</p>
              </div>

              <div className="rounded-lg border border-border/50 bg-card/50 p-4 transition-colors hover:border-border hover:bg-card">
                <div className="mb-2 flex items-center gap-2 font-semibold text-foreground">
                  <span className="font-mono text-sm text-primary">file_stat</span>
                </div>
                <p className="text-sm text-muted-foreground">Inspect metadata such as file size, type, and modification timestamps.</p>
              </div>

              <div className="rounded-lg border border-border/50 bg-card/50 p-4 transition-colors hover:border-border hover:bg-card">
                <div className="mb-2 flex items-center gap-2 font-semibold text-foreground">
                  <span className="font-mono text-sm text-primary">file_search</span>
                </div>
                <p className="text-sm text-muted-foreground">
                  Perform semantic or hybrid searches across file contents to retrieve relevant context.
                </p>
              </div>

              <div className="rounded-lg border border-border/50 bg-card/50 p-4 transition-colors hover:border-border hover:bg-card">
                <div className="mb-2 flex items-center gap-2 font-semibold text-foreground">
                  <span className="font-mono text-sm text-primary">file_delete</span>
                </div>
                <p className="text-sm text-muted-foreground">Remove specific files or recursively delete directory subtrees.</p>
              </div>
            </div>

            <div className="flex items-center gap-2 text-sm text-muted-foreground">
              Detailed user manual:
              <a
                href="https://github.com/Laisky/laisky-blog-graphql/blob/master/docs/manual/mcp_files.md"
                target="_blank"
                rel="noreferrer"
                className="font-medium text-primary underline decoration-primary/30 underline-offset-4 transition-colors hover:decoration-primary"
              >
                docs/manual/mcp_files.md
              </a>
            </div>
          </div>
        )}
      </section>

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
              <span>{rootEntries.length} root item(s)</span>
            </div>
            <div className="max-h-[480px] overflow-y-auto px-1 py-1">
              {rootEntries.length === 0 ? (
                <div className="px-3 py-2 text-sm text-muted-foreground">No entries yet. Load a project to see files.</div>
              ) : (
                <div className="space-y-[1px]">
                  {rootEntries.map((entry) => (
                    <FileTreeNode
                      key={entry.path}
                      entry={entry}
                      level={0}
                      expandedPaths={expandedPaths}
                      dirCache={dirCache}
                      loadingPaths={loadingPaths}
                      onToggle={toggleFolder}
                      onSelect={(e) => {
                        openFilePreview(e.path);
                        setWritePath(e.path);
                        setDeletePath(e.path);
                      }}
                      selectedPath={selectedPath}
                    />
                  ))}
                </div>
              )}
            </div>
          </div>
        </CardContent>
      </Card>

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

      <Dialog open={isFilePreviewOpen} onOpenChange={setIsFilePreviewOpen}>
        <DialogContent className="flex h-[90vh] w-[95vw] max-w-5xl flex-col overflow-hidden p-0">
          <DialogHeader className="border-b border-border/60 px-6 py-4">
            <DialogTitle className="truncate text-left">File Preview</DialogTitle>
            <DialogDescription className="break-all text-left">{selectedPath || 'No file selected'}</DialogDescription>
          </DialogHeader>
          <div className="flex-1 space-y-3 overflow-y-auto px-6 py-4">
            {readError && <div className="text-sm text-destructive">{readError}</div>}
            {isReading && (
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                <RefreshCw className="h-4 w-4 animate-spin" />
                Loading file content...
              </div>
            )}
            {!isReading && !readError && selectedStat && (
              <div className="grid gap-2 rounded-md border border-border/60 bg-muted/30 p-3 text-xs text-muted-foreground sm:grid-cols-2">
                <div className="flex items-center justify-between gap-2">
                  <span>Type</span>
                  <span className="font-medium text-foreground">{selectedStat.type}</span>
                </div>
                <div className="flex items-center justify-between gap-2">
                  <span>Size</span>
                  <span className="font-medium text-foreground">{selectedStat.size} bytes</span>
                </div>
                <div className="flex items-center justify-between gap-2">
                  <span>Created</span>
                  <span className="font-medium text-foreground">{formatTimestamp(selectedStat.created_at)}</span>
                </div>
                <div className="flex items-center justify-between gap-2">
                  <span>Updated</span>
                  <span className="font-medium text-foreground">{formatTimestamp(selectedStat.updated_at)}</span>
                </div>
              </div>
            )}
            {!isReading && (
              <Textarea
                value={selectedContent}
                readOnly
                rows={18}
                placeholder={selectedIsDirectory ? 'Directory selected.' : 'File content will appear here.'}
                className="min-h-[420px] font-mono text-sm"
              />
            )}
          </div>
        </DialogContent>
      </Dialog>
    </div>
  );
}
