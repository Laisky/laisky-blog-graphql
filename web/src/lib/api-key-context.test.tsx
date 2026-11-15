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

    it('keeps the console usable and keeps checking when a background response carries GraphQL errors', async () => {
      // The resolver turns an upstream balance failure into a GraphQL error over HTTP 200.
      // An upstream 401 also covers an exhausted key, so it is not a verdict on the key either.
      const fetchMock = await renderInsufficient();
      fetchMock.mockResolvedValue({
        ok: true,
        json: async () => ({ errors: [{ message: 'http request: dial tcp: i/o timeout', path: ['ValidateOneapiApiKey'] }], data: null }),
      } as Response);
      vi.spyOn(console, 'error').mockImplementation(() => undefined);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(screen.getByTestId('status').textContent).toBe('insufficient');
      expect(screen.getByTestId('quota').textContent).toBe('0');

      fetchMock.mockResolvedValue({
        ok: true,
        json: async () => ({ data: { ValidateOneapiApiKey: { remain_quota: 300000, used_quota: 100 } } }),
      } as Response);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
      expect(screen.getByTestId('status').textContent).toBe('valid');
    });

    it.each([
      ['no data', { data: null }],
      ['a non-numeric quota', { data: { ValidateOneapiApiKey: { remain_quota: 'n/a', used_quota: 0 } } }],
    ])('keeps the current status when a background response has %s', async (_label, payload) => {
      const fetchMock = await renderInsufficient();
      fetchMock.mockResolvedValue({ ok: true, json: async () => payload } as Response);

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(screen.getByTestId('status').textContent).toBe('insufficient');
      expect(screen.getByTestId('quota').textContent).toBe('0');

      await act(async () => {
        await vi.advanceTimersByTimeAsync(10000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(2);
    });

    it('never overlaps background checks and stops them once the key is disconnected', async () => {
      const fetchMock = await renderInsufficient();
      let release: (value: Response) => void = () => undefined;
      fetchMock.mockReturnValueOnce(new Promise<Response>((resolve) => (release = resolve)));

      await act(async () => {
        await vi.advanceTimersByTimeAsync(30000);
      });
      // The slow first check is still in flight: no second request was stacked on top of it.
      expect(fetchMock).toHaveBeenCalledTimes(1);
      const signal = (fetchMock.mock.calls[0][1] as RequestInit).signal;
      expect(signal?.aborted).toBe(false);

      await act(async () => {
        screen.getByTestId('disconnect').click();
      });
      expect(signal?.aborted).toBe(true);
      await act(async () => {
        release(insufficientResponse);
        await vi.advanceTimersByTimeAsync(30000);
      });
      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(screen.getByTestId('status').textContent).toBe('none');
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

  it('still reports GraphQL errors from an explicit validation as an error', async () => {
    vi.mocked(globalThis.fetch).mockResolvedValue({
      ok: true,
      json: async () => ({ errors: [{ message: 'invalid response status 401' }], data: null }),
    } as Response);
    vi.spyOn(console, 'error').mockImplementation(() => undefined);
    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    await act(async () => {
      screen.getByTestId('setKey').click();
    });

    expect(screen.getByTestId('status').textContent).toBe('error');
    expect(screen.getByTestId('quota').textContent).toBe('');
  });

  it('revalidates a key whose stored status was left at "validating" by an interrupted check', async () => {
    // Closing the tab mid-validation persisted the transient status; it must not lock the console forever.
    localStorage.setItem('mcp_api_key', 'saved-key');
    localStorage.setItem('mcp_api_key_status', 'validating');
    vi.mocked(globalThis.fetch).mockResolvedValue({
      ok: true,
      json: async () => ({ data: { ValidateOneapiApiKey: { remain_quota: 300000, used_quota: 0 } } }),
    } as Response);

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );
    await act(async () => {
      await vi.runOnlyPendingTimersAsync();
    });

    expect(globalThis.fetch).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('status').textContent).toBe('valid');
  });

  it('treats an unrecognised stored status or quota as unknown', async () => {
    localStorage.setItem('mcp_api_key', 'saved-key');
    localStorage.setItem('mcp_api_key_status', 'bogus');
    localStorage.setItem('mcp_api_key_quota', 'not-a-number');
    vi.mocked(globalThis.fetch).mockReturnValue(new Promise<Response>(() => undefined));

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );

    expect(screen.getByTestId('quota').textContent).toBe('');
    // The unknown status is revalidated rather than trusted.
    expect(screen.getByTestId('status').textContent).toBe('validating');
    expect(globalThis.fetch).toHaveBeenCalledTimes(1);
  });

  it('keeps working when site storage is blocked', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => {
      throw new DOMException('The operation is insecure.', 'SecurityError');
    });
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
      throw new DOMException('The operation is insecure.', 'SecurityError');
    });
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
      throw new DOMException('The operation is insecure.', 'SecurityError');
    });
    vi.mocked(globalThis.fetch).mockResolvedValue({
      ok: true,
      json: async () => ({ data: { ValidateOneapiApiKey: { remain_quota: 300000, used_quota: 0 } } }),
    } as Response);

    render(
      <ApiKeyProvider>
        <TestComponent />
      </ApiKeyProvider>
    );
    expect(screen.getByTestId('status').textContent).toBe('none');

    await act(async () => {
      screen.getByTestId('setKey').click();
    });
    expect(screen.getByTestId('apiKey').textContent).toBe('new-key');
    expect(screen.getByTestId('status').textContent).toBe('valid');
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

