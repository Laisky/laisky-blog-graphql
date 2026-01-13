import { buildAuthorizationHeader, resolveCurrentApiBasePath } from '../shared/auth';

export interface CallLogEntry {
  id: string;
  tool: string;
  status: string;
  user_prefix: string;
  cost_credits: number;
  cost_unit: string;
  cost_usd: string;
  duration_ms: number;
  parameters: Record<string, unknown>;
  error?: string;
  occurred_at: string;
  created_at: string;
  updated_at: string;
}

export interface CallLogPagination {
  page: number;
  page_size: number;
  total_items: number;
  total_pages: number;
  has_next: boolean;
  has_prev: boolean;
}

export interface CallLogMeta {
  quotes_per_usd: number;
}

export interface CallLogListResponse {
  data: CallLogEntry[];
  pagination: CallLogPagination;
  meta: CallLogMeta;
}

export interface CallLogQuery {
  page?: number;
  pageSize?: number;
  sortBy?: string;
  sortOrder?: 'ASC' | 'DESC';
  tool?: string;
  user?: string;
  from?: string;
  to?: string;
}

export async function fetchCallLogs(apiKey: string, query: CallLogQuery, signal?: AbortSignal): Promise<CallLogListResponse> {
  const authorization = buildAuthorizationHeader(apiKey);
  if (!authorization) {
    throw new Error('API key is required');
  }

  const apiBasePath = resolveCurrentApiBasePath();

  const params = new URLSearchParams();
  if (query.page && query.page > 0) params.set('page', String(query.page));
  if (query.pageSize && query.pageSize > 0) params.set('page_size', String(query.pageSize));
  if (query.sortBy) params.set('sort_by', query.sortBy);
  if (query.sortOrder) params.set('sort_order', query.sortOrder);
  if (query.tool) params.set('tool', query.tool);
  if (query.user) params.set('user', query.user);
  if (query.from) params.set('from', query.from);
  if (query.to) params.set('to', query.to);

  const qs = params.toString();
  const url = `${apiBasePath}api/logs${qs ? `?${qs}` : ''}`;

  const response = await fetch(url, {
    cache: 'no-store',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    signal,
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  return response.json();
}
