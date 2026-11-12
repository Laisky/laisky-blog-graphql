import '@testing-library/jest-dom/vitest';
import { useApiKey } from '@/lib/api-key-context';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from './api';
import { UserRequestsPage } from './page';

// Mock dependencies
vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
  normalizeApiKey: (k: string) => k,
}));

vi.mock('./api', () => ({
  listUserRequests: vi.fn(),
  createUserRequest: vi.fn(),
  getReturnMode: vi.fn(() => 'all'),
  setDescriptionCollapsed: vi.fn(),
  getDescriptionCollapsed: vi.fn(() => false),
  getPreferencesFromServer: vi.fn(),
  getHoldState: vi.fn(),
  deleteAllPendingRequests: vi.fn(),
  deleteConsumedRequests: vi.fn(),
  deleteUserRequest: vi.fn(),
  releaseHold: vi.fn(),
  reorderUserRequests: vi.fn(),
  setHold: vi.fn(),
  setReturnModeOnServer: vi.fn(),
  setCommandTemplateOnServer: vi.fn(),
  setReturnMode: vi.fn(),
  searchUserRequests: vi.fn(),
  getQuota: vi.fn().mockResolvedValue({ user_identity: 'u', used_bytes: 0, quota_bytes: 0, object_count: 0, ttl_days: 0 }),
}));

// Mock child components that might interfere or are complex
vi.mock('./hold-button', () => ({
  HoldButton: ({ disabled, onActivate }: { disabled?: boolean; onActivate?: () => void }) => (
    <button disabled={disabled} onClick={onActivate}>
      Hold
    </button>
  ),
}));
vi.mock('./request-cards', () => ({
  ConsumedCard: () => <div>Consumed</div>,
  EmptyState: () => <div>Empty</div>,
  PendingRequestCard: () => <div>Pending</div>,
}));
vi.mock('./saved-commands', () => ({
  SavedCommands: () => <div>SavedCommands</div>,
}));
vi.mock('./task-id-selector', () => ({
  TaskIdSelector: ({ disabled }: { disabled?: boolean }) => <input data-testid="task-id-selector" disabled={disabled} />,
  useTaskIdHistory: () => ({ recordUsage: vi.fn() }),
}));

