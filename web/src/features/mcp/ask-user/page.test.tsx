import '@testing-library/jest-dom/vitest';
import { act, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { useApiKey } from '@/lib/api-key-context';

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
  });
});
