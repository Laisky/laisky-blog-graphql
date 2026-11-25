import '@testing-library/jest-dom/vitest';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import type { ReactNode } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { PreviewScrollRegion } from './preview-scroll-region';

// jsdom has no layout, so the geometry the region reads is supplied per test. Real layout, scrolling and
// browser focus handling are covered by preview.browser.test.tsx.
type Geometry = {
  scrollHeight: number; clientHeight: number; scrollTop: number;
  frameHeight?: number; headerHeight?: number; noticeTop?: number; noticeHeight?: number; followingTop?: number;
};
function define(element: Element, key: string, get: () => number, set?: (value: number) => void) {
  Object.defineProperty(element, key, { configurable: true, get, set });
}
function renderRegion(geometry: Geometry, notice?: ReactNode) {
  const view = render(<PreviewScrollRegion header={<button type="button">Header action</button>} notice={notice}><p>file body</p></PreviewScrollRegion>);
  const frame = view.container.querySelector<HTMLDivElement>('[data-preview-layout]')!;
  const scroller = view.container.querySelector<HTMLDivElement>('[data-preview-scroll]')!;
  const noticeBox = view.container.querySelector<HTMLDivElement>('[data-preview-notice]')!;
  define(frame, 'clientHeight', () => geometry.frameHeight ?? 0);
  define(frame.firstElementChild!, 'offsetHeight', () => geometry.headerHeight ?? 0);
  define(scroller, 'scrollHeight', () => geometry.scrollHeight);
  define(scroller, 'clientHeight', () => geometry.clientHeight);
  define(scroller, 'scrollTop', () => geometry.scrollTop, (value) => { geometry.scrollTop = value; });
  define(noticeBox, 'offsetTop', () => geometry.noticeTop ?? 0);
  define(noticeBox, 'offsetHeight', () => geometry.noticeHeight ?? 0);
  define(screen.getByText('file body'), 'offsetTop', () => geometry.followingTop ?? 0);
  const scrollTo = vi.fn();
  scroller.scrollTo = scrollTo as unknown as typeof scroller.scrollTo;
  return { ...view, frame, scroller, noticeBox, scrollTo };
}
const button = (name: 'Scroll to top' | 'Scroll to bottom' | 'Show message') => screen.queryByRole('button', { name });

let observers: { callback: ResizeObserverCallback; targets: Element[]; disconnect: ReturnType<typeof vi.fn> }[] = [];
function resizeAll() { act(() => { for (const observer of observers) observer.callback([], observer as unknown as ResizeObserver); }); }
/** settleFocus lets the region notice that focus moved: it checks one task after a blur. */
async function settleFocus() { await act(async () => { await new Promise((done) => setTimeout(done, 0)); }); }

beforeEach(() => {
  observers = [];
  vi.stubGlobal('ResizeObserver', class {
    private readonly entry: (typeof observers)[number];
    constructor(callback: ResizeObserverCallback) { this.entry = { callback, targets: [], disconnect: vi.fn() }; observers.push(this.entry); }
    observe(target: Element) { this.entry.targets.push(target); }
    unobserve() {}
    disconnect() { this.entry.disconnect(); }
  });
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); vi.restoreAllMocks(); });

