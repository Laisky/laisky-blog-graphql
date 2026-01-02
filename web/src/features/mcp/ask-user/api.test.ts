import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { listRequests, submitAnswer } from './api';

function createListResponse(): Response {
    return {
        ok: true,
        json: async () => ({ pending: [], history: [] }),
        text: async () => 'ok',
    } as unknown as Response;
}

function createOkResponse(): Response {
    return {
        ok: true,
        json: async () => ({}),
        text: async () => 'ok',
    } as unknown as Response;
}

describe('ask_user API helpers', () => {
    const originalFetch = globalThis.fetch;
    let fetchMock: ReturnType<typeof vi.fn>;

    beforeEach(() => {
        fetchMock = vi.fn().mockResolvedValue(createListResponse());
        globalThis.fetch = fetchMock as unknown as typeof globalThis.fetch;
        window.history.replaceState({}, '', '/mcp');
    });

    afterEach(() => {
        globalThis.fetch = originalFetch;
        vi.restoreAllMocks();
    });

    it('uses the updated pathname for listRequests and submitAnswer', async () => {
        await listRequests('test-key');
        expect(fetchMock).toHaveBeenCalledWith('/mcp/api/requests', expect.any(Object));

        fetchMock.mockClear();
        fetchMock.mockResolvedValue(createListResponse());
        window.history.replaceState({}, '', '/mcp/tools/ask_user');

        await listRequests('test-key');
        expect(fetchMock).toHaveBeenCalledWith(
            '/mcp/tools/ask_user/api/requests',
            expect.any(Object)
        );

        fetchMock.mockClear();
        fetchMock.mockResolvedValue(createOkResponse());

        await submitAnswer('test-key', 'req-1', 'answer');
        expect(fetchMock).toHaveBeenCalledWith(
            '/mcp/tools/ask_user/api/requests/req-1',
            expect.objectContaining({ method: 'POST' })
        );
    });
});
