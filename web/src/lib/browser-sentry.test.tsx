import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, Component, type ReactNode } from 'react';
import { flushSync } from 'react-dom';
import { createRoot } from 'react-dom/client';
import type { BrowserOptions } from '@sentry/react';
import { browserReporter, initializeBrowserSentry, createBrowserReporter, createRootErrorHandlers } from './browser-sentry';

const HOST = 'mcp.laisky.com';
const config = { enabled: 'true', site: 'mcp', dsn: 'https://0123456789abcdef0123456789abcdef@o123.ingest.sentry.io/456', environment: 'staging', release: 'abcdef0123456789' };
function fakeSdk() {
  return { init: vi.fn<(options: BrowserOptions) => ReturnType<typeof import('@sentry/react')['init']>>().mockReturnValue({} as ReturnType<typeof import('@sentry/react')['init']>), captureException: vi.fn(), reactErrorHandler: vi.fn(() => vi.fn()) };
}
const reporterFor = (sdk = fakeSdk()) => createBrowserReporter(async () => sdk, () => true);
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.unstubAllEnvs(); vi.useRealTimers(); });

describe('deployment gating and SDK lifecycle', () => {
  it.each(['sso.laisky.com', 'cv.laisky.com', 'chat2.laisky.com', 'mcp.laisky.com.evil.test', 'localhost', 'MCP.LAISKY.COM'])('never loads the SDK for %s', async (host) => {
    const load = vi.fn(); await createBrowserReporter(load, () => true).init(config, host); expect(load).not.toHaveBeenCalled();
  });
  it.each(['enabled', 'site', 'dsn', 'environment', 'release'] as const)('stays disabled with missing %s', async (key) => {
    const load = vi.fn(); const reporter = createBrowserReporter(load, () => true);
    await reporter.init({ ...config, [key]: undefined }, HOST); reporter.capture(new Error('ordinary'), 'bootstrap');
    expect(load).not.toHaveBeenCalled(); expect(reporter.isEnabled()).toBe(false);
  });
  it.each([{ ...config, enabled: 'false' }, { ...config, site: 'sso' }, { ...config, environment: 'private@example.com' }, { ...config, release: 'secret-token' },
    { ...config, dsn: config.dsn + '?token=private' }, { ...config, dsn: config.dsn.replace('https:', 'http:') },
    { ...config, dsn: config.dsn.replace('@', ':secret@') }, { ...config, dsn: config.dsn.replace('ingest.sentry.io', 'evil.test') }])('rejects invalid public settings', async (invalid) => {
    const load = vi.fn(); await createBrowserReporter(load, () => true).init(invalid, HOST); expect(load).not.toHaveBeenCalled();
  });
  it.each(['http://mcp.laisky.com', 'https://mcp.laisky.com:8443', 'https://sso.laisky.com', 'https://mcp.laisky.com.evil.test'])('refuses initialization on origin %s', async (url) => {
    vi.stubGlobal('window', { location: new URL(url) }); const initialize = vi.spyOn(browserReporter, 'init');
    await initializeBrowserSentry(); expect(initialize).not.toHaveBeenCalled();
  });
  it.each(['https://mcp.laisky.com', 'https://mcp.laisky.com:443/path?token=private#fragment'])('accepts normalized target origin %s', async (url) => {
    vi.stubGlobal('window', { location: new URL(url) });
    for (const [key, value] of Object.entries(config)) vi.stubEnv(`VITE_SENTRY_${key.toUpperCase()}`, value);
    const initialize = vi.spyOn(browserReporter, 'init').mockResolvedValue(); await initializeBrowserSentry();
    expect(initialize).toHaveBeenCalledExactlyOnceWith(config, HOST);
  });
  it('refuses loading on a disallowed origin and rechecks after asynchronous SDK loading', async () => {
    const sdk = fakeSdk(); const load = vi.fn(async () => sdk);
    await createBrowserReporter(load, () => false).init(config, HOST); expect(load).not.toHaveBeenCalled();
    let allowed = true; let resolve!: (sdk: ReturnType<typeof fakeSdk>) => void;
    const reporter = createBrowserReporter(() => new Promise((done) => { resolve = done; }), () => allowed);
    const startup = reporter.init(config, HOST); allowed = false; resolve(sdk); await startup;
    expect(sdk.init).not.toHaveBeenCalled(); expect(reporter.isEnabled()).toBe(false);
  });
  it('initializes once, snapshots config and keeps official defaults excluding sessions', async () => {
    const sdk = fakeSdk(); const load = vi.fn(async () => sdk); const mutable = { ...config };
    const reporter = createBrowserReporter(load, () => true); const first = reporter.init(mutable, HOST);
    mutable.environment = 'private'; mutable.release = 'private'; await Promise.all([first, reporter.init(config, HOST)]);
    expect(load).toHaveBeenCalledTimes(1); expect(sdk.init).toHaveBeenCalledTimes(1);
    const options = sdk.init.mock.calls[0][0]; expect(options.environment).toBe('staging'); expect(options.release).toBe('mcp@abcdef0123456789');
    expect(options.defaultIntegrations).toBeUndefined(); expect(options.transport).toBeUndefined();
    const integrations = [{ name: 'BrowserSession' }, { name: 'GlobalHandlers' }, { name: 'LinkedErrors' }, { name: 'Dedupe' }, { name: 'Breadcrumbs' }];
    expect(typeof options.integrations === 'function' ? options.integrations(integrations) : []).toEqual(integrations.slice(1));
    expect(options.tracesSampleRate).toBeUndefined(); expect(options.dataCollection?.httpBodies).toEqual([]);
    expect(options.dataCollection?.frameContextLines).toBeUndefined(); expect(options.dataCollection?.stackFrameVariables).toBe(false);
    expect(options.beforeSendLog?.({} as never, {})).toBeNull(); expect(options.beforeSendMetric?.({} as never)).toBeNull();
    expect(options.beforeSendTransaction).toBeUndefined();
    expect(options.transportOptions?.fetchOptions).toEqual({ credentials: 'omit', referrerPolicy: 'no-referrer' });
    expect(options.enhanceFetchErrorMessages).toBe('report-only');
  });
  it('closes readiness/capture/send hooks when origin changes and clears attachments', async () => {
    const sdk = fakeSdk(); let allowed = true; const reporter = createBrowserReporter(async () => sdk, () => allowed);
    await reporter.init(config, HOST); allowed = false; reporter.capture(new Error('ordinary'), 'router');
    expect(reporter.isEnabled()).toBe(false); expect(sdk.captureException).not.toHaveBeenCalled();
    const hint = { attachments: [{ filename: 'private', data: 'private' }] };
    expect(sdk.init.mock.calls[0][0].beforeSend!({}, hint)).toBeNull(); expect(hint.attachments).toEqual([]);
  });
  it.each(['import', 'init', 'empty'] as const)('remains usable on SDK %s failure', async (failure) => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {}); const sdk = fakeSdk();
    if (failure === 'init') sdk.init.mockImplementation(() => { throw new Error('private-token'); });
    if (failure === 'empty') sdk.init.mockReturnValue(undefined);
    const reporter = createBrowserReporter(async () => { if (failure === 'import') throw new Error('private-token'); return sdk; }, () => true);
    await expect(reporter.init(config, HOST)).resolves.toBeUndefined(); await reporter.init(config, HOST);
    expect(reporter.isEnabled()).toBe(false); expect(() => reporter.capture('ordinary', 'bootstrap')).not.toThrow();
    expect(createRootErrorHandlers(reporter, reporter.isEnabled())).toBeUndefined();
    expect(JSON.stringify(warn.mock.calls)).not.toContain('private-token');
    if (failure !== 'import') expect(sdk.init.mock.calls[0][0].beforeSend!({}, {})).toBeNull();
  });
  it('bounds optional startup and never installs a late SDK client', async () => {
    vi.useFakeTimers(); vi.spyOn(console, 'warn').mockImplementation(() => {}); const sdk = fakeSdk(); let resolve!: (sdk: ReturnType<typeof fakeSdk>) => void;
    const reporter = createBrowserReporter(() => new Promise((done) => { resolve = done; }), () => true);
    const startup = reporter.init(config, HOST); await vi.advanceTimersByTimeAsync(3000); await startup; resolve(sdk); await Promise.resolve();
    expect(reporter.isEnabled()).toBe(false); expect(sdk.init).not.toHaveBeenCalled();
  });
  it('contains capture failure with a fixed diagnostic', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {}); const sdk = fakeSdk(); sdk.captureException.mockImplementation(() => { throw new Error('private'); });
    const reporter = reporterFor(sdk); await reporter.init(config, HOST); expect(() => reporter.capture(new Error('ordinary'), 'bootstrap')).not.toThrow();
    expect(warn).toHaveBeenCalledExactlyOnceWith('Browser error reporting could not capture an error');
  });
  it('deduplicates paired primitive sources without caching raw payload or suppressing independent repeats', async () => {
    vi.useFakeTimers(); const sdk = fakeSdk(); const reporter = reporterFor(sdk); await reporter.init(config, HOST);
    reporter.capture('ordinary', 'react-caught'); reporter.capture('ordinary', 'router'); expect(sdk.captureException).toHaveBeenCalledTimes(1);
    reporter.capture('ordinary', 'react-caught'); reporter.capture('distinct', 'router'); expect(sdk.captureException).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(501); reporter.capture('ordinary', 'router'); expect(sdk.captureException).toHaveBeenCalledTimes(4);
  });
});

