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
  setReturnMode: vi.fn(),
}));

// Mock child components that might interfere or are complex
vi.mock('./hold-button', () => ({
  HoldButton: () => <button>Hold</button>,
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
  TaskIdSelector: () => <input data-testid="task-id-selector" />,
  useTaskIdHistory: () => ({ recordUsage: vi.fn() }),
}));

describe('UserRequestsPage Keyboard Behavior', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (useApiKey as any).mockReturnValue({ apiKey: 'test-api-key' });
    (api.listUserRequests as any).mockResolvedValue({ pending: [], consumed: [] });
    (api.getPreferencesFromServer as any).mockResolvedValue({ return_mode: 'all' });
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
    (api.createUserRequest as any).mockResolvedValue({ id: '1', status: 'pending' });
    render(<UserRequestsPage />);

    const textarea = screen.getByPlaceholderText(/Describe the feedback/i);
    fireEvent.change(textarea, { target: { value: 'test command' } });

    // Press Ctrl+Enter
    fireEvent.keyDown(textarea, { key: 'Enter', code: 'Enter', ctrlKey: true });

    await waitFor(() => {
      expect(api.createUserRequest).toHaveBeenCalledWith('test-api-key', 'test command', undefined);
    });
  });

  it('should trigger send on Meta+Enter (for Mac)', async () => {
    (api.createUserRequest as any).mockResolvedValue({ id: '1', status: 'pending' });
    render(<UserRequestsPage />);

    const textarea = screen.getByPlaceholderText(/Describe the feedback/i);
    fireEvent.change(textarea, { target: { value: 'test command' } });

    // Press Meta+Enter (Cmd+Enter)
    fireEvent.keyDown(textarea, { key: 'Enter', code: 'Enter', metaKey: true });

    await waitFor(() => {
      expect(api.createUserRequest).toHaveBeenCalledWith('test-api-key', 'test command', undefined);
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
    } as any);

    expect(api.createUserRequest).not.toHaveBeenCalled();
  });
});
