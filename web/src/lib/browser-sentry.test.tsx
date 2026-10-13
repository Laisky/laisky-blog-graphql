import { afterEach, describe, expect, it, vi } from 'vitest';
import { act, Component, type ReactNode } from 'react';
import { flushSync } from 'react-dom';
import { createRoot } from 'react-dom/client';
import type { BrowserOptions, ErrorEvent } from '@sentry/react';

import { browserReporter, initializeBrowserSentry, createBrowserReporter as buildBrowserReporter, createRootErrorHandlers, projectErrorEvent, projectErrorEnvelope, safeAssetUrl } from './browser-sentry';

// Unit and memory-transport tests explicitly isolate origin policy from jsdom's default origin.
const createBrowserReporter = (...args: Parameters<typeof buildBrowserReporter>) => buildBrowserReporter(args[0], args[1], args[2] ?? (() => true));
const HOST = 'mcp.laisky.com';
const config = { enabled: 'true', site: 'mcp', dsn: 'https://0123456789abcdef0123456789abcdef@o123.ingest.sentry.io/456', environment: 'staging', release: 'abcdef0123456789' };
const settings = { environment: 'staging', release: 'mcp@abcdef0123456789' };
const asset = 'https://mcp.laisky.com/assets/index-AbCd1234.js';
const event: ErrorEvent = { type: undefined, exception: { values: [{ type: 'TypeError', value: 'secret prompt', stacktrace: { frames: [{ filename: `${asset}?api_key=secret#bearer`, lineno: 42, colno: 9, vars: { token: 'secret' }, function: 'personal email' }] } }] } };

function fakeSdk() {
  return { init: vi.fn<(options: BrowserOptions) => ReturnType<typeof import('@sentry/react')['init']>>().mockReturnValue({} as ReturnType<typeof import('@sentry/react')['init']>), captureException: vi.fn(), globalHandlersIntegration: vi.fn(() => ({ name: 'GlobalHandlers' })), dedupeIntegration: vi.fn(() => ({ name: 'Dedupe' })), makeFetchTransport: vi.fn<typeof import('@sentry/react')['makeFetchTransport']>(() => ({ send: vi.fn(async () => ({})), flush: vi.fn(async () => true) })) };
}
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); vi.unstubAllEnvs(); vi.useRealTimers(); });

