import { afterEach, expect, it, vi } from 'vitest';
import * as Sentry from '@sentry/react';
import { act, StrictMode, type ReactNode } from 'react';
import { createRoot } from 'react-dom/client';
import { flushSync } from 'react-dom';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { RouteError } from '@/pages/route-error';
import { browserReporter, createBrowserReporter, createRootErrorHandlers } from './browser-sentry';
import { markPrivateError, parsePrivateJson, requestWithUnexpectedError, setUnexpectedRequestReporter } from './private-error';

const config = { enabled: 'true', site: 'mcp', dsn: 'https://0123456789abcdef0123456789abcdef@o123.ingest.sentry.io/456', environment: 'staging', release: 'abcdef0123456789' };
type Envelope = Parameters<ReturnType<typeof Sentry.makeFetchTransport>['send']>[0];
async function memoryReporter() {
  const envelopes: Envelope[] = [];
  const reporter = createBrowserReporter(async () => ({ ...Sentry,
    init: (options) => Sentry.init({ ...options, transport: () => ({ send: async (envelope) => { envelopes.push(envelope); return { statusCode: 200 }; }, flush: async () => true }) }),
  }), () => true);
  await reporter.init(config, 'mcp.laisky.com');
  const events = () => envelopes.flatMap((envelope) => envelope[1].filter((item) => item[0].type === 'event').map((item) => item[1] as Sentry.ErrorEvent));
  return { reporter, envelopes, events };
}
const offline = (run: () => Promise<void>) => () => Sentry.withScope(new Sentry.Scope(), () => Sentry.withIsolationScope(new Sentry.Scope(), async () => {
  try { await run(); } finally {
    await Sentry.close(1000);
    // The browser stack strategy intentionally shares its isolation scope.
    for (const scope of [Sentry.getCurrentScope(), Sentry.getIsolationScope()]) {
      const data = scope.getScopeData();
      for (const key of Object.keys(data.extra)) scope.setExtra(key, undefined);
      for (const key of Object.keys(data.contexts)) scope.setContext(key, null);
      for (const key of Object.keys(data.tags)) scope.setTag(key, undefined);
      scope.setUser(null); scope.clearBreadcrumbs(); scope.clearAttachments();
    }
  }
}));
afterEach(async () => { await Sentry.close(1000); setUnexpectedRequestReporter((error, context) => browserReporter.capture(error, 'request', context)); vi.restoreAllMocks(); vi.unstubAllGlobals(); });

it('emits useful ordinary messages/full lazy frames/functions/React context/Debug IDs with SDK defaults', offline(async () => {
  const { reporter, envelopes, events } = await memoryReporter();
  Sentry.getClient()!.addEventProcessor((event) => ({ ...event, debug_meta: { images: [{ type: 'sourcemap', debug_id: 'aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee', code_file: 'https://mcp.laisky.com/assets/lazy-ZyXw4321.js' }] }, contexts: { ...event.contexts, browser: { name: 'Firefox', version: '140' } } }));
  const error = new TypeError('Cannot render the tool dashboard'); error.stack = 'TypeError: Cannot render the tool dashboard\n    at renderDashboard (https://mcp.laisky.com/assets/lazy-ZyXw4321.js:12:4)\n    at dispatch (https://cdn.example.test/vendor.js:90:3)';
  reporter.captureReact(error, { componentStack: '\n at Dashboard\n at Router' }, 'react-caught'); await Sentry.flush(1000);
  expect(events()).toHaveLength(1); const event = events()[0]; const wire = JSON.stringify(event);
  expect(wire).toContain('Cannot render the tool dashboard'); expect(wire).toContain('Dashboard'); expect(wire).toContain('renderDashboard'); expect(wire).toContain('dispatch');
  expect(wire).toContain('lazy-ZyXw4321.js'); expect(wire).toContain('vendor.js'); expect(event.debug_meta?.images?.[0].debug_id).toBe('aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee');
  expect(event.contexts?.browser?.name).toBe('Firefox'); expect(event.release).toBe('mcp@abcdef0123456789'); expect(envelopes.every((envelope) => envelope[1].every((item) => item[0].type === 'event'))).toBe(true);
}));