describe('PreviewScrollRegion', () => {
  it('renders the header outside the single scroller, and the notice before the body inside it', () => {
    const { scroller } = renderRegion({ scrollHeight: 500, clientHeight: 500, scrollTop: 0 }, <p role="alert">save failed</p>);
    const alert = screen.getByRole('alert');
    expect(scroller.contains(screen.getByRole('button', { name: 'Header action' }))).toBe(false);
    expect(scroller.contains(alert)).toBe(true);
    expect(alert.compareDocumentPosition(screen.getByText('file body')) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it('renders its children without jump buttons while the content fits', () => {
    renderRegion({ scrollHeight: 500, clientHeight: 500, scrollTop: 0 });
    resizeAll();
    expect(screen.getByText('file body')).toBeInTheDocument();
    expect(button('Scroll to top')).toBeNull();
    expect(button('Scroll to bottom')).toBeNull();
  });

  it('keeps the buttons hidden for a small overflow the scrollbar already covers', () => {
    // 15px hidden out of 472px visible: the case of a short window and a minimum-height editor.
    renderRegion({ scrollHeight: 487, clientHeight: 472, scrollTop: 0 });
    resizeAll();
    expect(button('Scroll to bottom')).toBeNull();
  });

  it('observes the frame, header, scroller, content and notice so editor growth shows the buttons', () => {
    const geometry = { scrollHeight: 500, clientHeight: 500, scrollTop: 0 };
    const { frame, scroller, noticeBox } = renderRegion(geometry);
    expect(observers).toHaveLength(1);
    expect(observers[0].targets).toEqual([frame, frame.firstElementChild, scroller, scroller.firstElementChild, noticeBox]);
    geometry.scrollHeight = 4000;
    resizeAll();
    expect(button('Scroll to top')).toHaveAttribute('aria-disabled', 'true');
    expect(button('Scroll to bottom')).not.toHaveAttribute('aria-disabled');
  });

  it('jumps to the bottom and back, tracking which edge is reached', () => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 0 };
    const { scroller, scrollTo } = renderRegion(geometry);
    resizeAll();

    fireEvent.click(button('Scroll to bottom')!);
    expect(scrollTo).toHaveBeenLastCalledWith({ top: 4000, behavior: 'smooth' });
    geometry.scrollTop = 3000;
    fireEvent.scroll(scroller);
    expect(button('Scroll to bottom')).toHaveAttribute('aria-disabled', 'true');
    expect(button('Scroll to top')).not.toHaveAttribute('aria-disabled');

    fireEvent.click(button('Scroll to top')!);
    expect(scrollTo).toHaveBeenLastCalledWith({ top: 0, behavior: 'smooth' });
  });

  it('treats a sub-pixel distance from an edge as being at that edge', () => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 2999.5 };
    renderRegion(geometry);
    resizeAll();
    expect(button('Scroll to bottom')).toHaveAttribute('aria-disabled', 'true');
    expect(button('Scroll to top')).not.toHaveAttribute('aria-disabled');
  });

  it('ignores a jump toward the edge it is already at', () => {
    const { scrollTo } = renderRegion({ scrollHeight: 4000, clientHeight: 1000, scrollTop: 0 });
    resizeAll();
    fireEvent.click(button('Scroll to top')!);
    expect(scrollTo).not.toHaveBeenCalled();
  });

  it('jumps instantly when the user prefers reduced motion', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query === '(prefers-reduced-motion: reduce)' }));
    const { scrollTo } = renderRegion({ scrollHeight: 4000, clientHeight: 1000, scrollTop: 0 });
    resizeAll();
    fireEvent.click(button('Scroll to bottom')!);
    expect(scrollTo).toHaveBeenLastCalledWith({ top: 4000, behavior: 'auto' });
  });

  it('stops observing size changes after unmount', () => {
    renderRegion({ scrollHeight: 500, clientHeight: 500, scrollTop: 0 });
    cleanup();
    expect(observers[0].disconnect).toHaveBeenCalledTimes(1);
  });
});

describe('PreviewScrollRegion focus', () => {
  it.each(['Scroll to top', 'Scroll to bottom'] as const)('keeps a focused "%s" mounted and inert when the overflow goes away', async (name) => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 1500 };
    const { scrollTo } = renderRegion(geometry);
    resizeAll();
    const focused = button(name)!;
    focused.focus();

    geometry.scrollHeight = 1000; geometry.scrollTop = 0; // The viewport grew taller than the content.
    resizeAll();
    expect(document.activeElement).toBe(focused);
    expect(focused).toBeInTheDocument();
    expect(focused).toHaveAttribute('aria-disabled', 'true');
    expect(focused).not.toBeDisabled();
    fireEvent.click(focused);
    expect(scrollTo).not.toHaveBeenCalled();

    // Focus moves only because the user moved it; then the group goes away.
    screen.getByRole('button', { name: 'Header action' }).focus();
    await settleFocus();
    expect(button('Scroll to top')).toBeNull();
    expect(button('Scroll to bottom')).toBeNull();
    expect(document.activeElement).toBe(screen.getByRole('button', { name: 'Header action' }));
  });

  it('keeps the group while focus moves inside it', async () => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 1500 };
    renderRegion(geometry);
    resizeAll();
    button('Scroll to top')!.focus();
    geometry.scrollHeight = 1000; geometry.scrollTop = 0;
    resizeAll();
    button('Scroll to bottom')!.focus();
    await settleFocus();
    expect(document.activeElement).toBe(button('Scroll to bottom'));
    expect(button('Scroll to top')).toBeInTheDocument();
  });

  it('keeps the group when only the window loses focus', async () => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 1500 };
    renderRegion(geometry);
    resizeAll();
    const focused = button('Scroll to bottom')!;
    focused.focus();
    geometry.scrollHeight = 1000; geometry.scrollTop = 0;
    resizeAll();
    // A window blur fires blur on the element but leaves it as the document's active element.
    fireEvent.blur(focused, { relatedTarget: null });
    await settleFocus();
    expect(document.activeElement).toBe(focused);
    expect(button('Scroll to bottom')).toBeInTheDocument();
  });
});

