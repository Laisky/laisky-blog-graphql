import '@testing-library/jest-dom/vitest';
import { useApiKey } from '@/lib/api-key-context';
import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
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
  getQuota: vi.fn().mockResolvedValue({ user_identity: 'u', used_bytes: 0, quota_bytes: 0, object_count: 0, ttl_days: 0 }),
}));

// Mock child components that might interfere or are complex
vi.mock('./hold-button', () => ({
  HoldButton: ({ disabled }: { disabled?: boolean }) => <button disabled={disabled}>Hold</button>,
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
