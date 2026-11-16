import '@testing-library/jest-dom/vitest';
import { useApiKey } from '@/lib/api-key-context';
import { HttpStatusError } from '@/lib/http-error';
import { findDanglingIdRefs } from '@/test-utils/aria-idrefs';
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

  it.each(['99', '-1', 'abc'])('renders when the stored delete option index %j is not a valid option', (stored) => {
    localStorage.setItem('mcp_delete_consumed_option', stored);
    try {
      render(<UserRequestsPage />);
      expect(screen.getByPlaceholderText(/Describe the feedback/i)).toBeInTheDocument();
    } finally {
      localStorage.removeItem('mcp_delete_consumed_option');
    }
  });

  it('renders and remembers nothing when site storage is blocked', () => {
    const refused = () => {
      throw new DOMException('The operation is insecure.', 'SecurityError');
    };
    const getItem = vi.spyOn(Storage.prototype, 'getItem').mockImplementation(refused);
    const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(refused);
    try {
      render(<UserRequestsPage />);
      expect(screen.getByPlaceholderText(/Describe the feedback/i)).toBeInTheDocument();
    } finally {
      getItem.mockRestore();
      setItem.mockRestore();
    }
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

  it('keeps the default and does not send on the IME-confirming Ctrl+Enter that Safari reports as keyCode 229', async () => {
    render(<UserRequestsPage />);

    const textarea = screen.getByPlaceholderText(/Describe the feedback/i);
    fireEvent.change(textarea, { target: { value: '日本語' } });

    // fireEvent returns false when a handler called preventDefault(); the IME needs the default to commit its text.
    expect(fireEvent.keyDown(textarea, { key: 'Enter', code: 'Enter', ctrlKey: true, keyCode: 229 })).toBe(true);
    await act(async () => {});
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
    // The collapsed editor is not rendered, so the trigger must not reference it.
    expect(trigger).not.toHaveAttribute('aria-controls');

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

describe('UserRequestsPage quota readout', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'test-api-key', isToolConsoleLocked: false, status: 'valid' });
    vi.mocked(api.listUserRequests).mockResolvedValue({ pending: [], consumed: [] });
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all' });
  });

  it('shows the bytes unfinished uploads reserve, counted like admission counts them', async () => {
    vi.mocked(api.getQuota).mockResolvedValueOnce({
      user_identity: 'u',
      used_bytes: 1024 * 1024,
      reserved_bytes: 1024 * 1024,
      available_bytes: 2 * 1024 * 1024,
      quota_bytes: 4 * 1024 * 1024,
      object_count: 1,
      ttl_days: 7,
    });
    render(<UserRequestsPage />);
    expect(
      await screen.findByText('1.0 MiB / 4.0 MiB used + 1.0 MiB reserved by unfinished uploads (50.0%), images expire in 7 days'),
    ).toBeInTheDocument();
  });

  it('keeps the readout of servers that do not report reservations', async () => {
    vi.mocked(api.getQuota).mockResolvedValueOnce({
      user_identity: 'u',
      used_bytes: 1024 * 1024,
      quota_bytes: 4 * 1024 * 1024,
      object_count: 1,
      ttl_days: 7,
    });
    render(<UserRequestsPage />);
    expect(await screen.findByText('1.0 MiB / 4.0 MiB used (25.0%), images expire in 7 days')).toBeInTheDocument();
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

describe('UserRequestsPage preference restore', () => {
  const customTemplate = '<cmd>{{content}}</cmd>';

  const flush = (ms = 0) =>
    act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });

  const templateEditor = () => screen.queryByRole('textbox', { name: 'Command template' }) as HTMLTextAreaElement | null;
  const returnModeButton = (name: string) => within(screen.getByRole('group', { name: /return mode/i })).getByRole('button', { name });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'test-api-key', isToolConsoleLocked: false, status: 'valid' });
    vi.mocked(api.listUserRequests).mockResolvedValue({ pending: [], consumed: [] });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('restores saved preferences even when the first list refresh fails', async () => {
    vi.mocked(api.listUserRequests).mockRejectedValueOnce(new Error('list unavailable'));
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'first', command_template: customTemplate });
    render(<UserRequestsPage />);
    await flush();

    // The list failure must not discard the preferences that did load.
    expect(templateEditor()?.value).toBe(customTemplate);
    expect(returnModeButton('First (earliest) only')).toHaveAttribute('aria-pressed', 'true');

    // The list recovers on its own retry without asking for preferences again.
    await flush(8000);
    expect(api.listUserRequests).toHaveBeenCalledTimes(2);
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(1);
  });

  it('retries preferences that failed to load and restores them once the server answers', async () => {
    vi.mocked(api.getPreferencesFromServer)
      .mockRejectedValueOnce(new Error('preferences unavailable'))
      .mockResolvedValue({ return_mode: 'first', command_template: customTemplate });
    render(<UserRequestsPage />);
    await flush();

    // Until the saved template is known the editor stays read-only, so a save cannot
    // silently replace a template the user has not seen.
    fireEvent.click(screen.getByRole('button', { name: /customize command template/i }));
    expect(templateEditor()).toBeDisabled();
    expect(screen.getByText(/could not load your saved preferences/i)).toBeInTheDocument();

    await flush(2000);
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(2);
    expect(templateEditor()?.value).toBe(customTemplate);
    expect(templateEditor()).toBeEnabled();
    expect(returnModeButton('First (earliest) only')).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByText(/could not load your saved preferences/i)).toBeNull();

    // Once restored, preferences are not requested again.
    await flush(60_000);
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(2);
  });

  it('backs off between preference retries instead of retrying at a fixed rate', async () => {
    vi.mocked(api.getPreferencesFromServer).mockRejectedValue(new Error('preferences unavailable'));
    render(<UserRequestsPage />);
    await flush();
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(1);

    await flush(2000);
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(2);
    await flush(3999);
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(2);
    await flush(1);
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(3);
  });

  it('keeps a return mode the user picked while preferences were still loading', async () => {
    const prefs = deferred<api.UserPreferencesResponse>();
    vi.mocked(api.getPreferencesFromServer).mockReturnValue(prefs.promise);
    vi.mocked(api.setReturnModeOnServer).mockResolvedValue({ return_mode: 'all' });
    render(<UserRequestsPage />);
    await flush();

    fireEvent.click(returnModeButton('All commands'));
    await flush();
    expect(api.setReturnModeOnServer).toHaveBeenCalledWith('test-api-key', 'all');

    // The read was issued before the user's write, so its value is stale.
    prefs.resolve({ return_mode: 'first', command_template: customTemplate });
    await flush();
    expect(returnModeButton('All commands')).toHaveAttribute('aria-pressed', 'true');
    expect(api.setReturnMode).not.toHaveBeenCalledWith('first');
    // The template was not touched, so it is still restored.
    expect(templateEditor()?.value).toBe(customTemplate);
  });

  it('ignores preferences that arrive for an API key the page has switched away from', async () => {
    const stale = deferred<api.UserPreferencesResponse>();
    vi.mocked(api.getPreferencesFromServer).mockImplementation((key) =>
      key === 'old-key' ? stale.promise : Promise.resolve({ return_mode: 'all', command_template: '' })
    );
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'old-key', isToolConsoleLocked: false, status: 'valid' });
    const view = render(<UserRequestsPage />);
    await flush();

    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'new-key', isToolConsoleLocked: false, status: 'valid' });
    view.rerender(<UserRequestsPage />);
    await flush();

    stale.resolve({ return_mode: 'first', command_template: customTemplate });
    await flush();
    expect(screen.getByRole('button', { name: /customize command template/i })).toHaveAttribute('aria-expanded', 'false');
    expect(returnModeButton('All commands')).toHaveAttribute('aria-pressed', 'true');
  });

  it.each([
    [401, /api key was not accepted/i],
    [403, /not allowed to read/i],
    [404, /HTTP 404/],
  ])('stops retrying a final HTTP %i refusal and offers a manual retry', async (status, message) => {
    vi.mocked(api.getPreferencesFromServer).mockRejectedValue(new HttpStatusError('refused', status));
    render(<UserRequestsPage />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: /customize command template/i }));

    const alert = screen.getByRole('alert');
    expect(alert).toHaveTextContent(/could not load your saved preferences/i);
    expect(alert).toHaveTextContent(message);
    expect(within(alert).getByRole('button', { name: /^retry$/i })).toBeEnabled();
    // A final refusal is not retried on a timer, and saving stays blocked: the saved
    // template is still unknown, so a save could replace one the user has not seen.
    await flush(10 * 60_000);
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(1);
    expect(templateEditor()).toBeDisabled();
    expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled();
  });

  it('restores preferences after a manual retry of a final refusal', async () => {
    vi.mocked(api.getPreferencesFromServer)
      .mockRejectedValueOnce(new HttpStatusError('refused', 401))
      .mockResolvedValue({ return_mode: 'first', command_template: customTemplate });
    render(<UserRequestsPage />);
    await flush();

    fireEvent.click(within(screen.getByRole('alert')).getByRole('button', { name: /^retry$/i }));
    await flush();
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(2);
    expect(screen.queryByText(/could not load your saved preferences/i)).toBeNull();
    expect(templateEditor()?.value).toBe(customTemplate);
    expect(templateEditor()).toBeEnabled();
    expect(returnModeButton('First (earliest) only')).toHaveAttribute('aria-pressed', 'true');
  });

  it('keeps retrying a prolonged 503 with capped backoff, blocks saving, and recovers when the server answers', async () => {
    vi.mocked(api.getPreferencesFromServer).mockRejectedValue(new HttpStatusError('unavailable', 503));
    render(<UserRequestsPage />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: /customize command template/i }));

    // 2 + 4 + 8 + 16 s, then one retry every 30 s: 4 retries in the first 30 s and 9 more by 5 min.
    await flush(5 * 60_000);
    expect(vi.mocked(api.getPreferencesFromServer).mock.calls.length).toBeGreaterThanOrEqual(12);
    expect(vi.mocked(api.getPreferencesFromServer).mock.calls.length).toBeLessThanOrEqual(15);
    expect(screen.getByRole('status')).toHaveTextContent(/retrying/i);
    expect(templateEditor()).toBeDisabled();
    expect(screen.getByRole('button', { name: /^save$/i })).toBeDisabled();

    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'first', command_template: customTemplate });
    await flush(30_000);
    expect(templateEditor()?.value).toBe(customTemplate);
    expect(templateEditor()).toBeEnabled();
    expect(screen.queryByText(/could not load your saved preferences/i)).toBeNull();
  });

  it('lets the user retry at once instead of waiting out the backoff', async () => {
    vi.mocked(api.getPreferencesFromServer).mockRejectedValue(new HttpStatusError('unavailable', 503));
    render(<UserRequestsPage />);
    await flush(2000 + 4000 + 8000);
    const calls = vi.mocked(api.getPreferencesFromServer).mock.calls.length;

    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all', command_template: customTemplate });
    fireEvent.click(within(screen.getByRole('status')).getByRole('button', { name: /retry now/i }));
    await flush();
    expect(api.getPreferencesFromServer).toHaveBeenCalledTimes(calls + 1);
    expect(screen.queryByText(/could not load your saved preferences/i)).toBeNull();
  });
});

