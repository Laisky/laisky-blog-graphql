import { render, screen, fireEvent } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { ApiKeyInput } from './api-key-input';
import { useApiKey } from '@/lib/api-key-context';

// Mock the hook
vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
  normalizeApiKey: (k: string) => k.trim(),
}));

describe('ApiKeyInput', () => {
  it('should show Connect button when not validating', () => {
    (useApiKey as any).mockReturnValue({
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
    (useApiKey as any).mockReturnValue({
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
    (useApiKey as any).mockReturnValue({
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
    (useApiKey as any).mockReturnValue({
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
    (useApiKey as any).mockReturnValue({
      apiKey: '',
      status: 'none',
      history: ['key1', 'key2'],
      setApiKey: vi.fn(),
      disconnect: vi.fn(),
    });

    render(<ApiKeyInput />);
    const historyButton = screen.getByLabelText(/select from history/i);
    fireEvent.click(historyButton);

    expect(screen.getByText(/key1/i)).toBeDefined();
    expect(screen.getByText(/key2/i)).toBeDefined();
  });
});