describe('ApiKeyContext superseded explicit validations', () => {
  const originalFetch = globalThis.fetch;

  /** pendingChecks records each validation request by key, in the order they were sent. */
  let pendingChecks: Array<{ key: string; signal?: AbortSignal | null; resolve: (response: Response) => void; reject: (error: unknown) => void }>;

  /** SwitchingComponent drives key switches the way the console's key menu does. */
  function SwitchingComponent() {
    const { apiKey, status, remainQuota, isToolConsoleLocked, setApiKey, switchApiKey, disconnect, validateApiKey } = useApiKey();
    return (
      <div>
        <div data-testid="apiKey">{apiKey}</div>
        <div data-testid="status">{status}</div>
        <div data-testid="quota">{remainQuota}</div>
        <div data-testid="locked">{String(isToolConsoleLocked)}</div>
        <button onClick={() => setApiKey('key-a')} data-testid="useA">
          Use A
        </button>
        <button onClick={() => switchApiKey('key-b')} data-testid="switchB">
          Switch to B
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

  const quotaResponse = (quota: number) =>
    ({ ok: true, status: 200, json: async () => ({ data: { ValidateOneapiApiKey: { remain_quota: quota, used_quota: 1 } } }) }) as Response;
  const refusedResponse = () => ({ ok: false, status: 401, json: async () => ({}) }) as Response;
  const check = (key: string, index = 0) => pendingChecks.filter((entry) => entry.key === key)[index];
  const text = (id: string) => screen.getByTestId(id).textContent;
  const click = (id: string) =>
    act(async () => {
      screen.getByTestId(id).click();
    });
  const settle = (fn: () => void) =>
    act(async () => {
      fn();
      await Promise.resolve();
    });

  beforeEach(() => {
    vi.useFakeTimers();
    localStorage.clear();
    pendingChecks = [];
    globalThis.fetch = vi.fn((_input: RequestInfo | URL, init?: RequestInit) => {
      const key = (JSON.parse(String(init?.body)) as { variables: { apiKey: string } }).variables.apiKey;
      return new Promise<Response>((resolve, reject) => {
        pendingChecks.push({ key, signal: init?.signal, resolve, reject });
        init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    }) as typeof fetch;
  });

  afterEach(() => {
    vi.useRealTimers();
    globalThis.fetch = originalFetch;
    vi.restoreAllMocks();
  });

  const renderProvider = () =>
    render(
      <ApiKeyProvider>
        <SwitchingComponent />
      </ApiKeyProvider>
    );

  it('ignores a success for the previous key that arrives after the new key was validated', async () => {
    renderProvider();
    await click('useA');
    await click('switchB');
    await settle(() => check('key-b').resolve(quotaResponse(0)));
    expect(text('status')).toBe('insufficient');

    await settle(() => check('key-a').resolve(quotaResponse(300000)));
    expect(text('apiKey')).toBe('key-b');
    expect(text('status')).toBe('insufficient');
    expect(text('quota')).toBe('0');
  });

  it('ignores a failure for the previous key that arrives after the new key was validated', async () => {
    renderProvider();
    await click('useA');
    await click('switchB');
    await settle(() => check('key-b').resolve(quotaResponse(300000)));
    expect(text('status')).toBe('valid');

    await settle(() => check('key-a').resolve(refusedResponse()));
    expect(text('status')).toBe('valid');
    expect(text('quota')).toBe('300000');
    expect(text('locked')).toBe('false');
  });

  it('keeps the new key validating when the previous key answers first', async () => {
    renderProvider();
    await click('useA');
    await click('switchB');
    await settle(() => check('key-a').resolve(quotaResponse(300000)));
    expect(text('status')).toBe('validating');
    expect(text('locked')).toBe('true');

    await settle(() => check('key-b').resolve(refusedResponse()));
    expect(text('status')).toBe('error');
  });

  it('aborts the previous key check when the key changes', async () => {
    renderProvider();
    await click('useA');
    const signal = check('key-a').signal;
    expect(signal?.aborted).toBe(false);

    await click('switchB');
    expect(signal?.aborted).toBe(true);
    expect(check('key-b').signal?.aborted).toBe(false);
  });

  it('never unlocks the console for a key that was disconnected while its check was in flight', async () => {
    renderProvider();
    await click('useA');
    const pending = check('key-a');
    await click('disconnect');
    await settle(() => pending.resolve(quotaResponse(300000)));

    expect(text('apiKey')).toBe('');
    expect(text('status')).toBe('none');
    expect(text('quota')).toBe('');
    expect(text('locked')).toBe('true');
  });

  it('never unlocks the console for a key that was cleared while its check was in flight', async () => {
    let context: ReturnType<typeof useApiKey> | undefined;
    function Capture() {
      context = useApiKey();
      return null;
    }
    render(
      <ApiKeyProvider>
        <SwitchingComponent />
        <Capture />
      </ApiKeyProvider>
    );
    await click('useA');
    const pending = check('key-a');
    await act(async () => {
      context!.setApiKey('');
    });
    await settle(() => pending.resolve(quotaResponse(300000)));

    expect(text('apiKey')).toBe('');
    expect(text('status')).toBe('none');
    expect(text('locked')).toBe('true');
  });

  it('drops a background re-check that settles after an explicit validation started', async () => {
    let context: ReturnType<typeof useApiKey> | undefined;
    function Capture() {
      context = useApiKey();
      return null;
    }
    localStorage.setItem('mcp_api_key', 'key-a');
    localStorage.setItem('mcp_api_key_status', 'insufficient');
    localStorage.setItem('mcp_api_key_quota', '0');
    render(
      <ApiKeyProvider>
        <SwitchingComponent />
        <Capture />
      </ApiKeyProvider>
    );
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10_000);
    });
    const background = check('key-a');
    expect(background).toBeDefined();

    // Start the explicit check and let the older background reading settle in the same
    // turn, before React re-renders and its poller is disposed: only the validation
    // generation can tell that reading is stale.
    await act(async () => {
      void context!.validateApiKey();
      background.resolve(quotaResponse(300000));
      for (let i = 0; i < 5; i += 1) await Promise.resolve();
    });
    expect(text('status')).toBe('validating');
    expect(text('quota')).toBe('0');

    await settle(() => check('key-a', 1).resolve(quotaResponse(0)));
    expect(text('status')).toBe('insufficient');
  });

  it('applies only the newest of two checks for the same key', async () => {
    localStorage.setItem('mcp_api_key', 'key-a');
    localStorage.setItem('mcp_api_key_status', 'valid');
    renderProvider();
    await click('validate');
    await click('validate');
    await settle(() => check('key-a', 1).resolve(quotaResponse(0)));
    expect(text('status')).toBe('insufficient');

    await settle(() => check('key-a', 0).resolve(quotaResponse(300000)));
    expect(text('status')).toBe('insufficient');
    expect(text('quota')).toBe('0');
  });
});
