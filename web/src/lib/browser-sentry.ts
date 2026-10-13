import type { BrowserOptions, ErrorEvent, EventHint } from '@sentry/react';
import { setUnexpectedRequestReporter, type UnexpectedRequestContext } from './private-error';
import { sanitizeBreadcrumb, sanitizeErrorEvent } from './sentry-privacy';

const HOST = 'mcp.laisky.com';

/** BrowserSentryConfig contains only explicit public deployment values. */
export type BrowserSentryConfig = { enabled?: string; site?: string; dsn?: string; environment?: string; release?: string };
type Settings = { dsn: string; environment: string; release: string };
type Sdk = Pick<typeof import('@sentry/react'), 'init' | 'captureException' | 'reactErrorHandler'>;
type ReactErrorInfo = Parameters<ReturnType<Sdk['reactErrorHandler']>>[1];
/** ErrorSource labels recognized application error paths without including request payloads. */
export type ErrorSource = 'bootstrap' | 'react-caught' | 'react-uncaught' | 'react-recoverable' | 'router' | 'request';

function resolveSettings(config: BrowserSentryConfig, hostname: string): Settings | undefined {
  if (hostname !== HOST || config.enabled !== 'true' || config.site !== 'mcp' ||
      !['development', 'staging', 'production'].includes(config.environment ?? '') ||
      !/^[a-f0-9]{7,40}$/.test(config.release ?? '') || !config.dsn || config.dsn.length > 512) return;
  try {
    const dsn = new URL(config.dsn);
    if (dsn.protocol !== 'https:' || !/^[a-f0-9]{32}$/.test(dsn.username) || dsn.password ||
        !/^o[0-9]+\.ingest(?:\.[a-z]+)?\.sentry\.io$/.test(dsn.hostname) || dsn.port ||
        !/^\/[0-9]{1,20}$/.test(dsn.pathname) || dsn.search || dsn.hash) return;
    return { dsn: config.dsn, environment: config.environment!, release: `mcp@${config.release}` };
  } catch { return; }
}

