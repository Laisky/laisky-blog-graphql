import type { Breadcrumb, BreadcrumbHint, ErrorEvent, EventHint } from '@sentry/react';
import { getPrivateErrorMessage, getPrivateErrorRedactions } from './private-error';

const PRIVATE_FIELDS = /^(?:authorization|proxy-authorization|(?:http[_-]?|request[_-]?|response[_-]?)?headers|cookies?|set-cookie|(?:api|sso|session|access|refresh)[_-]?(?:tokens?|keys?)|token|password|passwd|secret|jwt|bearer|local[_-]?storage|prompts?|completions?|messages|chat[_-]?history|history|arguments_?|tool[_-]?(?:arguments|results?)|structured[_-]?content|raw[_-]?(?:response|request|payload)|content|text|answer|command_template|file_content|request[_-]?body|response[_-]?body|body|input|output|private|user[_-]?(?:id|name|email|identity|metadata|prefix)|ai_identity|username|email|phone|ip_address|profile|user)$/i;
const PRIVATE_CONSOLE = new Set(['graphql errors', 'failed to validate api key', 'Failed to load runtime config', 'Failed to submit answer:', 'Failed to prepare MCP Inspector defaults:', 'Failed to set MCP Inspector default preference']);
const URL_FIELDS = new Set(['url', 'filename', 'abs_path', 'code_file', 'from', 'to', 'referrer', 'referer']);

