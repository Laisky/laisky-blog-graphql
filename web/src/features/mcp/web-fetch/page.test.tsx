import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useApiKey } from '@/lib/api-key-context';
import { fetchGraphQL } from '@/lib/graphql';

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

/** emptyLogs is the call-log response shape the page expects when no runs exist. */
function emptyLogs() {
  return {
    data: [],
    meta: { quotes_per_usd: 1 },
    pagination: { has_next: false, has_prev: false, page: 1, page_size: 10, total_items: 0, total_pages: 0 },
  };
}

describe('WebFetchPage output format selection', () => {
  beforeEach(() => {
    vi.mocked(fetchCallLogs).mockResolvedValue(emptyLogs());
    mockApiKeyState('insufficient');
  });

  afterEach(() => {
    vi.mocked(fetchGraphQL).mockReset();
    cleanup();
  });

  it('requests Markdown by default and reports the format it received', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({
      WebFetch: { url: 'https://example.com', created_at: '2026-01-01T00:00:00Z', content: '# Title', output_markdown: true },
    });

    render(<WebFetchPage />);
    fireEvent.change(screen.getByPlaceholderText(/enter url/i), { target: { value: 'https://example.com' } });
    fireEvent.click(screen.getByRole('button', { name: /run fetch/i }));

    await waitFor(() => expect(fetchGraphQL).toHaveBeenCalled());
    const [, , variables] = vi.mocked(fetchGraphQL).mock.calls[0];
    expect(variables).toMatchObject({ url: 'https://example.com', output_markdown: true });
    await waitFor(() => expect(screen.getByText('# Title')).toBeInTheDocument());
    // The badge next to the result states the format the server reported, which
    // is distinct from the radio label the caller selected.
    const heading = screen.getByText(/Fetched Content/i).parentElement;
    expect(heading).not.toBeNull();
    expect(within(heading as HTMLElement).getByText('Markdown')).toBeInTheDocument();
  });

  it('requests raw HTML when the caller selects it', async () => {
    vi.mocked(fetchGraphQL).mockResolvedValue({
      WebFetch: { url: 'https://example.com', created_at: '2026-01-01T00:00:00Z', content: '<h1>Title</h1>', output_markdown: false },
    });

    render(<WebFetchPage />);
    fireEvent.change(screen.getByPlaceholderText(/enter url/i), { target: { value: 'https://example.com' } });
    fireEvent.click(screen.getByRole('radio', { name: /raw html/i }));
    fireEvent.click(screen.getByRole('button', { name: /run fetch/i }));

    await waitFor(() => expect(fetchGraphQL).toHaveBeenCalled());
    const [, , variables] = vi.mocked(fetchGraphQL).mock.calls[0];
    expect(variables).toMatchObject({ url: 'https://example.com', output_markdown: false });
    // The raw body must be displayed as text, never injected as live markup.
    await waitFor(() => expect(screen.getByText('<h1>Title</h1>')).toBeInTheDocument());
    expect(screen.queryByRole('heading', { name: 'Title' })).toBeNull();
    const rawHeading = screen.getByText(/Fetched Content/i).parentElement;
    expect(rawHeading).not.toBeNull();
    expect(within(rawHeading as HTMLElement).getByText('Raw HTML')).toBeInTheDocument();
  });

  it('keeps the format selector usable while the console is locked but reports nothing', async () => {
    mockApiKeyState('none');
    render(<WebFetchPage />);
    expect(screen.getByRole('radio', { name: /raw html/i })).toBeDisabled();
    expect(screen.getByRole('radio', { name: /markdown/i })).toBeDisabled();
    await waitFor(() => expect(fetchGraphQL).not.toHaveBeenCalled());
  });
});
