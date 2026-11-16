/**
 * ID_REFERENCE_ATTRIBUTES are the ARIA and HTML attributes whose value is a list of element
 * ids in the same document. A reference to an id that is not in the document is an
 * authoring error ("The aria-controls attribute must point to an element in the same
 * document", W3C Nu HTML Checker) that assistive technology silently ignores.
 */
const ID_REFERENCE_ATTRIBUTES = [
  'aria-activedescendant',
  'aria-controls',
  'aria-describedby',
  'aria-details',
  'aria-errormessage',
  'aria-flowto',
  'aria-labelledby',
  'aria-owns',
] as const;

/** findDanglingIdRefs lists every ID reference under root that names an id missing from root's document. */
export function findDanglingIdRefs(root: Document | Element = document): string[] {
  const doc = root instanceof Document ? root : root.ownerDocument;
  const dangling: string[] = [];
  const describe = (element: Element) => {
    const id = element.id ? `#${element.id}` : '';
    const name = element.getAttribute('aria-label') ?? element.textContent?.trim().slice(0, 40) ?? '';
    return `<${element.tagName.toLowerCase()}${id}> "${name}"`;
  };
  for (const attribute of ID_REFERENCE_ATTRIBUTES) {
    for (const element of root.querySelectorAll(`[${attribute}]`)) {
      for (const id of (element.getAttribute(attribute) ?? '').split(/\s+/).filter(Boolean)) {
        if (!doc.getElementById(id)) dangling.push(`${describe(element)} ${attribute}="${id}"`);
      }
    }
  }
  for (const label of root.querySelectorAll('label[for]')) {
    const id = label.getAttribute('for') ?? '';
    if (id && !doc.getElementById(id)) dangling.push(`${describe(label)} for="${id}"`);
  }
  return dangling;
}
