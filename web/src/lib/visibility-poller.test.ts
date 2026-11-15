import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createVisibilityPoller, type PollRunContext } from './visibility-poller';

let visibility: DocumentVisibilityState = 'visible';

function setVisibility(next: DocumentVisibilityState) {
  visibility = next;
  document.dispatchEvent(new Event('visibilitychange'));
}

/** deferred returns a promise whose settlement the test controls. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

describe('createVisibilityPoller', () => {
  beforeEach(() => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
    visibility = 'visible';
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility });
  });

  afterEach(() => {
    vi.useRealTimers();
    Reflect.deleteProperty(document, 'visibilityState');
  });

  it('runs the initial poll at default priority and later ticks at low priority', async () => {
    const calls: PollRunContext[] = [];
    const poller = createVisibilityPoller(async (context) => {
      calls.push(context);
      return 5000;
    });

    poller.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(calls).toHaveLength(1);
    expect(calls[0].reason).toBe('initial');
    expect(calls[0].priority).toBeUndefined();

    await vi.advanceTimersByTimeAsync(5000);
    expect(calls).toHaveLength(2);
    expect(calls[1].reason).toBe('timer');
    expect(calls[1].priority).toBe('low');

    poller.dispose();
  });

  it('does not poll while the document is hidden and refreshes once on becoming visible', async () => {
    const run = vi.fn(async () => 5000);
    const poller = createVisibilityPoller(run);

    poller.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(run).toHaveBeenCalledTimes(1);

    setVisibility('hidden');
    await vi.advanceTimersByTimeAsync(60_000);
    expect(run).toHaveBeenCalledTimes(1);

    setVisibility('visible');
    await vi.advanceTimersByTimeAsync(0);
    expect(run).toHaveBeenCalledTimes(2);
    const resumed = run.mock.calls[1] as unknown as [PollRunContext];
    expect(resumed[0].reason).toBe('visible');
    expect(resumed[0].priority).toBeUndefined();

    // The resumed run re-arms the normal cadence exactly once.
    await vi.advanceTimersByTimeAsync(5000);
    expect(run).toHaveBeenCalledTimes(3);

    poller.dispose();
  });

  it('pauses a tick whose request finishes while hidden', async () => {
    const pending = deferred<number | null>();
    const run = vi.fn((context: PollRunContext) => (context.reason === 'initial' ? pending.promise : Promise.resolve(5000)));
    const poller = createVisibilityPoller(run);

    poller.start();
    setVisibility('hidden');
    pending.resolve(5000);
    await vi.advanceTimersByTimeAsync(30_000);
    expect(run).toHaveBeenCalledTimes(1);

    setVisibility('visible');
    await vi.advanceTimersByTimeAsync(0);
    expect(run).toHaveBeenCalledTimes(2);

    poller.dispose();
  });

  it('does not start a second request when becoming visible during an in-flight run', async () => {
    const pending = deferred<number | null>();
    const run = vi.fn(() => pending.promise);
    const poller = createVisibilityPoller(run);

    poller.start();
    setVisibility('hidden');
    setVisibility('visible');
    await vi.advanceTimersByTimeAsync(0);
    expect(run).toHaveBeenCalledTimes(1);

    poller.dispose();
  });

  it('does not double-schedule when visibilitychange fires repeatedly', async () => {
    const run = vi.fn(async () => 5000);
    const poller = createVisibilityPoller(run);

    poller.start();
    await vi.advanceTimersByTimeAsync(0);
    setVisibility('visible');
    setVisibility('visible');
    await vi.advanceTimersByTimeAsync(0);
    // A visible document with a pending timer does not need a resume refresh.
    expect(run).toHaveBeenCalledTimes(1);

    await vi.advanceTimersByTimeAsync(5000);
    expect(run).toHaveBeenCalledTimes(2);

    poller.dispose();
  });

  it('stays stopped when a run resolves null even after visibility changes', async () => {
    const run = vi.fn(async () => null);
    const poller = createVisibilityPoller(run);

    poller.schedule(1000);
    await vi.advanceTimersByTimeAsync(1000);
    expect(run).toHaveBeenCalledTimes(1);

    setVisibility('hidden');
    setVisibility('visible');
    await vi.advanceTimersByTimeAsync(10_000);
    expect(run).toHaveBeenCalledTimes(1);

    poller.dispose();
  });

  it('aborts the in-flight run and stops listening on dispose', async () => {
    let signal: AbortSignal | undefined;
    const run = vi.fn((context: PollRunContext) => {
      signal = context.signal;
      return new Promise<number | null>(() => undefined);
    });
    const poller = createVisibilityPoller(run);

    poller.start();
    poller.dispose();
    expect(signal?.aborted).toBe(true);

    setVisibility('hidden');
    setVisibility('visible');
    await vi.advanceTimersByTimeAsync(60_000);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it('refresh aborts a slower run and ignores its late result', async () => {
    const first = deferred<number | null>();
    const contexts: PollRunContext[] = [];
    const run = vi.fn((context: PollRunContext) => {
      contexts.push(context);
      return contexts.length === 1 ? first.promise : Promise.resolve(5000);
    });
    const poller = createVisibilityPoller(run);

    poller.start();
    poller.refresh();
    expect(contexts[0].signal.aborted).toBe(true);
    expect(contexts[1].reason).toBe('manual');
    expect(contexts[1].priority).toBeUndefined();

    // The superseded run settling later must not arm a second timer chain.
    first.resolve(10);
    await vi.advanceTimersByTimeAsync(4999);
    expect(run).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(1);
    expect(run).toHaveBeenCalledTimes(3);

    poller.dispose();
  });
});
