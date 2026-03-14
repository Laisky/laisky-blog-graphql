import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { useApiKey } from '@/lib/api-key-context';

import { fetchCallLogs } from '../call-log/api';
import { WebFetchPage } from './page';

vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
}));

vi.mock('@/lib/graphql', () => ({
  fetchGraphQL: vi.fn(),
}));

vi.mock('../call-log/api', () => ({
  fetchCallLogs: vi.fn(),
}));

/**
 * mockApiKeyState configures the API key hook for a specific web_fetch console status.
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

describe('WebFetchPage tool console gating', () => {
  it.each(['none', 'error', 'validating'] as const)('disables web_fetch controls when status is %s', async (status) => {
    vi.mocked(fetchCallLogs).mockResolvedValue({
      data: [],
      meta: { quotes_per_usd: 1 },
      pagination: {
        has_next: false,
        has_prev: false,
        page: 1,
        page_size: 10,
        total_items: 0,
        total_pages: 0,
      },
    });
    mockApiKeyState(status);

    render(<WebFetchPage />);

    expect(screen.getByPlaceholderText(/enter url/i)).toBeDisabled();
    expect(screen.getByRole('button', { name: /run fetch/i })).toBeDisabled();

    await waitFor(() => {
      expect(fetchCallLogs).not.toHaveBeenCalled();
    });
  });

  it('keeps web_fetch interactive when the status is insufficient', async () => {
    vi.mocked(fetchCallLogs).mockResolvedValue({
      data: [],
      meta: { quotes_per_usd: 1 },
      pagination: {
        has_next: false,
        has_prev: false,
        page: 1,
        page_size: 10,
        total_items: 0,
        total_pages: 0,
      },
    });
    mockApiKeyState('insufficient');

    render(<WebFetchPage />);

    const urlInput = screen.getByPlaceholderText(/enter url/i);
    const runButton = screen.getByRole('button', { name: /run fetch/i });

    expect(urlInput).toBeEnabled();
    fireEvent.change(urlInput, { target: { value: 'https://example.com' } });
    expect(runButton).toBeEnabled();

    await waitFor(() => {
      expect(fetchCallLogs).toHaveBeenCalled();
    });
  });
});