describe('UserRequestsPage Keyboard Behavior', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'test-api-key', isToolConsoleLocked: false, status: 'valid' });
    vi.mocked(api.listUserRequests).mockResolvedValue({ pending: [], consumed: [] });
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all' });
  });

  it('should show the correct hint in the placeholder', () => {
    render(<UserRequestsPage />);
    const textarea = screen.getByPlaceholderText(/Describe the feedback.*Ctrl \+ Enter to queue/i);
    expect(textarea).toBeDefined();
  });

  it('should NOT trigger send on plain Enter', async () => {
    render(<UserRequestsPage />);

    const textarea = screen.getByPlaceholderText(/Describe the feedback/i);
    fireEvent.change(textarea, { target: { value: 'test command' } });

    // Press Enter without modifiers
    fireEvent.keyDown(textarea, { key: 'Enter', code: 'Enter' });

    expect(api.createUserRequest).not.toHaveBeenCalled();
  });

  it('should trigger send on Ctrl+Enter', async () => {
    vi.mocked(api.createUserRequest).mockResolvedValue({ id: '1', status: 'pending' } as never);
    render(<UserRequestsPage />);

    const textarea = screen.getByPlaceholderText(/Describe the feedback/i);
    fireEvent.change(textarea, { target: { value: 'test command' } });

    // Press Ctrl+Enter
    fireEvent.keyDown(textarea, { key: 'Enter', code: 'Enter', ctrlKey: true });

    await waitFor(() => {
      expect(api.createUserRequest).toHaveBeenCalledWith('test-api-key', expect.objectContaining({ content: 'test command' }));
    });
  });

  it('should trigger send on Meta+Enter (for Mac)', async () => {
    vi.mocked(api.createUserRequest).mockResolvedValue({ id: '1', status: 'pending' } as never);
    render(<UserRequestsPage />);

    const textarea = screen.getByPlaceholderText(/Describe the feedback/i);
    fireEvent.change(textarea, { target: { value: 'test command' } });

    // Press Meta+Enter (Cmd+Enter)
    fireEvent.keyDown(textarea, { key: 'Enter', code: 'Enter', metaKey: true });

    await waitFor(() => {
      expect(api.createUserRequest).toHaveBeenCalledWith('test-api-key', expect.objectContaining({ content: 'test command' }));
    });
  });

  it('should NOT trigger send when composing in IME even with Ctrl+Enter', async () => {
    render(<UserRequestsPage />);

    const textarea = screen.getByPlaceholderText(/Describe the feedback/i);
    fireEvent.change(textarea, { target: { value: 'test' } });

    // Simulate IME composition session by adding isComposing
    fireEvent.keyDown(textarea, {
      key: 'Enter',
      code: 'Enter',
      ctrlKey: true,
      isComposing: true,
      nativeEvent: { isComposing: true },
    } as unknown as KeyboardEvent);

    expect(api.createUserRequest).not.toHaveBeenCalled();
  });

  it.each(['none', 'error', 'validating'] as const)('should disable editor actions when status is %s', (status) => {
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: status === 'none' ? '' : 'test-api-key',
      isToolConsoleLocked: true,
      status,
    });

    render(<UserRequestsPage />);

    expect(screen.getByPlaceholderText(/Describe the feedback/i)).toBeDisabled();
    expect(screen.getByTestId('task-id-selector')).toBeDisabled();
    expect(screen.getByRole('button', { name: /hold/i })).toBeDisabled();
    expect(screen.getByRole('button', { name: /queue/i })).toBeDisabled();
  });

  it('should keep editor inputs enabled when status is insufficient', () => {
    vi.mocked(useApiKey).mockReturnValue({
      apiKey: 'test-api-key',
      isToolConsoleLocked: false,
      status: 'insufficient',
    });

    render(<UserRequestsPage />);

    expect(screen.getByPlaceholderText(/Describe the feedback/i)).toBeEnabled();
    expect(screen.getByTestId('task-id-selector')).toBeEnabled();
    expect(screen.getByRole('button', { name: /hold/i })).toBeEnabled();
  });
});