describe('UserRequestsPage API key switch', () => {
  const flush = (ms = 0) =>
    act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  const useKey = (apiKey: string) => vi.mocked(useApiKey).mockReturnValue({ apiKey, isToolConsoleLocked: false, status: 'valid' });
  const resultCards = () => screen.queryAllByText('Consumed');

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
    useKey('key-a');
    vi.mocked(api.listUserRequests).mockResolvedValue({ pending: [], consumed: [] });
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all' });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  /** typeSearch opens the history search and types a query, which starts the debounce. */
  const typeSearch = async (query: string) => {
    fireEvent.click(screen.getByRole('button', { name: /search history/i }));
    await flush();
    fireEvent.change(screen.getByRole('textbox', { name: /search directive history/i }), { target: { value: query } });
  };

  it('never sends a debounced search for the previous key after a direct switch', async () => {
    vi.mocked(api.searchUserRequests).mockResolvedValue({ results: [makeRequest('a-secret')] } as Awaited<ReturnType<typeof api.searchUserRequests>>);
    const view = render(<UserRequestsPage />);
    await flush();
    await typeSearch('deploy');

    useKey('key-b');
    view.rerender(<UserRequestsPage />);
    await flush(1000);

    expect(api.searchUserRequests).not.toHaveBeenCalledWith('key-a', expect.anything(), expect.anything());
    expect(resultCards()).toHaveLength(0);
  });

  it('aborts an in-flight search for the previous key and never shows its results', async () => {
    const pending = deferred<Awaited<ReturnType<typeof api.searchUserRequests>>>();
    vi.mocked(api.searchUserRequests).mockReturnValue(pending.promise);
    const view = render(<UserRequestsPage />);
    await flush();
    await typeSearch('deploy');
    await flush(250);
    expect(api.searchUserRequests).toHaveBeenCalledWith('key-a', 'deploy', expect.anything());
    const { signal } = vi.mocked(api.searchUserRequests).mock.calls[0][2] as { signal: AbortSignal };

    useKey('key-b');
    view.rerender(<UserRequestsPage />);
    await flush();
    expect(signal.aborted).toBe(true);

    pending.resolve({ results: [makeRequest('a-secret')] } as Awaited<ReturnType<typeof api.searchUserRequests>>);
    await flush();
    expect(resultCards()).toHaveLength(0);
  });

  it('never appends a load-more page fetched for the previous key', async () => {
    vi.mocked(api.listUserRequests).mockImplementation(async (key, options) => {
      if (key === 'key-a' && !options?.cursor) {
        return { pending: [], consumed: Array.from({ length: 10 }, (_, i) => makeRequest(`a${i}`)), total_consumed: 20 };
      }
      return { pending: [], consumed: [] };
    });
    const view = render(<UserRequestsPage />);
    await flush();
    expect(resultCards()).toHaveLength(10);

    const more = deferred<Awaited<ReturnType<typeof api.listUserRequests>>>();
    vi.mocked(api.listUserRequests).mockImplementation(async (key, options) => (key === 'key-a' && options?.cursor ? more.promise : { pending: [], consumed: [] }));
    fireEvent.click(screen.getByRole('button', { name: /load more/i }));
    await flush();

    useKey('key-b');
    view.rerender(<UserRequestsPage />);
    await flush();
    expect(resultCards()).toHaveLength(0);

    more.resolve({ pending: [], consumed: [makeRequest('a10'), makeRequest('a11')], total_consumed: 20 });
    await flush();
    expect(resultCards()).toHaveLength(0);
  });
});

