import '@testing-library/jest-dom/vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { useApiKey } from '@/lib/api-key-context';

import { fetchCallLogs } from './api';
import { CallLogPage } from './page';

vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
}));

vi.mock('./api', () => ({
  fetchCallLogs: vi.fn(),
}));

/**
 * mockApiKeyState configures the API key hook for the call-log page.
 * It accepts the API key status and returns nothing.
 */
function mockApiKeyState(status: 'none' | 'error' | 'validating' | 'insufficient') {
  vi.mocked(useApiKey).mockReturnValue({
    apiKey: status === 'none' ? '' : 'saved-key',
    disconnect: vi.fn(),
    history: [],
    isToolConsoleLocked: status === 'none' || status === 'error' || status === 'validating',
    keyEntries: [],
    remainQuota: null,
    removeFromHistory: vi.fn(),
    sessionId: 0,
    setAliasForKey: vi.fn(),
    setApiKey: vi.fn(),
    status,
    switchApiKey: vi.fn(),
    validateApiKey: vi.fn(),
  });
}

describe('CallLogPage tool console gating', () => {
  it.each(['none', 'error', 'validating'] as const)('disables call-log filters when status is %s', async (status) => {
    vi.mocked(fetchCallLogs).mockResolvedValue({
      data: [],
      meta: { quotes_per_usd: 1 },
      pagination: {
        has_next: false,
        has_prev: false,
        page: 1,
        page_size: 20,
        total_items: 0,
        total_pages: 0,
      },
    });
    mockApiKeyState(status);

    render(<CallLogPage />);

    expect(screen.getByPlaceholderText(/first 7 characters/i)).toBeDisabled();
    screen.getAllByRole('combobox').forEach((element) => {
      expect(element).toBeDisabled();
    });
    expect(screen.getByRole('button', { name: /previous/i })).toBeDisabled();

    await waitFor(() => {
      expect(fetchCallLogs).not.toHaveBeenCalled();
    });
  });

  it('keeps call-log filters enabled when the status is insufficient', async () => {
    vi.mocked(fetchCallLogs).mockResolvedValue({
      data: [],
      meta: { quotes_per_usd: 1 },
      pagination: {
        has_next: false,
        has_prev: false,
        page: 1,
        page_size: 20,
        total_items: 0,
        total_pages: 0,
      },
    });
    mockApiKeyState('insufficient');

    render(<CallLogPage />);

    expect(screen.getByPlaceholderText(/first 7 characters/i)).toBeEnabled();
    screen.getAllByRole('combobox').forEach((element) => {
      expect(element).toBeEnabled();
    });

    await waitFor(() => {
      expect(fetchCallLogs).toHaveBeenCalled();
    });
  });
});
