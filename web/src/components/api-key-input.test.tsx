import { useApiKey } from '@/lib/api-key-context';
import { fireEvent, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiKeyInput } from './api-key-input';

// Mock the hook
vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
  normalizeApiKey: (k: string) => k.trim(),
}));

describe('ApiKeyInput', () => {
  it('should show Connect button when not validating', () => {
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: '',
      status: 'none',
      history: [],
      setApiKey: vi.fn(),
      disconnect: vi.fn(),
    });

    render(<ApiKeyInput />);
    expect(screen.getByRole('button', { name: /connect/i })).toBeDefined();
    expect(screen.queryByTestId('loader')).toBeNull();
  });

  it('should show loading spinner and disable button when validating', () => {
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: '',
      status: 'validating',
      history: [],
      setApiKey: vi.fn(),
      disconnect: vi.fn(),
    });

    render(<ApiKeyInput />);
    const button = screen.getByRole('button', { name: /connect/i });
    expect(button.hasAttribute('disabled')).toBe(true);
    // Loader icon usually has a class with animate-spin
    expect(document.querySelector('.animate-spin')).toBeDefined();
  });

  it('should show Re-validate when same key is entered', () => {
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: 'test-key',
      status: 'valid',
      history: [],
      setApiKey: vi.fn(),
      disconnect: vi.fn(),
    });

    render(<ApiKeyInput />);
    expect(screen.getByRole('button', { name: /re-validate/i })).toBeDefined();
  });

  it('should call setApiKey on form submit', () => {
    const setApiKey = vi.fn();
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: '',
      status: 'none',
      history: [],
      setApiKey: setApiKey,
      disconnect: vi.fn(),
    });

    const { container } = render(<ApiKeyInput />);
    const input = screen.getByPlaceholderText(/enter your api key/i);
    fireEvent.change(input, { target: { value: '  new-key  ' } });

    const form = container.querySelector('form');
    if (form) fireEvent.submit(form);

    expect(setApiKey).toHaveBeenCalledWith('new-key');
  });

  it('should show history dropdown when available', () => {
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: '',
      status: 'none',
      history: ['key1', 'key2'],
      setApiKey: vi.fn(),
      disconnect: vi.fn(),
    });

    render(<ApiKeyInput />);
    const historyButton = screen.getByLabelText(/select from history/i);
    fireEvent.click(historyButton);

    expect(screen.getAllByText(/key1/i).length).toBeGreaterThan(0);
    expect(screen.getAllByText(/key2/i).length).toBeGreaterThan(0);
  });

  it('labels the API key field programmatically and keeps password managers out', () => {
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: '',
      status: 'none',
      history: [],
      setApiKey: vi.fn(),
      disconnect: vi.fn(),
    });

    render(<ApiKeyInput />);
    const input = screen.getByLabelText('API key');
    expect(input.tagName).toBe('INPUT');
    expect(input.getAttribute('id')).toBeTruthy();
    expect(input.getAttribute('name')).toBe('apiKey');
    // The key has its own history UI; it is not a password-manager credential.
    expect(input.getAttribute('autocomplete')).toBe('off');
  });

  it('keeps a fixed toggle name and exposes the reveal state through aria-pressed', () => {
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: '',
      status: 'none',
      history: [],
      setApiKey: vi.fn(),
      disconnect: vi.fn(),
    });

    render(<ApiKeyInput />);
    const toggle = screen.getByRole('button', { name: 'Show API key' });
    expect(toggle.getAttribute('aria-pressed')).toBe('false');
    expect(screen.getByLabelText('API key').getAttribute('type')).toBe('password');

    fireEvent.click(toggle);

    // The name stays stable; only the pressed state changes.
    const pressed = screen.getByRole('button', { name: 'Show API key' });
    expect(pressed).toBe(toggle);
    expect(pressed.getAttribute('aria-pressed')).toBe('true');
    expect(screen.queryByRole('button', { name: /hide api key/i })).toBeNull();
    expect(screen.getByLabelText('API key').getAttribute('type')).toBe('text');
  });

  it('selects a history entry through a real button that keyboard users can reach', () => {
    const setApiKey = vi.fn();
    const removeFromHistory = vi.fn();
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: '',
      status: 'none',
      history: ['sk-history-key-0001'],
      keyEntries: [{ key: 'sk-history-key-0001', alias: 'Work key' }],
      setApiKey,
      removeFromHistory,
      disconnect: vi.fn(),
    });

    render(<ApiKeyInput />);
    fireEvent.click(screen.getByRole('button', { name: /select from history/i }));

    const selectButton = screen.getByRole('button', { name: /^work key/i });
    expect(selectButton.tagName).toBe('BUTTON');
    expect(selectButton.getAttribute('type')).toBe('button');

    // The delete control is a sibling, never nested inside the select button.
    // Its name carries the entry alias so several delete buttons stay distinguishable.
    const deleteButton = screen.getByRole('button', { name: 'Remove Work key from history' });
    expect(selectButton.contains(deleteButton)).toBe(false);

    fireEvent.click(deleteButton);
    expect(removeFromHistory).toHaveBeenCalledWith('sk-history-key-0001');
    expect(setApiKey).not.toHaveBeenCalled();

    fireEvent.click(selectButton);
    expect(setApiKey).toHaveBeenCalledWith('sk-history-key-0001');
  });
});
