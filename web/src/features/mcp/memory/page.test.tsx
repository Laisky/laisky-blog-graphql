import '@testing-library/jest-dom/vitest';
import { render, screen } from '@testing-library/react';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { useApiKey } from '@/lib/api-key-context';

import { MemoryPage } from './page';

vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
}));

beforeEach(() => {
  window.localStorage.clear();
  vi.clearAllMocks();
});

/**
 * mockApiKeyState configures the API key hook for a specific console-locking status.
 * It accepts the API key status and returns nothing.
 */
function mockApiKeyState(status: 'none' | 'error' | 'validating' | 'valid' | 'insufficient') {
  vi.mocked(useApiKey).mockReturnValue({
    apiKey: status === 'none' ? '' : 'saved-key',
    disconnect: vi.fn(),
    history: [],
    isToolConsoleLocked: status === 'none' || status === 'error' || status === 'validating',
    keyEntries: [],
    remainQuota: null,
    removeFromHistory: vi.fn(),
    sessionId: 0,
    setAliasForKey: vi.fn(),
    setApiKey: vi.fn(),
    status,
    switchApiKey: vi.fn(),
    validateApiKey: vi.fn(),
  });
}

describe('MemoryPage tool console gating', () => {
  it.each(['none', 'error', 'validating'] as const)('disables memory controls when status is %s', (status) => {
    mockApiKeyState(status);

    render(<MemoryPage />);

    expect(screen.getByLabelText(/^Project$/i)).toBeDisabled();
    expect(screen.getByRole('button', { name: /run memory_before_turn/i })).toBeDisabled();
  });

  it.each(['valid', 'insufficient'] as const)('keeps memory controls enabled when status is %s', (status) => {
    mockApiKeyState(status);

    render(<MemoryPage />);

    expect(screen.getByLabelText(/^Project$/i)).toBeEnabled();
    expect(screen.getByRole('button', { name: /run memory_before_turn/i })).toBeEnabled();
  });

  it('defaults memory plugin to rag', () => {
    mockApiKeyState('valid');

    render(<MemoryPage />);

    expect(screen.getByLabelText(/^Memory Plugin$/i)).toHaveValue('rag');
  });

  it('restores the persisted memory plugin selection from localStorage', () => {
    window.localStorage.setItem(
      'mcp.memory.inputs.v1',
      JSON.stringify({
        memoryPlugin: 'pageindex',
        project: 'demo-project',
        sessionId: 'session-1',
        userId: 'user-1',
        turnId: 'turn-1',
        maxInputTok: 120000,
        baseInstructions: '',
        currentInputText: '[]',
        inputItemsText: '[]',
        outputItemsText: '[]',
        listPath: '',
        listDepth: 8,
        listLimit: 200,
      })
    );
    mockApiKeyState('valid');

    render(<MemoryPage />);

    expect(screen.getByLabelText(/^Memory Plugin$/i)).toHaveValue('pageindex');
  });
});
