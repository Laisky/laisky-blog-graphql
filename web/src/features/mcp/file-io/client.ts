import { buildAuthorizationHeader, resolveToolApiBase } from '../shared/auth';
import { callMcpTool, type CallToolResponse } from '../shared/mcp-api';
import { FileIORequestError } from './version-state';

export function extractFilePayload<T>(result: CallToolResponse): T {
  const data = result as CallToolResponse & { structured?: unknown; structuredContent?: unknown; structured_content?: unknown };
  const structured = data.structured ?? data.structuredContent ?? data.structured_content;
  if (structured !== undefined) return structured as T;
  const text = result.content?.find((item) => typeof item.text === 'string')?.text;
  if (!text) throw new Error('Tool response missing structured payload.');
  return JSON.parse(text) as T;
}

export async function callFileTool<T>(apiKey: string, name: string, args: Record<string, unknown>): Promise<T> {
  if (!apiKey) throw new Error('API key is required.');
  const response = await callMcpTool(apiKey, name, args);
  if (response.isError) {
    let failure: { message?: string; code?: string } = {};
    try {
      const parsed = extractFilePayload<unknown>(response);
      if (parsed && typeof parsed === 'object') failure = parsed as typeof failure;
    } catch { /* Plain-text tool errors are valid. */ }
    throw new FileIORequestError(failure.message || response.content?.[0]?.text || 'FileIO request failed.', failure.code);
  }
  return extractFilePayload<T>(response);
}

export async function callFileAPI<T>(apiKey: string, method: 'GET' | 'PUT' | 'POST', path: string,
  options: { query?: Record<string, string>; body?: unknown; headers?: Record<string, string> } = {}): Promise<T> {
  const authorization = buildAuthorizationHeader(apiKey);
  if (!authorization) throw new Error('API key is required.');
  const url = `${resolveToolApiBase('file_io')}api${path}${options.query ? `?${new URLSearchParams(options.query)}` : ''}`;
  const response = await fetch(url, {
    method, cache: 'no-store',
    headers: {
      ...options.headers, Authorization: authorization, 'Cache-Control': 'no-store', Pragma: 'no-cache',
      ...(options.body === undefined ? {} : { 'Content-Type': 'application/json' }),
    },
    body: options.body === undefined ? undefined : JSON.stringify(options.body),
  });
  if (!response.ok) {
    let message = response.statusText || `HTTP ${response.status}`;
    try {
      const text = await response.text();
      if (text) {
        try { message = (JSON.parse(text) as { error?: string }).error || text; } catch { message = text; }
      }
    } catch { /* Keep the HTTP status when the body cannot be read. */ }
    throw new FileIORequestError(message, undefined, response.status);
  }
  return await response.json() as T;
}
