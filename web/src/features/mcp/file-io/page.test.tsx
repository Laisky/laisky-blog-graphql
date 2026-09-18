import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { FileIOPage } from './page';
import { callFileAPI, callFileTool } from './client';
import { FileIORequestError } from './version-state';

const auth = vi.hoisted(() => ({ apiKey: 'test-key', isToolConsoleLocked: false }));
vi.mock('@/lib/api-key-context', () => ({ useApiKey: () => auth }));
vi.mock('./client', () => ({ callFileAPI: vi.fn(), callFileTool: vi.fn() }));
// Keep the tests focused on the console's request/state behavior, not Radix focus trapping.
vi.mock('@/components/ui/confirm-dialog', () => ({
  Dialog: ({ open, children, onOpenChange }: { open: boolean; children: ReactNode; onOpenChange: (open: boolean) => void }) => open ? <div>{children}<button onClick={() => onOpenChange(false)}>Close preview</button></div> : null,
  DialogContent: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogHeader: ({ children }: { children: ReactNode }) => <div>{children}</div>,
  DialogTitle: ({ children }: { children: ReactNode }) => <h2>{children}</h2>,
  DialogDescription: ({ children }: { children: ReactNode }) => <p>{children}</p>,
}));
const id = '76c3e40a3c4b4f5d9c2617d1b8094593';
const v1 = `${id}:1`, v2 = `${id}:2`, v9 = `${id}:9`;
const read = (content = 'base', version = v1) => ({ content, content_encoding: 'utf-8', version });
const entries = ['a', 'b'].map((name) => ({ name: `${name}.txt`, path: `/${name}.txt`, type: 'FILE', size: 4, created_at: '', updated_at: '' }));
const tool = vi.mocked(callFileTool);
const api = vi.mocked(callFileAPI);
function input(label: string, value: string) { fireEvent.change(screen.getByLabelText(label), { target: { value } }); }
function click(name: string) { fireEvent.click(screen.getByRole('button', { name, exact: true })); }
function enabled(name: string) { return !(screen.getByRole('button', { name, exact: true }) as HTMLButtonElement).disabled; }
async function openPreview() {
  click('Refresh list');
  await screen.findByRole('button', { name: 'a.txt' });
  click('a.txt');
  await waitFor(() => expect((screen.getByLabelText('File content') as HTMLTextAreaElement).value).toBe('base'));
  await waitFor(() => expect(enabled('Reload latest')).toBe(true));
}
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
beforeEach(() => {
  window.localStorage.clear();
  window.localStorage.setItem('mcp.file_io.inputs.v1', JSON.stringify({ project: 'p' }));
  auth.apiKey = 'test-key'; auth.isToolConsoleLocked = false;
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  tool.mockImplementation(async (_key, name) => {
    if (name === 'file_list') return { entries, has_more: false };
    if (name === 'file_read') return read();
    // A later stat must never replace the read's token.
    if (name === 'file_stat') return { ...entries[0], exists: true, version: v9 };
    if (name === 'file_write') return { bytes_written: 5, version: v2 };
    if (name === 'file_delete') return { deleted_count: 1 };
    if (name === 'file_rename') return { moved_count: 1 };
    throw new Error(`Unexpected tool ${name}`);
  });
  api.mockImplementation(async (_key, method, path) => {
    if (method === 'GET' && path === '/versions') return { versions: [{ id: '7', size: 3, created_at: '2026-01-01' }] };
    if (method === 'GET') return { ...read('old'), size: 3, created_at: '2026-01-01' };
    return { bytes_written: 5, version: v2 };
  });
});
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.clearAllMocks(); });

