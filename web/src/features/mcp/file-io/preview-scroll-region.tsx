import { ArrowDownToLine, ArrowUpToLine } from 'lucide-react';
import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react';

import { Button } from '@/components/ui/button';

type ScrollEdges = { longContent: boolean; atTop: boolean; atBottom: boolean };

// Fractional scroll offsets (page zoom, HiDPI) must not leave the region "almost" at an edge.
const EDGE_TOLERANCE_PX = 1;
// Jump buttons appear only once at least this share of a region's height is hidden; a few
// overflowing pixels are reachable with the scrollbar and would only add noise.
const JUMP_MIN_HIDDEN_RATIO = 0.25;
const fitsEdges: ScrollEdges = { longContent: false, atTop: true, atBottom: true };

function readEdges(element: HTMLElement): ScrollEdges {
  const maxScrollTop = element.scrollHeight - element.clientHeight;
  return {
    longContent: maxScrollTop > Math.max(EDGE_TOLERANCE_PX, element.clientHeight * JUMP_MIN_HIDDEN_RATIO),
    atTop: element.scrollTop <= EDGE_TOLERANCE_PX,
    atBottom: element.scrollTop >= maxScrollTop - EDGE_TOLERANCE_PX,
  };
}

function jumpBehavior(): ScrollBehavior {
  const reduceMotion = typeof window.matchMedia === 'function' && window.matchMedia('(prefers-reduced-motion: reduce)').matches;
  return reduceMotion ? 'auto' : 'smooth';
}

/**
 * PreviewScrollRegion is the only vertical scroller of the file preview body. Its content column is
 * at least as tall as the region, so a `grow` child fills the space left over, and a child that grows
 * past the region scrolls the whole body. When the content is clearly longer than the region, floating
 * buttons jump to the top or bottom; the one already at its edge stays focusable but is aria-disabled.
 */
export function PreviewScrollRegion({ children }: { children: ReactNode }) {
  const scrollRef = useRef<HTMLDivElement>(null);
  const contentRef = useRef<HTMLDivElement>(null);
  const [edges, setEdges] = useState<ScrollEdges>(fitsEdges);

  const update = useCallback(() => {
    const element = scrollRef.current;
    if (!element) return;
    const next = readEdges(element);
    setEdges((prev) => prev.longContent === next.longContent && prev.atTop === next.atTop && prev.atBottom === next.atBottom ? prev : next);
  }, []);

  useEffect(() => {
    update();
    if (typeof ResizeObserver === 'undefined') return;
    // The region resizes with the viewport; the content resizes as the auto-sized editor grows.
    const observer = new ResizeObserver(update);
    if (scrollRef.current) observer.observe(scrollRef.current);
    if (contentRef.current) observer.observe(contentRef.current);
    return () => observer.disconnect();
  }, [update]);

  function jump(target: 'top' | 'bottom') {
    const element = scrollRef.current;
    if (!element || (target === 'top' ? edges.atTop : edges.atBottom)) return;
    element.scrollTo({ top: target === 'top' ? 0 : element.scrollHeight, behavior: jumpBehavior() });
  }

  return <div className="relative min-h-0 flex-1">
    <div ref={scrollRef} onScroll={update} data-preview-scroll="" className="h-full overflow-y-auto">
      <div ref={contentRef} className="flex min-h-full flex-col gap-3 px-6 py-4">{children}</div>
    </div>
    {edges.longContent && <div className="pointer-events-none absolute bottom-4 right-8 flex flex-col gap-2">
      <Button type="button" variant="outline" size="icon" aria-label="Scroll to top" title="Scroll to top" aria-disabled={edges.atTop}
        className="pointer-events-auto rounded-full shadow-md aria-disabled:cursor-default aria-disabled:opacity-40" onClick={() => jump('top')}>
        <ArrowUpToLine className="h-4 w-4" />
      </Button>
      <Button type="button" variant="outline" size="icon" aria-label="Scroll to bottom" title="Scroll to bottom" aria-disabled={edges.atBottom}
        className="pointer-events-auto rounded-full shadow-md aria-disabled:cursor-default aria-disabled:opacity-40" onClick={() => jump('bottom')}>
        <ArrowDownToLine className="h-4 w-4" />
      </Button>
    </div>}
  </div>;
}
