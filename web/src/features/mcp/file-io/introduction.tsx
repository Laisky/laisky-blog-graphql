import { ChevronDown, ChevronUp, FolderOpen } from 'lucide-react';
import { useState } from 'react';
import { Button } from '@/components/ui/button';

const descriptions = [
  ['file_write', 'APPEND, OVERWRITE and TRUNCATE all require expected_version, or create_only=true for a new file.'],
  ['file_read', 'Returns content and its version together. Pin subsequent byte ranges with expected_version.'],
  ['file_list_versions', 'List retained history with string IDs and bounded before_id pagination.'],
  ['file_read_version', 'Read immutable historical bytes. Check content_encoding before interpreting legacy binary content.'],
  ['file_restore_version', 'Restore history with the original live expected_version, or create_only=true after deletion. History IDs do not authorize overwriting.'],
  ['file_list', 'Browse project files and directories. Listing entries is not a file-edit snapshot.'],
  ['file_stat', 'Inspect metadata and the current file version. Do not pair a later stat version with earlier read content.'],
  ['file_search', 'Find relevant indexed content. Read the live file before editing; search chunks are not an edit base.'],
  ['file_delete', 'Delete one file with its expected_version. Recursive directory deletion is not supported by this versioned client contract.'],
  ['file_rename', 'Move one file with its source version. Require an absent destination, or protect an existing destination with its own version.'],
];

/** FileIOIntroduction explains the FileIO contract, including the mandatory version preconditions, before the workspace loads. */
export function FileIOIntroduction() {
  const [collapsed, setCollapsed] = useState(() => {
    try { return localStorage.getItem('mcp_file_io_description_collapsed') === 'true'; } catch { return false; }
  });
  return (
    <section className="space-y-3">
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-2 text-sm font-medium uppercase tracking-widest text-primary">
          <FolderOpen className="h-4 w-4" /><span>FileIO Console</span>
        </div>
        <Button variant="ghost" size="sm" onClick={() => {
          setCollapsed(!collapsed);
          try { localStorage.setItem('mcp_file_io_description_collapsed', String(!collapsed)); } catch { /* Optional preference. */ }
        }}>
          {collapsed ? <>Show Introduction <ChevronDown className="ml-2 h-4 w-4" /></> : <>Hide Introduction <ChevronUp className="ml-2 h-4 w-4" /></>}
        </Button>
      </div>
      {!collapsed && <div className="space-y-5">
        <h1 className="text-3xl font-bold tracking-tight sm:text-4xl">file_io</h1>
        <p className="max-w-4xl text-lg leading-relaxed text-muted-foreground">
          Project-scoped remote files for shared agent context and persistent memory.
          Version checks reject stale edits instead of silently overwriting another client's changes.
        </p>
        <div className="rounded-lg border border-primary/30 bg-primary/5 p-4 text-sm space-y-2">
          <p><strong>Read → review → edit → write with the returned version.</strong> Versions are opaque strings, not numbers or history IDs.</p>
          <p>All mutations require conditions, including APPEND. On VERSION_CONFLICT (HTTP 412), keep your draft, read the latest content, and recompute the change. Do not just swap in a new token.</p>
          <p>PRECONDITION_REQUIRED (HTTP 428) means the condition is missing. Writes and byte ranges must preserve valid UTF-8 boundaries. No force-write, automatic merge, or success replay is provided.</p>
        </div>
        <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {descriptions.map(([name, text]) => <div key={name} className="rounded-lg border border-border/50 bg-card/50 p-4">
            <div className="mb-2 font-mono text-sm font-semibold text-primary">{name}</div>
            <p className="text-sm text-muted-foreground">{text}</p>
          </div>)}
        </div>
        <details className="rounded-lg border border-border/60 p-4 text-sm">
          <summary className="cursor-pointer font-medium">MCP request examples</summary>
          <div className="mt-3 space-y-3">
            <p>Create a new file (fails if it already exists):</p>
            <pre className="overflow-x-auto rounded bg-muted p-3">{JSON.stringify({ name: 'file_write', arguments: { project: 'notes', path: '/state.json', mode: 'TRUNCATE', content: '{"count":0}', create_only: true } }, null, 2)}</pre>
            <p>Read the file; use its actual returned version with your edit (the token below is illustrative):</p>
            <pre className="overflow-x-auto rounded bg-muted p-3">{JSON.stringify({ name: 'file_read', arguments: { project: 'notes', path: '/state.json', offset: 0, length: -1 } }, null, 2)}</pre>
            <pre className="overflow-x-auto rounded bg-muted p-3">{JSON.stringify({ name: 'file_write', arguments: { project: 'notes', path: '/state.json', mode: 'TRUNCATE', content: '{"count":1}', expected_version: '76c3e40a3c4b4f5d9c2617d1b8094593:42' } }, null, 2)}</pre>
            <p>MCP history tools use <code>history_id</code> as an exact decimal string. To restore, also supply the original live <code>expected_version</code>, or <code>create_only=true</code> for an absent path. List pages use <code>next_cursor</code> as the next <code>before_id</code>.</p>
            <p>Editor saves and historical restores send <code>If-Match: "&lt;live-file-version&gt;"</code>. The history ID selects the old bytes; it never substitutes for the live version.</p>
          </div>
        </details>
        <p className="text-sm text-muted-foreground">Client protocol and examples: <a className="text-primary underline" href="https://github.com/Laisky/laisky-blog-graphql/blob/master/docs/manual/fileio_versioning.md" target="_blank" rel="noreferrer">FileIO versioned editing manual</a></p>
      </div>}
    </section>
  );
}