describe('FileIO page mandatory version workflow', () => {
  it('keeps a successful content snapshot editable when optional metadata fails', async () => {
    const implementation = tool.getMockImplementation()!;
    tool.mockImplementation((key, name, args) => name === 'file_stat'
      ? Promise.reject(new Error('metadata temporarily unavailable')) : implementation(key, name, args));
    render(<FileIOPage />); await openPreview();
    expect(screen.getByRole('note').textContent).toContain('Content loaded; optional metadata unavailable');
    expect(screen.queryByRole('alert')).toBeNull();
    input('File content', 'my edit'); click('Save');
    await waitFor(() => expect(api).toHaveBeenCalledWith('test-key', 'PUT', '/file', expect.objectContaining({
      headers: { 'If-Match': `"${v1}"` },
    })));
    await screen.findByText(/Saved 5 bytes/);
    expect(screen.queryByRole('note')).toBeNull();
  });
  it('ignores a late metadata failure from the previous file selection', async () => {
    const delayed = deferred<Record<string, unknown>>();
    const implementation = tool.getMockImplementation()!;
    tool.mockImplementation((key, name, args) => name === 'file_stat' && args.path === '/a.txt'
      ? delayed.promise.then(() => { throw new Error('old metadata failed'); })
      : name === 'file_read' && args.path === '/b.txt' ? Promise.resolve(read('B', v2)) : implementation(key, name, args));
    render(<FileIOPage />); click('Refresh list'); await screen.findByRole('button', { name: 'a.txt' });
    click('a.txt');
    await waitFor(() => expect(tool).toHaveBeenCalledWith('test-key', 'file_stat', { project: 'p', path: '/a.txt' }));
    click('b.txt');
    await waitFor(() => expect((screen.getByLabelText('File content') as HTMLTextAreaElement).value).toBe('B'));
    await act(async () => delayed.resolve({}));
    expect(screen.queryByRole('note')).toBeNull();
    expect(screen.queryByRole('alert')).toBeNull();
  });
  it('blocks unconditioned writes and explicitly creates with create_only', async () => {
    render(<FileIOPage />);
    input('Target Path', 'new.txt'); input('Write Content (UTF-8)', 'hello');
    expect(enabled('Write')).toBe(false);
    fireEvent.click(screen.getByLabelText('Create only (fail if the file exists)'));
    click('Write');
    await waitFor(() => expect(tool).toHaveBeenCalledWith('test-key', 'file_write', expect.objectContaining({ path: '/new.txt', create_only: true })));
    const args = tool.mock.calls.find((call) => call[1] === 'file_write')![2];
    expect(args).not.toHaveProperty('expected_version');
  });
  it.each(['APPEND', 'OVERWRITE', 'TRUNCATE'])('uses the reviewed version for %s and does not advance the standalone draft', async (mode) => {
    render(<FileIOPage />);
    input('Target Path', '/a.txt'); click('Read write base');
    await waitFor(() => expect(enabled('Write')).toBe(true));
    input('Write Mode', mode); input('Write Content (UTF-8)', 'hello');
    click('Write');
    await waitFor(() => expect(tool).toHaveBeenCalledWith('test-key', 'file_write', expect.objectContaining({ expected_version: v1, mode })));
    await waitFor(() => expect(enabled('Write')).toBe(false));
    expect((screen.getByLabelText('Write Content (UTF-8)') as HTMLTextAreaElement).value).toBe('hello');
  });
  it('confirms the write-base warning once per session instead of on every read', async () => {
    const confirm = vi.mocked(window.confirm);
    confirm.mockReturnValueOnce(false);
    render(<FileIOPage />);
    input('Target Path', '/a.txt'); input('Write Content (UTF-8)', 'my edit');
    click('Read write base');
    expect(confirm).toHaveBeenCalledTimes(1);
    expect(tool.mock.calls.filter((call) => call[1] === 'file_read')).toHaveLength(0);
    click('Read write base');
    await waitFor(() => expect(enabled('Write')).toBe(true));
    expect(confirm).toHaveBeenCalledTimes(2);
    click('Read write base');
    await waitFor(() => expect(tool.mock.calls.filter((call) => call[1] === 'file_read')).toHaveLength(2));
    expect(confirm).toHaveBeenCalledTimes(2);
    expect((screen.getByLabelText('Write Content (UTF-8)') as HTMLTextAreaElement).value).toBe('my edit');
    expect(screen.getByText(/Reading a new base keeps your draft/)).toBeTruthy();
  });
  it('preserves a rejected MCP draft and never silently reloads/retries', async () => {
    render(<FileIOPage />);
    input('Target Path', '/a.txt'); click('Read write base');
    await waitFor(() => expect(enabled('Write')).toBe(true));
    input('Write Content (UTF-8)', 'my edit');
    tool.mockRejectedValueOnce(new FileIORequestError('stale', 'VERSION_CONFLICT'));
    click('Write');
    await screen.findByText(/Version conflict:/);
    expect((screen.getByLabelText('Write Content (UTF-8)') as HTMLTextAreaElement).value).toBe('my edit');
    expect(tool.mock.calls.filter((call) => call[1] === 'file_read')).toHaveLength(1);
    expect(tool.mock.calls.filter((call) => call[1] === 'file_write')).toHaveLength(1);
  });
  it('uses the read token, not a later stat token, and keeps the successful response token for the next save', async () => {
    render(<FileIOPage />); await openPreview();
    input('File content', 'my edit'); click('Save');
    await waitFor(() => expect(api).toHaveBeenCalledWith('test-key', 'PUT', '/file', { body: { project: 'p', path: '/a.txt', content: 'my edit' }, headers: { 'If-Match': `"${v1}"` } }));
    await screen.findByText(/Saved 5 bytes/);
    input('File content', 'next edit'); click('Save');
    await waitFor(() => expect(api).toHaveBeenCalledWith('test-key', 'PUT', '/file', expect.objectContaining({ headers: { 'If-Match': `"${v2}"` } })));
    expect(tool.mock.calls.filter((call) => call[1] === 'file_read')).toHaveLength(1);
  });
  it('keeps the editor draft on HTTP 412 and 428 without replacing its base', async () => {
    render(<FileIOPage />); await openPreview();
    input('File content', 'keep my draft');
    api.mockRejectedValueOnce(new FileIORequestError('stale', undefined, 412));
    click('Save'); await screen.findByText(/Version conflict:/);
    expect((screen.getByLabelText('File content') as HTMLTextAreaElement).value).toBe('keep my draft');
    api.mockRejectedValueOnce(new FileIORequestError('missing', undefined, 428));
    click('Save'); await screen.findByText(/A file version is required/);
    const puts = api.mock.calls.filter((call) => call[1] === 'PUT');
    expect(puts).toHaveLength(2);
    puts.forEach((call) => expect(call[3]?.headers).toEqual({ 'If-Match': `"${v1}"` }));
  });
  it('keeps a large history ID exact in preview and restore URLs', async () => {
    const large = '9007199254740993';
    const implementation = api.getMockImplementation()!;
    api.mockImplementation((key, method, path, options) => method === 'GET' && path === '/versions'
      ? Promise.resolve({ versions: [{ id: large, size: 3, created_at: '2026-01-01' }] })
      : implementation(key, method, path, options));
    render(<FileIOPage />); await openPreview();
    await waitFor(() => expect((screen.getByLabelText('File history') as HTMLSelectElement).disabled).toBe(false));
    input('File history', large);
    await waitFor(() => expect(api).toHaveBeenCalledWith('test-key', 'GET', `/versions/${large}/content`, expect.anything()));
    await waitFor(() => expect(enabled('Restore as latest')).toBe(true));
    click('Restore as latest');
    await waitFor(() => expect(api).toHaveBeenCalledWith('test-key', 'POST', `/versions/${large}/restore`, expect.objectContaining({
      headers: { 'If-Match': `"${v1}"` },
    })));
  });
  it('restores read-only history against the observed live version, not the history ID', async () => {
    render(<FileIOPage />); await openPreview();
    await waitFor(() => expect((screen.getByLabelText('File history') as HTMLSelectElement).disabled).toBe(false));
    input('File history', '7');
    await waitFor(() => expect((screen.getByLabelText('File content') as HTMLTextAreaElement).value).toBe('old'));
    expect(enabled('Save')).toBe(false);
    await waitFor(() => expect(enabled('Restore as latest')).toBe(true));
    click('Restore as latest');
    await waitFor(() => expect(api).toHaveBeenCalledWith('test-key', 'POST', '/versions/7/restore', { body: { project: 'p', path: '/a.txt' }, headers: { 'If-Match': `"${v1}"` } }));
  });
  it('deletes only the reviewed file and exposes no recursive bypass', async () => {
    render(<FileIOPage />);
    input('Path To Delete', '/a.txt'); expect(enabled('Delete')).toBe(false);
    expect(screen.queryByLabelText('Recursive delete')).toBeNull();
    click('Read delete target'); await waitFor(() => expect(enabled('Delete')).toBe(true)); click('Delete');
    await waitFor(() => expect(tool).toHaveBeenCalledWith('test-key', 'file_delete', { project: 'p', path: '/a.txt', expected_version: v1 }));
  });
  it('requires both reviewed versions before replacing a rename destination', async () => {
    render(<FileIOPage />);
    input('Source Path', '/a.txt'); input('Destination Path', '/b.txt'); click('Read source');
    await waitFor(() => expect(enabled('Rename')).toBe(true));
    fireEvent.click(screen.getByLabelText('Replace an existing destination using its version'));
    expect(enabled('Rename')).toBe(false); click('Read destination');
    await waitFor(() => expect(enabled('Rename')).toBe(true)); click('Rename');
    await waitFor(() => expect(tool).toHaveBeenCalledWith('test-key', 'file_rename', {
      project: 'p', from_path: '/a.txt', to_path: '/b.txt', overwrite: true, expected_version: v1, expected_destination_version: v1,
    }));
  });
  it('invalidates reviewed versions immediately when a form path changes', async () => {
    render(<FileIOPage />); input('Target Path', '/a.txt'); click('Read write base');
    await waitFor(() => expect(enabled('Write')).toBe(true));
    input('Target Path', '/b.txt'); expect(enabled('Write')).toBe(false);
    input('Target Path', '/a.txt'); expect(enabled('Write')).toBe(false);
  });
  it('ignores an older read that completes after selecting another file', async () => {
    const old = deferred<ReturnType<typeof read>>();
    const implementation = tool.getMockImplementation()!;
    tool.mockImplementation((key, name, args) => name === 'file_read' && args.path === '/a.txt' ? old.promise : name === 'file_read' ? Promise.resolve(read('B')) : implementation(key, name, args));
    render(<FileIOPage />); click('Refresh list'); await screen.findByRole('button', { name: 'a.txt' });
    click('a.txt'); click('b.txt');
    await waitFor(() => expect((screen.getByLabelText('File content') as HTMLTextAreaElement).value).toBe('B'));
    await act(async () => old.resolve(read('late A')));
    expect((screen.getByLabelText('File content') as HTMLTextAreaElement).value).toBe('B');
  });
  it.each(['project', 'apiKey', 'locked'])('drops pending read state when %s changes', async (scope) => {
    const old = deferred<ReturnType<typeof read>>();
    const implementation = tool.getMockImplementation()!;
    tool.mockImplementation((key, name, args) => name === 'file_read' ? old.promise : implementation(key, name, args));
    const view = render(<FileIOPage />);
    input('Target Path', '/a.txt'); click('Read write base');
    if (scope === 'project') input('Project *', 'other');
    else { if (scope === 'apiKey') auth.apiKey = 'other-key'; else auth.isToolConsoleLocked = true; view.rerender(<FileIOPage />); }
    await act(async () => old.resolve(read()));
    expect(enabled('Write')).toBe(false);
    expect(screen.queryByText(/Read version:/)).toBeNull();
  });
  it('does not persist observed version tokens with stored draft inputs', async () => {
    render(<FileIOPage />); input('Target Path', '/a.txt'); click('Read write base');
    await waitFor(() => expect(enabled('Write')).toBe(true));
    input('Write Content (UTF-8)', 'draft');
    const saved = window.localStorage.getItem('mcp.file_io.inputs.v1')!;
    expect(saved).toContain('draft'); expect(saved).not.toContain(v1);
  });
});