describe('React19 root wiring', () => {
  it('uses caught callback once and preserves mounted boundary UI and component stack', async () => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true); const captureReact = vi.fn(); const error = new Error('Dashboard failed');
    class Boundary extends Component<{ children: ReactNode }, { failed: boolean }> {
      state = { failed: false }; static getDerivedStateFromError() { return { failed: true }; }
      render() { return this.state.failed ? <p>Existing fallback</p> : this.props.children; }
    }
    function Thrower(): ReactNode { throw error; }
    const container = document.createElement('div'); const root = createRoot(container, createRootErrorHandlers({ captureReact }, true));
    await act(async () => root.render(<Boundary><Thrower /></Boundary>)); expect(container.textContent).toBe('Existing fallback');
    expect(captureReact).toHaveBeenCalledTimes(1); expect(captureReact).toHaveBeenCalledWith(error, expect.objectContaining({ componentStack: expect.stringContaining('Thrower') }), 'react-caught');
    await act(async () => root.unmount());
  });
  it('reports an uncaught failure through a mounted React root', () => {
    const captureReact = vi.fn(); const error = new Error('Dashboard failed');
    function Thrower(): ReactNode { throw error; }
    const root = createRoot(document.createElement('div'), createRootErrorHandlers({ captureReact }, true));
    flushSync(() => root.render(<Thrower />));
    expect(captureReact).toHaveBeenCalledExactlyOnceWith(error, expect.objectContaining({ componentStack: expect.stringContaining('Thrower') }), 'react-uncaught');
    flushSync(() => root.unmount());
  });
  it('delegates uncaught and recoverable callbacks and leaves disabled defaults intact', () => {
    const captureReact = vi.fn(); const error = new Error('ordinary'); const info = { componentStack: 'at Dashboard' };
    const options = createRootErrorHandlers({ captureReact }, true)!; options.onUncaughtError(error, info); options.onRecoverableError(error, info);
    expect(captureReact).toHaveBeenNthCalledWith(1, error, info, 'react-uncaught'); expect(captureReact).toHaveBeenNthCalledWith(2, error, info, 'react-recoverable');
    expect(createRootErrorHandlers({ captureReact }, false)).toBeUndefined();
  });
});
