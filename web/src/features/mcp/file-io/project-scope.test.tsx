import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { FileIOPage } from './page';
import { callFileAPI, callFileTool } from './client';
import { fileIOInputStorageKey } from './use-file-io-input-storage';

const auth = vi.hoisted(() => ({ apiKey: 'key-a', isToolConsoleLocked: false, sessionId: 1 }));
vi.mock('@/lib/api-key-context', () => ({ useApiKey: () => auth }));
vi.mock('./client', () => ({ callFileAPI: vi.fn(), callFileTool: vi.fn() }));
const api = vi.mocked(callFileAPI);
function field(label: string) { return screen.getByLabelText(label) as HTMLInputElement; }
function set(label: string, value: string) { fireEvent.change(field(label), { target: { value } }); }
async function tick() { await act(async () => { await vi.advanceTimersByTimeAsync(200); }); }
beforeEach(() => {
  vi.useFakeTimers(); window.localStorage.clear();
  Object.assign(auth, { apiKey: 'key-a', isToolConsoleLocked: false, sessionId: 1 });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  api.mockImplementation(async (key, _method, path) => {
    if (path === '/projects') return { projects: key === 'key-a' ? ['a-private', 'shared'] : ['b-private', 'shared'], has_more: false };
    throw new Error(`Unexpected path ${path}`);
  });
});
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); vi.clearAllMocks(); });

describe('FileIO page project and credential boundaries', () => {
  it('does not display an unowned legacy project or draft on first load', () => {
    window.localStorage.setItem('mcp.file_io.inputs.v1', JSON.stringify({ project: 'old-owner', writeContent: 'private draft' }));
    render(<FileIOPage />);
    expect(field('Project *').value).toBe(''); expect(field('Write Content (UTF-8)').value).toBe('');
    expect(screen.queryByDisplayValue('private draft')).toBeNull();
  });
  it('synchronously replaces the project, drafts, and visible suggestions when credentials change', async () => {
    window.localStorage.setItem(fileIOInputStorageKey('key-a')!, JSON.stringify({ project: 'shared', writeContent: 'a-draft' }));
    window.localStorage.setItem(fileIOInputStorageKey('key-b')!, JSON.stringify({ project: 'shared', writeContent: 'b-draft' }));
    const view = render(<FileIOPage />); fireEvent.focus(field('Project *')); await tick();
    expect(screen.getByRole('option', { name: 'a-private' })).toBeTruthy();
    auth.apiKey = 'key-b'; auth.sessionId += 1; view.rerender(<FileIOPage />);
    expect(field('Project *').value).toBe('shared'); expect(field('Write Content (UTF-8)').value).toBe('b-draft');
    expect(screen.queryByRole('listbox', { name: 'Existing projects' })).toBeNull();
    expect(screen.queryByRole('option', { name: 'a-private' })).toBeNull(); expect(screen.queryByDisplayValue('a-draft')).toBeNull();
    fireEvent.focus(field('Project *')); await tick();
    expect(screen.queryByRole('option', { name: 'a-private' })).toBeNull();
    expect(screen.getByRole('option', { name: 'b-private' })).toBeTruthy();
    expect(window.localStorage.getItem(fileIOInputStorageKey('key-b')!)).not.toContain('a-draft');
  });
  it('clears locked views without overwriting saved drafts and restores only the unlocking key', async () => {
    window.localStorage.setItem(fileIOInputStorageKey('key-a')!, JSON.stringify({ project: 'a-private', writeContent: 'a-draft' }));
    const view = render(<FileIOPage />); fireEvent.focus(field('Project *')); await tick();
    auth.isToolConsoleLocked = true; view.rerender(<FileIOPage />);
    expect(field('Project *').disabled).toBe(true); expect(field('Project *').value).toBe('');
    expect(field('Write Content (UTF-8)').value).toBe(''); expect(screen.queryByRole('listbox', { name: 'Existing projects' })).toBeNull();
    expect(screen.queryByRole('option', { name: 'a-private' })).toBeNull();
    expect(window.localStorage.getItem(fileIOInputStorageKey('key-a')!)).toContain('a-draft');
    auth.isToolConsoleLocked = false; view.rerender(<FileIOPage />);
    expect(field('Project *').value).toBe('a-private'); expect(field('Write Content (UTF-8)').value).toBe('a-draft');
  });
  it('aborts and ignores previous-session suggestions even when the same key reconnects', async () => {
    let resolve!: (value: unknown) => void;
    api.mockImplementationOnce(() => new Promise((done) => { resolve = done; }));
    const view = render(<FileIOPage />); fireEvent.focus(field('Project *')); await tick();
    const signal = api.mock.calls[0][3]?.signal;
    auth.sessionId += 1; view.rerender(<FileIOPage />);
    expect(signal?.aborted).toBe(true);
    await act(async () => resolve({ projects: ['previous-session'], has_more: false }));
    expect(screen.queryByText('previous-session')).toBeNull();
    expect(screen.queryByRole('listbox')).toBeNull();
  });
  it('keeps drafts when a project switch is declined and discards them only after acceptance', async () => {
    window.localStorage.setItem(fileIOInputStorageKey('key-a')!, JSON.stringify({ project: 'shared', writeContent: 'keep me' }));
    render(<FileIOPage />); fireEvent.focus(field('Project *')); await tick();
    vi.mocked(window.confirm).mockReturnValueOnce(false);
    fireEvent.click(screen.getByRole('option', { name: 'a-private' }));
    expect(field('Project *').value).toBe('shared'); expect(field('Write Content (UTF-8)').value).toBe('keep me');
    fireEvent.click(screen.getByRole('option', { name: 'a-private' }));
    expect(field('Project *').value).toBe('a-private'); expect(field('Write Content (UTF-8)').value).toBe('');
    set('Project *', 'shared');
    expect(field('Write Content (UTF-8)').value).toBe('');
    expect(callFileTool).not.toHaveBeenCalled();
  });
});
