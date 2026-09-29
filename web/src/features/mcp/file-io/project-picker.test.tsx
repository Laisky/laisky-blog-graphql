import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { ProjectPicker } from './project-picker';
import { listProjects, type ProjectPage } from './project-options';

vi.mock('./project-options', async (original) => ({
  ...await original<typeof import('./project-options')>(), listProjects: vi.fn(),
}));
const list = vi.mocked(listProjects);
const page = (projects: string[], cursor?: string): ProjectPage => ({ projects, has_more: Boolean(cursor), next_cursor: cursor });
function Harness({ apiKey = 'key-a', disabled = false, allow = true }: { apiKey?: string; disabled?: boolean; allow?: boolean }) {
  const [value, setValue] = useState('');
  return <><label htmlFor="project">Project</label><ProjectPicker id="project" apiKey={apiKey}
    disabled={disabled} value={value} onChange={(next) => { if (!allow) return false; setValue(next); return true; }} /></>;
}
function input() { return screen.getByRole('combobox') as HTMLInputElement; }
function type(value: string) { fireEvent.change(input(), { target: { value } }); }
async function tick(ms = 200) { await act(async () => { await vi.advanceTimersByTimeAsync(ms); }); }
function deferred<T>() {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
}
beforeEach(() => { vi.useFakeTimers(); list.mockResolvedValue(page(['alpha', 'my-chat-project'])); });
afterEach(() => { cleanup(); vi.useRealTimers(); vi.restoreAllMocks(); vi.clearAllMocks(); });

