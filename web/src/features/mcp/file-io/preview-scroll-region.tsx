import { ArrowDownToLine, ArrowUp, ArrowUpToLine } from 'lucide-react';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';

import { Button } from '@/components/ui/button';
import { useFocusWithin } from '@/lib/use-focus-within';
import { cn } from '@/lib/utils';

type ScrollState = { pinned: boolean; longContent: boolean; atTop: boolean; atBottom: boolean; noticeAbove: boolean };

// Fractional scroll offsets (page zoom, HiDPI) must not leave the region "almost" at an edge.
const EDGE_TOLERANCE_PX = 1;
// Jump buttons appear only once at least this share of a region's height is hidden; a few
// overflowing pixels are reachable with the scrollbar and would only add noise.
const JUMP_MIN_HIDDEN_RATIO = 0.25;
// The header stays pinned only while this much height (CSS px, so page zoom is included) is left for
// the scroll region. Below it (a short window, 200%+ zoom, a long path on a narrow screen) pinned
// chrome would squeeze the editor to nothing, so the header scrolls with the body instead.
const MIN_PINNED_REGION_PX = 192;
const initialState: ScrollState = { pinned: true, longContent: false, atTop: true, atBottom: true, noticeAbove: false };

function sameState(a: ScrollState, b: ScrollState) {
  return a.pinned === b.pinned && a.longContent === b.longContent && a.atTop === b.atTop && a.atBottom === b.atBottom && a.noticeAbove === b.noticeAbove;
}

function jumpBehavior(): ScrollBehavior {
  const reduceMotion = typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  return reduceMotion ? 'auto' : 'smooth';
}

function supportsScrollAnchoring() {
  return typeof CSS !== 'undefined' && typeof CSS.supports === 'function' && CSS.supports('overflow-anchor', 'auto');
}

/**
 * PreviewScrollRegion lays out the file preview body: a `header` (title, actions) above one vertical
 * scroller that holds `notice` and then `children`. There is never a second scroller.
 *
 * - The header is pinned while the frame leaves the scroller at least MIN_PINNED_REGION_PX; otherwise
 *   the whole frame scrolls as one, header included, so the editor stays reachable at any zoom.
 * - The content column is at least as tall as the scroller, so a `grow` child fills the space left over,
 *   and a child that grows past the scroller scrolls the whole body.
 * - `notice` is feedback of any length (errors, statuses, warnings). It opens the scrolled content, so a
 *   long message scrolls away like the rest of the body instead of taking the editor's room. Browsers
 *   without CSS scroll anchoring get the same effect from JS: a notice that changes size above a scrolled
 *   viewport does not move what the user is looking at. When the notice is out of view, a floating
 *   "Show message" button scrolls back to it.
 * - When the content is clearly longer than the scroller, floating buttons jump to the top or bottom.
 *
 * Automatic updates never move focus or the user's place: the region scrolls on its own only to keep the
 * viewed content still (the anchoring emulation above), and a floating control that owns focus stays
 * mounted, inert through `aria-disabled`, until the user moves focus away.
 */
