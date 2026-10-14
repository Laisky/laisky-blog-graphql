import { afterEach, expect, it, vi } from 'vitest';
import type { BrowserOptions, ErrorEvent } from '@sentry/react';
import { createBrowserReporter } from './browser-sentry';

const config = { enabled: 'true', site: 'mcp', dsn: 'https://0123456789abcdef0123456789abcdef@o123.ingest.sentry.io/456', environment: 'staging', release: 'abcdef0123456789' };
function fakeSdk() {
  return { init: vi.fn<(options: BrowserOptions) => ReturnType<typeof import('@sentry/react')['init']>>().mockReturnValue({} as ReturnType<typeof import('@sentry/react')['init']>), captureException: vi.fn(), reactErrorHandler: vi.fn(() => vi.fn()), globalHandlersIntegration: vi.fn(() => ({ name: 'GlobalHandlers' })), dedupeIntegration: vi.fn(() => ({ name: 'Dedupe' })), makeFetchTransport: vi.fn<typeof import('@sentry/react')['makeFetchTransport']>(() => ({ send: vi.fn(async () => ({})), flush: vi.fn(async () => true) })) };
}
afterEach(() => vi.restoreAllMocks());

it('preserves ordinary diagnostics, lazy/vendor frames, function names, React context and Debug IDs', async () => {
  const sdk = fakeSdk(); const reporter = createBrowserReporter(async () => sdk, () => true);
  await reporter.init(config, 'mcp.laisky.com');
  const event: ErrorEvent = { exception: { values: [{ type: 'CustomRenderError', value: 'Cannot render the tool dashboard', stacktrace: { frames: [
    { filename: 'https://mcp.laisky.com/assets/app-AbCd1234.js', function: 'renderDashboard', lineno: 4 },
    { filename: 'https://mcp.laisky.com/assets/vendor-QwEr5678.js', function: 'dispatch', lineno: 2 },
  ] } }] }, contexts: { browser: { name: 'Firefox', version: '140' }, react: { componentStack: '\n at Dashboard' } },
  debug_meta: { images: [{ type: 'sourcemap', debug_id: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee', code_file: 'https://mcp.laisky.com/assets/app-AbCd1234.js' }] } };
  const clean = await sdk.init.mock.calls[0][0].beforeSend!(event, {});
  expect(clean).toMatchObject(event);
});

it('keeps useful HTTP breadcrumbs while sanitizing credential-bearing URLs', async () => {
  const sdk = fakeSdk(); const reporter = createBrowserReporter(async () => sdk, () => true);
  await reporter.init(config, 'mcp.laisky.com');
  const options = sdk.init.mock.calls[0][0];
  const clean = options.beforeBreadcrumb!({ category: 'fetch', type: 'http', data: { method: 'POST', status_code: 503, url: 'https://user:private@mcp.laisky.com/query?token=private#private' } }, {});
  expect(clean).toMatchObject({ category: 'fetch', data: { method: 'POST', status_code: 503, url: 'https://mcp.laisky.com/query' } });
  expect(options.defaultIntegrations).not.toBe(false);
});