describe('deployment gating and SDK lifecycle', () => {
  it('closes capture, readiness, hooks and final transport when the origin no longer matches', async () => {
    const sdk = fakeSdk(); let allowed = true;
    const reporter = createBrowserReporter(async () => sdk, asset, () => allowed);
    await reporter.init(config, HOST); expect(reporter.isEnabled()).toBe(true);
    const options = sdk.init.mock.calls[0][0];
    const transport = options.transport!({} as Parameters<NonNullable<BrowserOptions['transport']>>[0]);
    allowed = false;
    expect(reporter.isEnabled()).toBe(false); reporter.capture(new Error('private'), 'bootstrap');
    expect(sdk.captureException).not.toHaveBeenCalled();
    const hint = { attachments: [{ filename: 'private', data: 'private' }] };
    expect(options.beforeSend!(event, hint)).toBeNull(); expect(hint.attachments).toEqual([]);
    await expect(transport.send([{}, [[{ type: 'event' }, event]]])).resolves.toEqual({});
    expect(sdk.makeFetchTransport.mock.results[0].value.send).not.toHaveBeenCalled();
  });
  it('refuses SDK loading before origin approval and rechecks origin after asynchronous loading', async () => {
    const sdk = fakeSdk(); const load = vi.fn(async () => sdk);
    await createBrowserReporter(load, asset, () => false).init(config, HOST); expect(load).not.toHaveBeenCalled();
    let allowed = true; let resolve!: (sdk: ReturnType<typeof fakeSdk>) => void;
    const reporter = createBrowserReporter(() => new Promise((done) => { resolve = done; }), asset, () => allowed);
    const startup = reporter.init(config, HOST); allowed = false; resolve(sdk); await startup;
    expect(sdk.init).not.toHaveBeenCalled(); expect(reporter.isEnabled()).toBe(false);
  });
  it.each(['http://mcp.laisky.com', 'https://mcp.laisky.com:8443', 'https://sso.laisky.com', 'https://mcp.laisky.com.evil.test'])('refuses browser initialization on origin %s', async (url) => {
    vi.stubGlobal('window', { location: new URL(url) });
    const initialize = vi.spyOn(browserReporter, 'init');
    await initializeBrowserSentry();
    expect(initialize).not.toHaveBeenCalled();
  });
  it.each(['https://mcp.laisky.com', 'https://mcp.laisky.com:443/path?private=secret#private'])('allows only the normalized target HTTPS origin %s', async (url) => {
    vi.stubGlobal('window', { location: new URL(url) });
    vi.stubEnv('VITE_SENTRY_ENABLED', 'true'); vi.stubEnv('VITE_SENTRY_SITE', 'mcp');
    vi.stubEnv('VITE_SENTRY_DSN', config.dsn); vi.stubEnv('VITE_SENTRY_ENVIRONMENT', config.environment);
    vi.stubEnv('VITE_SENTRY_RELEASE', config.release);
    const initialize = vi.spyOn(browserReporter, 'init').mockResolvedValue();
    await initializeBrowserSentry();
    expect(initialize).toHaveBeenCalledExactlyOnceWith(config, HOST);
  });

  it('deduplicates only paired primitive sources within 500ms, preserving same-source and distinct failures', async () => {
    vi.useFakeTimers(); const sdk = fakeSdk(); const reporter = createBrowserReporter(async () => sdk); await reporter.init(config, HOST);
    reporter.capture('private', 'react-caught'); reporter.capture('private', 'router');
    expect(sdk.captureException).toHaveBeenCalledTimes(1);
    reporter.capture('private', 'react-caught'); reporter.capture('distinct private', 'router');
    expect(sdk.captureException).toHaveBeenCalledTimes(3);
    await vi.advanceTimersByTimeAsync(501); reporter.capture('private', 'router'); expect(sdk.captureException).toHaveBeenCalledTimes(4);
    const long = 'private'.repeat(1000); reporter.capture(long, 'react-caught'); reporter.capture(long, 'router'); expect(sdk.captureException).toHaveBeenCalledTimes(5);
    reporter.capture('private'.repeat(10000), 'router'); expect(sdk.captureException).toHaveBeenCalledTimes(5);
    for (let index = 0; index < 33; index++) reporter.capture(`private-${index}`, 'react-caught');
    reporter.capture('private-0', 'router'); expect(sdk.captureException).toHaveBeenCalledTimes(39);
  });

  it.each(['throw', 'reject'] as const)('contains transport send %s and flush failures without SDK retry loops', async (failure) => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {}); const sdk = fakeSdk();
    const reporter = createBrowserReporter(async () => sdk); await reporter.init(config, HOST);
    const options = sdk.init.mock.calls[0][0]; const transport = options.transport!({} as Parameters<NonNullable<BrowserOptions['transport']>>[0]);
    const upstream = sdk.makeFetchTransport.mock.results[0].value;
    if (failure === 'throw') upstream.send.mockImplementation(() => { throw new Error('private'); });
    else upstream.send.mockRejectedValue(new Error('private'));
    upstream.flush.mockRejectedValue(new Error('private'));
    await expect(transport.send([{}, [[{ type: 'event' }, event]]])).resolves.toEqual({});
    await expect(transport.flush(1)).resolves.toBe(false);
    expect(warn).toHaveBeenCalledTimes(1); expect(warn).toHaveBeenCalledWith('Browser error reporting transport unavailable');
    expect(sdk.makeFetchTransport.mock.calls[0][0].fetchOptions).toEqual({ credentials: 'omit', referrerPolicy: 'no-referrer' });
    expect(() => reporter.capture(new Error('private'), 'bootstrap')).not.toThrow();
  });

  it('keeps reporting disabled when SDK init returns no client', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {});
    const sdk = fakeSdk(); sdk.init.mockReturnValue(undefined); const reporter = createBrowserReporter(async () => sdk);
    await reporter.init(config, HOST); expect(reporter.isEnabled()).toBe(false); reporter.capture('private', 'bootstrap');
    expect(sdk.captureException).not.toHaveBeenCalled();
  });
  it('bounds SDK loading and never initializes an import that resolves after timeout', async () => {
    vi.useFakeTimers(); vi.spyOn(console, 'warn').mockImplementation(() => {});
    const sdk = fakeSdk(); let resolve!: (sdk: ReturnType<typeof fakeSdk>) => void;
    const reporter = createBrowserReporter(() => new Promise((done) => { resolve = done; }));
    const startup = reporter.init(config, HOST); await vi.advanceTimersByTimeAsync(3000); await startup;
    expect(reporter.isEnabled()).toBe(false); resolve(sdk); await Promise.resolve();
    expect(sdk.init).not.toHaveBeenCalled();
  });
  it('clears attachment hints and preserves disabled React root defaults', async () => {
    const sdk = fakeSdk(); const reporter = createBrowserReporter(async () => sdk);
    expect(createRootErrorHandlers()).toBeUndefined(); await reporter.init(config, HOST);
    const hint = { attachments: [{ filename: 'private.txt', data: 'private' }] };
    (sdk.init.mock.calls[0][0] as BrowserOptions).beforeSend!(event, hint);
    expect(hint.attachments).toEqual([]);
  });

  it.each(['sso.laisky.com', 'cv.laisky.com', 'chat2.laisky.com', 'mcp.laisky.com.evil.test', 'localhost', 'MCP.LAISKY.COM'])('does not even load the SDK on %s', async (host) => {
    const load = vi.fn(); await createBrowserReporter(load).init(config, host); expect(load).not.toHaveBeenCalled();
  });
  it.each(['enabled', 'site', 'dsn', 'environment', 'release'] as const)('stays disabled with missing %s', async (key) => {
    const load = vi.fn(); const reporter = createBrowserReporter(load);
    await reporter.init({ ...config, [key]: undefined }, HOST); reporter.capture(new Error('private'), 'bootstrap');
    expect(load).not.toHaveBeenCalled();
  });
  it.each([
    { ...config, site: 'sso' }, { ...config, environment: 'private@example.com' }, { ...config, release: 'secret-token' },
    { ...config, dsn: config.dsn + '?token=private' }, { ...config, dsn: config.dsn.replace('https:', 'http:') },
    { ...config, dsn: config.dsn.replace('@', ':secret@') }, { ...config, dsn: config.dsn.replace('ingest.sentry.io', 'evil.test') },
  ])('rejects invalid explicit settings', async (invalid) => {
    const load = vi.fn(); await createBrowserReporter(load).init(invalid, HOST); expect(load).not.toHaveBeenCalled();
  });
  it('initializes once under concurrent calls and snapshots validated identifiers', async () => {
    const sdk = fakeSdk(); const load = vi.fn(async () => sdk); const mutable = { ...config };
    const reporter = createBrowserReporter(load); const first = reporter.init(mutable, HOST); mutable.environment = 'private'; mutable.release = 'private';
    await Promise.all([first, reporter.init(config, HOST)]);
    expect(load).toHaveBeenCalledTimes(1); expect(sdk.init).toHaveBeenCalledTimes(1);
    const options = sdk.init.mock.calls[0][0] as BrowserOptions;
    expect(options.environment).toBe('staging'); expect(options.release).toBe(settings.release);
    expect(options.defaultIntegrations).toBe(false); expect(options.integrations).toEqual([{ name: 'GlobalHandlers' }, { name: 'Dedupe' }]);
    expect(options.dataCollection).toEqual({ userInfo: false, cookies: false, httpHeaders: false, httpBodies: [], urlQueryParams: false,
      graphQL: { document: false, variables: false }, genAI: { inputs: false, outputs: false }, databaseQueryData: false, queues: false, stackFrameVariables: false, frameContextLines: 0 });
    expect(options.sendClientReports).toBe(false); expect(options.beforeSendLog).toBeDefined(); expect(options.beforeSendMetric).toBeDefined(); 
    expect(options.tracesSampleRate).toBeUndefined(); expect(options.tracePropagationTargets).toEqual([]); expect(options.maxBreadcrumbs).toBe(0);
    expect(options.beforeBreadcrumb?.({}, {})).toBeNull();
  });
  it('remains usable after an SDK import failure without printing failure payloads', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    const reporter = createBrowserReporter(async () => { throw new Error('private api key'); });
    await expect(reporter.init(config, HOST)).resolves.toBeUndefined(); expect(() => reporter.capture('private', 'bootstrap')).not.toThrow();
    expect(warn).toHaveBeenCalledWith('Browser error reporting could not initialize'); expect(JSON.stringify(warn.mock.calls)).not.toContain('private');
  });
  it('fails closed after partial SDK initialization and never retries or captures', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {}); const sdk = fakeSdk();
    sdk.init.mockImplementation(() => { throw new Error('private'); }); const reporter = createBrowserReporter(async () => sdk);
    await reporter.init(config, HOST); await reporter.init(config, HOST); reporter.capture('private', 'bootstrap');
    const options = sdk.init.mock.calls[0][0] as BrowserOptions;
    expect(options.beforeSend?.(event, {})).toBeNull(); expect(sdk.init).toHaveBeenCalledTimes(1); expect(sdk.captureException).not.toHaveBeenCalled();
  });
  it('captures a valid boundary error and drops duplicate manual/global reports', async () => {
    const sdk = fakeSdk(); const reporter = createBrowserReporter(async () => sdk); await reporter.init(config, HOST);
    const error = new TypeError('private'); reporter.capture(error, 'react-caught');
    expect(sdk.captureException).toHaveBeenCalledWith(error, { tags: { source: 'react-caught' } });
    const beforeSend = (sdk.init.mock.calls[0][0] as BrowserOptions).beforeSend!;
    expect(beforeSend(event, { originalException: error })).toMatchObject({ exception: { values: [{ value: 'Browser application error' }] } });
    expect(beforeSend(event, { originalException: error })).toBeNull();
    expect(beforeSend(event, { originalException: new Error('different') })).not.toBeNull();
  });
  it('contains capture failures and keeps caller behavior intact', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {}); const sdk = fakeSdk();
    sdk.captureException.mockImplementation(() => { throw new Error('private'); }); const reporter = createBrowserReporter(async () => sdk);
    await reporter.init(config, HOST); expect(() => reporter.capture(new Error('private'), 'bootstrap')).not.toThrow();
    expect(warn).toHaveBeenCalledWith('Browser error reporting could not capture an error');
  });
});