describe('editable project picker', () => {
  it('discovers existing projects on focus and selects by mouse without rewriting the ID', async () => {
    render(<Harness />); fireEvent.focus(input()); await tick();
    expect(list).toHaveBeenCalledWith('key-a', '', '', expect.any(AbortSignal));
    fireEvent.click(screen.getByRole('option', { name: 'my-chat-project' }));
    expect(input().value).toBe('my-chat-project');
    expect(input().getAttribute('aria-expanded')).toBe('false');
  });
  it('debounces typing and sends the whole search to the server rather than filtering one page', async () => {
    render(<Harness />); type('M'); await tick(100); type('MC'); await tick(100); type('MCP');
    expect(list).not.toHaveBeenCalled();
    list.mockResolvedValue(page(['my-chat-project'])); await tick();
    expect(list).toHaveBeenCalledTimes(1);
    expect(list).toHaveBeenCalledWith('key-a', 'MCP', '', expect.any(AbortSignal));
    expect(screen.getAllByRole('option')).toHaveLength(1);
    expect(input().value).toBe('MCP');
  });
  it('allows unlisted manual IDs and never chooses automatically on blur or Enter', async () => {
    list.mockResolvedValue(page([])); render(<Harness />); type('new-project'); await tick();
    fireEvent.keyDown(input(), { key: 'Enter' });
    fireEvent.blur(input(), { relatedTarget: null });
    expect(input().value).toBe('new-project');
    expect(screen.queryByRole('listbox')).toBeNull();
  });
  it('supports keyboard navigation, keeps focus in the input, and preserves text on Escape', async () => {
    render(<Harness />); fireEvent.focus(input()); await tick();
    fireEvent.keyDown(input(), { key: 'ArrowDown' });
    const optionID = input().getAttribute('aria-activedescendant');
    expect(document.getElementById(optionID!)?.textContent).toBe('alpha');
    fireEvent.keyDown(input(), { key: 'ArrowUp' });
    fireEvent.keyDown(input(), { key: 'Enter' });
    expect(input().value).toBe('my-chat-project');
    fireEvent.keyDown(input(), { key: 'ArrowDown' }); await tick();
    fireEvent.keyDown(input(), { key: 'Escape' });
    expect(input().value).toBe('my-chat-project');
    expect(input().getAttribute('aria-expanded')).toBe('false');
  });
  it('does not select an option while composing text', async () => {
    render(<Harness />); fireEvent.focus(input()); await tick(); fireEvent.keyDown(input(), { key: 'ArrowDown' });
    fireEvent.keyDown(input(), { key: 'Enter', isComposing: true });
    expect(input().value).toBe('');
    expect(input().getAttribute('aria-expanded')).toBe('true');
  });
  it('honors a rejected project change without clearing the current list', async () => {
    render(<Harness allow={false} />); fireEvent.focus(input()); await tick();
    fireEvent.click(screen.getByRole('option', { name: 'alpha' }));
    expect(input().value).toBe(''); expect(screen.getAllByRole('option')).toHaveLength(2);
  });
  it('loads one bounded page at a time and resets the cursor after a new query', async () => {
    list.mockResolvedValueOnce(page(['alpha'], 'alpha')).mockResolvedValueOnce(page(['beta'])).mockResolvedValueOnce(page(['beta']));
    render(<Harness />); fireEvent.focus(input()); await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Load more projects' })); await tick(0);
    expect(list).toHaveBeenNthCalledWith(2, 'key-a', '', 'alpha', expect.any(AbortSignal));
    expect(screen.getAllByRole('option')).toHaveLength(2);
    type('bt'); expect(screen.queryByRole('option')).toBeNull(); await tick();
    expect(list).toHaveBeenLastCalledWith('key-a', 'bt', '', expect.any(AbortSignal));
  });
  it('keeps manual input available on lookup failure without displaying server details', async () => {
    list.mockRejectedValue(new Error('private server details')); render(<Harness />); type('new'); await tick();
    expect(screen.getByRole('status').textContent).toContain('unavailable');
    expect(screen.queryByText('private server details')).toBeNull();
    expect(input().disabled).toBe(false); type('new-id'); expect(input().value).toBe('new-id');
  });
  it('does not query missing credentials, locked consoles, or invalid fragments', async () => {
    const view = render(<Harness apiKey="" />); fireEvent.focus(input()); await tick(); expect(list).not.toHaveBeenCalled();
    view.rerender(<Harness disabled />); await tick(); expect(input().disabled).toBe(true);
    view.rerender(<Harness />); type('%'); await tick(); expect(list).not.toHaveBeenCalled();
  });
  it('restarts a search even when trimming produces the same query', async () => {
    render(<Harness />); type('alpha'); await tick(); type('alpha '); await tick();
    expect(list).toHaveBeenCalledTimes(2); expect(screen.getAllByRole('option')).toHaveLength(2);
  });
  it('ignores an aborted old query even when its transport resolves after the new query', async () => {
    const old = deferred<ProjectPage>(); list.mockReturnValueOnce(old.promise).mockResolvedValueOnce(page(['beta']));
    render(<Harness />); type('a'); await tick(); const signal = list.mock.calls[0][3];
    type('b'); expect(signal.aborted).toBe(true); expect(screen.queryByRole('option')).toBeNull(); await tick();
    await act(async () => old.resolve(page(['alpha'])));
    expect(screen.queryByRole('option', { name: 'alpha' })).toBeNull();
    expect(screen.getByRole('option', { name: 'beta' })).toBeTruthy();
  });
  it.each(['credential', 'lock', 'unmount'])('drops pending pages on %s changes even if abort is ignored', async (kind) => {
    const old = deferred<ProjectPage>(); list.mockReturnValueOnce(old.promise);
    const view = render(<Harness />); fireEvent.focus(input()); await tick(); const signal = list.mock.calls[0][3];
    if (kind === 'credential') view.rerender(<Harness apiKey="key-b" />);
    else if (kind === 'lock') view.rerender(<Harness disabled />);
    else view.unmount();
    expect(signal.aborted).toBe(true);
    await act(async () => old.resolve(page(['a-private'])));
    expect(screen.queryByText('a-private')).toBeNull();
    if (kind === 'credential') {
      list.mockResolvedValue(page(['b-private'])); fireEvent.focus(input()); await tick();
      expect(list).toHaveBeenLastCalledWith('key-b', '', '', expect.any(AbortSignal));
      expect(screen.getByRole('option', { name: 'b-private' })).toBeTruthy();
    }
  });
  it('cancels a late pagination result when the search changes', async () => {
    const old = deferred<ProjectPage>();
    list.mockResolvedValueOnce(page(['alpha'], 'alpha')).mockReturnValueOnce(old.promise).mockResolvedValueOnce(page(['beta']));
    render(<Harness />); fireEvent.focus(input()); await tick();
    fireEvent.click(screen.getByRole('button', { name: 'Load more projects' })); await tick(0);
    const signal = list.mock.calls[1][3]; type('b'); await tick();
    await act(async () => old.resolve(page(['a-private'])));
    expect(signal.aborted).toBe(true); expect(screen.queryByText('a-private')).toBeNull();
    expect(screen.getByRole('option', { name: 'beta' })).toBeTruthy();
  });
});
