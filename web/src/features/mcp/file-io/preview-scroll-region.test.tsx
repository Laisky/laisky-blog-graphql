import '@testing-library/jest-dom/vitest';
import { act, cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { PreviewScrollRegion } from './preview-scroll-region';

type Geometry = { scrollHeight: number; clientHeight: number; scrollTop: number };

// jsdom has no layout, so the scroller's geometry is supplied per test.
function renderRegion(geometry: Geometry) {
  const view = render(<PreviewScrollRegion><p>file body</p></PreviewScrollRegion>);
  const scroller = view.container.querySelector<HTMLDivElement>('[data-preview-scroll]')!;
  for (const key of ['scrollHeight', 'clientHeight'] as const) {
    Object.defineProperty(scroller, key, { configurable: true, get: () => geometry[key] });
  }
  Object.defineProperty(scroller, 'scrollTop', { configurable: true, get: () => geometry.scrollTop, set: (value: number) => { geometry.scrollTop = value; } });
  const scrollTo = vi.fn();
  scroller.scrollTo = scrollTo as unknown as typeof scroller.scrollTo;
  return { scroller, scrollTo };
}
const jumpButton = (name: 'Scroll to top' | 'Scroll to bottom') => screen.queryByRole('button', { name });

let observers: { callback: ResizeObserverCallback; targets: Element[]; disconnect: ReturnType<typeof vi.fn> }[] = [];
function resizeAll() { act(() => { for (const observer of observers) observer.callback([], observer as unknown as ResizeObserver); }); }

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
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

describe('PreviewScrollRegion', () => {
  it('renders its children without jump buttons while the content fits', () => {
    renderRegion({ scrollHeight: 500, clientHeight: 500, scrollTop: 0 });
    resizeAll();
    expect(screen.getByText('file body')).toBeInTheDocument();
    expect(jumpButton('Scroll to top')).toBeNull();
    expect(jumpButton('Scroll to bottom')).toBeNull();
  });

  it('keeps the buttons hidden for a small overflow the scrollbar already covers', () => {
    // 15px hidden out of 472px visible: the case of a short window and a minimum-height editor.
    renderRegion({ scrollHeight: 487, clientHeight: 472, scrollTop: 0 });
    resizeAll();
    expect(jumpButton('Scroll to bottom')).toBeNull();
  });

  it('observes both the scroller and its content so editor growth shows the buttons', () => {
    const geometry = { scrollHeight: 500, clientHeight: 500, scrollTop: 0 };
    const { scroller } = renderRegion(geometry);
    expect(observers).toHaveLength(1);
    expect(observers[0].targets).toEqual([scroller, scroller.firstElementChild]);
    geometry.scrollHeight = 4000;
    resizeAll();
    expect(jumpButton('Scroll to top')).toHaveAttribute('aria-disabled', 'true');
    expect(jumpButton('Scroll to bottom')).toHaveAttribute('aria-disabled', 'false');
  });

  it('jumps to the bottom and back, tracking which edge is reached', () => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 0 };
    const { scroller, scrollTo } = renderRegion(geometry);
    resizeAll();

    fireEvent.click(jumpButton('Scroll to bottom')!);
    expect(scrollTo).toHaveBeenLastCalledWith({ top: 4000, behavior: 'smooth' });
    geometry.scrollTop = 3000;
    fireEvent.scroll(scroller);
    expect(jumpButton('Scroll to bottom')).toHaveAttribute('aria-disabled', 'true');
    expect(jumpButton('Scroll to top')).toHaveAttribute('aria-disabled', 'false');

    fireEvent.click(jumpButton('Scroll to top')!);
    expect(scrollTo).toHaveBeenLastCalledWith({ top: 0, behavior: 'smooth' });
  });

  it('treats a sub-pixel distance from an edge as being at that edge', () => {
    const geometry = { scrollHeight: 4000, clientHeight: 1000, scrollTop: 2999.5 };
    renderRegion(geometry);
    resizeAll();
    expect(jumpButton('Scroll to bottom')).toHaveAttribute('aria-disabled', 'true');
    expect(jumpButton('Scroll to top')).toHaveAttribute('aria-disabled', 'false');
  });

  it('ignores a jump toward the edge it is already at', () => {
    const { scrollTo } = renderRegion({ scrollHeight: 4000, clientHeight: 1000, scrollTop: 0 });
    resizeAll();
    fireEvent.click(jumpButton('Scroll to top')!);
    expect(scrollTo).not.toHaveBeenCalled();
  });

  it('jumps instantly when the user prefers reduced motion', () => {
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: query === '(prefers-reduced-motion: reduce)' }));
    const { scrollTo } = renderRegion({ scrollHeight: 4000, clientHeight: 1000, scrollTop: 0 });
    resizeAll();
    fireEvent.click(jumpButton('Scroll to bottom')!);
    expect(scrollTo).toHaveBeenLastCalledWith({ top: 4000, behavior: 'auto' });
  });

  it('stops observing size changes after unmount', () => {
    renderRegion({ scrollHeight: 500, clientHeight: 500, scrollTop: 0 });
    cleanup();
    expect(observers[0].disconnect).toHaveBeenCalledTimes(1);
  });
});
