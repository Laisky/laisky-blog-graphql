import { afterEach, expect, it, vi } from 'vitest';
import * as Sentry from '@sentry/react';
import { createBrowserReporter } from './browser-sentry';

afterEach(async () => { await Sentry.close(1000); vi.restoreAllMocks(); });
it('uses the real pinned SDK to capture manual, unhandled error and rejection events with a memory-only transport', async () => {
  const envelopes: unknown[] = [];
  const reporter = createBrowserReporter(async () => ({ ...Sentry,
    makeFetchTransport: () => ({ send: async (envelope) => { envelopes.push(envelope); return { statusCode: 200 }; }, flush: async () => true }),
  }), 'https://mcp.laisky.com/assets/index-AbCd1234.js');
  await reporter.init({ enabled: 'true', site: 'mcp', dsn: 'https://0123456789abcdef0123456789abcdef@o123.ingest.sentry.io/456', environment: 'staging', release: 'abcdef0123456789' }, 'mcp.laisky.com');
  Sentry.setUser({ email: 'private@example.com', id: 'private' }); Sentry.setExtra('private', 'private');
  Sentry.getCurrentScope().addAttachment({ filename: 'private.txt', data: 'private' });
  const manual = new TypeError('private prompt'); manual.stack = 'TypeError: private prompt\n    at render (https://mcp.laisky.com/assets/index-AbCd1234.js?key=private#secret:10:2)';
  reporter.capture(manual, 'react-caught'); reporter.capture(manual, 'router');
  const unhandled = new RangeError('private tool args'); unhandled.stack = 'RangeError: private tool args\n    at run (https://mcp.laisky.com/assets/index-AbCd1234.js?prompt=private:12:2)';
  // Under jsdom, the SDK instruments globalThis; invoke its installed browser handler directly.
  globalThis.onerror?.call(window, 'private tool result', 'https://mcp.laisky.com/assets/index-AbCd1234.js', 12, 2, unhandled);
  const rejection = new Error('private history'); rejection.stack = 'Error: private history\n    at asyncRun (https://mcp.laisky.com/assets/index-AbCd1234.js?token=private:14:2)';
  const rejected = new Event('unhandledrejection'); Object.defineProperty(rejected, 'reason', { value: rejection });
  globalThis.onunhandledrejection?.call(window, rejected as PromiseRejectionEvent);
  Sentry.captureException(new Error('private internal SDK message'), { data: { __sentry__: true }, attachments: [{ filename: 'private.txt', data: 'private' }] });
  await Sentry.flush(1000);
  expect(envelopes).toHaveLength(4);
  const wire = JSON.stringify(envelopes); expect(wire).toContain('Browser application error'); expect(wire).toContain('mcp@abcdef0123456789');
  for (const forbidden of ['private', 'secret', '?key=', '?prompt=', '?token=', 'tool args', 'tool result', 'history']) expect(wire).not.toContain(forbidden);
  expect(wire).not.toContain('session'); expect(wire).not.toContain('transaction');
});