describe('UserRequestsPage Command Template', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'test-api-key', isToolConsoleLocked: false, status: 'valid' });
    vi.mocked(api.listUserRequests).mockResolvedValue({ pending: [], consumed: [] });
  });

  it('should auto-expand and render the Command Template editor when server returns a non-default template', async () => {
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({
      return_mode: 'all',
      command_template: '<cmd>{{content}}</cmd>',
    });

    render(<UserRequestsPage />);

    // The editor should auto-expand because a custom template is saved.
    await waitFor(() => {
      expect(screen.getByRole('heading', { name: /command template/i })).toBeDefined();
    });

    await waitFor(() => {
      const textarea = screen.getByPlaceholderText(/Example:/i) as HTMLTextAreaElement;
      expect(textarea.value).toBe('<cmd>{{content}}</cmd>');
    });

    // Trigger label should reflect the expanded state.
    expect(screen.getByRole('button', { name: /hide command template/i })).toHaveAttribute('aria-expanded', 'true');
  });

  it('should render only the trigger on load when no custom template is set and keep the editor hidden', async () => {
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all', command_template: '' });

    render(<UserRequestsPage />);

    // Wait for preferences to finish loading so the trigger reflects the saved template.
    const trigger = await screen.findByRole('button', { name: /customize command template/i });
    expect(trigger).toHaveAttribute('aria-expanded', 'false');
    expect(trigger).toHaveAttribute('aria-controls', 'command-template-editor');

    // The editor must NOT be rendered yet.
    expect(screen.queryByPlaceholderText(/Example:/i)).toBeNull();
    expect(screen.queryByRole('heading', { name: /command template/i })).toBeNull();
  });

  it('should toggle the editor visibility when the trigger is clicked', async () => {
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all', command_template: '' });

    render(<UserRequestsPage />);

    const trigger = await screen.findByRole('button', { name: /customize command template/i });
    expect(screen.queryByPlaceholderText(/Example:/i)).toBeNull();

    fireEvent.click(trigger);

    // After expanding, the editor is visible.
    const textarea = (await screen.findByPlaceholderText(/Example:/i)) as HTMLTextAreaElement;
    expect(textarea).toBeDefined();
    expect(screen.getByRole('button', { name: /hide command template/i })).toHaveAttribute('aria-expanded', 'true');

    // Collapsing again hides the editor.
    fireEvent.click(screen.getByRole('button', { name: /hide command template/i }));
    expect(screen.queryByPlaceholderText(/Example:/i)).toBeNull();
    expect(screen.getByRole('button', { name: /customize command template/i })).toHaveAttribute('aria-expanded', 'false');
  });

  it('should preserve draft edits when the editor is collapsed and re-expanded', async () => {
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all', command_template: '' });

    render(<UserRequestsPage />);

    fireEvent.click(await screen.findByRole('button', { name: /customize command template/i }));

    const textarea = (await screen.findByPlaceholderText(/Example:/i)) as HTMLTextAreaElement;
    fireEvent.change(textarea, { target: { value: '<cmd>{{content}}</cmd>' } });
    expect(textarea.value).toBe('<cmd>{{content}}</cmd>');

    // Collapse.
    fireEvent.click(screen.getByRole('button', { name: /hide command template/i }));
    expect(screen.queryByPlaceholderText(/Example:/i)).toBeNull();

    // Re-expand; the draft should still be there.
    fireEvent.click(screen.getByRole('button', { name: /customize command template/i }));
    const reopened = (await screen.findByPlaceholderText(/Example:/i)) as HTMLTextAreaElement;
    expect(reopened.value).toBe('<cmd>{{content}}</cmd>');
  });

  it('should disable Save when template is missing the {{content}} placeholder', async () => {
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all', command_template: '' });

    render(<UserRequestsPage />);

    fireEvent.click(await screen.findByRole('button', { name: /customize command template/i }));

    const textarea = await screen.findByPlaceholderText(/Example:/i);
    fireEvent.change(textarea, { target: { value: 'no placeholder here' } });

    expect(screen.getByText(/must contain the placeholder/i)).toBeDefined();
    expect(screen.getByRole('button', { name: /^Save$/i })).toBeDisabled();
  });

  it('should call setCommandTemplateOnServer when Save is clicked with a valid template', async () => {
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all', command_template: '' });
    vi.mocked(api.setCommandTemplateOnServer).mockResolvedValue({
      return_mode: 'all',
      command_template: '<cmd>{{content}}</cmd>',
    });

    render(<UserRequestsPage />);

    fireEvent.click(await screen.findByRole('button', { name: /customize command template/i }));

    const textarea = await screen.findByPlaceholderText(/Example:/i);
    fireEvent.change(textarea, { target: { value: '<cmd>{{content}}</cmd>' } });

    const saveBtn = screen.getByRole('button', { name: /^Save$/i });
    expect(saveBtn).toBeEnabled();
    fireEvent.click(saveBtn);

    await waitFor(() => {
      expect(api.setCommandTemplateOnServer).toHaveBeenCalledWith('test-api-key', '<cmd>{{content}}</cmd>');
    });
  });

  it('should show the default {{content}} template and keep Save disabled when server returns empty template', async () => {
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all', command_template: '' });

    render(<UserRequestsPage />);

    fireEvent.click(await screen.findByRole('button', { name: /customize command template/i }));

    const textarea = (await screen.findByPlaceholderText(/Example:/i)) as HTMLTextAreaElement;
    await waitFor(() => {
      expect(textarea.value).toBe('{{content}}');
    });

    // Save should be disabled because the default is effectively equivalent to the empty saved state.
    expect(screen.getByRole('button', { name: /^Save$/i })).toBeDisabled();
  });

  it('should reset the textarea to {{content}} when Reset to default is clicked', async () => {
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all', command_template: '' });

    render(<UserRequestsPage />);

    fireEvent.click(await screen.findByRole('button', { name: /customize command template/i }));

    const textarea = (await screen.findByPlaceholderText(/Example:/i)) as HTMLTextAreaElement;
    await waitFor(() => {
      expect(textarea.value).toBe('{{content}}');
    });

    fireEvent.change(textarea, { target: { value: '<cmd>{{content}}</cmd>' } });
    expect(textarea.value).toBe('<cmd>{{content}}</cmd>');

    const resetBtn = screen.getByRole('button', { name: /reset to default/i });
    expect(resetBtn).toBeEnabled();
    fireEvent.click(resetBtn);

    expect(textarea.value).toBe('{{content}}');
  });
});

