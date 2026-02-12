import { useApiKey } from '@/lib/api-key-context';
import { render, screen, waitFor } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import { getPreferencesFromServer } from '../user-requests/api';
import { SettingsPage } from './page';

// Mock the hook
vi.mock('@/lib/api-key-context', () => ({
  useApiKey: vi.fn(),
  normalizeApiKey: (k: string) => k.trim(),
}));

// Mock the ApiKeyInput component to simplify
vi.mock('@/components/api-key-input', () => ({
  ApiKeyInput: () => <div data-testid="api-key-input">Mocked AppKeyInput</div>,
}));

vi.mock('../user-requests/api', () => ({
  getPreferencesFromServer: vi.fn().mockResolvedValue({
    return_mode: 'all',
    available_tools: ['file_read', 'file_write'],
    disabled_tools: [],
  }),
  setDisabledToolsOnServer: vi.fn().mockResolvedValue({
    return_mode: 'all',
    available_tools: ['file_read', 'file_write'],
    disabled_tools: ['file_write'],
  }),
}));

describe('SettingsPage', () => {
  it('should show authenticated status when valid', () => {
    (useApiKey as any).mockReturnValue({
      apiKey: 'test-key',
      status: 'valid',
      remainQuota: 1234.56,
      history: [],
      disconnect: vi.fn(),
    });

    render(<SettingsPage />);
    expect(screen.getAllByText(/authenticated/i).length).toBeGreaterThan(0);
    expect(screen.getByText(/balance: 1,234.56/i)).toBeDefined();
  });

  it('should render mcp tools list when preferences loaded', async () => {
    (useApiKey as any).mockReturnValue({
      apiKey: 'test-key',
      status: 'valid',
      remainQuota: 1234,
      history: [],
      disconnect: vi.fn(),
    });

    render(<SettingsPage />);

    expect(screen.getByRole('heading', { name: 'MCP Tools' })).toBeDefined();
    await waitFor(() => {
      expect(getPreferencesFromServer).toHaveBeenCalled();
    });
  });

  it('should show insufficient balance status', () => {
    (useApiKey as any).mockReturnValue({
      apiKey: 'test-key',
      status: 'insufficient',
      remainQuota: 0,
      history: [],
      disconnect: vi.fn(),
    });

    render(<SettingsPage />);
    expect(screen.getAllByText(/insufficient balance/i).length).toBeGreaterThan(0);
    expect(screen.getByText(/quota is 0/i)).toBeDefined();
  });

  it('should show invalid status when error', () => {
    (useApiKey as any).mockReturnValue({
      apiKey: 'test-key',
      status: 'error',
      remainQuota: null,
      history: [],
      disconnect: vi.fn(),
    });

    render(<SettingsPage />);
    expect(screen.getByText(/invalid api key/i)).toBeDefined();
  });

  it('should show disconnect button when key is set', () => {
    const disconnect = vi.fn();
    (useApiKey as any).mockReturnValue({
      apiKey: 'test-key',
      status: 'valid',
      remainQuota: 1000,
      history: [],
      disconnect: disconnect,
    });

    render(<SettingsPage />);
    const disconnectBtn = screen.getByText(/disconnect & clear key/i);
    disconnectBtn.click();
    expect(disconnect).toHaveBeenCalled();
  });
});