describe('strict outbound privacy projection', () => {
  it('guards internal event envelopes and strips headers and every attachment/non-error item', () => {
    const envelope = [{ event_id: '0123456789abcdef0123456789abcdef', trace: { private: 'private' }, sdk: { private: 'private' } },
      [[{ type: 'event' }, { ...event, extra: { private: 'private' }, user: { email: 'private' } }], [{ type: 'attachment', filename: 'private.txt' }, 'private'], [{ type: 'span' }, { private: 'private' }]]];
    const clean = projectErrorEnvelope(envelope as Parameters<typeof projectErrorEnvelope>[0], settings)!;
    expect(clean[1]).toHaveLength(1); expect(JSON.stringify(clean)).not.toContain('private'); expect(clean[0]).not.toHaveProperty('trace');
    expect(projectErrorEnvelope([{}, [[{ type: 'session' }, { private: 'private' }]]] as Parameters<typeof projectErrorEnvelope>[0], settings)).toBeNull();
    expect(projectErrorEnvelope([{}, [[{ type: 'event' }, { type: 'transaction', message: 'private' }]]] as Parameters<typeof projectErrorEnvelope>[0], settings)).toBeNull();
  });
  it('keeps the final transport closed after a failed partial initialization', async () => {
    vi.spyOn(console, 'warn').mockImplementation(() => {}); const sdk = fakeSdk();
    sdk.init.mockImplementation(() => { throw new Error('private'); }); const reporter = createBrowserReporter(async () => sdk);
    await reporter.init(config, HOST);
    const options = sdk.init.mock.calls[0][0]; const transport = options.transport!({} as Parameters<NonNullable<BrowserOptions['transport']>>[0]);
    await transport.send([{}, [[{ type: 'event' }, event]]]);
    expect(sdk.makeFetchTransport.mock.results[0].value.send).not.toHaveBeenCalled();
  });

  it('removes bodies, headers, cookies, auth, MCP args/results, prompts, history and user metadata everywhere', () => {
    const dirty = { ...event, message: 'private', request: { url: 'https://mcp.laisky.com/tools?key=private', headers: { Authorization: 'private', Cookie: 'private' }, data: 'private' },
      user: { email: 'private', id: 'private', ip_address: 'private' }, extra: { apiKey: 'private', toolArguments: 'private', toolResults: 'private', prompts: 'private', completions: 'private', history: 'private', localStorage: 'private' },
      contexts: { custom: { private: 'private' } }, breadcrumbs: [{ message: 'private' }], tags: { source: 'router', email: 'private' },
      debug_meta: { images: [{ type: 'private', code_file: 'private' }] }, fingerprint: ['private'] } as ErrorEvent;
    const projected = projectErrorEvent(dirty, settings, asset)!; const wire = JSON.stringify(projected);
    expect(projected.tags).toEqual({ site: 'mcp', source: 'router' }); expect(projected.exception?.values?.[0].stacktrace?.frames).toEqual([{ filename: asset, lineno: 42, colno: 9, in_app: true }]);
    for (const forbidden of ['private', 'secret', 'bearer', 'api_key', 'personal', 'Authorization', 'Cookie', 'request', 'user', 'extra', 'contexts', 'breadcrumbs', 'fingerprint', 'debug_meta']) expect(wire).not.toContain(forbidden);
    expect(projected.environment).toBe(settings.environment); expect(projected.release).toBe(settings.release);
  });
  it('bounds event size and drops non-errors, malformed input and unknown error names', () => {
    const frame = { filename: asset, lineno: -1, colno: Number.POSITIVE_INFINITY, function: 'private', vars: { private: true } };
    const dirty = { type: undefined, exception: { values: Array.from({ length: 100 }, () => ({ type: 'private', value: 'private'.repeat(10000), stacktrace: { frames: Array.from({ length: 100 }, () => frame) } })) } } as ErrorEvent;
    const projected = projectErrorEvent(dirty, settings, asset)!;
    expect(projected.exception?.values).toHaveLength(4); expect(projected.exception?.values?.[0].stacktrace?.frames).toHaveLength(20);
    expect(projected.exception?.values?.[0].type).toBe('Error'); expect(JSON.stringify(projected).length).toBeLessThan(14000);
    expect(projectErrorEvent({ type: 'transaction' } as unknown as ErrorEvent, settings)).toBeNull();
    expect(projectErrorEvent({ type: undefined, message: 'private' }, settings)).toBeNull();
    expect(projectErrorEvent(null as unknown as ErrorEvent, settings)).toBeNull();
  });
  it.each(['https://sso.laisky.com/assets/index-AbCd1234.js', 'https://mcp.laisky.com/private@example.com', 'data:private', 'blob:https://mcp.laisky.com/private',
    'https://user:private@mcp.laisky.com/assets/index-AbCd1234.js', 'https://mcp.laisky.com/assets/prompt-secret-AbCd1234.js',
    'https://mcp.laisky.com/assets/index-private-token.js', 'https://mcp.laisky.com/assets/index-secret12.js', '//evil.test/assets/index-AbCd1234.js'])('drops unsafe stack URLs %s', (url) => { expect(safeAssetUrl(url, asset)).toBeUndefined(); });
  it('strips query strings and fragments from same-origin approved bundle URLs', () => {
    expect(safeAssetUrl('/assets/index-AbCd1234.js?token=private#history', asset)).toBe(asset);
    expect(safeAssetUrl(asset + '?prompt=private', asset)).toBe(asset);
  });
});

