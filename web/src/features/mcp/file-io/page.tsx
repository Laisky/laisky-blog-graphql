import { ArrowRightLeft, ChevronDown, ChevronRight, FileText, Folder, FolderOpen, History, RefreshCw, Save, Search, Trash2, UploadCloud } from 'lucide-react';
import { useEffect, useRef, useState, type ReactNode } from 'react';

import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/confirm-dialog';
import { Input } from '@/components/ui/input';
import { Textarea } from '@/components/ui/textarea';
import { useApiKey } from '@/lib/api-key-context';
import { cn } from '@/lib/utils';

import { callFileAPI, callFileTool } from './client';
import { FileIOIntroduction } from './introduction';
import { useFileIOInputDefaults, usePersistFileIOInputs, type FileIOPersistedInputs } from './use-file-io-input-storage';
import { useFileRequestLane, useFileSnapshot } from './use-file-snapshot';
import { canonicalFilePath, expectedVersion, fileIOErrorMessage, fileSnapshot, ifMatch, renameCondition, requireFileVersion, writeCondition, type FileSnapshot, type ReadPayload } from './version-state';

type FileEntry = { name: string; path: string; type: 'FILE' | 'DIRECTORY'; size: number; created_at: string; updated_at: string };
type FileListPayload = { entries: FileEntry[]; has_more: boolean };
type FileStatPayload = FileEntry & { exists: boolean; version?: string };
type FileWritePayload = { bytes_written: number; version: string };
type FileVersionEntry = { id: number; size: number; created_at: string };
type HistoryContent = { content: string; content_encoding: string; size: number; created_at: string };
type FileSearchChunk = { file_path: string; file_seek_start_bytes: number; file_seek_end_bytes: number; chunk_content: string; score: number };
type DirCache = Record<string, { entries: FileEntry[]; hasMore: boolean }>;
const inputLabelClass = 'text-xs font-medium uppercase tracking-wide text-muted-foreground';
const selectClass = 'h-10 w-full rounded-md border border-input bg-background px-3 text-sm text-foreground shadow-sm disabled:opacity-60';
const dateFormatter = new Intl.DateTimeFormat(undefined, { dateStyle: 'medium', timeStyle: 'medium' });

function formatTimestamp(value: string) {
  const parsed = new Date(value);
  return Number.isNaN(parsed.getTime()) ? value || '—' : dateFormatter.format(parsed);
}
function Field({ id, label, children }: { id: string; label: string; children: ReactNode }) {
  return <div className="space-y-1"><label htmlFor={id} className={inputLabelClass}>{label}</label>{children}</div>;
}
function Feedback({ error, info }: { error?: string | null; info?: string | null }) {
  return <>{error && <p role="alert" className="text-sm text-destructive">{error}</p>}{info && <p role="status" className="break-all text-sm text-emerald-600">{info}</p>}</>;
}
function VersionReview({ title, review, disabled, onRead }: {
  title: string; review: ReturnType<typeof useFileSnapshot>; disabled: boolean; onRead?: () => void;
}) {
  return <div className="space-y-2 rounded-md border border-border/60 p-3">
    <Button type="button" variant="outline" size="sm" disabled={disabled || review.pending} onClick={onRead ?? (() => void review.load())}>
      <RefreshCw className={cn('mr-2 h-4 w-4', review.pending && 'animate-spin')} />Read {title}
    </Button>
    <p className="break-all text-xs text-muted-foreground">{review.snapshot ? <>Read version: <code>{review.snapshot.version}</code></> : 'Read and review the file to enable this operation.'}</p>
    {review.snapshot && <details><summary className="cursor-pointer text-xs">Reviewed content</summary><pre className="mt-2 max-h-48 overflow-auto whitespace-pre-wrap break-all text-xs">{review.snapshot.content}</pre></details>}
    <Feedback error={review.error} />
  </div>;
}

