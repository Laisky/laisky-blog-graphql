import { markPrivateError, type PrivateErrorLabel } from './private-error';

/**
 * HttpStatusError is thrown for a reply that arrived with a non-2xx HTTP status. Its
 * message is the server's reply text (or the status text), exactly as the plain Error it
 * replaces carried, and status keeps the HTTP status so callers can tell a final refusal
 * from a failure that is worth retrying.
 */
export class HttpStatusError extends Error {
  /** status is the HTTP status code of the refused reply. */
  readonly status: number;

  constructor(message: string, status: number) {
    super(message);
    this.name = 'HttpStatusError';
    this.status = status;
  }
}

/**
 * responseStatusError reads a non-2xx reply into an HttpStatusError whose message is
 * marked private under label, so telemetry reports only the fixed label.
 */
export async function responseStatusError(response: Response, label: PrivateErrorLabel): Promise<HttpStatusError> {
  const message = (await response.text()) || response.statusText;
  return markPrivateError(new HttpStatusError(message, response.status), label);
}

/**
 * RETRYABLE_CLIENT_STATUSES are the 4xx codes that describe a temporary condition rather
 * than a verdict on the request: 408 Request Timeout (RFC 9110 §15.5.9), 425 Too Early
 * (RFC 8470 §5.2) and 429 Too Many Requests (RFC 6585 §4).
 */
const RETRYABLE_CLIENT_STATUSES = new Set([408, 425, 429]);

/**
 * isRetryableRequestFailure reports whether repeating the same request may succeed.
 * Only a reply with a final status is a verdict: 5xx and the temporary 4xx codes are
 * retryable, every other non-2xx status (401, 403, 404, …) is not. Anything without an
 * HTTP status (a transport failure, an unreadable or malformed body) is retryable,
 * because it says nothing about the request itself.
 */
export function isRetryableRequestFailure(error: unknown): boolean {
  if (!(error instanceof HttpStatusError)) {
    return true;
  }
  return error.status >= 500 || RETRYABLE_CLIENT_STATUSES.has(error.status);
}
