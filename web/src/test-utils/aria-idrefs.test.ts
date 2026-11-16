import { afterEach, describe, expect, it } from 'vitest';

import { findDanglingIdRefs } from './aria-idrefs';

describe('findDanglingIdRefs', () => {
  afterEach(() => {
    document.body.innerHTML = '';
  });

  it('accepts references to ids in the document, including hidden elements', () => {
    document.body.innerHTML = `
      <button aria-expanded="false" aria-controls="panel">Toggle</button>
      <div id="panel" hidden>Content</div>
      <h2 id="title">Title</h2><form aria-labelledby="title" aria-describedby="hint panel"></form>
      <p id="hint">Hint</p><label for="field">Field</label><input id="field">`;
    expect(findDanglingIdRefs()).toEqual([]);
  });

  it('reports each missing id of a multi-id reference and a dangling label', () => {
    document.body.innerHTML = `
      <button aria-controls="gone" aria-label="Toggle">x</button>
      <p id="hint"></p><input aria-describedby="hint missing">
      <label for="nowhere">Field</label>`;
    expect(findDanglingIdRefs()).toEqual([
      '<button> "Toggle" aria-controls="gone"',
      '<input> "" aria-describedby="missing"',
      '<label> "Field" for="nowhere"',
    ]);
  });
});
