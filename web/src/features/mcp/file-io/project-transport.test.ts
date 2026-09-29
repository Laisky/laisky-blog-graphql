import { afterEach, describe, expect, it, vi } from 'vitest';
import { listProjects } from './project-options';

vi.mock('../shared/auth', () => ({
  buildAuthorizationHeader: (key: string) => key ? `Bearer ${key}` : '',
  resolveToolApiBase: () => '/mcp/tools/file_io/',
}));
vi.mock('../shared/mcp-api', () => ({ callMcpTool: vi.fn() }));
afterEach(() => vi.unstubAllGlobals());

describe('project discovery HTTP boundary', () => {
  it('uses the existing same-origin prefix, bearer authentication, and no-store without identity query fields', async () => {
    const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify({ projects: ['p'], has_more: false }), { status: 200 }));
    vi.stubGlobal('fetch', fetch);
    const signal = new AbortController().signal;
    await listProjects('current-key', 'p', '', signal);
    expect(fetch).toHaveBeenCalledTimes(1);
    const [url, options] = fetch.mock.calls[0];
    expect(url).toBe('/mcp/tools/file_io/api/projects?q=p&after=&limit=50');
    expect(options).toMatchObject({ method: 'GET', signal, cache: 'no-store', headers: {
      Authorization: 'Bearer current-key', 'Cache-Control': 'no-store', Pragma: 'no-cache',
    } });
    expect(url).not.toContain('current-key');
  });
  it('propagates cancellation without replaying the request', async () => {
    const fetch = vi.fn().mockImplementation((_url: string, options: RequestInit) => new Promise((_resolve, reject) => {
      options.signal?.addEventListener('abort', () => reject(new DOMException('Aborted', 'AbortError')), { once: true });
    }));
    vi.stubGlobal('fetch', fetch);
    const controller = new AbortController();
    const request = listProjects('key', '', '', controller.signal);
    controller.abort(); await expect(request).rejects.toMatchObject({ name: 'AbortError' });
    expect(fetch).toHaveBeenCalledTimes(1);
  });
  it('does not fetch with empty credentials', async () => {
    const fetch = vi.fn(); vi.stubGlobal('fetch', fetch);
    await expect(listProjects('', '', '', new AbortController().signal)).rejects.toThrow();
    expect(fetch).not.toHaveBeenCalled();
  });
});
