import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { useState } from 'react';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';

import { saveTaskIdHistory, TaskIdSelector } from './task-id-selector';

/** Harness owns the controlled value the selector edits. */
function Harness() {
  const [value, setValue] = useState('');
  return <TaskIdSelector value={value} onChange={setValue} />;
}

describe('TaskIdSelector keyboard handling', () => {
  beforeEach(() => {
    localStorage.clear();
    saveTaskIdHistory([{ id: 'entry-1', value: 'graphql', pinned: false, usedAt: 1 }]);
  });

  afterEach(() => {
    cleanup();
    localStorage.clear();
  });

  it('keeps the default of the IME-confirming Enter (keyCode 229) and does not pick the highlighted entry', () => {
    render(<Harness />);
    const input = screen.getByPlaceholderText<HTMLInputElement>('Optional task identifier');
    fireEvent.focus(input);
    fireEvent.keyDown(input, { key: 'ArrowDown' });

    // fireEvent returns false when a handler called preventDefault(); the IME needs the default to commit its text.
    expect(fireEvent.keyDown(input, { key: 'Enter', keyCode: 229 })).toBe(true);
    expect(input.value).toBe('');

    expect(fireEvent.keyDown(input, { key: 'Enter', keyCode: 13 })).toBe(false);
    expect(input.value).toBe('graphql');
  });

  it('ignores Enter while a composition is active', () => {
    render(<Harness />);
    const input = screen.getByPlaceholderText<HTMLInputElement>('Optional task identifier');
    fireEvent.focus(input);
    fireEvent.keyDown(input, { key: 'ArrowDown' });

    expect(fireEvent.keyDown(input, { key: 'Enter', isComposing: true })).toBe(true);
    expect(input.value).toBe('');
  });
});