describe('PreviewScrollRegion notice', () => {
  const notice = <p role="alert">save failed</p>;

  it('offers "Show message" only once the notice is scrolled out of view', () => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 0, noticeTop: 16, noticeHeight: 600 };
    const { scroller, scrollTo } = renderRegion(geometry, notice);
    resizeAll();
    expect(button('Show message')).toBeNull();
    geometry.scrollTop = 500; // Part of the notice is still visible.
    fireEvent.scroll(scroller);
    expect(button('Show message')).toBeNull();
    geometry.scrollTop = 616;
    fireEvent.scroll(scroller);
    expect(button('Show message')).not.toHaveAttribute('aria-disabled');
    fireEvent.click(button('Show message')!);
    expect(scrollTo).toHaveBeenLastCalledWith({ top: 0, behavior: 'smooth' });
  });

  it('never offers "Show message" without a notice', () => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 3000, noticeTop: 16, noticeHeight: 0 };
    renderRegion(geometry);
    resizeAll();
    expect(button('Show message')).toBeNull();
  });

  it('keeps a focused "Show message" mounted and inert after the notice comes back into view', async () => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 2000, noticeTop: 16, noticeHeight: 600 };
    const { scroller } = renderRegion(geometry, notice);
    resizeAll();
    const show = button('Show message')!;
    show.focus();
    geometry.scrollTop = 0;
    fireEvent.scroll(scroller);
    expect(document.activeElement).toBe(show);
    expect(show).toHaveAttribute('aria-disabled', 'true');
    screen.getByRole('button', { name: 'Header action' }).focus();
    await settleFocus();
    expect(button('Show message')).toBeNull();
  });

  it('keeps the viewed content in place when a notice appears above it in a browser without scroll anchoring', () => {
    vi.stubGlobal('CSS', { supports: () => false }); // jsdom has no CSS global; this is a browser without overflow-anchor.
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 2000, noticeTop: 0, noticeHeight: 0, followingTop: 0 };
    const view = renderRegion(geometry);
    resizeAll();
    view.rerender(<PreviewScrollRegion header={<button type="button">Header action</button>} notice={notice}><p>file body</p></PreviewScrollRegion>);
    Object.assign(geometry, { noticeHeight: 600, followingTop: 612, scrollHeight: 4612 });
    resizeAll();
    expect(geometry.scrollTop).toBe(2612);
  });

  it('lets a notice push the content down when the region is not scrolled, like native anchoring', () => {
    vi.stubGlobal('CSS', { supports: () => false }); // jsdom has no CSS global; this is a browser without overflow-anchor.
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 0, noticeTop: 0, noticeHeight: 0, followingTop: 0 };
    const view = renderRegion(geometry);
    resizeAll();
    view.rerender(<PreviewScrollRegion header={<button type="button">Header action</button>} notice={notice}><p>file body</p></PreviewScrollRegion>);
    Object.assign(geometry, { noticeHeight: 600, followingTop: 612, scrollHeight: 4612 });
    resizeAll();
    expect(geometry.scrollTop).toBe(0);
  });

  it('leaves anchoring to the browser when it supports it', () => {
    vi.stubGlobal('CSS', { supports: (property: string) => property === 'overflow-anchor' });
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 2000, noticeTop: 0, noticeHeight: 0, followingTop: 0 };
    const view = renderRegion(geometry);
    resizeAll();
    view.rerender(<PreviewScrollRegion header={<button type="button">Header action</button>} notice={notice}><p>file body</p></PreviewScrollRegion>);
    Object.assign(geometry, { noticeHeight: 600, followingTop: 612, scrollHeight: 4612 });
    resizeAll();
    expect(geometry.scrollTop).toBe(2000);
  });
});

describe('PreviewScrollRegion layout', () => {
  it('pins the header while the scroller keeps enough room', () => {
    const { frame, scroller } = renderRegion({ scrollHeight: 4000, clientHeight: 500, scrollTop: 0, frameHeight: 646, headerHeight: 145 });
    resizeAll();
    expect(frame).toHaveAttribute('data-preview-layout', 'pinned');
    expect(frame.className).toContain('overflow-hidden');
    expect(scroller.className).toContain('overflow-y-auto');
  });

  it('scrolls the header with the body when pinning it would squeeze the scroller', () => {
    // A 1280×1024 window at 400% zoom: 230 CSS px of dialog, 277 of header and actions.
    const { frame, scroller } = renderRegion({ scrollHeight: 4000, clientHeight: 0, scrollTop: 0, frameHeight: 228, headerHeight: 277 });
    resizeAll();
    expect(frame).toHaveAttribute('data-preview-layout', 'flow');
    expect(frame.className).toContain('overflow-y-auto');
    expect(scroller.className).not.toContain('overflow-y-auto');
  });

  it('keeps the pinned default before there is any layout', () => {
    const { frame } = renderRegion({ scrollHeight: 0, clientHeight: 0, scrollTop: 0, frameHeight: 0, headerHeight: 0 });
    resizeAll();
    expect(frame).toHaveAttribute('data-preview-layout', 'pinned');
  });
});