describe('UserRequestsPage polling backoff', () => {
  const flush = (ms = 0) =>
    act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] });
    vi.mocked(useApiKey).mockReturnValue({ apiKey: 'test-api-key', isToolConsoleLocked: false, status: 'valid' });
    vi.mocked(api.listUserRequests).mockResolvedValue({ pending: [], consumed: [] });
    vi.mocked(api.getPreferencesFromServer).mockResolvedValue({ return_mode: 'all' });
    // Take the top of each jitter range so the schedules below are exact.
    vi.spyOn(Math, 'random').mockReturnValue(0.999999);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('backs off list refreshes while they keep failing and returns to the 5 s cadence once one loads', async () => {
    vi.mocked(api.listUserRequests).mockRejectedValue(new TypeError('Failed to fetch'));
    render(<UserRequestsPage />);
    await flush();
    expect(api.listUserRequests).toHaveBeenCalledTimes(1);
    await flush(8000);
    expect(api.listUserRequests).toHaveBeenCalledTimes(2);
    await flush(15_999);
    expect(api.listUserRequests).toHaveBeenCalledTimes(2);
    await flush(1);
    expect(api.listUserRequests).toHaveBeenCalledTimes(3);
    // Loads at 0, 8, 24 and 56 s, then once a minute: 13 loads in 10 minutes instead of 76.
    await flush(600_000 - 24_000);
    expect(api.listUserRequests).toHaveBeenCalledTimes(13);

    vi.mocked(api.listUserRequests).mockResolvedValue({ pending: [], consumed: [] });
    await flush(60_000);
    expect(api.listUserRequests).toHaveBeenCalledTimes(14);
    await flush(5000);
    expect(api.listUserRequests).toHaveBeenCalledTimes(15);

    // A success starts the backoff over: the next failure is retried after 8 s again.
    vi.mocked(api.listUserRequests).mockRejectedValue(new TypeError('Failed to fetch'));
    await flush(1000);
    expect(api.listUserRequests).toHaveBeenCalledTimes(16);
    await flush(7999);
    expect(api.listUserRequests).toHaveBeenCalledTimes(16);
    await flush(1);
    expect(api.listUserRequests).toHaveBeenCalledTimes(17);
  });

  it('backs off hold-state polls while they keep failing and returns to once a second when one answers', async () => {
    vi.mocked(api.setHold).mockResolvedValue({ active: true, waiting: false, remaining_secs: 300 });
    vi.mocked(api.getHoldState).mockRejectedValue(new TypeError('Failed to fetch'));
    render(<UserRequestsPage />);
    await flush();
    fireEvent.click(screen.getByRole('button', { name: 'Hold' }));
    await flush();
    expect(api.setHold).toHaveBeenCalledTimes(1);

    // Polls at 1 s, then 2, 4, 8 and 16 s apart, then every 30 s (capped): 7 polls in 2 minutes instead of 120.
    await flush(1000);
    expect(api.getHoldState).toHaveBeenCalledTimes(1);
    await flush(1999);
    expect(api.getHoldState).toHaveBeenCalledTimes(1);
    await flush(1);
    expect(api.getHoldState).toHaveBeenCalledTimes(2);
    await flush(120_000 - 3000);
    expect(api.getHoldState).toHaveBeenCalledTimes(7);

    // The 8th poll (at 121 s) answers, so the next one follows a second later.
    vi.mocked(api.getHoldState).mockResolvedValue({ active: true, waiting: false, remaining_secs: 200 });
    await flush(1000);
    expect(api.getHoldState).toHaveBeenCalledTimes(8);
    await flush(1000);
    expect(api.getHoldState).toHaveBeenCalledTimes(9);
    await flush(1000);
    expect(api.getHoldState).toHaveBeenCalledTimes(10);

    // A success starts the backoff over: the next failure is retried after 2 s again.
    vi.mocked(api.getHoldState).mockRejectedValue(new TypeError('Failed to fetch'));
    await flush(1000);
    expect(api.getHoldState).toHaveBeenCalledTimes(11);
    await flush(1999);
    expect(api.getHoldState).toHaveBeenCalledTimes(11);
    await flush(1);
    expect(api.getHoldState).toHaveBeenCalledTimes(12);
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

  it('never references a collapsed panel that is not rendered', async () => {
    render(<UserRequestsPage />);
    await screen.findByRole('button', { name: /choose what to delete/i });
    const description = screen.getByRole('button', { name: /hide info/i });
    const template = screen.getByRole('button', { name: /customize command template/i });
    // The template editor starts collapsed and is not rendered.
    expect(template).toHaveAttribute('aria-expanded', 'false');
    expect(findDanglingIdRefs()).toEqual([]);

    fireEvent.click(description);
    expect(description).toHaveAttribute('aria-expanded', 'false');
    expect(findDanglingIdRefs()).toEqual([]);

    // Expanded panels are referenced again.
    fireEvent.click(description);
    fireEvent.click(template);
    expect(description).toHaveAttribute('aria-controls', 'user-requests-description');
    expect(template).toHaveAttribute('aria-controls', 'command-template-editor');
    expect(findDanglingIdRefs()).toEqual([]);
  });

  it('announces image attachment errors', async () => {
    render(<UserRequestsPage />);
    const textarea = screen.getByRole('textbox', { name: /create user directive/i });
    const files = Array.from({ length: 6 }, (_, i) => new File(['x'], `f${i}.txt`, { type: 'text/plain' }));
    fireEvent.paste(textarea, { clipboardData: { items: files.map((file) => ({ kind: 'file', getAsFile: () => file })) } });

    expect(await screen.findByRole('alert')).toHaveTextContent(/5 image limit reached/i);
  });
});
