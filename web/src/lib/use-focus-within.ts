import { useCallback, useState, type FocusEvent } from 'react';

/** FocusWithinHandlers are spread on the element whose subtree focus is tracked. */
export type FocusWithinHandlers = {
  onFocus: (event: FocusEvent<HTMLElement>) => void;
  onBlur: (event: FocusEvent<HTMLElement>) => void;
};

/**
 * useFocusWithin reports whether focus is inside the element that receives the returned handlers.
 *
 * Use it to keep a conditionally rendered control mounted while it owns focus: unmounting or hiding the
 * focused element lets the browser (or a focus trap) move focus somewhere the user did not choose.
 *
 * Focus counts as gone only once it has settled outside the element, one task after the blur:
 * - Unmounting during the blur would race the browser's own focus move; focus traps (Radix FocusScope)
 *   see a removed node while the document is focused and pull focus back to their container.
 * - When the window loses focus, the document keeps its focused element and focuses it again on return,
 *   so the element still holds focus after that blur and stays mounted.
 */
export function useFocusWithin(): { focusWithin: boolean; focusWithinHandlers: FocusWithinHandlers } {
  const [focusWithin, setFocusWithin] = useState(false);
  const onFocus = useCallback(() => setFocusWithin(true), []);
  const onBlur = useCallback((event: FocusEvent<HTMLElement>) => {
    const container = event.currentTarget;
    if (event.relatedTarget instanceof Node && container.contains(event.relatedTarget)) return;
    window.setTimeout(() => {
      if (!container.contains(container.ownerDocument.activeElement)) setFocusWithin(false);
    }, 0);
  }, []);
  return { focusWithin, focusWithinHandlers: { onFocus, onBlur } };
}
