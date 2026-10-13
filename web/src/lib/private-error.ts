/** Only fixed summaries may replace private server-derived messages in telemetry. */
export type PrivateErrorLabel =
  | 'GraphQL request failed'
  | 'MCP request failed'
  | 'Invalid MCP JSON response'
  | 'FileIO request failed'
  | 'Invalid FileIO JSON response'
  | 'Ask user request failed'
  | 'Invalid ask user JSON response'
  | 'User requests operation failed'
  | 'Invalid user requests JSON response'
  | 'Call log request failed'
  | 'Invalid call log JSON response'
  | 'Memory request failed'
  | 'Invalid memory JSON response'
  | 'Invalid memory input';

const privateMessages = new WeakMap<object, PrivateErrorLabel>();
const MAX_CAUSE_DEPTH = 8;

export type RequestOperation = 'graphql' | 'mcp' | 'file-io' | 'ask-user' | 'user-requests' | 'call-log' | 'memory';
export interface UnexpectedRequestContext {
  operation: RequestOperation;
  status?: number;
  code?: number;
}
type UnexpectedRequestReporter = (error: unknown, context: UnexpectedRequestContext) => void;
let unexpectedReporter: UnexpectedRequestReporter | undefined;
const reportedErrors = new WeakSet<object>();
const operations = new Set<RequestOperation>(['graphql', 'mcp', 'file-io', 'ask-user', 'user-requests', 'call-log', 'memory']);

/** setUnexpectedRequestReporter installs the ready browser reporter without an SDK dependency. */
export function setUnexpectedRequestReporter(reporter?: UnexpectedRequestReporter): void {
  unexpectedReporter = reporter;
}

/** reportUnexpectedRequestError observes actionable failures once and preserves UI control flow. */
export function reportUnexpectedRequestError(error: unknown, context: UnexpectedRequestContext): void {
  if (!unexpectedReporter || !operations.has(context.operation)) return;
  if ((error instanceof Error || (typeof DOMException !== 'undefined' && error instanceof DOMException)) && (error.name === 'AbortError' || error.name === 'TimeoutError')) return;
  if (!error || (typeof error !== 'object' && typeof error !== 'function')) return;
  if (reportedErrors.has(error)) return;
  const safeContext: UnexpectedRequestContext = { operation: context.operation };
  if (Number.isInteger(context.status) && context.status! >= 100 && context.status! <= 599) safeContext.status = context.status;
  if (Number.isSafeInteger(context.code) && Math.abs(context.code!) <= 2147483647) safeContext.code = context.code;
  reportedErrors.add(error);
  try { unexpectedReporter(error, safeContext); }
  catch {
    try { console.warn('Unexpected request error reporting failed.'); } catch { /* Preserve the request failure. */ }
  }
}

/** operationForLabel maps only controlled private response labels to fixed request operations. */
function operationForLabel(label: PrivateErrorLabel): RequestOperation {
  if (label.includes('Call log') || label.includes('call log')) return 'call-log';
  if (label.includes('Memory') || label.includes('memory')) return 'memory';
  if (label.includes('GraphQL')) return 'graphql';
  if (label.includes('FileIO')) return 'file-io';
  if (label.includes('ask user') || label.includes('Ask user')) return 'ask-user';
  if (label.includes('user requests') || label.includes('User requests')) return 'user-requests';
  return 'mcp';
}

/** requestWithUnexpectedError observes transport failures while leaving HTTP replies to callers. */
export async function requestWithUnexpectedError(input: RequestInfo | URL, init: RequestInit,
  operation: RequestOperation, fetcher: typeof fetch = fetch): Promise<Response> {
  try { return await fetcher(input, init); }
  catch (error) {
    if (!init.signal?.aborted) reportUnexpectedRequestError(error, { operation });
    throw error;
  }
}

/** markPrivateError annotates telemetry without changing UI messages, stacks or fields. */
export function markPrivateError<T extends Error>(error: T, label: PrivateErrorLabel): T {
  privateMessages.set(error, label);
  return error;
}

/** getPrivateErrorMessage finds a private summary without invoking cause getters. */
export function getPrivateErrorMessage(error: unknown): string | undefined {
  const seen = new Set<object>();
  let current = error;
  for (let depth = 0; depth < MAX_CAUSE_DEPTH; depth += 1) {
    if (!current || (typeof current !== 'object' && typeof current !== 'function') || seen.has(current)) return undefined;
    seen.add(current);
    const message = privateMessages.get(current);
    if (message) return message;
    try {
      const cause = Object.getOwnPropertyDescriptor(current, 'cause');
      current = cause && 'value' in cause ? cause.value : undefined;
    } catch { return undefined; }
  }
  return undefined;
}

/** getPrivateErrorRedactions identifies exact private messages without retaining raw message caches. */
export function getPrivateErrorRedactions(error: unknown): Array<{ message: string; replacement: string }> {
  const redactions: Array<{ message: string; replacement: string }> = [];
  const seen = new Set<object>();
  let current = error;
  for (let depth = 0; depth < MAX_CAUSE_DEPTH; depth += 1) {
    if (!current || (typeof current !== 'object' && typeof current !== 'function') || seen.has(current)) break;
    seen.add(current);
    try {
      const replacement = privateMessages.get(current);
      if (replacement) {
        const message = Object.getOwnPropertyDescriptor(current, 'message');
        if (message && 'value' in message && typeof message.value === 'string') {
          redactions.push({ message: message.value, replacement });
        }
      }
      const cause = Object.getOwnPropertyDescriptor(current, 'cause');
      current = cause && 'value' in cause ? cause.value : undefined;
    } catch { break; }
  }
  return redactions;
}

/** parsePrivateJson retains native parse failures while marking their private snippets. */
export function parsePrivateJson<T>(text: string, label: PrivateErrorLabel, observeUnexpected = true): T {
  try { return JSON.parse(text) as T; }
  catch (error) {
    if (error instanceof SyntaxError) markPrivateError(error, label);
    if (observeUnexpected) reportUnexpectedRequestError(error, { operation: operationForLabel(label) });
    throw error;
  }
}

/** readPrivateJsonResponse preserves Response.json semantics and annotates parse failures. */
export async function readPrivateJsonResponse<T = Record<string, unknown>>(response: Response, label: PrivateErrorLabel): Promise<T> {
  try { return await response.json() as T; }
  catch (error) {
    if (error instanceof SyntaxError) markPrivateError(error, label);
    reportUnexpectedRequestError(error, { operation: operationForLabel(label) });
    throw error;
  }
}