function FileTreeNode({ entry, level, expanded, cache, loading, onToggle, onSelect, selected, disabled }: {
  entry: FileEntry; level: number; expanded: Set<string>; cache: DirCache; loading: Set<string>;
  onToggle: (entry: FileEntry) => void; onSelect: (path: string) => void; selected: string; disabled: boolean;
}) {
  const directory = entry.type === 'DIRECTORY';
  const open = expanded.has(entry.path);
  const children = cache[entry.path];
  return <div>
    <button type="button" disabled={disabled} aria-expanded={directory ? open : undefined}
      className={cn('flex w-full items-center gap-2 rounded-sm py-1 pr-2 text-left text-sm hover:bg-accent/50 disabled:opacity-60', selected === entry.path && 'bg-accent font-medium')}
      style={{ paddingLeft: `${Math.max(4, level * 16)}px` }} onClick={() => directory ? onToggle(entry) : onSelect(entry.path)}>
      {directory ? loading.has(entry.path) ? <RefreshCw className="h-4 w-4 animate-spin" /> : open ? <ChevronDown className="h-4 w-4" /> : <ChevronRight className="h-4 w-4" /> : <span className="w-4" />}
      {directory ? open ? <FolderOpen className="h-4 w-4 text-primary" /> : <Folder className="h-4 w-4 text-primary" /> : <FileText className="h-4 w-4" />}
      <span className="truncate">{entry.name || entry.path}</span>
    </button>
    {directory && open && children && <div>{children.entries.map((child) => <FileTreeNode key={child.path} entry={child} level={level + 1} expanded={expanded} cache={cache} loading={loading} onToggle={onToggle} onSelect={onSelect} selected={selected} disabled={disabled} />)}
      {children.hasMore && <p className="pl-8 text-xs text-muted-foreground">List truncated; browse this prefix with a higher limit.</p>}
      {!children.entries.length && <p className="pl-8 text-xs text-muted-foreground">(Empty)</p>}
    </div>}
  </div>;
}

function processEntries(basePath: string, entries: FileEntry[]) {
  const base = basePath === '/' ? '' : basePath.replace(/\/$/, '');
  const roots: FileEntry[] = [];
  const cache: DirCache = {};
  const expanded = new Set<string>();
  for (const entry of entries) {
    const parent = entry.path.split('/').slice(0, -1).join('/');
    if (parent === base) roots.push(entry);
    else { (cache[parent] ??= { entries: [], hasMore: false }).entries.push(entry); expanded.add(parent); }
  }
  const sort = (a: FileEntry, b: FileEntry) => a.type !== b.type ? a.type === 'DIRECTORY' ? -1 : 1 : a.name.localeCompare(b.name);
  roots.sort(sort);
  Object.values(cache).forEach((item) => item.entries.sort(sort));
  return { roots, cache, expanded };
}

/** Remount request-bound state on tenant, lock, or project changes. Never persist tokens. */
export function FileIOPage() {
  const { apiKey, isToolConsoleLocked } = useApiKey();
  const defaults = useFileIOInputDefaults();
  const [project, setProject] = useState(defaults.project ?? '');
  const [dirty, setDirty] = useState(false);
  const scope = JSON.stringify([apiKey, isToolConsoleLocked, project]);
  return <div className="space-y-8">
    <FileIOIntroduction />
    <Field id="file-io-project" label="Project *"><Input id="file-io-project" placeholder="Required" required
      disabled={isToolConsoleLocked || !apiKey} value={project} onChange={(event) => {
        if (dirty && !window.confirm('Switch projects and discard the open drafts? Copy them first to keep your changes.')) return;
        setDirty(false);
        setProject(event.target.value);
      }} /></Field>
    <FileIOWorkspace key={scope} apiKey={isToolConsoleLocked ? '' : apiKey || ''} project={project}
      defaults={defaults.project === project ? defaults : {}} onDirtyChange={setDirty} />
  </div>;
}

