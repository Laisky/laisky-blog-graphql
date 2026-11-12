import type { KeyboardEvent as ReactKeyboardEvent } from 'react';

/** IME_PROCESS_KEY_CODE is the legacy keyCode (VK_PROCESSKEY) browsers report for keys consumed by an IME. */
const IME_PROCESS_KEY_CODE = 229;

/**
 * isImeComposingKeyDown reports whether a keydown belongs to an IME composition,
 * such as the Enter that confirms a Japanese, Chinese or Korean conversion.
 * The keyCode 229 check catches Safari, which can clear isComposing before the
 * confirming Enter keydown reaches script. Call it only after matching Enter,
 * because some virtual keyboards report 229 for ordinary character keys.
 */
export function isImeComposingKeyDown(event: ReactKeyboardEvent): boolean {
  return event.nativeEvent.isComposing || event.nativeEvent.keyCode === IME_PROCESS_KEY_CODE;
}
