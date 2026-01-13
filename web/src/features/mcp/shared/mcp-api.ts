import { buildAuthorizationHeader, resolveMcpEndpoint } from './auth';

export interface CallToolResponse {
  content: Array<{
    type: string;
    text?: string;
    [key: string]: unknown;
  }>;
  isError?: boolean;
}

export async function callMcpTool(apiKey: string, toolName: string, arguments_?: Record<string, unknown>): Promise<CallToolResponse> {
  const authorization = buildAuthorizationHeader(apiKey);
  if (!authorization) {
    throw new Error('API key is required');
  }

  const endpoint = resolveMcpEndpoint();
  // Standard MCP JSON-RPC call over HTTP POST
  const response = await fetch(endpoint, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
    },
    body: JSON.stringify({
      jsonrpc: '2.0',
      method: 'tools/call',
      params: {
        name: toolName,
        arguments: arguments_ || {},
      },
      id: Date.now(),
    }),
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(text || `HTTP ${response.status}`);
  }

  const json = await response.json();
  if (json.error) {
    throw new Error(json.error.message || 'JSON-RPC Error');
  }

  return json.result;
}
