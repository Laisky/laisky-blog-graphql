import '@testing-library/jest-dom/vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useApiKey } from '@/lib/api-key-context';
import { findDanglingIdRefs } from '@/test-utils/aria-idrefs';

import * as api from './api';
import { AskUserPage } from './page';

vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
  normalizeApiKey: (k: string) => k,
}));

vi.mock('./api', () => ({
  listRequests: vi.fn(),
  submitAnswer: vi.fn(),
}));

const pendingQuestion: api.AskUserRequest = {
  id: 'q1',
  question: 'Which branch should I deploy?',
  status: 'pending',
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
};

describe('AskUserPage background polling', () => {
  let visibility: DocumentVisibilityState = 'visible';

  const flush = (ms = 0) =>
    act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });

  const setVisibility = (next: DocumentVisibilityState) => {
    visibility = next;
    act(() => {
      document.dispatchEvent(new Event('visibilitychange'));
    });
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
    visibility = 'visible';
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => visibility });
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'test-key', isToolConsoleLocked: false, status: 'valid' } as ReturnType<typeof useApiKey>);
    vi.mocked(api.listRequests).mockResolvedValue({ pending: [pendingQuestion], history: [] });
    vi.mocked(api.submitAnswer).mockResolvedValue();
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    Reflect.deleteProperty(document, 'visibilityState');
  });

  it('loads at default priority and marks timer polls as low priority', async () => {
    render(<AskUserPage />);
    await flush();
    expect(api.listRequests).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.listRequests).mock.calls[0][2]).toBeUndefined();

    await flush(5000);
    expect(api.listRequests).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.listRequests).mock.calls[1][2]).toBe('low');
  });

  it('backs off while the list keeps failing and returns to the 5 s cadence once it loads', async () => {
    vi.mocked(api.listRequests).mockRejectedValue(new TypeError('Failed to fetch'));
    // Take the top of each jitter range so the schedule is exact: 8, 16, 32, then 60 s (capped).
    vi.spyOn(Math, 'random').mockReturnValue(0.999999);
    render(<AskUserPage />);
    await flush();
    expect(api.listRequests).toHaveBeenCalledTimes(1);
    await flush(8000);
    expect(api.listRequests).toHaveBeenCalledTimes(2);
    await flush(15_999);
    expect(api.listRequests).toHaveBeenCalledTimes(2);
    await flush(1);
    expect(api.listRequests).toHaveBeenCalledTimes(3);
    // Loads at 0, 8, 24 and 56 s, then once a minute: 13 loads in 10 minutes instead of 76.
    await flush(600_000 - 24_000);
    expect(api.listRequests).toHaveBeenCalledTimes(13);

    vi.mocked(api.listRequests).mockResolvedValue({ pending: [pendingQuestion], history: [] });
    await flush(60_000);
    expect(api.listRequests).toHaveBeenCalledTimes(14);
    await flush(5000);
    expect(api.listRequests).toHaveBeenCalledTimes(15);

    // A success starts the backoff over: the next failure is retried after 8 s again.
    vi.mocked(api.listRequests).mockRejectedValue(new TypeError('Failed to fetch'));
    await flush(1000);
    expect(api.listRequests).toHaveBeenCalledTimes(16);
    await flush(7999);
    expect(api.listRequests).toHaveBeenCalledTimes(16);
    await flush(1);
    expect(api.listRequests).toHaveBeenCalledTimes(17);
  });

  it('stops polling while hidden and refreshes immediately when visible again', async () => {
    render(<AskUserPage />);
    await flush();

    setVisibility('hidden');
    await flush(60_000);
    expect(api.listRequests).toHaveBeenCalledTimes(1);

    setVisibility('visible');
    await flush();
    expect(api.listRequests).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.listRequests).mock.calls[1][2]).toBeUndefined();
  });

  it('aborts the in-flight poll and stops listening on unmount', async () => {
    vi.mocked(api.listRequests).mockReturnValue(new Promise(() => undefined));
    const { unmount } = render(<AskUserPage />);
    await flush();
    const signal = vi.mocked(api.listRequests).mock.calls[0][1];

    unmount();
    expect(signal?.aborted).toBe(true);

    setVisibility('hidden');
    setVisibility('visible');
    await flush(60_000);
    expect(api.listRequests).toHaveBeenCalledTimes(1);
  });

  it('labels the answer box with its question and refreshes at default priority after submitting', async () => {
    render(<AskUserPage />);
    await flush();

    const answer = screen.getByRole('textbox', { name: pendingQuestion.question });
    fireEvent.change(answer, { target: { value: 'main' } });
    fireEvent.click(screen.getByRole('button', { name: /send answer/i }));
    await flush();

    expect(api.submitAnswer).toHaveBeenCalledWith('test-key', 'q1', 'main');
    expect(api.listRequests).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.listRequests).mock.calls[1][2]).toBeUndefined();
    expect(findDanglingIdRefs()).toEqual([]);
  });
});