it.each([false, true])('filters marked raw inner response across SDK linked/React copies and truncation (long=%s)', (long) => offline(async () => {
  const { reporter, events } = await memoryReporter();
  const payload = long ? 'synthetic-private-response-'.repeat(40) : 'synthetic-private-response-data';
  const inner = markPrivateError(new Error(payload), 'MCP request failed');
  inner.stack = `Error: ${payload}\n    at decodeResponse (https://mcp.laisky.com/assets/lazy-ZyXw4321.js:8:3)`;
  const outer = new Error('Cannot initialize the dashboard', { cause: inner });
  outer.stack = 'Error: Cannot initialize the dashboard\n    at initializeDashboard (https://mcp.laisky.com/assets/index-AaBb1234.js:11:2)';
  Sentry.getClient()!.addEventProcessor((event) => ({ ...event, exception: { values: [...(event.exception?.values ?? []), { type: 'Error', value: `Error: ${payload}`.slice(0, 250) + (long ? '...' : '') }] } }));
  reporter.captureReact(outer, { componentStack: '\n at Dashboard' }, 'react-caught'); await Sentry.flush(1000);
  const wire = JSON.stringify(events()); expect(events()).toHaveLength(1); expect(wire).not.toContain('synthetic-private-response');
  expect(wire).toContain('MCP request failed'); expect(wire).toContain('Cannot initialize the dashboard'); expect(wire).toContain('Dashboard'); expect(wire).toContain('decodeResponse');
  expect(inner.message).toBe(payload);
})());

it('removes explicit user/headers/bodies/callback arguments/private aliases/attachments while keeping technical context', offline(async () => {
  const { reporter, envelopes, events } = await memoryReporter();
  Sentry.setUser({ id: 'synthetic-person', email: 'synthetic-person@example.test' });
  Sentry.setExtras({ api_token: 'synthetic-private-api', apiToken: 'synthetic-private-api2', apiKeys: ['synthetic-private-api3'], sso_token: 'synthetic-private-sso', rawResponse: 'synthetic-private-response', userId: 'synthetic-person', token_count: 42,
    arguments: ['synthetic-private-callback'], nested: { toolArguments: { prompt: 'synthetic-private-prompt' }, user_email: 'synthetic-person@example.test' } });
  Sentry.setContext('runtime', { name: 'browser', version: '1' });
  Sentry.getCurrentScope().addAttachment({ filename: 'synthetic-private.txt', data: 'synthetic-private-attachment' });
  Sentry.getClient()!.addEventProcessor((event) => ({ ...event, request: { url: 'https://user:synthetic-private@mcp.laisky.com/mcp?token=synthetic-private#secret', headers: { Authorization: 'synthetic-private', Cookie: 'synthetic-private' }, data: 'synthetic-private', cookies: { auth: 'synthetic-private' } } }));
  const error = new Error('Failed at https://user:synthetic-private@mcp.laisky.com/mcp?token=synthetic-private#secret');
  error.stack = 'Error: Failed\n at handleResponse (https://user:synthetic-private@mcp.laisky.com/assets/lazy-ZyXw4321.js?token=synthetic-private#secret:9:2)';
  reporter.capture(error, 'bootstrap'); await Sentry.flush(1000);
  const wire = JSON.stringify(envelopes); expect(wire).not.toContain('synthetic-private'); expect(wire).not.toContain('synthetic-person'); expect(wire).not.toContain('Authorization');
  expect(events()[0].extra?.token_count).toBe(42); expect(events()[0].contexts?.runtime?.name).toBe('browser'); expect(wire).toContain('handleResponse');
  expect(envelopes[0][1].map((item) => item[0].type)).toEqual(['event']);
}));

it('keeps safe navigation/network/console breadcrumbs and excludes private input, console objects and server-error logs', offline(async () => {
  const { reporter, events } = await memoryReporter();
  Sentry.addBreadcrumb({ category: 'navigation', data: { from: 'https://mcp.laisky.com/?token=synthetic-private', to: 'https://mcp.laisky.com/dashboard#synthetic-private' } });
  Sentry.addBreadcrumb({ category: 'fetch', data: { method: 'POST', status_code: 503, url: 'https://user:synthetic-private@mcp.laisky.com/mcp?prompt=synthetic-private', body: 'synthetic-private' } });
  console.info('Dashboard mounted'); console.error('graphql errors', { message: 'synthetic-private-response' }); console.warn('Unexpected http status', { rawResponse: 'synthetic-private-response', api_token: 'synthetic-private-key' });
  const input = document.createElement('textarea'); input.id = 'synthetic-private-input'; const click = new Event('click'); Object.defineProperty(click, 'target', { value: input });
  Sentry.addBreadcrumb({ category: 'ui.click', message: 'synthetic-private-input' }, { event: click });
  reporter.capture(new Error('Dashboard failed'), 'bootstrap'); await Sentry.flush(1000);
  const wire = JSON.stringify(events()); expect(wire).not.toContain('synthetic-private'); expect(wire).toContain('Dashboard mounted'); expect(wire).toContain('Unexpected http status'); expect(wire).toContain('graphql errors'); expect(wire).toContain('status_code'); expect(wire).toContain('/dashboard');
}));

