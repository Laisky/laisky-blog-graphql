import { buildAuthorizationHeader } from '@/features/mcp/shared/auth';
import { publicApiPath } from './api-base';

export interface GraphQLResponse<T> {
  data?: T;
  errors?: Array<{
    message: string;
    locations?: Array<{ line: number; column: number }>;
    path?: Array<string | number>;
    extensions?: Record<string, unknown>;
  }>;
}

/** Preserve machine-readable GraphQL errors without silently retrying mutations. */
export class GraphQLRequestError extends Error {
  readonly errors: NonNullable<GraphQLResponse<unknown>['errors']>;
  readonly status: number;
  constructor(message: string, status: number, errors: NonNullable<GraphQLResponse<unknown>['errors']> = []) {
    super(message);
    this.name = 'GraphQLRequestError';
    this.status = status;
    this.errors = errors;
  }
}

/** Execute once against the active public prefix with the canonical bearer header. */
export async function fetchGraphQL<T>(
  apiKey: string, query: string, variables: Record<string, unknown> = {}, signal?: AbortSignal,
): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json', Accept: 'application/json' };
  const authorization = buildAuthorizationHeader(apiKey);
  if (authorization) headers.Authorization = authorization;
  const response = await fetch(publicApiPath('/query'), {
    method: 'POST', headers, cache: 'no-store', signal,
    body: JSON.stringify({ query, variables }),
  });
  const text = await response.text();
  let json: GraphQLResponse<T>;
  try { json = JSON.parse(text) as GraphQLResponse<T>; }
  catch { throw new GraphQLRequestError(`Invalid GraphQL response (HTTP ${response.status})`, response.status); }
  if (!json || typeof json !== 'object' || Array.isArray(json)
      || (json.errors !== undefined && (!Array.isArray(json.errors)
        || json.errors.some((error) => !error || typeof error.message !== 'string')))) {
    throw new GraphQLRequestError('Invalid GraphQL response shape', response.status);
  }
  if (!response.ok || (json.errors?.length ?? 0) > 0) {
    throw new GraphQLRequestError(json.errors?.[0]?.message || `HTTP ${response.status}`, response.status, json.errors);
  }
  if (json.data === undefined || json.data === null) throw new GraphQLRequestError('No data returned from GraphQL', response.status);
  return json.data;
}
