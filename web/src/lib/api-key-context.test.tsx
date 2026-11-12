import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { ApiKeyProvider, useApiKey } from './api-key-context';

// Mock component to access context
function TestComponent() {
  const { apiKey, status, remainQuota, setApiKey, disconnect, validateApiKey, history, keyEntries, setAliasForKey } = useApiKey();
  return (
    <div>
      <div data-testid="apiKey">{apiKey}</div>
      <div data-testid="status">{status}</div>
      <div data-testid="quota">{remainQuota}</div>
      <div data-testid="history">{JSON.stringify(history)}</div>
      <div data-testid="entries">{JSON.stringify(keyEntries)}</div>
      <button onClick={() => setApiKey('new-key')} data-testid="setKey">
        Set Key
      </button>
      <button onClick={() => setAliasForKey('new-key', 'Primary')} data-testid="setAlias">
        Set Alias
      </button>
      <button onClick={() => disconnect()} data-testid="disconnect">
        Disconnect
      </button>
      <button onClick={() => validateApiKey()} data-testid="validate">
        Validate
      </button>
    </div>
  );
}

describe('ApiKeyContext', () => {
  const originalFetch = globalThis.fetch;

  beforeEach(() => {
    vi.useFakeTimers();
    localStorage.clear();
    globalThis.fetch = vi.fn();
  });

  afterEach(() => {
    vi.useRealTimers();
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  it('should initialize with empty values when localStorage is empty', () => {
    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    expect(screen.getByTestId('apiKey').textContent).toBe('');
    expect(screen.getByTestId('status').textContent).toBe('none');
    expect(screen.getByTestId('quota').textContent).toBe('');
  });

  it('should load values from localStorage on init', () => {
    localStorage.setItem('mcp_api_key', 'saved-key');
    localStorage.setItem('mcp_api_key_status', 'valid');
    localStorage.setItem('mcp_api_key_quota', '500');

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    expect(screen.getByTestId('apiKey').textContent).toBe('saved-key');
    expect(screen.getByTestId('status').textContent).toBe('valid');
    expect(screen.getByTestId('quota').textContent).toBe('500');
  });

  it('should update status to valid on successful validation', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValue({
      ok: true,
      json: async () => ({
        data: {
          ValidateOneapiApiKey: {
            remain_quota: 300000,
            used_quota: 100,
          },
        },
      }),
    } as Response);

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    // Initial state check
    expect(screen.getByTestId('status').textContent).toBe('none');

    await act(async () => {
      screen.getByTestId('setKey').click();
    });

    expect(screen.getByTestId('status').textContent).toBe('valid');
    expect(screen.getByTestId('quota').textContent).toBe('300000');
    // Key should be saved in LS (storage key is mcp_api_key)
    expect(localStorage.getItem('mcp_api_key')).toBe('new-key');
  });

  it('should update status to insufficient when quota is 0', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValue({
      ok: true,
      json: async () => ({
        data: {
          ValidateOneapiApiKey: {
            remain_quota: 0,
            used_quota: 100,
          },
        },
      }),
    } as Response);

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    await act(async () => {
      screen.getByTestId('setKey').click();
    });

    expect(screen.getByTestId('status').textContent).toBe('insufficient');
    expect(screen.getByTestId('quota').textContent).toBe('0');
  });

  it('should update status to error on failed validation', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 401,
    } as Response);

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    await act(async () => {
      screen.getByTestId('setKey').click();
    });

    await act(async () => {
      await vi.runAllTimersAsync();
    });

    expect(screen.getByTestId('status').textContent).toBe('error');
  });

  it('should poll when status is insufficient', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    // 1st call: insufficient
    fetchMock.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          ValidateOneapiApiKey: {
            remain_quota: 0,
            used_quota: 100,
          },
        },
      }),
    } as Response);
    // 2nd call (after polling): valid
    fetchMock.mockResolvedValueOnce({
      ok: true,
      json: async () => ({
        data: {
          ValidateOneapiApiKey: {
            remain_quota: 300000,
            used_quota: 100,
          },
        },
      }),
    } as Response);
    // Fallback
    fetchMock.mockResolvedValue({
      ok: true,
      json: async () => ({
        data: {
          ValidateOneapiApiKey: {
            remain_quota: 300000,
            used_quota: 100,
          },
        },
      }),
    } as Response);

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    await act(async () => {
      screen.getByTestId('setKey').click();
    });

    expect(screen.getByTestId('status').textContent).toBe('insufficient');

    // Fast-forward 10 seconds for polling
    await act(async () => {
      vi.advanceTimersByTime(10000);
    });

    expect(screen.getByTestId('status').textContent).toBe('valid');
    expect(screen.getByTestId('quota').textContent).toBe('300000');
  });

  describe('background quota re-check while insufficient', () => {
    const insufficientResponse = {
      ok: true,
      json: async () => ({ data: { ValidateOneapiApiKey: { remain_quota: 0, used_quota: 100 } } }),
    } as Response;

    /** renderInsufficient renders the provider and drives it to the insufficient status. */
    async function renderInsufficient() {
      const fetchMock = vi.mocked(globalThis.fetch);
      fetchMock.mockResolvedValueOnce(insufficientResponse);
      render(
        <ApiKeyProvider>
          <TestComponent />
        </ApiKeyProvider>
      );
      await act(async () => {
        screen.getByTestId('setKey').click();
      });
      expect(screen.getByTestId('status').textContent).toBe('insufficient');
      fetchMock.mockClear();
      return fetchMock;
    }

    /** setVisibility overrides document.visibilityState and notifies listeners. */
    function setVisibility(state: DocumentVisibilityState) {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => state });
      document.dispatchEvent(new Event('visibilitychange'));
    }

    afterEach(() => {
      Reflect.deleteProperty(document, 'visibilityState');
    });

    it('keeps the console unlocked while the background check is in flight', async () => {
      const fetchMock = await renderInsufficient();
      fetchMock.mockReturnValue(new Promise<Response>(() => undefined));

      await act(async () => {
        vi.advanceTimersByTime(10000);
      });

      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(screen.getByTestId('status').textContent).toBe('insufficient');
    });

    it('sends the background check at low fetch priority', async () => {
      const fetchMock = await renderInsufficient();
      fetchMock.mockResolvedValue(insufficientResponse);

      await act(async () => {
        vi.advanceTimersByTime(10000);
      });

      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock.mock.calls[0][1]).toMatchObject({ priority: 'low' });
    });

    it('keeps the current status when a background check fails in transit', async () => {
      const fetchMock = await renderInsufficient();
      fetchMock.mockRejectedValue(new TypeError('Failed to fetch'));
      vi.spyOn(console, 'error').mockImplementation(() => undefined);

      await act(async () => {
        vi.advanceTimersByTime(10000);
      });

      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(screen.getByTestId('status').textContent).toBe('insufficient');
    });

    it('skips checks while the tab is hidden and re-checks when it becomes visible', async () => {
      const fetchMock = await renderInsufficient();
      fetchMock.mockResolvedValue(insufficientResponse);

      await act(async () => {
        setVisibility('hidden');
        vi.advanceTimersByTime(30000);
      });
      expect(fetchMock).not.toHaveBeenCalled();

      await act(async () => {
        setVisibility('visible');
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock.mock.calls[0][1]).toMatchObject({ priority: 'low' });
    });
  });

  it('should reset values on disconnect', async () => {
    localStorage.setItem('mcp_api_key', 'saved-key');
    localStorage.setItem('mcp_api_key_status', 'valid');
    localStorage.setItem('mcp_api_key_quota', '500');

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    await act(async () => {
      screen.getByTestId('disconnect').click();
    });

    expect(screen.getByTestId('apiKey').textContent).toBe('');
    expect(screen.getByTestId('status').textContent).toBe('none');
    expect(screen.getByTestId('quota').textContent).toBe('');
    expect(localStorage.getItem('mcp_api_key')).toBeNull();
  });

  it('should update alias for a stored key', async () => {
    const fetchMock = vi.mocked(globalThis.fetch);
    fetchMock.mockResolvedValue({
      ok: true,
      json: async () => ({
        data: {
          ValidateOneapiApiKey: {
            remain_quota: 300000,
            used_quota: 100,
          },
        },
      }),
    } as Response);

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    await act(async () => {
      screen.getByTestId('setKey').click();
    });

    await act(async () => {
      screen.getByTestId('setAlias').click();
    });

    expect(screen.getByTestId('entries').textContent).toContain('"alias":"Primary"');
  });
});