it('captures global unhandled error/rejection once and removes a thrown response payload object', offline(async () => {
  const { events } = await memoryReporter(); const error = new RangeError('Unexpected dashboard range');
  globalThis.onerror?.call(window, error.message, 'https://mcp.laisky.com/assets/lazy-ZyXw4321.js', 10, 2, error);
  const rejected = new Event('unhandledrejection'); Object.defineProperty(rejected, 'reason', { value: { body: 'synthetic-private-response', text: 'synthetic-private-chat', code: 7 } });
  globalThis.onunhandledrejection?.call(window, rejected as PromiseRejectionEvent); await Sentry.flush(1000);
  expect(events()).toHaveLength(2); const wire = JSON.stringify(events()); expect(wire).toContain('Unexpected dashboard range'); expect(wire).not.toContain('synthetic-private'); expect(wire).toContain('Request response error');
}));

it('filters actual BrowserApiErrors callback arguments from a CustomEvent without losing the exception', offline(async () => {
  const { events } = await memoryReporter();
  const target = document.createElement('button');
  const prevent = (event: ErrorEvent) => event.preventDefault(); window.addEventListener('error', prevent);
  const callback = () => { throw new Error('Dashboard event handler failed'); };
  target.addEventListener('synthetic-test', callback);
  try {
    target.dispatchEvent(new CustomEvent('synthetic-test', { detail: { toolArguments: 'synthetic-private-callback' } }));
    await Sentry.flush(1000);
    expect(events()).toHaveLength(1); expect(JSON.stringify(events())).toContain('Dashboard event handler failed');
    expect(JSON.stringify(events())).not.toContain('synthetic-private-callback'); expect(events()[0].extra?.arguments).toBeUndefined();
  } finally { target.removeEventListener('synthetic-test', callback); window.removeEventListener('error', prevent); }
}));

it('reports actionable caught network/decode failures once without changing the original UI error', offline(async () => {
  const { reporter, events } = await memoryReporter(); setUnexpectedRequestReporter((error, context) => reporter.capture(error, 'request', context));
  const transportError = new TypeError('Failed to fetch'); const fetcher = vi.fn().mockRejectedValue(transportError);
  await expect(requestWithUnexpectedError('/mcp', {}, 'mcp', fetcher)).rejects.toBe(transportError); reporter.capture(transportError, 'bootstrap');
  expect(() => parsePrivateJson('synthetic-private-not-json', 'Invalid MCP JSON response')).toThrow(SyntaxError);
  await Sentry.flush(1000); expect(events()).toHaveLength(2); expect(JSON.stringify(events())).not.toContain('synthetic-private');
  expect(JSON.stringify(events())).toContain('Invalid MCP JSON response'); expect(events()[0].tags?.operation).toBe('mcp');
  const expected = new Response('synthetic-private-denied', { status: 401 }); await expect(requestWithUnexpectedError('/mcp', {}, 'mcp', vi.fn().mockResolvedValue(expected))).resolves.toBe(expected);
  await expect(requestWithUnexpectedError('/mcp', {}, 'mcp', vi.fn().mockRejectedValue(new DOMException('synthetic-private', 'AbortError')))).rejects.toThrow();
  await Sentry.flush(1000); expect(events()).toHaveLength(2);
}));

it('reports a mounted primitive router failure once across React caught hook and StrictMode fallback', offline(async () => {
  const { reporter, events } = await memoryReporter(); vi.spyOn(browserReporter, 'capture').mockImplementation(reporter.capture); vi.spyOn(console, 'error').mockImplementation(() => {});
  function Thrower(): ReactNode { throw 'Dashboard render failed'; }
  const router = createMemoryRouter([{ path: '/', element: <Thrower />, errorElement: <RouteError /> }]);
  const container = document.createElement('div'); const root = createRoot(container, createRootErrorHandlers(reporter, true));
  flushSync(() => root.render(<StrictMode><RouterProvider router={router} /></StrictMode>)); await act(async () => {}); await Sentry.flush(1000);
  expect(container.textContent).toContain('Page not found'); expect(events()).toHaveLength(1); expect(JSON.stringify(events())).toContain('Dashboard render failed'); flushSync(() => root.unmount());
}));
