import { requestWithUnexpectedError, markPrivateError, parsePrivateJson, readPrivateJsonResponse, reportUnexpectedRequestError } from '@/lib/private-error';
import { buildAuthorizationHeader, resolveToolApiBase } from '../shared/auth';
import { callMcpTool, type CallToolResponse } from '../shared/mcp-api';
import { FileIORequestError } from './version-state';

export function extractFilePayload<T>(result: CallToolResponse, observeUnexpected = true): T {
  const data = result as CallToolResponse & { structured?: unknown; structuredContent?: unknown; structured_content?: unknown };
  const structured = data.structured ?? data.structuredContent ?? data.structured_content;
  if (structured !== undefined) return structured as T;
  const text = result.content?.find((item) => typeof item.text === 'string')?.text;
  if (!text) {
    const error = new Error('Tool response missing structured payload.');
    if (observeUnexpected) reportUnexpectedRequestError(error, { operation: 'file-io' });
    throw error;
  }
  return parsePrivateJson<T>(text, 'Invalid FileIO JSON response', observeUnexpected);
}

export async function callFileTool<T>(apiKey: string, name: string, args: Record<string, unknown>): Promise<T> {
  if (!apiKey) throw new Error('API key is required.');
  const response = await callMcpTool(apiKey, name, args);
  if (response.isError) {
    let failure: { message?: string; code?: string } = {};
    try {
      const parsed = extractFilePayload<unknown>(response, false);
      if (parsed && typeof parsed === 'object') failure = parsed as typeof failure;
    } catch { /* Plain-text tool errors are valid. */ }
    throw markPrivateError(new FileIORequestError(failure.message || response.content?.[0]?.text || 'FileIO request failed.', failure.code),
      'FileIO request failed');
  }
  return extractFilePayload<T>(response);
}

export async function callFileAPI<T>(apiKey: string, method: 'GET' | 'PUT' | 'POST', path: string,
  options: { query?: Record<string, string>; body?: unknown; headers?: Record<string, string>; signal?: AbortSignal } = {}): Promise<T> {
  const authorization = buildAuthorizationHeader(apiKey);
  if (!authorization) throw new Error('API key is required.');
  const url = `${resolveToolApiBase('file_io')}api${path}${options.query ? `?${new URLSearchParams(options.query)}` : ''}`;
  const response = await requestWithUnexpectedError(url, {
    method, cache: 'no-store', signal: options.signal,
    headers: {
      ...options.headers, Authorization: authorization, 'Cache-Control': 'no-store', Pragma: 'no-cache',
      ...(options.body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  }, 'file-io');
  if (!response.ok) {
    let message = response.statusText || `HTTP ${response.status}`;
    try {
      const text = await response.text();
      if (text) {
        try { message = (JSON.parse(text) as { error?: string }).error || text; } catch { message = text; }
      }
    } catch { /* Keep the HTTP status when the body cannot be read. */ }
    throw markPrivateError(new FileIORequestError(message, undefined, response.status), 'FileIO request failed');
  }
  return readPrivateJsonResponse<T>(response, 'Invalid FileIO JSON response');
}
