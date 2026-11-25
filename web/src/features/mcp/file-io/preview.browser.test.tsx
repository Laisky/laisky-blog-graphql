import '@/index.css';
import { cleanup, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { page, userEvent } from 'vitest/browser';

import { callFileAPI, callFileTool } from './client';
import { FileIOPage } from './page';
import { fileIOInputStorageKey } from './use-file-io-input-storage';

// Real Chrome, real Tailwind CSS, real Radix dialog: these cases need layout, scrolling and the
// browser's own focus rules, none of which jsdom implements. Only the transport and the API key are mocked.
vi.mock('@/lib/api-key-context', () => ({ useApiKey: () => ({ apiKey: 'browser-key', isToolConsoleLocked: false, sessionId: 's' }) }));
vi.mock('./client', () => ({ callFileAPI: vi.fn(), callFileTool: vi.fn() }));

const tool = vi.mocked(callFileTool);
const api = vi.mocked(callFileAPI);
const version = '76c3e40a3c4b4f5d9c2617d1b8094593';
const fileLines = (count: number) => Array.from({ length: count }, (_, i) => `line ${i + 1}: lorem ipsum dolor sit amet`).join('\n');
// Thousands of words: several screens tall at every viewport below.
const longMessage = (prefix: string) => `${prefix}: ${Array.from({ length: 1500 }, (_, i) => `detail${i}`).join(' ')}`;

type Deferred<T> = { promise: Promise<T>; resolve: (value: T) => void; reject: (reason: unknown) => void };
function deferred<T>(): Deferred<T> {
  let resolve!: (value: T) => void, reject!: (reason: unknown) => void;
  const promise = new Promise<T>((done, fail) => { resolve = done; reject = fail; });
  return { promise, resolve, reject };
}
const frame = () => new Promise((done) => requestAnimationFrame(() => requestAnimationFrame(done)));

let lines = 220;
let versions: () => Promise<unknown>;
let save: () => Promise<unknown>;

beforeEach(async () => {
  await page.viewport(1280, 720);
  lines = 220;
  versions = async () => ({ versions: [{ id: '7', size: 3, created_at: '2026-01-01T00:00:00Z' }] });
  save = async () => ({ bytes_written: 5, version: `${version}:2` });
  window.localStorage.setItem(fileIOInputStorageKey('browser-key')!, JSON.stringify({ project: 'p' }));
  tool.mockImplementation(async (_key, name, args) => {
    if (name === 'file_list') return { entries: [{ name: 'a.txt', path: '/a.txt', type: 'FILE', size: 4, created_at: '', updated_at: '' }], has_more: false };
    if (name === 'file_read') return { content: fileLines(lines), content_encoding: 'utf-8', version: `${version}:1` };
    if (name === 'file_stat') return { name: 'a.txt', path: args.path, type: 'FILE', size: 4, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z', exists: true, version: `${version}:1` };
    throw new Error(`unexpected tool ${name}`);
  });
  api.mockImplementation(async (_key, method, path) => {
    if (method === 'GET' && path === '/projects') return { projects: ['p'], has_more: false };
    if (method === 'GET' && path === '/versions') return versions();
    if (method === 'PUT') return save();
    throw new Error(`unexpected ${method} ${path}`);
  });
});
afterEach(() => { cleanup(); window.localStorage.clear(); });

const dialog = () => screen.getByRole('dialog');
const editor = () => screen.getByLabelText('File content') as HTMLTextAreaElement;
const action = (name: string) => screen.getByRole('button', { name, exact: true }) as HTMLButtonElement;
const unavailable = (element: HTMLElement) => (element as HTMLButtonElement).disabled || element.getAttribute('aria-disabled') === 'true';

async function openPreview() {
  render(<FileIOPage />);
  await userEvent.click(screen.getByRole('button', { name: 'Refresh list' }));
  await userEvent.click(await screen.findByRole('button', { name: 'a.txt' }));
  await waitFor(() => expect(editor().value).toBe(fileLines(lines)));
  await waitFor(() => expect(unavailable(action('Reload latest'))).toBe(false));
  // Let the dialog's open animation (a transform) finish, so positions are stable.
  await Promise.all(document.getAnimations().map((animation) => animation.finished));
}

/** userScrollers lists the dialog's elements that the user can scroll vertically right now. */
function userScrollers() {
  return [dialog(), ...dialog().querySelectorAll<HTMLElement>('*')].filter((element) => {
    const { overflowY } = getComputedStyle(element);
    return (overflowY === 'auto' || overflowY === 'scroll') && element.scrollHeight > element.clientHeight + 1;
  });
}
function onlyScroller() {
  const scrollers = userScrollers();
  expect(scrollers, 'the dialog must have exactly one vertical scroller').toHaveLength(1);
  return scrollers[0];
}
function inView(element: Element, scroller: Element) {
  const box = element.getBoundingClientRect(), port = scroller.getBoundingClientRect();
  return box.bottom > port.top + 8 && box.top < port.bottom - 8;
}
/** wheelUntil scrolls like a user, with real wheel events over the scroller, until the element is visible in `port`. */
async function wheelUntil(element: Element, scroller: Element, direction: 'up' | 'down', port: Element = scroller) {
  for (let i = 0; i < 120 && !inView(element, port); i++) {
    await userEvent.wheel(scroller, { delta: { y: direction === 'down' ? 1200 : -1200 } });
    await frame();
  }
  return inView(element, port);
}

describe('File Preview layout in a real browser', () => {
  // 640×360 is a 1280×720 window at 200% zoom; 320×256 is 1280×1024 at 400% (WCAG 1.4.10 reflow).
  it.each([[1280, 720], [390, 844], [640, 360], [320, 256]])('keeps the draft reachable below a multi-screen save error at %ix%i', async (width, height) => {
    await page.viewport(width, height);
    await openPreview();
    await userEvent.type(editor(), 'my edit ');
    const draft = editor().value;
    expect(draft).toContain('my edit');
    const message = longMessage('Save rejected');
    save = async () => { throw new Error(message); };

    await userEvent.click(action('Save'));
    const alert = await screen.findByRole('alert');

    // The complete message is kept, in the same single scroller as the editor, before it.
    expect(alert.textContent).toBe(message);
    const scroller = onlyScroller();
    expect(scroller.contains(alert) && scroller.contains(editor())).toBe(true);
    expect(alert.compareDocumentPosition(editor()) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    expect(alert.getBoundingClientRect().height).toBeGreaterThan(scroller.clientHeight);

    // Ordinary downward scrolling reaches the preserved, editable draft.
    expect(await wheelUntil(editor(), scroller, 'down')).toBe(true);
    expect(editor().value).toBe(draft);
    expect(editor().disabled || editor().readOnly).toBe(false);
    await userEvent.type(editor(), 'more ');
    expect(editor().value).toContain('more');

    // The actions stay reachable: back at the top of the same scroller, or pinned above it.
    expect(await wheelUntil(screen.getByRole('group', { name: 'File actions' }), scroller, 'up', dialog())).toBe(true);
    const saveButton = action('Save');
    await userEvent.hover(saveButton); // Playwright scrolls it into view and checks that nothing covers it.
    const box = saveButton.getBoundingClientRect();
    expect(document.elementFromPoint(box.left + box.width / 2, box.top + box.height / 2)?.closest('button')).toBe(saveButton);
    expect(unavailable(saveButton)).toBe(false);
  }, 60_000);

  it('keeps short feedback compact, right under the actions', async () => {
    lines = 3;
    await openPreview();
    await userEvent.type(editor(), 'x');
    save = async () => { throw new Error('Version conflict: the file changed since you opened it.'); };
    await userEvent.click(action('Save'));
    const alert = await screen.findByRole('alert');
    const toolbar = screen.getByRole('group', { name: 'File actions' }).getBoundingClientRect();
    const box = alert.getBoundingClientRect();
    expect(box.top - toolbar.bottom).toBeLessThanOrEqual(24);
    expect(box.height).toBeLessThan(48);
    expect(userScrollers()).toHaveLength(0);
    expect(inView(editor(), dialog())).toBe(true);
    expect(screen.queryByRole('button', { name: 'Show message' })).toBeNull();
  });
});

describe('File Preview focus in a real browser', () => {
  it.each(['Scroll to top', 'Scroll to bottom'])('keeps focus on "%s" when a resize removes the overflow, until the user tabs away', async (name) => {
    await openPreview();
    const scroller = onlyScroller();
    if (name === 'Scroll to top') scroller.scrollTop = 600;
    await frame();
    const button = await screen.findByRole('button', { name });
    button.focus();
    await page.viewport(1280, 6000);
    await waitFor(() => expect(userScrollers()).toHaveLength(0));
    await frame();

    expect(document.activeElement).toBe(button);
    expect(button.isConnected).toBe(true);
    expect(button.getAttribute('aria-disabled')).toBe('true');
    const before = scroller.scrollTop;
    await userEvent.keyboard('{Enter}');
    expect(scroller.scrollTop).toBe(before);

    // The user moves focus: Tab goes to the next control, never to the dialog container, and the group goes away.
    while (document.activeElement?.closest('[aria-label^="Scroll to"]') || /^Scroll to/.test(document.activeElement?.getAttribute('aria-label') ?? '')) {
      await userEvent.keyboard('{Tab}');
    }
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Scroll to bottom' })).toBeNull());
    expect(document.activeElement).not.toBe(document.body);
    expect(document.activeElement).not.toBe(dialog());
  });

  it('keeps focus on Save while a slow save runs and after it fails', async () => {
    await openPreview();
    await userEvent.type(editor(), 'x');
    const pending = deferred<unknown>();
    save = () => pending.promise;
    const saveButton = action('Save');
    await userEvent.click(saveButton);
    await waitFor(() => expect(unavailable(saveButton)).toBe(true));
    await frame();
    expect(document.activeElement).toBe(saveButton);

    pending.reject(new Error('Save rejected'));
    await screen.findByRole('alert');
    await frame();
    expect(document.activeElement).toBe(saveButton);
    expect(unavailable(saveButton)).toBe(false);
  });

  it('keeps editor focus and selection through feedback, resizes and layout switches', async () => {
    const late = deferred<unknown>();
    versions = () => late.promise;
    lines = 40;
    await openPreview();
    const field = editor();
    field.focus();
    field.setSelectionRange(120, 135);
    const kept = () => {
      expect(document.activeElement).toBe(field);
      expect([field.selectionStart, field.selectionEnd]).toEqual([120, 135]);
    };

    late.reject(new Error(longMessage('History list unavailable')));
    await screen.findByRole('alert');
    await frame();
    kept();
    for (const [width, height] of [[320, 256], [1280, 6000], [390, 844], [1280, 720]]) {
      await page.viewport(width, height);
      await frame();
      kept();
    }
  });
});

describe('File Preview scroll stability in a real browser', () => {
  it.each([['native', false], ['emulated', true]])('does not move the viewed content when feedback appears above it (%s anchoring)', async (_mode, emulate) => {
    if (emulate) {
      const supports = CSS.supports.bind(CSS) as (property: string, value: string) => boolean;
      vi.spyOn(CSS, 'supports').mockImplementation(((property: string, value: string) => property === 'overflow-anchor' ? false : supports(property, value)) as never);
    }
    const late = deferred<unknown>();
    versions = () => late.promise;
    await openPreview();
    const scroller = onlyScroller();
    if (emulate) scroller.style.overflowAnchor = 'none'; // A browser without scroll anchoring (Safari before 27).
    scroller.scrollTop = 2000;
    await frame();
    const top = editor().getBoundingClientRect().top;

    late.reject(new Error(longMessage('History list unavailable')));
    await screen.findByRole('alert');
    await frame();
    expect(Math.abs(editor().getBoundingClientRect().top - top)).toBeLessThanOrEqual(1);

    // The message is above the viewport: a floating button offers to scroll back to it, and keeps focus after.
    const show = await screen.findByRole('button', { name: 'Show message' });
    show.focus();
    await userEvent.keyboard('{Enter}');
    await waitFor(() => expect(inView(screen.getByRole('alert'), scroller)).toBe(true));
    expect(document.activeElement).toBe(show);
    expect(show.getAttribute('aria-disabled')).toBe('true');
  });
});