export function PreviewScrollRegion({ header, notice, children }: { header?: ReactNode; notice?: ReactNode; children: ReactNode }) {
  const frameRef = useRef<HTMLDivElement>(null);
  const headerRef = useRef<HTMLDivElement>(null);
  const scrollRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const noticeRef = useRef<HTMLDivElement>(null);
  const anchor = useRef<{ node: Element | null; top: number }>({ node: null, top: 0 });
  const [state, setState] = useState<ScrollState>(initialState);
  const jumps = useFocusWithin();
  const noticeJump = useFocusWithin();

  const update = useCallback(() => {
    const frame = frameRef.current, head = headerRef.current, element = scrollRef.current, note = noticeRef.current;
    if (!frame || !head || !element || !note) return;
    const height = note.childElementCount > 0 ? note.offsetHeight : 0;
    // Emulate scroll anchoring where the browser lacks it (Safari before 27): when the notice changes while the
    // viewport is past it, keep the viewport on the content that follows. Like native anchoring, an unscrolled
    // region lets a new notice push the content down, where the user sees it.
    const following = note.nextElementSibling instanceof HTMLElement ? note.nextElementSibling : null;
    const followingTop = following?.offsetTop ?? 0;
    const previous = anchor.current;
    if (following && previous.node === following && followingTop !== previous.top && element.scrollTop > 0
      && element.scrollTop >= previous.top && !supportsScrollAnchoring()) {
      element.scrollTop += followingTop - previous.top;
    }
    anchor.current = { node: following, top: followingTop };
    const maxScrollTop = element.scrollHeight - element.clientHeight;
    // The scroller is the notice's offset parent, so this is the notice's bottom in content coordinates.
    const noticeBottom = height > 0 ? note.offsetTop + height : 0;
    const next: ScrollState = {
      // Without layout (no height at all) keep the default instead of guessing.
      pinned: frame.clientHeight > 0 ? frame.clientHeight - head.offsetHeight >= MIN_PINNED_REGION_PX : true,
      longContent: maxScrollTop > Math.max(EDGE_TOLERANCE_PX, element.clientHeight * JUMP_MIN_HIDDEN_RATIO),
      atTop: element.scrollTop <= EDGE_TOLERANCE_PX,
      atBottom: element.scrollTop >= maxScrollTop - EDGE_TOLERANCE_PX,
      noticeAbove: noticeBottom > 0 && element.scrollTop >= noticeBottom,
    };
    setState((prev) => sameState(prev, next) ? prev : next);
  }, []);

  useEffect(() => {
    update();
    if (typeof ResizeObserver === 'undefined') return;
    // The frame and scroller resize with the viewport, the header as its actions wrap, the content as the
    // auto-sized editor grows, and the notice as messages come and go (sometimes without changing the content).
    const observer = new ResizeObserver(update);
    for (const target of [frameRef.current, headerRef.current, scrollRef.current, contentRef.current, noticeRef.current]) if (target) observer.observe(target);
    return () => observer.disconnect();
  }, [update]);

  function scrollToOffset(top: number) {
    scrollRef.current?.scrollTo({ top, behavior: jumpBehavior() });
  }
  function jump(target: 'top' | 'bottom') {
    const element = scrollRef.current;
    if (!element || (target === 'top' ? state.atTop : state.atBottom)) return;
    scrollToOffset(target === 'top' ? 0 : element.scrollHeight);
  }

  const showJumps = state.longContent || jumps.focusWithin;
  const showNoticeJump = state.noticeAbove || noticeJump.focusWithin;
  return <div ref={frameRef} data-preview-layout={state.pinned ? 'pinned' : 'flow'}
    className={cn('flex min-h-0 flex-1 flex-col', state.pinned ? 'overflow-hidden' : 'overflow-y-auto')}>
    <div ref={headerRef} className="flex-none">{header}</div>
    <div className={cn('relative', state.pinned ? 'min-h-0 flex-1' : 'flex-none')}>
      <div ref={scrollRef} onScroll={update} data-preview-scroll="" className={cn('relative', state.pinned && 'h-full overflow-y-auto')}>
        <div ref={contentRef} className="flex min-h-full flex-col gap-3 px-6 py-4">
          <div ref={noticeRef} data-preview-notice="" className="space-y-2 empty:hidden">{notice}</div>
          {children}
        </div>
      </div>
      {showNoticeJump && <div className="pointer-events-none absolute right-8 top-3" {...noticeJump.focusWithinHandlers}>
        <Button type="button" variant="outline" size="sm" focusableWhenDisabled disabled={!state.noticeAbove}
          className="pointer-events-auto rounded-full shadow-md aria-disabled:cursor-default aria-disabled:opacity-40" onClick={() => scrollToOffset(0)}>
          <ArrowUp className="mr-1 h-4 w-4" />Show message
        </Button>
      </div>}
      {showJumps && <div className="pointer-events-none absolute bottom-4 right-8 flex flex-col gap-2" {...jumps.focusWithinHandlers}>
        <Button type="button" variant="outline" size="icon" aria-label="Scroll to top" title="Scroll to top" focusableWhenDisabled disabled={state.atTop}
          className="pointer-events-auto rounded-full shadow-md aria-disabled:cursor-default aria-disabled:opacity-40" onClick={() => jump('top')}>
          <ArrowUpToLine className="h-4 w-4" />
        </Button>
        <Button type="button" variant="outline" size="icon" aria-label="Scroll to bottom" title="Scroll to bottom" focusableWhenDisabled disabled={state.atBottom}
          className="pointer-events-auto rounded-full shadow-md aria-disabled:cursor-default aria-disabled:opacity-40" onClick={() => jump('bottom')}>
          <ArrowDownToLine className="h-4 w-4" />
        </Button>
      </div>}
    </div>
  </div>;
}
