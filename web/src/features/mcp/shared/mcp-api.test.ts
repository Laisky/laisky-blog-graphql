import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

function createJSONResponse(body: unknown, headers?: Record<string, string>): Response {
  return {
    ok: true,
    status: 200,
    headers: new Headers(headers),
    json: async () => body,
    text: async () => JSON.stringify(body),
  } as unknown as Response;
}

function createTextErrorResponse(status: number, body: string): Response {
  return {
    ok: false,
    status,
    headers: new Headers(),
    json: async () => ({ error: { message: body } }),
    text: async () => body,
  } as unknown as Response;
}

describe('callMcpTool', () => {
  const originalFetch = globalThis.fetch;
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.resetModules();
    fetchMock = vi.fn();
    globalThis.fetch = fetchMock as unknown as typeof globalThis.fetch;
  });

  afterEach(() => {
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it('initializes once and reuses MCP session for subsequent tool calls', async () => {
    const { callMcpTool } = await import('./mcp-api');

    fetchMock
      .mockResolvedValueOnce(
        createJSONResponse(
          {
            jsonrpc: '2.0',
            id: 1,
            result: { protocolVersion: '2025-06-18' },
          },
          { 'Mcp-Session-Id': 'mcp-session-first' },
        ),
      )
      .mockResolvedValueOnce(
        createJSONResponse({
          jsonrpc: '2.0',
          id: 2,
          result: { content: [], isError: false },
        }),
      )
      .mockResolvedValueOnce(
        createJSONResponse({
          jsonrpc: '2.0',
          id: 3,
          result: { content: [], isError: false },
        }),
      );

    await callMcpTool('test-key', 'file_write', { project: 'demo' });
    await callMcpTool('test-key', 'file_read', { project: 'demo' });

    expect(fetchMock).toHaveBeenCalledTimes(3);

    const initializeRequest = fetchMock.mock.calls[0];
    expect(initializeRequest[0]).toBe('/mcp');
    expect(String((initializeRequest[1] as RequestInit).body)).toContain('"method":"initialize"');

    const firstToolCallHeaders = (fetchMock.mock.calls[1][1] as RequestInit).headers as Record<string, string>;
    expect(firstToolCallHeaders['Mcp-Session-Id']).toBe('mcp-session-first');

    const secondToolCallHeaders = (fetchMock.mock.calls[2][1] as RequestInit).headers as Record<string, string>;
    expect(secondToolCallHeaders['Mcp-Session-Id']).toBe('mcp-session-first');
  });

  it('reinitializes and retries once when server reports invalid session id', async () => {
    const { callMcpTool } = await import('./mcp-api');

    fetchMock
      .mockResolvedValueOnce(
        createJSONResponse(
          {
            jsonrpc: '2.0',
            id: 1,
            result: { protocolVersion: '2025-06-18' },
          },
          { 'Mcp-Session-Id': 'mcp-session-stale' },
        ),
      )
      .mockResolvedValueOnce(createTextErrorResponse(400, 'Invalid session ID\n'))
      .mockResolvedValueOnce(
        createJSONResponse(
          {
            jsonrpc: '2.0',
            id: 2,
            result: { protocolVersion: '2025-06-18' },
          },
          { 'Mcp-Session-Id': 'mcp-session-fresh' },
        ),
      )
      .mockResolvedValueOnce(
        createJSONResponse({
          jsonrpc: '2.0',
          id: 3,
          result: { content: [], isError: false },
        }),
      );

    await callMcpTool('test-key', 'file_write', { project: 'demo' });

    expect(fetchMock).toHaveBeenCalledTimes(4);
    const staleHeaders = (fetchMock.mock.calls[1][1] as RequestInit).headers as Record<string, string>;
    expect(staleHeaders['Mcp-Session-Id']).toBe('mcp-session-stale');

    const freshHeaders = (fetchMock.mock.calls[3][1] as RequestInit).headers as Record<string, string>;
    expect(freshHeaders['Mcp-Session-Id']).toBe('mcp-session-fresh');
  });

  it('propagates initialize failure as request error', async () => {
    const { callMcpTool } = await import('./mcp-api');

    fetchMock.mockResolvedValueOnce(createTextErrorResponse(500, 'initialize failed'));

    await expect(callMcpTool('test-key', 'file_write', { project: 'demo' })).rejects.toThrow('initialize failed');
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});