/** deferred returns a promise whose settlement the test controls. */
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

function makeRequest(id: string): api.UserRequest {
  return { id, content: `content ${id}`, status: 'consumed', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-01T00:00:00Z' } as api.UserRequest;
}

describe('UserRequestsPage background work', () => {
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
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'test-api-key', isToolConsoleLocked: false, status: 'valid' });
    vi.mocked(api.listUserRequests).mockResolvedValue({ pending: [], consumed: [] });
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all' });
  });

  afterEach(() => {
    vi.useRealTimers();
    Reflect.deleteProperty(document, 'visibilityState');
  });

  it('marks only timer-driven list polls as low priority', async () => {
    render(<UserRequestsPage />);
    await flush();

    expect(api.listUserRequests).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.listUserRequests).mock.calls[0][1]?.priority).toBeUndefined();

    await flush(5000);
    expect(api.listUserRequests).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.listUserRequests).mock.calls[1][1]).toEqual(expect.objectContaining({ priority: 'low' }));
  });

  it('does not poll the list while hidden and refreshes at default priority once visible', async () => {
    render(<UserRequestsPage />);
    await flush();
    expect(api.listUserRequests).toHaveBeenCalledTimes(1);

    setVisibility('hidden');
    await flush(60_000);
    expect(api.listUserRequests).toHaveBeenCalledTimes(1);

    setVisibility('visible');
    await flush();
    expect(api.listUserRequests).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.listUserRequests).mock.calls[1][1]?.priority).toBeUndefined();
  });

  it('chains hold-state polls after each response so a slow response never overlaps the next', async () => {
    vi.mocked(api.setHold).mockResolvedValue({ active: true, waiting: false, remaining_secs: 0 });
    const slow = deferred<api.HoldState>();
    vi.mocked(api.getHoldState).mockReturnValueOnce(slow.promise).mockResolvedValue({ active: true, waiting: true, remaining_secs: 19 });

    render(<UserRequestsPage />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: /hold/i }));
    await flush();

    await flush(1000);
    expect(api.getHoldState).toHaveBeenCalledTimes(1);
    const [, , signal, priority] = vi.mocked(api.getHoldState).mock.calls[0];
    expect(signal).toBeInstanceOf(AbortSignal);
    expect(priority).toBe('low');

    // While the first response is outstanding no further request may start.
    await flush(5000);
    expect(api.getHoldState).toHaveBeenCalledTimes(1);

    slow.resolve({ active: true, waiting: true, remaining_secs: 20 });
    await flush();
    await flush(999);
    expect(api.getHoldState).toHaveBeenCalledTimes(1);
    await flush(1);
    expect(api.getHoldState).toHaveBeenCalledTimes(2);
  });

  it('pauses hold-state polling while hidden and aborts the in-flight poll on unmount', async () => {
    vi.mocked(api.setHold).mockResolvedValue({ active: true, waiting: false, remaining_secs: 0 });
    vi.mocked(api.getHoldState).mockReturnValue(new Promise<api.HoldState>(() => undefined));

    const { unmount } = render(<UserRequestsPage />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: /hold/i }));
    await flush();

    setVisibility('hidden');
    await flush(10_000);
    expect(api.getHoldState).not.toHaveBeenCalled();

    setVisibility('visible');
    await flush();
    expect(api.getHoldState).toHaveBeenCalledTimes(1);
    const signal = vi.mocked(api.getHoldState).mock.calls[0][2];

    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it('debounces history search and ignores a stale response that arrives late', async () => {
    const stale = deferred<api.UserRequestSearchResponse>();
    vi.mocked(api.searchUserRequests)
      .mockReturnValueOnce(stale.promise)
      .mockResolvedValueOnce({ results: [makeRequest('fresh')] });

    render(<UserRequestsPage />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: /search history/i }));
    await flush();

    const input = screen.getByRole('textbox', { name: /search directive history/i });
    fireEvent.change(input, { target: { value: 'a' } });
    fireEvent.change(input, { target: { value: 'ab' } });
    await flush(249);
    expect(api.searchUserRequests).not.toHaveBeenCalled();

    await flush(1);
    expect(api.searchUserRequests).toHaveBeenCalledTimes(1);
    expect(api.searchUserRequests).toHaveBeenLastCalledWith('test-api-key', 'ab', expect.objectContaining({ limit: 50 }));
    const firstSignal = vi.mocked(api.searchUserRequests).mock.calls[0][2]?.signal;

    fireEvent.change(input, { target: { value: 'abc' } });
    expect(firstSignal?.aborted).toBe(true);
    await flush(250);
    expect(api.searchUserRequests).toHaveBeenCalledTimes(2);
    expect(api.searchUserRequests).toHaveBeenLastCalledWith('test-api-key', 'abc', expect.objectContaining({ limit: 50 }));

    // The superseded request resolves after the fresh one; its results must not win.
    stale.resolve({ results: [makeRequest('s1'), makeRequest('s2'), makeRequest('s3')] });
    await flush();

    expect(screen.getAllByText('Consumed')).toHaveLength(1);
    const count = screen.getByRole('status', { name: /search result count/i });
    expect(within(count).getByText('1')).toBeInTheDocument();
  });

  it('clearing the search cancels a pending debounced request', async () => {
    render(<UserRequestsPage />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: /search history/i }));
    await flush();

    const input = screen.getByRole('textbox', { name: /search directive history/i });
    fireEvent.change(input, { target: { value: 'abc' } });
    fireEvent.click(screen.getByRole('button', { name: /clear search/i }));
    await flush(1000);

    expect(api.searchUserRequests).not.toHaveBeenCalled();
  });
});

