import type { BrowserOptions, ErrorEvent, EventHint } from '@sentry/react';

const HOST = 'mcp.laisky.com';
const ERROR_TYPES = new Set(['Error', 'TypeError', 'ReferenceError', 'RangeError', 'SyntaxError', 'URIError', 'EvalError', 'AggregateError']);
const SOURCES = new Set(['bootstrap', 'react-caught', 'react-uncaught', 'react-recoverable', 'router']);

/** BrowserSentryConfig contains only explicit public deployment values. */
export type BrowserSentryConfig = {
  enabled?: string;
  site?: string;
  dsn?: string;
  environment?: string;
  release?: string;
};

type Settings = { dsn: string; environment: string; release: string };
type Sdk = Pick<typeof import('@sentry/react'), 'init' | 'captureException' | 'globalHandlersIntegration' | 'dedupeIntegration' | 'makeFetchTransport'>;
/** ErrorSource limits event labels to recognized application error paths. */
export type ErrorSource = 'bootstrap' | 'react-caught' | 'react-uncaught' | 'react-recoverable' | 'router';

/** resolveSettings requires deliberate deployment configuration for the exact MCP host. */
function resolveSettings(config: BrowserSentryConfig, hostname: string): Settings | undefined {
  if (hostname !== HOST || config.enabled !== 'true' || config.site !== 'mcp' ||
      !['development', 'staging', 'production'].includes(config.environment ?? '') ||
      !/^[a-f0-9]{7,40}$/.test(config.release ?? '') || !config.dsn || config.dsn.length > 512) return;
  try {
    const dsn = new URL(config.dsn);
    if (dsn.protocol !== 'https:' || !/^[a-f0-9]{32}$/.test(dsn.username) || dsn.password ||
        !/^o[0-9]+\.ingest(?:\.[a-z]+)?\.sentry\.io$/.test(dsn.hostname) || dsn.port ||
        !/^\/[0-9]+$/.test(dsn.pathname) || dsn.search || dsn.hash) return;
    return { dsn: config.dsn, environment: config.environment!, release: `mcp@${config.release}` };
  } catch { return; }
}

/** normalizeAssetUrl retains only built app assets; routes, credentials, queries and fragments never leave the browser. */
function normalizeAssetUrl(input: unknown): string | undefined {
  if (typeof input !== 'string' || input.length > 2048) return;
  try {
    const url = new URL(input, `https://${HOST}/`);
    if (url.protocol !== 'https:' || url.hostname !== HOST || url.port || url.username || url.password ||
        !/^\/assets\/(?:index|browser-sentry)-[A-Za-z0-9_-]{8}\.js$/.test(url.pathname)) return;
    return `https://${HOST}${url.pathname}`;
  } catch { return; }
}

/** safeAssetUrl accepts only the exact loaded entry asset, preventing fabricated secret-bearing hashes. */
export function safeAssetUrl(input: unknown, entryAssetUrl = import.meta.url): string | undefined {
  const filename = normalizeAssetUrl(input);
  return filename && filename === normalizeAssetUrl(entryAssetUrl) ? filename : undefined;
}

function safePosition(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0 && value <= 10_000_000 ? value : undefined;
}

