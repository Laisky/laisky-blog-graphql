import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { fetchCallLogs } from './api';

function createCallLogResponse(): Response {
  const payload = {
    data: [],
    pagination: {
      page: 1,
      page_size: 20,
      total_items: 0,
      total_pages: 0,
      has_next: false,
      has_prev: false,
    },
    meta: { quotes_per_usd: 1000 },
  };

  return {
    ok: true,
    json: async () => payload,
    text: async () => JSON.stringify(payload),
  } as unknown as Response;
}

describe('fetchCallLogs', () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    fetchMock = vi.fn().mockResolvedValue(createCallLogResponse());
    globalThis.fetch = fetchMock as unknown as typeof globalThis.fetch;
    window.history.replaceState({}, '', '/mcp');
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it('uses the current pathname whenever it builds the API URL', async () => {
    await fetchCallLogs('test-key', {});

    expect(fetchMock).toHaveBeenCalledWith(
      '/mcp/api/logs',
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: 'Bearer test-key' }),
      })
    );

    fetchMock.mockClear();
    fetchMock.mockResolvedValue(createCallLogResponse());
    window.history.replaceState({}, '', '/mcp/tools/call_log');

    await fetchCallLogs('test-key', { page: 2, pageSize: 50 });

    expect(fetchMock).toHaveBeenCalledWith('/mcp/tools/call_log/api/logs?page=2&page_size=50', expect.any(Object));
  });
});