describe('UserRequestsPage accessibility', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'test-api-key', isToolConsoleLocked: false, status: 'valid' });
    vi.mocked(api.listUserRequests).mockResolvedValue({ pending: [], consumed: [makeRequest('c1')], total_consumed: 1 });
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all' });
  });

  it('exposes the return mode as pressed toggle buttons', async () => {
    render(<UserRequestsPage />);

    const group = screen.getByRole('group', { name: /return mode/i });
    const all = within(group).getByRole('button', { name: 'All commands' });
    const first = within(group).getByRole('button', { name: 'First (earliest) only' });
    expect(all).toHaveAttribute('aria-pressed', 'true');
    expect(first).toHaveAttribute('aria-pressed', 'false');

    fireEvent.click(first);
    expect(all).toHaveAttribute('aria-pressed', 'false');
    expect(first).toHaveAttribute('aria-pressed', 'true');
    await waitFor(() => expect(api.setReturnModeOnServer).toHaveBeenCalledWith('test-api-key', 'first'));
  });

  it('names the composer and icon-only controls and reports the description toggle state', async () => {
    render(<UserRequestsPage />);

    expect(screen.getByRole('textbox', { name: /create user directive/i })).toBeInTheDocument();

    const toggle = screen.getByRole('button', { name: /hide info/i });
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    expect(toggle).toHaveAttribute('aria-controls', 'user-requests-description');
    expect(document.getElementById('user-requests-description')).not.toBeNull();

    expect(screen.getByRole('button', { name: /search history/i })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: /choose what to delete/i })).toBeInTheDocument();
  });

  it('announces image attachment errors', async () => {
    render(<UserRequestsPage />);
    const textarea = screen.getByRole('textbox', { name: /create user directive/i });
    const files = Array.from({ length: 6 }, (_, i) => new File(['x'], `f${i}.txt`, { type: 'text/plain' }));
    fireEvent.paste(textarea, { clipboardData: { items: files.map((file) => ({ kind: 'file', getAsFile: () => file })) } });

    expect(await screen.findByRole('alert')).toHaveTextContent(/5 image limit reached/i);
  });
});
