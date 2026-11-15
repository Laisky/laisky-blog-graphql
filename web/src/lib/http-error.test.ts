import { describe, expect, it } from 'vitest';

import { HttpStatusError, isRetryableRequestFailure, responseStatusError } from './http-error';
import { getPrivateErrorMessage } from './private-error';

describe('isRetryableRequestFailure', () => {
  it.each([408, 425, 429, 500, 502, 503, 504, 599])('retries HTTP %i', (status) => {
    expect(isRetryableRequestFailure(new HttpStatusError('x', status))).toBe(true);
  });

  it.each([400, 401, 403, 404, 405, 409, 410, 413, 422, 451])('does not retry the final HTTP %i', (status) => {
    expect(isRetryableRequestFailure(new HttpStatusError('x', status))).toBe(false);
  });

  it.each([
    ['a transport failure', new TypeError('Failed to fetch')],
    ['a malformed body', new SyntaxError('Unexpected token <')],
    ['an error without a status', new Error('boom')],
    ['a non-error value', 'boom'],
  ])('retries %s, which says nothing about the request', (_label, error) => {
    expect(isRetryableRequestFailure(error)).toBe(true);
  });
});

describe('responseStatusError', () => {
  it('keeps the reply text as the message, the status, and the private label', async () => {
    const error = await responseStatusError(new Response('key revoked', { status: 401, statusText: 'Unauthorized' }), 'User requests operation failed');
    expect(error).toBeInstanceOf(HttpStatusError);
    expect(error).toBeInstanceOf(Error);
    expect(error.message).toBe('key revoked');
    expect(error.status).toBe(401);
    expect(getPrivateErrorMessage(error)).toBe('User requests operation failed');
  });

  it('falls back to the status text for an empty reply', async () => {
    const error = await responseStatusError(new Response('', { status: 503, statusText: 'Service Unavailable' }), 'User requests operation failed');
    expect(error.message).toBe('Service Unavailable');
    expect(error.status).toBe(503);
  });
});