function FileIOWorkspace({ apiKey, project, defaults, onDirtyChange }: {
  apiKey: string; project: string; defaults: Partial<FileIOPersistedInputs>; onDirtyChange: (dirty: boolean) => void;
}) {
  const [currentPath, setCurrentPath] = useState(defaults.currentPath ?? '');
  const [depth, setDepth] = useState(defaults.depth ?? 1);
  const [limit, setLimit] = useState(defaults.limit ?? 200);
  const [roots, setRoots] = useState<FileEntry[]>([]);
  const [dirCache, setDirCache] = useState<DirCache>({});
  const [expanded, setExpanded] = useState(new Set<string>());
  const [loadingPaths, setLoadingPaths] = useState(new Set<string>());
  const [hasMore, setHasMore] = useState(false);
  const [browserError, setBrowserError] = useState<string | null>(null);
  const [listing, setListing] = useState(false);
  const listLane = useFileRequestLane();
  const treeLane = useFileRequestLane();

  const [previewPath, setPreviewPath] = useState('');
  const [previewOpen, setPreviewOpen] = useState(false);
  const [base, setBase] = useState<FileSnapshot | null>(null);
  const [previewStat, setPreviewStat] = useState<FileStatPayload | null>(null);
  const [draft, setDraft] = useState('');
  const [loadedContent, setLoadedContent] = useState('');
  const [encoding, setEncoding] = useState('utf-8');
  const [versions, setVersions] = useState<FileVersionEntry[]>([]);
  const [historyId, setHistoryId] = useState<number | null>(null);
  const [reading, setReading] = useState(false);
  const [loadingVersions, setLoadingVersions] = useState(false);
  const [readError, setReadError] = useState<string | null>(null);
  const [readInfo, setReadInfo] = useState<string | null>(null);
  const previewLane = useFileRequestLane();
  const historyLane = useFileRequestLane();

  const [writePath, setWritePath] = useState(defaults.writePath ?? '');
  const [writeMode, setWriteMode] = useState<'APPEND' | 'OVERWRITE' | 'TRUNCATE'>(defaults.writeMode ?? 'APPEND');
  const [writeOffset, setWriteOffset] = useState(defaults.writeOffset ?? 0);
  const [writeContent, setWriteContent] = useState(defaults.writeContent ?? '');
  const [createOnly, setCreateOnly] = useState(false);
  const writeReview = useFileSnapshot(apiKey, project, writePath);
  const [writeError, setWriteError] = useState<string | null>(null);
  const [writeInfo, setWriteInfo] = useState<string | null>(null);

  const [deletePath, setDeletePath] = useState(defaults.deletePath ?? '');
  const deleteReview = useFileSnapshot(apiKey, project, deletePath);
  const [deleteError, setDeleteError] = useState<string | null>(null);
  const [deleteInfo, setDeleteInfo] = useState<string | null>(null);
  const [renameFromPath, setRenameFromPath] = useState(defaults.renameFromPath ?? '');
  const [renameToPath, setRenameToPath] = useState(defaults.renameToPath ?? '');
  const [renameOverwrite, setRenameOverwrite] = useState(defaults.renameOverwrite ?? false);
  const sourceReview = useFileSnapshot(apiKey, project, renameFromPath);
  const targetReview = useFileSnapshot(apiKey, project, renameToPath);
  const [renameError, setRenameError] = useState<string | null>(null);
  const [renameInfo, setRenameInfo] = useState<string | null>(null);

  const [searchQuery, setSearchQuery] = useState(defaults.searchQuery ?? '');
  const [searchPrefix, setSearchPrefix] = useState(defaults.searchPrefix ?? '');
  const [searchLimit, setSearchLimit] = useState(defaults.searchLimit ?? 5);
  const [searchResults, setSearchResults] = useState<FileSearchChunk[]>([]);
  const [searchError, setSearchError] = useState<string | null>(null);
  const [searching, setSearching] = useState(false);
  const searchLane = useFileRequestLane();
  const mutationLane = useFileRequestLane();
  const mutationGate = useRef(false);
  const [busy, setBusy] = useState(false);
  const unavailable = !apiKey || !project.trim();
  useEffect(() => { onDirtyChange(draft !== loadedContent || writeContent.length > 0); }, [draft, loadedContent, writeContent, onDirtyChange]);

  // Persist drafts/paths only. Restored text is never paired with a fresh token automatically.
  usePersistFileIOInputs({ project, currentPath, depth, limit, selectedPath: previewPath, selectedContent: draft,
    writePath, writeMode, writeOffset, writeContent, deletePath, deleteRecursive: false,
    renameFromPath, renameToPath, renameOverwrite, searchQuery, searchPrefix, searchLimit });

  async function mutate(action: (current: () => boolean) => Promise<void>, report: (message: string) => void) {
    if (unavailable || mutationGate.current) return;
    mutationGate.current = true;
    const current = mutationLane.begin();
    setBusy(true);
    try { await action(current); } catch (err) { if (current()) report(fileIOErrorMessage(err)); }
    finally { mutationGate.current = false; if (current()) setBusy(false); }
  }
  async function loadList() {
    const current = listLane.begin();
    treeLane.cancel();
    setLoadingPaths(new Set());
    setBrowserError(null); setListing(true);
    try {
      const path = canonicalFilePath(currentPath);
      const payload = await callFileTool<FileListPayload>(apiKey, 'file_list', { project, path: path === '/' ? '' : path, depth, limit });
      if (!current()) return;
      const data = processEntries(path, payload.entries ?? []);
      setRoots(data.roots); setDirCache(data.cache); setExpanded(data.expanded); setHasMore(Boolean(payload.has_more));
    } catch (err) { if (current()) setBrowserError(fileIOErrorMessage(err)); }
    finally { if (current()) setListing(false); }
  }
  async function toggleFolder(entry: FileEntry) {
    if (expanded.has(entry.path)) { setExpanded((prev) => { const next = new Set(prev); next.delete(entry.path); return next; }); return; }
    setExpanded((prev) => new Set(prev).add(entry.path));
    if (dirCache[entry.path]) return;
    const current = treeLane.begin(entry.path);
    setLoadingPaths((prev) => new Set(prev).add(entry.path));
    try {
      const payload = await callFileTool<FileListPayload>(apiKey, 'file_list', { project, path: entry.path, depth: 1, limit });
      if (current()) setDirCache((prev) => ({ ...prev, [entry.path]: { entries: payload.entries ?? [], hasMore: payload.has_more } }));
    } catch (err) { if (current()) setBrowserError(fileIOErrorMessage(err)); }
    finally { if (current()) setLoadingPaths((prev) => { const next = new Set(prev); next.delete(entry.path); return next; }); }
  }
  async function loadVersions(path: string) {
    const current = historyLane.begin();
    setLoadingVersions(true);
    try {
      const payload = await callFileAPI<{ versions: FileVersionEntry[] }>(apiKey, 'GET', '/versions', { query: { project, path } });
      if (current()) setVersions(payload.versions ?? []);
    } catch (err) { if (current()) setReadError(`History list: ${fileIOErrorMessage(err)}`); }
    finally { if (current()) setLoadingVersions(false); }
  }
  async function loadPreview(path: string) {
    if (busy || unavailable) return;
    if (draft !== loadedContent && !window.confirm('Discard this unsaved draft and read the current file? Copy it first to keep your changes.')) return;
    const current = previewLane.begin();
    historyLane.cancel();
    const target = canonicalFilePath(path);
    setPreviewPath(target); setPreviewOpen(true); setBase(null); setPreviewStat(null);
    setDraft(''); setLoadedContent(''); setEncoding('utf-8'); setHistoryId(null); setVersions([]);
    setReading(true); setReadError(null); setReadInfo(null);
    void loadVersions(target);
    try {
      const payload = await callFileTool<ReadPayload>(apiKey, 'file_read', { project, path: target, offset: 0, length: -1 });
      if (!current()) return;
      const snapshot = fileSnapshot(target, payload);
      setBase(snapshot); setDraft(snapshot.content); setLoadedContent(snapshot.content);
      // Metadata is displayed only when it describes the read generation; it is never the edit token.
      const stat = await callFileTool<FileStatPayload>(apiKey, 'file_stat', { project, path: target });
      if (current() && stat.version === snapshot.version) setPreviewStat(stat);
    } catch (err) { if (current()) setReadError(fileIOErrorMessage(err)); }
    finally { if (current()) setReading(false); }
  }
  async function loadHistory(id: number) {
    if (!base || busy) return;
    if (draft !== loadedContent && !window.confirm('Discard this unsaved draft to preview a historical version?')) return;
    const current = previewLane.begin();
    setReading(true); setReadError(null); setReadInfo(null);
    try {
      const payload = await callFileAPI<HistoryContent>(apiKey, 'GET', `/versions/${id}/content`, { query: { project, path: previewPath } });
      if (!current()) return;
      setDraft(payload.content); setLoadedContent(payload.content); setEncoding(payload.content_encoding); setHistoryId(id);
      // Keep base untouched: restore must compare against the live file observed when opened.
    } catch (err) { if (current()) setReadError(fileIOErrorMessage(err)); }
    finally { if (current()) setReading(false); }
  }
  async function saveFile() {
    if (!base || historyId !== null || reading || encoding !== 'utf-8') return;
    setReadError(null); setReadInfo(null);
    const content = draft;
    await mutate(async (current) => {
      const payload = await callFileAPI<FileWritePayload>(apiKey, 'PUT', '/file', {
        body: { project, path: previewPath, content }, headers: ifMatch(base, previewPath),
      });
      if (!current()) return;
      const next = fileSnapshot(previewPath, { content, content_encoding: 'utf-8', version: payload.version });
      setBase(next); setLoadedContent(content); setPreviewStat(null);
      setReadInfo(`Saved ${payload.bytes_written} bytes. Version: ${next.version}`);
      void loadVersions(previewPath); void loadList();
    }, setReadError);
  }
  async function restoreVersion() {
    if (!base || historyId === null || reading || encoding !== 'utf-8') return;
    setReadError(null); setReadInfo(null);
    const content = loadedContent;
    await mutate(async (current) => {
      const payload = await callFileAPI<FileWritePayload>(apiKey, 'POST', `/versions/${historyId}/restore`, {
        body: { project, path: previewPath }, headers: ifMatch(base, previewPath),
      });
      if (!current()) return;
      const next = fileSnapshot(previewPath, { content, content_encoding: 'utf-8', version: payload.version });
      setBase(next); setDraft(content); setLoadedContent(content); setHistoryId(null); setPreviewStat(null);
      setReadInfo(`Restored ${payload.bytes_written} bytes as a new live version: ${next.version}`);
      void loadVersions(previewPath); void loadList();
    }, setReadError);
  }
  async function submitWrite() {
    setWriteError(null); setWriteInfo(null);
    await mutate(async (current) => {
      const payload = await callFileTool<FileWritePayload>(apiKey, 'file_write', {
        project, path: canonicalFilePath(writePath), content: writeContent, content_encoding: 'utf-8', offset: writeOffset, mode: writeMode,
        ...writeCondition(writeReview.snapshot, writePath, createOnly),
      });
      if (!current()) return;
      setWriteInfo(`Wrote ${payload.bytes_written} bytes. Version: ${requireFileVersion(payload.version)}`);
      writeReview.clear(); // Do not advance an APPEND draft's base and accidentally submit it again.
      void loadList();
    }, setWriteError);
  }
  async function submitDelete() {
    setDeleteError(null); setDeleteInfo(null);
    await mutate(async (current) => {
      const payload = await callFileTool<{ deleted_count: number }>(apiKey, 'file_delete', {
        project, path: canonicalFilePath(deletePath), expected_version: expectedVersion(deleteReview.snapshot, deletePath),
      });
      if (!current()) return;
      deleteReview.clear(); setDeleteInfo(`Deleted ${payload.deleted_count} file(s).`);
      if (canonicalFilePath(deletePath) === previewPath) setReadError('This file was deleted. Any open draft is retained, but its old version cannot be saved.');
      void loadList();
    }, setDeleteError);
  }
  async function submitRename() {
    setRenameError(null); setRenameInfo(null);
    await mutate(async (current) => {
      const payload = await callFileTool<{ moved_count: number }>(apiKey, 'file_rename', {
        project, from_path: canonicalFilePath(renameFromPath), to_path: canonicalFilePath(renameToPath),
        ...renameCondition(sourceReview.snapshot, renameFromPath, renameToPath, renameOverwrite, targetReview.snapshot),
      });
      if (!current()) return;
      sourceReview.clear(); targetReview.clear(); setRenameInfo(`Moved ${payload.moved_count} file(s). Read the destination to obtain its new version.`);
      if (canonicalFilePath(renameFromPath) === previewPath || canonicalFilePath(renameToPath) === previewPath) setReadError('This path was changed by the rename. Copy your draft before reloading the live file.');
      void loadList();
    }, setRenameError);
  }
  async function submitSearch() {
    const current = searchLane.begin();
    setSearchError(null); setSearching(true);
    try {
      const payload = await callFileTool<{ chunks: FileSearchChunk[] }>(apiKey, 'file_search', {
        project, query: searchQuery, path_prefix: canonicalFilePath(searchPrefix), limit: searchLimit,
      });
      if (current()) setSearchResults(payload.chunks ?? []);
    } catch (err) { if (current()) setSearchError(fileIOErrorMessage(err)); }
    finally { if (current()) setSearching(false); }
  }

  return <>
    <fieldset disabled={!apiKey || busy} className="m-0 min-w-0 space-y-8 border-0 p-0">
      <Card className="border border-border/60 bg-card">
        <CardHeader><CardTitle className="text-xl">Workspace Browser</CardTitle><CardDescription>Browse files. Opening a file loads its content and edit version together.</CardDescription></CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-3 md:grid-cols-2">
            <Field id="file-io-current-path" label="Path Prefix"><Input id="file-io-current-path" placeholder="Empty means root (/)" value={currentPath} onChange={(event) => { listLane.cancel(); treeLane.cancel(); setListing(false); setCurrentPath(event.target.value); setRoots([]); setDirCache({}); }} /></Field>
            <Field id="file-io-depth" label="Browse Depth"><Input id="file-io-depth" type="number" value={depth} min={0} onChange={(event) => setDepth(Number(event.target.value))} /></Field>
            <Field id="file-io-list-limit" label="List Limit"><Input id="file-io-list-limit" type="number" value={limit} min={1} onChange={(event) => setLimit(Number(event.target.value))} /></Field>
          </div>
          <div className="flex items-center gap-3"><Button onClick={() => void loadList()} disabled={unavailable || listing}><RefreshCw className={cn('mr-2 h-4 w-4', listing && 'animate-spin')} />Refresh list</Button>{hasMore && <Badge variant="secondary">List truncated</Badge>}</div>
          <Feedback error={browserError} />
          <div className="max-h-[480px] overflow-y-auto rounded-lg border border-border/60 bg-muted/30 p-2">
            {!roots.length ? <p className="text-sm text-muted-foreground">No entries yet. Load a project to see files.</p> : roots.map((entry) => <FileTreeNode key={entry.path} entry={entry} level={0} expanded={expanded} cache={dirCache} loading={loadingPaths}
              onToggle={(item) => void toggleFolder(item)} onSelect={(path) => {
                void loadPreview(path);
                writeReview.clear(); deleteReview.clear(); sourceReview.clear();
                setWritePath(path); setDeletePath(path); setRenameFromPath(path);
              }} selected={previewPath} disabled={!apiKey || busy} />)}
          </div>
        </CardContent>
      </Card>

      <div className="grid gap-6 lg:grid-cols-3">
        <Card><CardHeader><CardTitle className="text-xl">Write</CardTitle><CardDescription>Create explicitly, or read and review a base before editing.</CardDescription></CardHeader>
          <CardContent className="space-y-4">
            <Field id="file-io-write-path" label="Target Path"><Input id="file-io-write-path" placeholder="/path/to/file" value={writePath} onChange={(event) => { writeReview.clear(); setWritePath(event.target.value); }} /></Field>
            <label htmlFor="file-io-create-only" className="flex items-center gap-2 text-sm"><input id="file-io-create-only" type="checkbox" checked={createOnly} onChange={(event) => { writeReview.clear(); setCreateOnly(event.target.checked); }} />Create only (fail if the file exists)</label>
            {!createOnly && <VersionReview title="write base" review={writeReview} disabled={unavailable || !writePath} onRead={() => {
              if (writeContent && !window.confirm('Reading a new base keeps your draft. Review the returned content and recompute the draft before writing. Continue?')) return;
              void writeReview.load();
            }} />}
            <div className="grid gap-3 md:grid-cols-2">
              <Field id="file-io-write-mode" label="Write Mode"><select id="file-io-write-mode" className={selectClass} value={writeMode} onChange={(event) => setWriteMode(event.target.value as typeof writeMode)}><option>APPEND</option><option>OVERWRITE</option><option>TRUNCATE</option></select></Field>
              <Field id="file-io-write-offset" label="Offset (UTF-8 bytes)"><Input id="file-io-write-offset" type="number" min={0} value={writeOffset} disabled={writeMode !== 'OVERWRITE'} onChange={(event) => setWriteOffset(Number(event.target.value))} /></Field>
            </div>
            <Field id="file-io-write-content" label="Write Content (UTF-8)"><Textarea id="file-io-write-content" rows={8} value={writeContent} onChange={(event) => setWriteContent(event.target.value)} /></Field>
            <p className="text-xs text-muted-foreground">APPEND also requires a version. Offsets must not split a UTF-8 character. Saved drafts never carry a reusable version across page reloads.</p>
            <Button onClick={() => void submitWrite()} disabled={unavailable || !writePath || (!createOnly && !writeReview.snapshot) || writeReview.pending}><UploadCloud className="mr-2 h-4 w-4" />Write</Button>
            <Feedback error={writeError} info={writeInfo} />
          </CardContent>
        </Card>
        <Card><CardHeader><CardTitle className="text-xl">Rename</CardTitle><CardDescription>Move a single file; protect both source and destination.</CardDescription></CardHeader>
          <CardContent className="space-y-4">
            <Field id="file-io-rename-from" label="Source Path"><Input id="file-io-rename-from" value={renameFromPath} onChange={(event) => { sourceReview.clear(); setRenameFromPath(event.target.value); }} /></Field>
            <VersionReview title="source" review={sourceReview} disabled={unavailable || !renameFromPath} />
            <Field id="file-io-rename-to" label="Destination Path"><Input id="file-io-rename-to" value={renameToPath} onChange={(event) => { targetReview.clear(); setRenameToPath(event.target.value); }} /></Field>
            <label htmlFor="file-io-rename-overwrite" className="flex items-center gap-2 text-sm"><input id="file-io-rename-overwrite" type="checkbox" checked={renameOverwrite} onChange={(event) => { targetReview.clear(); setRenameOverwrite(event.target.checked); }} />Replace an existing destination using its version</label>
            {renameOverwrite ? <VersionReview title="destination" review={targetReview} disabled={unavailable || !renameToPath} /> : <p className="text-xs text-muted-foreground">The destination must not exist. Directory moves are not supported.</p>}
            <Button onClick={() => void submitRename()} disabled={unavailable || !renameToPath || !sourceReview.snapshot || sourceReview.pending || (renameOverwrite && (!targetReview.snapshot || targetReview.pending))}><ArrowRightLeft className="mr-2 h-4 w-4" />Rename</Button>
            <Feedback error={renameError} info={renameInfo} />
          </CardContent>
        </Card>
        <Card><CardHeader><CardTitle className="text-xl">Delete</CardTitle><CardDescription>Read and review the exact file before deleting it.</CardDescription></CardHeader>
          <CardContent className="space-y-4">
            <Field id="file-io-delete-path" label="Path To Delete"><Input id="file-io-delete-path" placeholder="/path/to/file" value={deletePath} onChange={(event) => { deleteReview.clear(); setDeletePath(event.target.value); }} /></Field>
            <VersionReview title="delete target" review={deleteReview} disabled={unavailable || !deletePath} />
            <p className="text-xs text-muted-foreground">Single-file deletion only. A file version does not protect a whole directory; recursive directory deletion is unavailable.</p>
            <Button variant="destructive" onClick={() => void submitDelete()} disabled={unavailable || !deleteReview.snapshot || deleteReview.pending}><Trash2 className="mr-2 h-4 w-4" />Delete</Button>
            <Feedback error={deleteError} info={deleteInfo} />
          </CardContent>
        </Card>
      </div>
      <Card><CardHeader><CardTitle className="text-xl">Search</CardTitle><CardDescription>Search indexed content; read the live file before editing a result.</CardDescription></CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-3 md:grid-cols-3">
            <Field id="file-io-search-query" label="Search Query"><Input id="file-io-search-query" value={searchQuery} onChange={(event) => setSearchQuery(event.target.value)} /></Field>
            <Field id="file-io-search-prefix" label="Path Prefix"><Input id="file-io-search-prefix" value={searchPrefix} onChange={(event) => setSearchPrefix(event.target.value)} /></Field>
            <Field id="file-io-search-limit" label="Result Limit"><Input id="file-io-search-limit" type="number" min={1} max={20} value={searchLimit} onChange={(event) => setSearchLimit(Number(event.target.value))} /></Field>
          </div>
          <Button onClick={() => void submitSearch()} disabled={unavailable || !searchQuery || searching}><Search className="mr-2 h-4 w-4" />Search</Button>
          <Feedback error={searchError} />
          <div className="space-y-3">{!searchResults.length ? <p className="text-sm text-muted-foreground">No results yet.</p> : searchResults.map((chunk) => <div key={`${chunk.file_path}-${chunk.file_seek_start_bytes}`} className="rounded-md border p-3">
            <div className="flex justify-between gap-2"><Button variant="link" onClick={() => void loadPreview(chunk.file_path)}>{chunk.file_path}</Button><Badge variant="secondary">Score {chunk.score.toFixed(3)}</Badge></div>
            <p className="text-xs text-muted-foreground">Bytes {chunk.file_seek_start_bytes} - {chunk.file_seek_end_bytes}</p><p className="mt-2 whitespace-pre-wrap text-sm">{chunk.chunk_content}</p>
          </div>)}</div>
        </CardContent>
      </Card>
    </fieldset>

    <Dialog open={previewOpen} onOpenChange={(open) => {
      if (busy) return;
      if (!open && draft !== loadedContent && !window.confirm('Close this unsaved draft? Copy it first to keep your changes.')) return;
      if (!open) { previewLane.cancel(); historyLane.cancel(); }
      setPreviewOpen(open);
    }}>
      <DialogContent className="flex h-[90vh] w-[95vw] max-w-5xl flex-col overflow-hidden p-0">
        <DialogHeader className="border-b border-border/60 px-6 py-4"><DialogTitle>File Preview</DialogTitle><DialogDescription className="break-all">{previewPath || 'No file selected'}</DialogDescription></DialogHeader>
        <div className="flex-1 space-y-3 overflow-y-auto px-6 py-4">
          {reading && <p className="text-sm text-muted-foreground">Loading file content...</p>}
          {base && <div className="space-y-1 rounded-md border bg-muted/30 p-3 text-xs text-muted-foreground">
            <p className="break-all">Live edit base: <code>{base.version}</code></p>
            <p>Read size: {new TextEncoder().encode(base.content).length} bytes</p>
            {previewStat && <p>Created: {formatTimestamp(previewStat.created_at)} · Updated: {formatTimestamp(previewStat.updated_at)}</p>}
            {historyId !== null && <p>Historical snapshot #{historyId} (read-only). Restore checks the live edit base above, not the history ID.</p>}
          </div>}
          {encoding !== 'utf-8' && <p className="text-sm text-amber-600">Historical binary content is shown encoded and cannot be saved or restored as UTF-8.</p>}
          <Textarea aria-label="File content" value={draft} onChange={(event) => setDraft(event.target.value)} rows={18}
            disabled={!apiKey || !base || busy || reading || encoding !== 'utf-8' || historyId !== null} className="min-h-[360px] font-mono text-sm" />
          {draft !== loadedContent && <p className="text-sm text-amber-600">Unsaved changes</p>}
          <Feedback error={readError} info={readInfo} />
        </div>
        <div className="flex flex-wrap items-center gap-2 border-t bg-muted/20 px-6 py-3">
          <Button variant="outline" size="sm" disabled={!apiKey || busy || reading} onClick={() => void loadPreview(previewPath)}><RefreshCw className="mr-2 h-4 w-4" />Reload latest</Button>
          <History className="h-4 w-4" />
          <select aria-label="File history" value={historyId === null ? 'current' : String(historyId)} disabled={!apiKey || !base || busy || reading || loadingVersions} className={cn(selectClass, 'w-auto')}
            onChange={(event) => event.target.value === 'current' ? void loadPreview(previewPath) : void loadHistory(Number(event.target.value))}>
            <option value="current">Current</option>{versions.map((item) => <option key={item.id} value={String(item.id)}>{formatTimestamp(item.created_at)} · {item.size} bytes</option>)}
          </select>
          <Button variant="outline" size="sm" disabled={!apiKey || !base || historyId === null || busy || reading || encoding !== 'utf-8'} onClick={() => void restoreVersion()}><History className="mr-2 h-4 w-4" />Restore as latest</Button>
          <Button size="sm" disabled={!apiKey || !base || historyId !== null || draft === loadedContent || busy || reading || encoding !== 'utf-8'} onClick={() => void saveFile()}><Save className="mr-2 h-4 w-4" />Save</Button>
        </div>
      </DialogContent>
    </Dialog>
  </>;
}
