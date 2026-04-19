import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { AttachmentStrip, type ComposeAttachment } from './AttachmentStrip';

function makeAttachment(overrides: Partial<ComposeAttachment> = {}): ComposeAttachment {
  return {
    clientId: 'a1',
    kind: 'file',
    label: 'a.png',
    previewUrl: 'blob:mock',
    status: 'ready',
    ...overrides,
  };
}

describe('AttachmentStrip', () => {
  it('renders nothing when empty', () => {
    const { container } = render(<AttachmentStrip attachments={[]} onRemove={() => {}} />);
    expect(container.firstChild).toBeNull();
  });

  it('calls onRemove when the × button is clicked', () => {
    const onRemove = vi.fn();
    render(<AttachmentStrip attachments={[makeAttachment()]} onRemove={onRemove} />);
    fireEvent.click(screen.getByRole('button', { name: /remove attachment a\.png/i }));
    expect(onRemove).toHaveBeenCalledWith('a1');
  });

  it('calls onRemove when Delete is pressed on the focused thumbnail', () => {
    const onRemove = vi.fn();
    render(<AttachmentStrip attachments={[makeAttachment()]} onRemove={onRemove} />);
    const tile = screen.getByRole('group', { name: /File attachment a\.png/i });
    tile.focus();
    fireEvent.keyDown(tile, { key: 'Delete' });
    expect(onRemove).toHaveBeenCalledWith('a1');
  });

  it('shows a red error badge for error attachments', () => {
    render(
      <AttachmentStrip
        attachments={[makeAttachment({ status: 'error', errorCode: 'unsupported_mime', errorMessage: 'bad MIME' })]}
        onRemove={() => {}}
      />
    );
    expect(screen.getByText('!')).toBeInTheDocument();
  });
});
