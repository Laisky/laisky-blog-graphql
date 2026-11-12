import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { useApiKey } from '@/lib/api-key-context';

import * as api from './api';
import { SavedCommands } from './saved-commands';

vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
  normalizeApiKey: (k: string) => k,
}));

vi.mock('./api', () => ({
  listSavedCommands: vi.fn(),
  createSavedCommand: vi.fn(),
  updateSavedCommand: vi.fn(),
  deleteSavedCommand: vi.fn(),
  reorderSavedCommands: vi.fn(),
  getSavedCommandsExpanded: vi.fn(() => true),
  setSavedCommandsExpanded: vi.fn(),
}));

const savedCommand: api.SavedCommand = {
  id: 'cmd-1',
  label: 'Deploy',
  content: 'deploy to staging',
  sort_order: 0,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
} as api.SavedCommand;

function renderSavedCommands(currentContent = 'draft directive') {
  const onSelectCommand = vi.fn();
  render(<SavedCommands currentContent={currentContent} onSelectCommand={onSelectCommand} onSaveCurrentContent={vi.fn()} />);
  return { onSelectCommand };
}

describe('SavedCommands keyboard and accessibility', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'test-key', isToolConsoleLocked: false, status: 'valid' } as ReturnType<typeof useApiKey>);
    vi.mocked(api.listSavedCommands).mockResolvedValue({ commands: [savedCommand] } as api.SavedCommandListResponse);
    vi.mocked(api.createSavedCommand).mockResolvedValue({ ...savedCommand, id: 'cmd-2', label: 'New' } as api.SavedCommand);
  });

  it('does not select a command on the IME-confirming Enter (keyCode 229) in search', async () => {
    const { onSelectCommand } = renderSavedCommands();
    const search = await screen.findByRole('textbox', { name: /search saved commands/i });
    fireEvent.change(search, { target: { value: 'dep' } });

    fireEvent.keyDown(search, { key: 'Enter', keyCode: 229 });
    expect(onSelectCommand).not.toHaveBeenCalled();

    fireEvent.keyDown(search, { key: 'Enter', keyCode: 13 });
    expect(onSelectCommand).toHaveBeenCalledWith('deploy to staging');
  });

  it('does not save a new command on the IME-confirming Enter (keyCode 229) in the label field', async () => {
    renderSavedCommands();
    await screen.findByRole('textbox', { name: /search saved commands/i });
    fireEvent.click(screen.getByRole('button', { name: /save current/i }));

    const label = screen.getByRole('textbox', { name: /label for the saved command/i });
    fireEvent.change(label, { target: { value: '日本語' } });

    fireEvent.keyDown(label, { key: 'Enter', keyCode: 229 });
    expect(api.createSavedCommand).not.toHaveBeenCalled();

    fireEvent.keyDown(label, { key: 'Enter', isComposing: true });
    expect(api.createSavedCommand).not.toHaveBeenCalled();

    fireEvent.keyDown(label, { key: 'Enter', keyCode: 13 });
    expect(api.createSavedCommand).toHaveBeenCalledWith('test-key', '日本語', 'draft directive');
  });

  it('reports the expanded state and names the icon-only close button', async () => {
    renderSavedCommands();
    await screen.findByRole('textbox', { name: /search saved commands/i });

    const toggle = screen.getByRole('button', { name: /saved commands/i, expanded: true });
    const controlled = toggle.getAttribute('aria-controls');
    expect(controlled).toBeTruthy();
    expect(document.getElementById(controlled!)).not.toBeNull();

    fireEvent.click(screen.getByRole('button', { name: /save current/i }));
    expect(screen.getByRole('button', { name: /cancel saving command/i })).toBeInTheDocument();

    fireEvent.click(toggle);
    expect(toggle).toHaveAttribute('aria-expanded', 'false');
  });

  it('announces load errors', async () => {
    vi.mocked(api.listSavedCommands).mockRejectedValue(new Error('backend unavailable'));
    renderSavedCommands();
    expect(await screen.findByRole('alert')).toHaveTextContent('backend unavailable');
  });
});
