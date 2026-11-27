import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';

import { Button } from './button';

afterEach(cleanup);

describe('Button focusableWhenDisabled', () => {
  it('renders aria-disabled instead of the native attribute and ignores activation', () => {
    const onClick = vi.fn();
    render(<form onSubmit={(event) => { event.preventDefault(); onClick('submit'); }}><Button type="submit" focusableWhenDisabled disabled onClick={onClick}>Save</Button></form>);
    const button = screen.getByRole('button', { name: 'Save' });
    expect(button).not.toBeDisabled();
    expect(button).toHaveAttribute('aria-disabled', 'true');
    fireEvent.click(button);
    expect(onClick).not.toHaveBeenCalled();
  });

  it('keeps a focused button focused when it becomes unavailable', () => {
    const { rerender } = render(<Button focusableWhenDisabled disabled={false}>Save</Button>);
    const button = screen.getByRole('button', { name: 'Save' });
    button.focus();
    rerender(<Button focusableWhenDisabled disabled>Save</Button>);
    expect(document.activeElement).toBe(button);
    expect(button).toHaveAttribute('aria-disabled', 'true');
    rerender(<Button focusableWhenDisabled disabled={false}>Save</Button>);
    expect(button).not.toHaveAttribute('aria-disabled');
  });

  it('calls the handler while available', () => {
    const onClick = vi.fn();
    render(<Button focusableWhenDisabled onClick={onClick}>Save</Button>);
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(onClick).toHaveBeenCalledTimes(1);
  });

  it('keeps the native disabled attribute by default', () => {
    render(<Button disabled>Save</Button>);
    expect(screen.getByRole('button', { name: 'Save' })).toBeDisabled();
  });
});
