import { buildAuthorizationHeader, resolveMcpEndpoint } from './auth';

export interface CallToolResponse {
  content: Array<{
    type: string;
    text?: string;
    [key: string]: unknown;
  }>;
  isError?: boolean;
}

const MCP_SESSION_HEADER = 'Mcp-Session-Id';
const MCP_PROTOCOL_VERSION = '2025-06-18';
const MCP_CLIENT_NAME = 'laisky-blog-web';
const MCP_CLIENT_VERSION = '1.0.0';

type JsonRPCError = {
  message?: string;
};

type JsonRPCResponse<T> = {
  result?: T;
  error?: JsonRPCError;
};

type SessionCache = {
  sessionId?: string;
  initializing?: Promise<string>;
};

const sessionCacheByKey = new Map<string, SessionCache>();

function getOrCreateSessionCache(cacheKey: string): SessionCache {
  const existing = sessionCacheByKey.get(cacheKey);
  if (existing) {
    return existing;
  }

  const created: SessionCache = {};
  sessionCacheByKey.set(cacheKey, created);
  return created;
}

function cacheKeyForSession(endpoint: string, authorization: string): string {
  return `${endpoint}::${authorization}`;
}

function invalidateSession(cacheKey: string): void {
  const cache = getOrCreateSessionCache(cacheKey);
  cache.sessionId = undefined;
  cache.initializing = undefined;
}

function createJSONRPCID(): number {
  return Date.now() + Math.floor(Math.random() * 1000);
}

function isSessionRecoveryError(status: number, message: string): boolean {
  if (status !== 400 && status !== 404) {
    return false;
  }

  const normalized = message.trim().toLowerCase();
  return normalized.includes('invalid session id') || normalized.includes('session terminated');
}

async function initializeSession(endpoint: string, authorization: string): Promise<string> {
  const response = await fetch(endpoint, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
    },
    body: JSON.stringify({
      jsonrpc: '2.0',
      method: 'initialize',
      params: {
        protocolVersion: MCP_PROTOCOL_VERSION,
        capabilities: {},
        clientInfo: {
          name: MCP_CLIENT_NAME,
          version: MCP_CLIENT_VERSION,
        },
      },
      id: createJSONRPCID(),
    }),
  });

  if (!response.ok) {
    const text = await response.text();
    throw new Error(text || `HTTP ${response.status}`);
  }

  const json = (await response.json()) as JsonRPCResponse<unknown>;
  if (json.error) {
    throw new Error(json.error.message || 'MCP initialize failed');
  }

  const sessionId = response.headers.get(MCP_SESSION_HEADER)?.trim();
  if (!sessionId) {
    throw new Error(`MCP initialize did not return ${MCP_SESSION_HEADER}`);
  }

  return sessionId;
}

async function ensureSession(endpoint: string, authorization: string, cacheKey: string): Promise<string> {
  const cache = getOrCreateSessionCache(cacheKey);
  if (cache.sessionId) {
    return cache.sessionId;
  }

  if (!cache.initializing) {
    cache.initializing = initializeSession(endpoint, authorization)
      .then((sessionId) => {
        cache.sessionId = sessionId;
        return sessionId;
      })
      .finally(() => {
        cache.initializing = undefined;
      });
  }

  return cache.initializing;
}

export async function callMcpTool(apiKey: string, toolName: string, arguments_?: Record<string, unknown>): Promise<CallToolResponse> {
  const authorization = buildAuthorizationHeader(apiKey);
  if (!authorization) {
    throw new Error('API key is required');
  }

  const endpoint = resolveMcpEndpoint();
  const sessionCacheKey = cacheKeyForSession(endpoint, authorization);

  async function executeToolCall(allowRetry: boolean): Promise<CallToolResponse> {
    const sessionId = await ensureSession(endpoint, authorization, sessionCacheKey);
    const response = await fetch(endpoint, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: authorization,
        [MCP_SESSION_HEADER]: sessionId,
      },
      body: JSON.stringify({
        jsonrpc: '2.0',
        method: 'tools/call',
        params: {
          name: toolName,
          arguments: arguments_ || {},
        },
        id: createJSONRPCID(),
      }),
    });

    if (!response.ok) {
      const text = await response.text();
      if (allowRetry && isSessionRecoveryError(response.status, text)) {
        invalidateSession(sessionCacheKey);
        return executeToolCall(false);
      }
      throw new Error(text || `HTTP ${response.status}`);
    }

    const json = (await response.json()) as JsonRPCResponse<CallToolResponse>;
    if (json.error) {
      throw new Error(json.error.message || 'JSON-RPC Error');
    }
    if (!json.result) {
      throw new Error('JSON-RPC response missing result');
    }

    return json.result;
  }

  return executeToolCall(true);
}
