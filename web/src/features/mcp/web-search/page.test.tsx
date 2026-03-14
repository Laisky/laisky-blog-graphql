import '@testing-library/jest-dom/vitest';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';

import { useApiKey } from '@/lib/api-key-context';

import { fetchCallLogs } from '../call-log/api';
import { WebSearchPage } from './page';

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
 * mockApiKeyState configures the API key hook for a specific web_search console status.
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

describe('WebSearchPage tool console gating', () => {
  it.each(['none', 'error', 'validating'] as const)('disables web_search controls when status is %s', async (status) => {
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

    render(<WebSearchPage />);

    expect(screen.getByPlaceholderText(/enter search query/i)).toBeDisabled();
    expect(screen.getByRole('button', { name: /run search/i })).toBeDisabled();

    await waitFor(() => {
      expect(fetchCallLogs).not.toHaveBeenCalled();
    });
  });

  it('keeps web_search interactive when the status is insufficient', async () => {
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

    render(<WebSearchPage />);

    const queryInput = screen.getByPlaceholderText(/enter search query/i);
    const runButton = screen.getByRole('button', { name: /run search/i });

    expect(queryInput).toBeEnabled();
    fireEvent.change(queryInput, { target: { value: 'latest mcp docs' } });
    expect(runButton).toBeEnabled();

    await waitFor(() => {
      expect(fetchCallLogs).toHaveBeenCalled();
    });
  });
});
