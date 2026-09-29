# Editable FileIO project combobox references

Reviewed 2026-09-29, for the project picker only.

- WAI-ARIA Authoring Practices, Combobox Pattern:
  <https://www.w3.org/WAI/ARIA/apg/patterns/combobox/>.
  Use an editable, manual-selection combobox: unlisted typed values remain usable.
  Keep DOM focus in the textbox, expose the highlighted list option using
  `aria-activedescendant`, and support Arrow keys, Enter and Escape without
  overriding normal text-editing keys or selecting during IME composition.
- React, Preserving and Resetting State:
  <https://react.dev/learn/preserving-and-resetting-state>.
  Component keys reset state for different identities. Apply the identity boundary
  to the entire project/workspace state, not only to the suggestions. Cancel async
  work as well; remounting does not undo a server mutation already submitted.

These interaction/state-management references are not an authorization mechanism.
The FileIO service must independently enforce the caller's API-key namespace.