describe('React 19 actual root behavior', () => {
  it('captures a caught render failure once while retaining boundary UI', async () => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true);
    const capture = vi.fn(); const error = new Error('private');
    class Boundary extends Component<{ children: ReactNode }, { failed: boolean }> {
      state = { failed: false }; static getDerivedStateFromError() { return { failed: true }; }
      render() { return this.state.failed ? <p>Existing fallback</p> : this.props.children; }
    }
    function Thrower(): ReactNode { throw error; }
    const container = document.createElement('div'); const root = createRoot(container, createRootErrorHandlers({ capture }, true));
    await act(async () => { root.render(<Boundary><Thrower /></Boundary>); });
    expect(container.textContent).toBe('Existing fallback'); expect(capture).toHaveBeenCalledTimes(1); expect(capture).toHaveBeenCalledWith(error, 'react-caught');
    await act(async () => root.unmount());
  });
  it('captures uncaught render errors through the actual root', async () => {
    const capture = vi.fn(); const error = new Error('private'); function Thrower(): ReactNode { throw error; }
    const root = createRoot(document.createElement('div'), createRootErrorHandlers({ capture }, true));
    flushSync(() => { root.render(<Thrower />); });
    expect(capture).toHaveBeenCalledWith(error, 'react-uncaught'); flushSync(() => root.unmount());
  });
  it('routes recoverable failures through the same reporter', () => {
    const capture = vi.fn(); const error = new Error('private'); createRootErrorHandlers({ capture }, true)!.onRecoverableError(error);
    expect(capture).toHaveBeenCalledWith(error, 'react-recoverable');
  });
});