/** projectErrorEvent constructs a bounded allowlist instead of recursively copying arbitrary app data. */
export function projectErrorEvent(event: ErrorEvent, settings: Pick<Settings, 'environment' | 'release'>, entryAssetUrl = import.meta.url): ErrorEvent | null {
  try {
    if (event.type && event.type !== 'error') return null;
    const values = event.exception?.values;
    if (!Array.isArray(values) || values.length === 0) return null;
    return {
      ...(typeof event.event_id === 'string' && /^[a-f0-9]{32}$/.test(event.event_id) ? { event_id: event.event_id } : {}),
      ...(typeof event.timestamp === 'number' && Number.isFinite(event.timestamp) ? { timestamp: event.timestamp } : {}),
      type: undefined,
      platform: 'javascript',
      level: 'error',
      environment: settings.environment,
      release: settings.release,
      tags: { site: 'mcp', ...(typeof event.tags?.source === 'string' && SOURCES.has(event.tags.source) ? { source: event.tags.source } : {}) },
      exception: { values: values.slice(0, 4).map((exception) => ({
        type: ERROR_TYPES.has(exception.type ?? '') ? exception.type : 'Error',
        // Error messages may contain prompts, MCP results, keys or personal data; none are forwarded.
        value: 'Browser application error',
        ...(exception.mechanism ? { mechanism: {
          type: ['onerror', 'onunhandledrejection'].includes(exception.mechanism.type) ? exception.mechanism.type : 'generic',
          handled: exception.mechanism.handled === true,
        } } : {}),
        stacktrace: { frames: (exception.stacktrace?.frames ?? []).slice(-20).flatMap((frame) => {
          const filename = safeAssetUrl(frame.filename, entryAssetUrl);
          if (!filename) return [];
          return [{ filename, lineno: safePosition(frame.lineno), colno: safePosition(frame.colno), in_app: true }];
        }) },
      })) },
    };
  } catch { return null; }
}


type Envelope = Parameters<ReturnType<NonNullable<BrowserOptions['transport']>>['send']>[0];

/** projectErrorEnvelope is the final privacy boundary, including SDK internal errors that bypass beforeSend. */
export function projectErrorEnvelope(envelope: Envelope, settings: Pick<Settings, 'environment' | 'release'>, entryAssetUrl = import.meta.url): Envelope | null {
  try {
    const items = envelope[1];
    if (!Array.isArray(items) || items.length > 10) return null;
    const events = items.filter((item) => item[0].type === 'event');
    if (events.length !== 1) return null;
    const clean = projectErrorEvent(events[0][1] as ErrorEvent, settings, entryAssetUrl);
    if (!clean) return null;
    // Every other item (including attachments, spans, logs, metrics, sessions and outcomes) is dropped.
    clean.event_id ??= crypto.randomUUID().replaceAll('-', '');
    return [{ sent_at: new Date().toISOString(), event_id: clean.event_id }, [[{ type: 'event' }, clean]]];
  } catch { return null; }
}