/** createBrowserReporter keeps optional instrumentation inert when config or the SDK is unavailable. */
export function createBrowserReporter(loadSdk: () => Promise<Sdk> = () => import('./sentry-sdk'), originAllowed = () => window.location.origin === 'https://mcp.laisky.com') {
  let initialization: Promise<void> | undefined;
  let sdk: Sdk | undefined;
  let ready = false;
  const reported = new WeakSet<object>();
  const primitiveReports = new Map<string, { source: ErrorSource; at: number }>();
  const acceptCapture = (error: unknown, source: ErrorSource): boolean => {
    if (!ready || !sdk || !originAllowed()) return false;
    if (source !== 'react-caught' && source !== 'router') return true;
    const kind = typeof error;
    if (!['string', 'number', 'boolean', 'undefined'].includes(kind) && error !== null) return true;
    const value = String(error);
    if (value.length > 65_536) return false;
    let first = 0x811c9dc5; let second = 0x9e3779b9;
    for (let index = 0; index < value.length; index++) {
      first = Math.imul(first ^ value.charCodeAt(index), 0x01000193); second = Math.imul(second ^ value.charCodeAt(index), 0x85ebca6b);
    }
    const key = `${kind}:${value.length}:${first >>> 0}:${second >>> 0}`;
    const now = Date.now();
    for (const [stored, item] of primitiveReports) if (now - item.at > 500) primitiveReports.delete(stored);
    const previous = primitiveReports.get(key);
    if (previous && previous.source !== source) return false;
    primitiveReports.delete(key); primitiveReports.set(key, { source, at: now });
    if (primitiveReports.size > 32) primitiveReports.delete(primitiveReports.keys().next().value!);
    return true;
  };
  return {
    init(config: BrowserSentryConfig, hostname: string): Promise<void> {
      if (!originAllowed()) return Promise.resolve();
      if (initialization) return initialization;
      const settings = resolveSettings(config, hostname);
      if (!settings) return Promise.resolve();
      initialization = (async () => {
        try {
          let timeout: ReturnType<typeof setTimeout> | undefined;
          let loaded: Sdk;
          try {
            loaded = await Promise.race([loadSdk(), new Promise<never>((_, reject) => { timeout = setTimeout(() => reject(new Error('Reporting startup timeout')), 3000); })]);
          } finally { clearTimeout(timeout); }
          if (!originAllowed()) return;
          const client = loaded.init({
            ...settings,
            // Keep SDK diagnostics defaults; automatic session envelopes are outside this errors-only feature.
            integrations: (defaults) => defaults.filter((integration) => integration.name !== 'BrowserSession'),
            transportOptions: { fetchOptions: { credentials: 'omit', referrerPolicy: 'no-referrer' } },
            dataCollection: {
              userInfo: false, cookies: false, httpHeaders: false, httpBodies: [], urlQueryParams: false,
              graphQL: { document: false, variables: false }, genAI: { inputs: false, outputs: false },
              databaseQueryData: false, queues: false, stackFrameVariables: false,
            },
            enhanceFetchErrorMessages: 'report-only',
            beforeSendLog: () => null,
            beforeSendMetric: () => null,
            beforeBreadcrumb: sanitizeBreadcrumb,
            beforeSend: (event: ErrorEvent, hint: EventHint) => {
              hint.attachments = [];
              if (!ready || !originAllowed()) return null;
              const original = hint.originalException;
              if (original && (typeof original === 'object' || typeof original === 'function')) {
                if (reported.has(original)) return null;
                reported.add(original);
              }
              return sanitizeErrorEvent(event, hint);
            },
          } satisfies BrowserOptions);
          if (!client) throw new Error('Reporting client unavailable');
          sdk = loaded; ready = true;
        } catch {
          console.warn('Browser error reporting could not initialize');
          sdk = undefined; ready = false;
        }
      })();
      return initialization;
    },
    isEnabled(): boolean { return ready && originAllowed(); },
    capture(error: unknown, source: ErrorSource, context?: UnexpectedRequestContext): void {
      if (!acceptCapture(error, source)) return;
      try {
        sdk!.captureException(error, { tags: { source, ...(context ? { operation: context.operation, ...(context.status === undefined ? {} : { http_status: String(context.status) }), ...(context.code === undefined ? {} : { error_code: String(context.code) }) } : {}) } });
      } catch { console.warn('Browser error reporting could not capture an error'); }
    },
    captureReact(error: unknown, info: ReactErrorInfo, source: ErrorSource): void {
      if (!acceptCapture(error, source)) return;
      try { sdk!.reactErrorHandler()(error, info); } catch { console.warn('Browser error reporting could not capture an error'); }
    },
  };
}

export const browserReporter = createBrowserReporter();
setUnexpectedRequestReporter((error, context) => browserReporter.capture(error, 'request', context));

/** initializeBrowserSentry requires explicit public config on the target MCP HTTPS origin. */
export function initializeBrowserSentry(): Promise<void> {
  if (window.location.origin !== 'https://mcp.laisky.com') return Promise.resolve();
  return browserReporter.init({ enabled: import.meta.env.VITE_SENTRY_ENABLED, site: import.meta.env.VITE_SENTRY_SITE,
    dsn: import.meta.env.VITE_SENTRY_DSN, environment: import.meta.env.VITE_SENTRY_ENVIRONMENT, release: import.meta.env.VITE_SENTRY_RELEASE }, window.location.hostname);
}

/** createRootErrorHandlers delegates React19 component-stack enrichment to the official SDK handler. */
export function createRootErrorHandlers(reporter: Pick<typeof browserReporter, 'captureReact'> = browserReporter, enabled = browserReporter.isEnabled()) {
  if (!enabled) return undefined;
  return {
    onCaughtError: (error: unknown, info: ReactErrorInfo) => reporter.captureReact(error, info, 'react-caught'),
    onUncaughtError: (error: unknown, info: ReactErrorInfo) => reporter.captureReact(error, info, 'react-uncaught'),
    onRecoverableError: (error: unknown, info: ReactErrorInfo) => reporter.captureReact(error, info, 'react-recoverable'),
  };
}