/** sanitizeUrl retains ordinary source/route paths while removing URL credentials, queries and fragments. */
export function sanitizeUrl(value: string): string {
  if (/^data:/i.test(value)) return '[inline source]';
  if (/^blob:/i.test(value)) return 'blob:' + sanitizeUrl(value.slice(5));
  if (value.startsWith('//')) return sanitizeUrl('https:' + value);
  if (!/^(?:https?|wss?|blob):/i.test(value)) return value.split(/[?#]/, 1)[0];
  try {
    const url = new URL(value);
    url.username = ''; url.password = ''; url.search = ''; url.hash = '';
    return url.toString();
  } catch { return '[invalid URL]'; }
}

/** sanitizeText filters credential shapes and embedded credential-bearing URLs without replacing ordinary messages. */
export function sanitizeText(value: string): string {
  return value
    .replace(/\b(?:https?|wss?):\/\/[^\s"'<>]+/gi, (match) => {
      const tail = match.match(/[),.;\]]+$/)?.[0] ?? '';
      return sanitizeUrl(tail ? match.slice(0, -tail.length) : match) + tail;
    })
    .replace(/\b(?:Bearer|Basic)\s+[^\s,;"']+/gi, (match) => match.split(/\s/, 1)[0] + ' [Filtered]')
    .replace(/\bsk-[A-Za-z0-9_-]{6,}/g, '[Filtered]')
    .replace(/\beyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b/g, '[Filtered]')
    .replace(/\b((?:api|sso|session|access|refresh)[_-]?(?:tokens?|keys?)|token|password|secret|authorization|cookie)\s*[:=]\s*(?:"[^"]*"|'[^']*'|[^\s,;]+)/gi, '$1=[Filtered]');
}

/** sanitizeData removes known payload/credential fields, retaining ordinary bounded diagnostic metadata. */
function sanitizeData(value: unknown, depth = 0, seen = new WeakSet<object>(), budget = { nodes: 2048, entries: 4096 }): unknown {
  if (--budget.nodes < 0) return '[Truncated]';
  if (typeof value === 'string') return sanitizeText(value);
  if (!value || typeof value !== 'object') return value;
  if (depth > 8 || seen.has(value)) return '[Truncated]';
  seen.add(value);
  if (value instanceof Error) return { name: value.name, message: getPrivateErrorMessage(value) ?? sanitizeText(value.message) };
  if (Array.isArray(value)) return value.slice(0, 100).map((item) => sanitizeData(item, depth + 1, seen, budget));
  const clean: Record<string, unknown> = {};
  let entries = 0;
  for (const key in value) {
    if (!Object.prototype.hasOwnProperty.call(value, key)) continue;
    if (entries++ >= 100 || --budget.entries < 0) break;
    const descriptor = Object.getOwnPropertyDescriptor(value, key)!;
    if (PRIVATE_FIELDS.test(key) || !('value' in descriptor)) continue;
    clean[key] = URL_FIELDS.has(key) && typeof descriptor.value === 'string'
      ? sanitizeUrl(descriptor.value) : sanitizeData(descriptor.value, depth + 1, seen, budget);
  }
  return clean;
}

/** sanitizeErrorEvent preserves SDK diagnostics/Debug IDs while excluding known private origins and payload categories. */
export function sanitizeErrorEvent(event: ErrorEvent, hint: EventHint): ErrorEvent | null {
  try {
    const clean = { ...event };
    delete clean.user;
    if (event.request) {
      clean.request = { ...event.request };
      delete clean.request.headers; delete clean.request.cookies; delete clean.request.data; delete clean.request.query_string;
      if (clean.request.url) clean.request.url = sanitizeUrl(clean.request.url);
    }
    const redactions = getPrivateErrorRedactions(hint.originalException);
    const privateObject = hint.originalException && typeof hint.originalException === 'object' && !(hint.originalException instanceof Error)
      && ['body', 'content', 'messages', 'structuredContent', 'rawResponse'].some((key) => Object.getOwnPropertyDescriptor(hint.originalException, key));
    const redactMessage = (message: string) => {
      if (privateObject) return 'Request response error';
      for (const redaction of redactions) {
        if (message === redaction.message) return redaction.replacement;
        // SDK maxValueLength truncates values before this hook. Recognize only
        // copies of the specifically annotated message, including Error prefixes.
        const copy = message.replace(/^(?:[A-Za-z][A-Za-z0-9]*Error|Error):\s*/, '');
        const prefix = copy.endsWith('...') ? copy.slice(0, -3) : copy;
        if (prefix.length >= 32 && redaction.message.startsWith(prefix)) return redaction.replacement;
        if (redaction.message.length >= 8 && message.includes(redaction.message)) message = message.replaceAll(redaction.message, redaction.replacement);
      }
      return sanitizeText(message);
    };
    if (clean.message) clean.message = redactMessage(clean.message);
    if (event.exception?.values) clean.exception = { ...event.exception, values: event.exception.values.map((exception) => ({
      ...exception,
      ...(exception.value === undefined ? {} : { value: redactMessage(exception.value) }),
      ...(exception.stacktrace ? { stacktrace: { ...exception.stacktrace, frames: exception.stacktrace.frames?.map((frame) => {
        const result = { ...frame };
        delete result.vars;
        if (result.filename) result.filename = sanitizeUrl(result.filename);
        if (result.abs_path) result.abs_path = sanitizeUrl(result.abs_path);
        return result;
      }) } } : {}),
    })) };
    if (event.contexts) clean.contexts = sanitizeData(event.contexts) as ErrorEvent['contexts'];
    if (event.extra) clean.extra = sanitizeData(event.extra) as ErrorEvent['extra'];
    if (event.tags) clean.tags = sanitizeData(event.tags) as ErrorEvent['tags'];
    if (event.breadcrumbs) clean.breadcrumbs = event.breadcrumbs.map((item) => sanitizeBreadcrumb(item, {})).filter((item): item is Breadcrumb => item !== null);
    if (event.debug_meta?.images) clean.debug_meta = { ...event.debug_meta, images: event.debug_meta.images.map((image) => ({ ...image, ...(image.code_file ? { code_file: sanitizeUrl(image.code_file) } : {}) })) };
    return clean;
  } catch {
    console.warn('Browser error privacy filtering could not process an event');
    return null;
  }
}

/** sanitizeBreadcrumb keeps navigation/network/ordinary console diagnostics without UI input or private response payloads. */
export function sanitizeBreadcrumb(breadcrumb: Breadcrumb, hint: BreadcrumbHint = {}): Breadcrumb | null {
  try {
    const clean = { ...breadcrumb };
    if (breadcrumb.category?.startsWith('ui.')) {
      const target = (hint.event as Event | undefined)?.target ?? hint.target;
      if (target instanceof Element) {
        if (target.closest('input, textarea, pre, code, [contenteditable="true"], [data-sentry-private]')) return null;
        // SDK DOM selectors may include user-derived IDs/titles; classes and element kind retain useful UI location.
        clean.message = target.tagName.toLowerCase() + [...target.classList].slice(0, 3).filter((name) => /^[a-z][a-z0-9:_-]{0,60}$/i.test(name)).map((name) => '.' + name).join('');
      } else if (clean.message) clean.message = sanitizeText(clean.message);
    } else if (breadcrumb.category === 'console') {
      const input = Array.isArray(hint.input) ? hint.input : [];
      if (typeof input[0] === 'string' && PRIVATE_CONSOLE.has(input[0])) {
        clean.message = input[0]; clean.data = { logger: 'console' }; return clean;
      }
      if (input.length) {
        // The SDK may already have formatted objects into message text. Rebuild
        // from sanitized arguments so removing data.arguments is sufficient.
        clean.message = input.map((item) => {
          const marked = getPrivateErrorMessage(item);
          if (marked) return marked;
          if (typeof item === 'string') return sanitizeText(item);
          if (item instanceof Error) return sanitizeText(item.message);
          if (typeof item === 'number' || typeof item === 'boolean') return String(item);
          return JSON.stringify(sanitizeData(item)) ?? '[Object]';
        }).join(' ');
      } else if (clean.message) clean.message = sanitizeText(clean.message);
    } else if (clean.message) clean.message = sanitizeText(clean.message);
    if (breadcrumb.data) clean.data = sanitizeData(breadcrumb.data) as Breadcrumb['data'];
    return clean;
  } catch {
    console.warn('Browser error privacy filtering could not process a breadcrumb');
    return null;
  }
}
