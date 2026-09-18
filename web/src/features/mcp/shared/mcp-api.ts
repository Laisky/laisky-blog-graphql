import { buildAuthorizationHeader, resolveMcpEndpoint } from './auth';
import { StreamableMcpClient, type McpCallOptions, type McpToolResult } from './mcp-transport';

/** CallToolResponse is the full MCP tool result, including optional structured content. */
export type CallToolResponse = McpToolResult;

// Bound retained endpoint/credential pairs. Eviction does not cancel an active call;
// a later call to an evicted pair simply negotiates again. Nothing is persisted.
const clients = new Map<string, StreamableMcpClient>();
const MAX_CACHED_CLIENTS = 32;

/** callMcpTool negotiates and invokes a tool without ever implicitly replaying its side effects. */
export async function callMcpTool(
  apiKey: string,
  toolName: string,
  arguments_: Record<string, unknown> = {},
  options: McpCallOptions = {},
): Promise<CallToolResponse> {
  options.signal?.throwIfAborted();
  const authorization = buildAuthorizationHeader(apiKey);
  if (!authorization) throw new Error('API key is required');
  const endpoint = resolveMcpEndpoint();
  const key = JSON.stringify([endpoint, authorization]);
  let client = clients.get(key);
  if (!client) client = new StreamableMcpClient(endpoint, authorization);
  clients.delete(key);
  clients.set(key, client);
  while (clients.size > MAX_CACHED_CLIENTS) clients.delete(clients.keys().next().value!);
  return client.call(toolName, arguments_, options);
}