/** createBrowserReporter provides an inert, idempotent facade when deployment config or the SDK is unavailable. */
export function createBrowserReporter(loadSdk: () => Promise<Sdk> = () => import('./sentry-sdk'), entryAssetUrl = import.meta.url, originAllowed = () => window.location.origin === 'https://mcp.laisky.com') {
  let initialization: Promise<void> | undefined;
  let sdk: Sdk | undefined;
  let ready = false;
  let transportFailureLogged = false;
  const transportFailure = () => {
    if (!transportFailureLogged) { console.warn('Browser error reporting transport unavailable'); transportFailureLogged = true; }
  };
  const reported = new WeakSet<object>();
  const primitiveReports = new Map<string, { source: ErrorSource; at: number }>();
  const primitiveKey = (error: unknown): string | undefined => {
    const kind = typeof error;
    if (kind !== 'string' && kind !== 'number' && kind !== 'boolean' && kind !== 'undefined' && error !== null) return;
    const value = String(error);
    if (value.length > 65_536) return;
    // Two local non-cryptographic words identify cross-source duplicates; hashes are never emitted or used for security.
    let first = 0x811c9dc5; let second = 0x9e3779b9;
    for (let index = 0; index < value.length; index++) {
      const character = value.charCodeAt(index);
      first = Math.imul(first ^ character, 0x01000193); second = Math.imul(second ^ character, 0x85ebca6b);
    }
    return `${kind}:${value.length}:${first >>> 0}:${second >>> 0}`;
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
            loaded = await Promise.race([
              loadSdk(),
              new Promise<never>((_, reject) => { timeout = setTimeout(() => reject(new Error('Reporting startup timeout')), 3000); }),
            ]);
          } finally { clearTimeout(timeout); }
          if (!originAllowed()) return;
          const client = loaded.init({
            ...settings,
            transport: (options) => {
              const upstream = loaded.makeFetchTransport({ ...options, fetchOptions: { ...options.fetchOptions, credentials: 'omit', referrerPolicy: 'no-referrer' } });
              return {
                send: async (envelope) => {
                  const clean = ready && originAllowed() ? projectErrorEnvelope(envelope, settings, entryAssetUrl) : null;
                  if (!clean) return {};
                  try { return await upstream.send(clean); } catch { transportFailure(); return {}; }
                },
                flush: async (timeout) => {
                  try { return await upstream.flush(timeout); } catch { transportFailure(); return false; }
                },
              };
            },
            defaultIntegrations: false,
            integrations: [loaded.globalHandlersIntegration(), loaded.dedupeIntegration()],
            dataCollection: {
              userInfo: false, cookies: false, httpHeaders: false, httpBodies: [], urlQueryParams: false,
              graphQL: { document: false, variables: false }, genAI: { inputs: false, outputs: false },
              databaseQueryData: false, queues: false, stackFrameVariables: false, frameContextLines: 0,
            },
            enhanceFetchErrorMessages: false,
            sendClientReports: false,
            beforeSendLog: () => null,
            beforeSendMetric: () => null,
            tracePropagationTargets: [],
            maxBreadcrumbs: 0,
            beforeBreadcrumb: () => null,
            beforeSend: (event: ErrorEvent, hint: EventHint) => {
              hint.attachments = [];
              if (!ready || !originAllowed()) return null;
              const clean = projectErrorEvent(event, settings, entryAssetUrl);
              const original = hint.originalException;
              if (clean && original && (typeof original === 'object' || typeof original === 'function')) {
                if (reported.has(original)) return null;
                reported.add(original);
              }
              return clean;
            },
          } satisfies BrowserOptions);
          if (!client) throw new Error('Reporting client unavailable');
          sdk = loaded;
          ready = true;
        } catch {
          // Initialization may fail after handlers are attached. beforeSend stays closed and UI startup continues.
          console.warn('Browser error reporting could not initialize');
          sdk = undefined;
          ready = false;
        }
      })();
      return initialization;
    },
    isEnabled(): boolean { return ready && originAllowed(); },
    capture(error: unknown, source: ErrorSource): void {
      if (!sdk || !ready || !originAllowed()) return;
      if (typeof error === 'string' && error.length > 65_536) return;
      if (source === 'react-caught' || source === 'router') {
        const key = primitiveKey(error);
        if (key) {
          const now = Date.now();
          for (const [stored, report] of primitiveReports) if (now - report.at > 500) primitiveReports.delete(stored);
          const previous = primitiveReports.get(key);
          if (previous && previous.source !== source) return;
          primitiveReports.delete(key); primitiveReports.set(key, { source, at: now });
          if (primitiveReports.size > 32) primitiveReports.delete(primitiveReports.keys().next().value!);
        }
      }
      try { sdk.captureException(error, { tags: { source } }); } catch { console.warn('Browser error reporting could not capture an error'); }
    },
  };
}

export const browserReporter = createBrowserReporter();

/** initializeBrowserSentry initializes only explicitly opted-in builds on the exact MCP HTTPS origin. */
export function initializeBrowserSentry(): Promise<void> {
  if (window.location.origin !== 'https://mcp.laisky.com') return Promise.resolve();
  return browserReporter.init({
    enabled: import.meta.env.VITE_SENTRY_ENABLED,
    site: import.meta.env.VITE_SENTRY_SITE,
    dsn: import.meta.env.VITE_SENTRY_DSN,
    environment: import.meta.env.VITE_SENTRY_ENVIRONMENT,
    release: import.meta.env.VITE_SENTRY_RELEASE,
  }, window.location.hostname);
}

/** createRootErrorHandlers covers React 19 boundary, uncaught and recoverable failures through one reporter. */
export function createRootErrorHandlers(reporter: Pick<typeof browserReporter, 'capture'> = browserReporter, enabled = browserReporter.isEnabled()) {
  if (!enabled) return undefined;
  return {
    onCaughtError: (error: unknown) => reporter.capture(error, 'react-caught'),
    onUncaughtError: (error: unknown) => reporter.capture(error, 'react-uncaught'),
    onRecoverableError: (error: unknown) => reporter.capture(error, 'react-recoverable'),
  };
}
