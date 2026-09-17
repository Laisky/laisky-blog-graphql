import { afterEach, describe, expect, it, vi } from 'vitest';
import { callFileAPI, callFileTool, extractFilePayload } from './client';
import { fileIOErrorMessage } from './version-state';
import { callMcpTool } from '../shared/mcp-api';

vi.mock('../shared/auth', () => ({
  buildAuthorizationHeader: (key: string) => key ? `Bearer ${key}` : '',
  resolveToolApiBase: () => '/tools/file_io/',
}));
vi.mock('../shared/mcp-api', () => ({ callMcpTool: vi.fn() }));
afterEach(() => { vi.unstubAllGlobals(); vi.clearAllMocks(); });

describe('FileIO console transports', () => {
  it('passes the exact captured condition to MCP', async () => {
    vi.mocked(callMcpTool).mockResolvedValue({ content: [{ type: 'text', text: '{"bytes_written":1,"version":"v2"}' }] });
    await callFileTool('key', 'file_write', { project: 'p', path: '/a', content: 'x', expected_version: 'v1' });
    expect(callMcpTool).toHaveBeenCalledTimes(1);
    expect(callMcpTool).toHaveBeenCalledWith('key', 'file_write', { project: 'p', path: '/a', content: 'x', expected_version: 'v1' });
  });
  it('preserves structured MCP conflict codes and never retries the tool', async () => {
    vi.mocked(callMcpTool).mockResolvedValue({ isError: true, content: [{ type: 'text', text: '{"code":"VERSION_CONFLICT","message":"changed"}' }] });
    await expect(callFileTool('key', 'file_write', {})).rejects.toMatchObject({ code: 'VERSION_CONFLICT' });
    expect(callMcpTool).toHaveBeenCalledTimes(1);
  });
  it('sends quoted If-Match on save and restore, with no preliminary stat or read', async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response('{"bytes_written":1,"version":"v2"}', { status: 200 }));
    vi.stubGlobal('fetch', fetcher);
    await callFileAPI('key', 'PUT', '/file', { body: { project: 'p', path: '/a', content: 'x' }, headers: { 'If-Match': '"v1"' } });
    expect(fetcher).toHaveBeenCalledTimes(1);
    const [url, options] = fetcher.mock.calls[0];
    expect(url).toBe('/tools/file_io/api/file');
    expect(options.headers).toMatchObject({ Authorization: 'Bearer key', 'If-Match': '"v1"' });
    expect(options.cache).toBe('no-store');
    expect(JSON.parse(options.body)).toEqual({ project: 'p', path: '/a', content: 'x' });
    fetcher.mockResolvedValue(new Response('{"bytes_written":1,"version":"v3"}', { status: 200 }));
    await callFileAPI('key', 'POST', '/versions/7/restore', { body: { project: 'p', path: '/a' }, headers: { 'If-Match': '"v2"' } });
    expect(fetcher.mock.calls[1][1].headers['If-Match']).toBe('"v2"');
  });
  it.each([412, 428])('preserves HTTP %i for actionable recovery', async (status) => {
    const fetcher = vi.fn().mockResolvedValue(new Response('{"error":"rejected"}', { status }));
    vi.stubGlobal('fetch', fetcher);
    let error: unknown;
    try { await callFileAPI('key', 'PUT', '/file'); } catch (caught) { error = caught; }
    expect(error).toMatchObject({ status });
    expect(fileIOErrorMessage(error)).toContain('draft is kept');
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
  it('does not send unauthenticated requests', async () => {
    const fetcher = vi.fn(); vi.stubGlobal('fetch', fetcher);
    await expect(callFileAPI('', 'PUT', '/file')).rejects.toThrow('API key');
    await expect(callFileTool('', 'file_write', {})).rejects.toThrow('API key');
    expect(fetcher).not.toHaveBeenCalled(); expect(callMcpTool).not.toHaveBeenCalled();
  });
  it('reads structured and textual payloads without dropping versions', () => {
    const content = [{ type: 'text', text: '{"version":"v1"}' }];
    expect(extractFilePayload({ content })).toEqual({ version: 'v1' });
    expect(extractFilePayload({ content, structuredContent: { version: 'v2' } } as Parameters<typeof extractFilePayload>[0])).toEqual({ version: 'v2' });
  });
});
