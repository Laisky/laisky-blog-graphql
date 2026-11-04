import '@testing-library/jest-dom/vitest';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import * as privateErrors from '@/lib/private-error';
import { callMcpTool } from '../shared/mcp-api';
import { useApiKey } from '@/lib/api-key-context';

import { MemoryPage } from './page';

vi.mock('../shared/mcp-api', () => ({ callMcpTool: vi.fn() }));

vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
}));

afterEach(() => { cleanup(); privateErrors.setUnexpectedRequestReporter(); vi.restoreAllMocks(); });

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

    // The memory plugin control is a Radix Select whose trigger renders the
    // selected item's label, so assert on the displayed text rather than a
    // native form value.
    expect(screen.getByLabelText(/^Memory plugin$/i)).toHaveTextContent(/^RAG$/);
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

    expect(screen.getByLabelText(/^Memory plugin$/i)).toHaveTextContent(/^pageindex$/);
  });
});

describe('MemoryPage private result boundaries', () => {
  it.each([
    ['plain text', 'synthetic private memory tool result'],
    ['structured error text', JSON.stringify({ message: 'synthetic private memory tool result' })],
  ])('displays the original %s remote error while marking it and leaving expected failure unreported', async (_format, text) => {
    mockApiKeyState('valid');
    const marker = vi.spyOn(privateErrors, 'markPrivateError');
    const observer = vi.fn(); privateErrors.setUnexpectedRequestReporter(observer);
    vi.mocked(callMcpTool).mockResolvedValue({ isError: true, content: [{ type: 'text', text }] });
    render(<MemoryPage />);
    fireEvent.click(screen.getByRole('button', { name: /run memory_before_turn/i }));
    expect(await screen.findByText('synthetic private memory tool result')).toBeInTheDocument();
    expect(marker).toHaveBeenCalledTimes(1);
    const error = marker.mock.calls[0][0];
    expect(error.message).toBe('synthetic private memory tool result');
    expect(error.stack).toContain('callTool');
    expect(privateErrors.getPrivateErrorMessage(error)).toBe('Memory request failed');
    expect(observer).not.toHaveBeenCalled();
    expect(callMcpTool).toHaveBeenCalledTimes(1);
  });

  it('reports malformed private tool JSON once and preserves its native message in the UI', async () => {
    mockApiKeyState('valid');
    const observer = vi.fn(); privateErrors.setUnexpectedRequestReporter(observer);
    vi.mocked(callMcpTool).mockResolvedValue({ content: [{ type: 'text', text: 'synthetic private memory payload' }] });
    render(<MemoryPage />);
    fireEvent.click(screen.getByRole('button', { name: /run memory_before_turn/i }));
    await screen.findByText(/not valid JSON|Unexpected token|JSON at position|JSON\.parse/i);
    expect(observer).toHaveBeenCalledTimes(1);
    const error = observer.mock.calls[0][0] as Error;
    expect(error).toBeInstanceOf(SyntaxError);
    expect(screen.getByText(error.message)).toBeInTheDocument();
    expect(privateErrors.getPrivateErrorMessage(error)).toBe('Invalid memory JSON response');
    expect(observer).toHaveBeenCalledExactlyOnceWith(error, { operation: 'memory' });
  });

  it('preserves successful private result display without emitting telemetry', async () => {
    mockApiKeyState('valid');
    const observer = vi.fn(); privateErrors.setUnexpectedRequestReporter(observer);
    const inputItems = [{ role: 'user', content: 'synthetic private memory content' }];
    vi.mocked(callMcpTool).mockResolvedValue({ content: [{ type: 'text', text: JSON.stringify({ input_items: inputItems, context_token_count: 1 }) }] });
    render(<MemoryPage />);
    fireEvent.click(screen.getByRole('button', { name: /run memory_before_turn/i }));
    await screen.findByText('context_token_count: 1');
    expect(screen.getByLabelText('input_items (Result)')).toHaveValue(JSON.stringify(inputItems, null, 2));
    expect(observer).not.toHaveBeenCalled();
  });

  it('keeps private input validation local while preserving the original diagnostic', async () => {
    mockApiKeyState('valid');
    const marker = vi.spyOn(privateErrors, 'markPrivateError');
    const observer = vi.fn(); privateErrors.setUnexpectedRequestReporter(observer);
    render(<MemoryPage />);
    fireEvent.change(screen.getByLabelText('current_input (JSON array)'), { target: { value: 'synthetic private input is not JSON' } });
    fireEvent.click(screen.getByRole('button', { name: /run memory_before_turn/i }));
    await screen.findByText(/current_input is not valid JSON:/);
    expect(marker).toHaveBeenCalledTimes(1);
    const error = marker.mock.calls[0][0];
    expect(screen.getByText(error.message)).toBeInTheDocument();
    expect(privateErrors.getPrivateErrorMessage(error)).toBe('Invalid memory input');
    expect(observer).not.toHaveBeenCalled();
    expect(callMcpTool).not.toHaveBeenCalled();
  });
});
