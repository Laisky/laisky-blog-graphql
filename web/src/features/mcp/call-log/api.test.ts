import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { getPrivateErrorMessage, setUnexpectedRequestReporter } from '@/lib/private-error';
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
    setUnexpectedRequestReporter();
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  // The prefixed mount is the only route registered when a public prefix is
  // configured; see internal/web/tool_http_routes.go.
  it('uses the current pathname whenever it builds the API URL', async () => {
    await fetchCallLogs('test-key', {});

    expect(fetchMock).toHaveBeenCalledWith(
      '/mcp/tools/call_log/api/logs',
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

  it.each([400, 401, 403, 404, 409, 412, 422, 429])('preserves private HTTP %i messages without reporting expected failures', async (status) => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    fetchMock.mockResolvedValue(new Response('synthetic private call parameters', { status }));
    const error = await fetchCallLogs('test-key', {}).catch((failure: unknown) => failure);
    expect(error).toBeInstanceOf(Error);
    expect((error as Error).message).toBe('synthetic private call parameters');
    expect((error as Error).stack).toContain('fetchCallLogs');
    expect(getPrivateErrorMessage(error)).toBe('Call log request failed');
    expect(observer).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('reports malformed private JSON once with safe operation context and preserves native UI details', async () => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    const error = new SyntaxError('synthetic private call log response');
    const stack = error.stack;
    fetchMock.mockResolvedValue({ ok: true, json: async () => { throw error; } });
    await expect(fetchCallLogs('test-key', { user: 'synthetic identity' })).rejects.toBe(error);
    expect(error.message).toBe('synthetic private call log response');
    expect(error.stack).toBe(stack);
    expect(getPrivateErrorMessage(error)).toBe('Invalid call log JSON response');
    expect(observer).toHaveBeenCalledExactlyOnceWith(error, { operation: 'call-log' });
  });

  it('observes transport failure once without attaching filters or credentials', async () => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    const error = new TypeError('Failed to fetch');
    fetchMock.mockRejectedValue(error);
    await expect(fetchCallLogs('synthetic-key', { user: 'private identity', tool: 'private tool' })).rejects.toBe(error);
    expect(observer).toHaveBeenCalledExactlyOnceWith(error, { operation: 'call-log' });
  });

  it('leaves caller cancellation unreported', async () => {
    const observer = vi.fn(); setUnexpectedRequestReporter(observer);
    const error = new DOMException('Cancelled', 'AbortError');
    fetchMock.mockRejectedValue(error);
    await expect(fetchCallLogs('test-key', {})).rejects.toBe(error);
    expect(observer).not.toHaveBeenCalled();
  });

  it('uses the root alias when the deployment has no public prefix', async () => {
    window.history.replaceState({}, '', '/tools/call_log');

    await fetchCallLogs('test-key', { page: 2, pageSize: 50 });

    expect(fetchMock).toHaveBeenCalledWith('/tools/call_log/api/logs?page=2&page_size=50', expect.any(Object));
  });
});
