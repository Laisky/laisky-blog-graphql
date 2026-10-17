import { getPrivateErrorMessage } from '@/lib/private-error';
import { beforeEach, describe, expect, it, vi } from 'vitest';

import { createUserRequest, getQuota, type ImageSubmissionError } from './api';

vi.mock('../shared/auth', () => ({
  buildAuthorizationHeader: (k: string) => (k ? `Bearer ${k}` : ''),
  resolveToolApiBase: () => '/',
}));

const mockFetch = vi.fn();

describe('createUserRequest', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubGlobal('fetch', mockFetch);
  });

  it('sends plain JSON for text-only submissions', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ request: { id: '1', content: 'hi' } }),
    });
    await createUserRequest('sk-test', { content: 'hi' });
    const call = mockFetch.mock.calls[0];
    expect(call[0]).toBe('/api/requests');
    expect(call[1].headers['Content-Type']).toBe('application/json');
    expect(JSON.parse(call[1].body)).toEqual({ content: 'hi', task_id: undefined });
  });

  it('uses multipart when files or urls are present', async () => {
    const file = new File([new Uint8Array([1, 2, 3])], 'a.png', { type: 'image/png' });
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ request: { id: '1' } }) });
    await createUserRequest('sk-test', { content: 'c', files: [file], urls: ['https://example.com/a.png'] });
    const call = mockFetch.mock.calls[0];
    expect(call[1].body).toBeInstanceOf(FormData);
    // Content-Type must NOT be set explicitly so the browser builds the boundary.
    expect(call[1].headers['Content-Type']).toBeUndefined();
    const form: FormData = call[1].body;
    expect(form.get('content')).toBe('c');
    expect(form.getAll('images').length).toBe(1);
    expect(form.getAll('image_urls')).toEqual(['https://example.com/a.png']);
  });

  it('throws a typed error when the server returns an image error', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: false,
      statusText: 'Payload Too Large',
      text: async () => JSON.stringify({ error: 'quota_exceeded', message: 'too big', attachment_index: 2 }),
    });
    let caught: ImageSubmissionError | null = null;
    try {
      await createUserRequest('sk-test', { content: 'x', files: [new File([''], 'a.png', { type: 'image/png' })] });
    } catch (err) {
      caught = err as ImageSubmissionError;
    }
    expect(caught).not.toBeNull();
    expect(caught!.code).toBe('quota_exceeded');
    expect(caught!.message).toBe('too big');
    expect(getPrivateErrorMessage(caught)).toBe('User requests operation failed');
    expect(caught!.attachmentIndex).toBe(2);
  });
});

describe('getQuota', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubGlobal('fetch', mockFetch);
  });

  it('returns the parsed quota payload', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ user_identity: 'u', used_bytes: 123, quota_bytes: 1000, object_count: 1, ttl_days: 7 }),
    });
    const q = await getQuota('sk-test');
    expect(q.used_bytes).toBe(123);
    expect(q.quota_bytes).toBe(1000);
  });
});

describe('private user request errors', () => {
  beforeEach(() => { vi.clearAllMocks(); vi.stubGlobal('fetch', mockFetch); });
  it('preserves raw failure text and keeps HTTP behavior unchanged', async () => {
    mockFetch.mockResolvedValueOnce(new Response('synthetic private user history', { status: 403 }));
    const error = await getQuota('test-key').catch((failure: unknown) => failure);
    expect(error).toMatchObject({ message: 'synthetic private user history' });
    expect(getPrivateErrorMessage(error)).toBe('User requests operation failed');
    expect(mockFetch).toHaveBeenCalledTimes(1);
  });
  it('annotates native parser errors from private request payloads', async () => {
    mockFetch.mockResolvedValueOnce(new Response('synthetic private request'));
    const error = await createUserRequest('test-key', 'request').catch((failure: unknown) => failure);
    expect(error).toBeInstanceOf(SyntaxError);
    expect(getPrivateErrorMessage(error)).toBe('Invalid user requests JSON response');
  });
});
