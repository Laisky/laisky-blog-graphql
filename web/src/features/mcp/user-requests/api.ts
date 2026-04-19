import { buildAuthorizationHeader, resolveToolApiBase } from '../shared/auth';

// ============================================================================
// Return Mode Configuration
// ============================================================================

/**
 * ReturnMode controls how the get_user_request tool returns pending commands.
 * - 'all': Returns all pending commands (FIFO order)
 * - 'first': Returns only the oldest (earliest) pending command
 */
export type ReturnMode = 'all' | 'first';

const RETURN_MODE_STORAGE_KEY = 'mcp_return_mode';

/**
 * getReturnMode retrieves the user's preferred return mode from localStorage.
 * Defaults to 'all' if not set.
 * @deprecated Use getReturnModeFromServer for server-persisted preference.
 */
export function getReturnMode(): ReturnMode {
  if (typeof window === 'undefined') return 'all';
  const stored = localStorage.getItem(RETURN_MODE_STORAGE_KEY);
  if (stored === 'first' || stored === 'all') {
    return stored;
  }
  return 'all';
}

/**
 * setReturnMode persists the user's preferred return mode to localStorage.
 * @deprecated Use setReturnModeOnServer for server-persisted preference.
 */
export function setReturnMode(mode: ReturnMode): void {
  if (typeof window === 'undefined') return;
  localStorage.setItem(RETURN_MODE_STORAGE_KEY, mode);
}

// ============================================================================
// Server-Side Preferences API
// ============================================================================

export interface UserPreferencesResponse {
  return_mode: ReturnMode;
  disabled_tools?: string[];
  available_tools?: string[];
  user_id?: string;
  key_hint?: string;
}

/**
 * getPreferencesFromServer retrieves the user's preferences from the server.
 * This is the authoritative source for return_mode preference.
 */
export async function getPreferencesFromServer(apiKey: string, signal?: AbortSignal): Promise<UserPreferencesResponse> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/preferences`, {
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

/**
 * setReturnModeOnServer persists the user's return_mode preference to the server.
 * This preference is used by the MCP tool when the AI agent doesn't specify return_mode.
 */
export async function setReturnModeOnServer(apiKey: string, mode: ReturnMode): Promise<UserPreferencesResponse> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/preferences`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ return_mode: mode }),
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  return response.json();
}

/**
 * setDisabledToolsOnServer persists the user's disabled MCP tools to the server.
 */
export async function setDisabledToolsOnServer(apiKey: string, disabledTools: string[]): Promise<UserPreferencesResponse> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/preferences`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ disabled_tools: disabledTools }),
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  return response.json();
}

// ============================================================================
// Section Collapse State Configuration
// ============================================================================

const SAVED_COMMANDS_EXPANDED_STORAGE_KEY = 'mcp_saved_commands_expanded';
const DESCRIPTION_COLLAPSED_STORAGE_KEY = 'mcp_description_collapsed';

/**
 * getDescriptionCollapsed retrieves the collapsed state of the description section.
 * Defaults to false (expanded) if not set.
 */
export function getDescriptionCollapsed(): boolean {
  if (typeof window === 'undefined') return false;
  return localStorage.getItem(DESCRIPTION_COLLAPSED_STORAGE_KEY) === 'true';
}

/**
 * setDescriptionCollapsed persists the collapsed state of the description section.
 */
export function setDescriptionCollapsed(collapsed: boolean): void {
  if (typeof window === 'undefined') return;
  localStorage.setItem(DESCRIPTION_COLLAPSED_STORAGE_KEY, String(collapsed));
}

/**
 * getSavedCommandsExpanded retrieves the expanded state of the Saved Commands section.
 * Defaults to true (expanded) if not set.
 */
export function getSavedCommandsExpanded(): boolean {
  if (typeof window === 'undefined') return true;
  const stored = localStorage.getItem(SAVED_COMMANDS_EXPANDED_STORAGE_KEY);
  // Default to true if not set
  return stored !== 'false';
}

/**
 * setSavedCommandsExpanded persists the expanded state of the Saved Commands section.
 */
export function setSavedCommandsExpanded(expanded: boolean): void {
  if (typeof window === 'undefined') return;
  localStorage.setItem(SAVED_COMMANDS_EXPANDED_STORAGE_KEY, String(expanded));
}

// ============================================================================
// User Request Types
// ============================================================================

export interface UserRequestImage {
  id: string;
  sha256: string;
  mime: string;
  url?: string;
  size: number;
  width: number;
  height: number;
  expires_at: string;
  source_url?: string;
  sort_order: number;
}

export interface UserRequest {
  id: string;
  content: string;
  status: string;
  task_id: string;
  created_at: string;
  updated_at: string;
  consumed_at?: string | null;
  user_identity?: string;
  images?: UserRequestImage[];
}

export interface QuotaResponse {
  user_identity: string;
  used_bytes: number;
  quota_bytes: number;
  object_count: number;
  ttl_days: number;
}

export type ImageErrorCode =
  | 'quota_exceeded'
  | 'image_too_large'
  | 'unsupported_mime'
  | 'too_many_images'
  | 'url_blocked'
  | 'url_fetch_failed'
  | 'url_timeout'
  | 'decode_failed'
  | 'storage_unavailable'
  | 'feature_disabled'
  | 'unknown';

export interface ImageSubmissionError extends Error {
  code: ImageErrorCode;
  attachmentIndex?: number;
}

export interface UserRequestListResponse {
  pending?: UserRequest[];
  consumed?: UserRequest[];
  total_consumed?: number;
  user_id?: string;
  key_hint?: string;
}

export interface UserRequestSearchResponse {
  results?: UserRequest[];
}

export interface SavedCommand {
  id: string;
  label: string;
  content: string;
  sort_order: number;
  created_at: string;
  updated_at: string;
}

export interface SavedCommandListResponse {
  commands: SavedCommand[];
  user_id?: string;
  key_hint?: string;
}

type TaskScopeOptions = {
  taskId?: string;
  allTasks?: boolean;
};

function ensureAuthorization(apiKey: string): string {
  const authorization = buildAuthorizationHeader(apiKey);
  if (!authorization) {
    throw new Error('API key is required');
  }
  return authorization;
}

function appendTaskScope(params: URLSearchParams, scope?: TaskScopeOptions) {
  if (!scope) {
    return;
  }
  if (scope.taskId) {
    params.append('task_id', scope.taskId);
  }
  if (scope.allTasks) {
    params.append('all_tasks', 'true');
  }
}

export async function listUserRequests(
  apiKey: string,
  options?: {
    cursor?: string;
    limit?: number;
    signal?: AbortSignal;
  }
): Promise<UserRequestListResponse> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const params = new URLSearchParams({ all_tasks: 'true' });
  if (options?.cursor) {
    params.append('cursor', options.cursor);
  }
  if (options?.limit) {
    params.append('limit', options.limit.toString());
  }

  const response = await fetch(`${apiBasePath}api/requests?${params.toString()}`, {
    cache: 'no-store',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    signal: options?.signal,
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  return response.json();
}

/**
 * searchUserRequests performs a fuzzy search on user requests.
 */
export async function searchUserRequests(
  apiKey: string,
  query: string,
  options?: {
    limit?: number;
    signal?: AbortSignal;
  }
): Promise<UserRequestSearchResponse> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const params = new URLSearchParams({ q: query });
  if (options?.limit) {
    params.append('limit', options.limit.toString());
  }

  const response = await fetch(`${apiBasePath}api/requests/search?${params.toString()}`, {
    cache: 'no-store',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    signal: options?.signal,
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  return response.json();
}

export interface CreateUserRequestOptions {
  content: string;
  taskId?: string;
  files?: File[];
  urls?: string[];
}

/**
 * createUserRequest submits a new directive. When files or urls are present
 * the request body uses multipart/form-data so attachments can ride along;
 * otherwise a plain JSON body keeps the wire format byte-identical to the
 * pre-image-support path.
 */
export async function createUserRequest(
  apiKey: string,
  options: CreateUserRequestOptions | string,
  legacyTaskId?: string
): Promise<UserRequest> {
  const normalized: CreateUserRequestOptions =
    typeof options === 'string'
      ? { content: options, taskId: legacyTaskId }
      : options;
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');

  const hasAttachments = Boolean((normalized.files && normalized.files.length) || (normalized.urls && normalized.urls.length));

  let response: Response;
  if (hasAttachments) {
    const form = new FormData();
    form.append('content', normalized.content ?? '');
    if (normalized.taskId) {
      form.append('task_id', normalized.taskId);
    }
    for (const file of normalized.files ?? []) {
      form.append('images', file, file.name);
    }
    for (const url of normalized.urls ?? []) {
      form.append('image_urls', url);
    }
    response = await fetch(`${apiBasePath}api/requests`, {
      method: 'POST',
      headers: {
        Authorization: authorization,
        'Cache-Control': 'no-store',
        Pragma: 'no-cache',
      },
      body: form,
    });
  } else {
    response = await fetch(`${apiBasePath}api/requests`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        Authorization: authorization,
        'Cache-Control': 'no-store',
        Pragma: 'no-cache',
      },
      body: JSON.stringify({ content: normalized.content, task_id: normalized.taskId }),
    });
  }

  if (!response.ok) {
    const parsed = await parseImageError(response);
    throw parsed;
  }

  const payload = await response.json();
  return payload.request as UserRequest;
}

// parseImageError converts a non-2xx response into an ImageSubmissionError
// carrying the typed error code and optional attachment index. This mirrors
// the server-side error contract from the proposal §5.1.
async function parseImageError(response: Response): Promise<ImageSubmissionError> {
  const text = (await response.text()) || response.statusText;
  let code: ImageErrorCode = 'unknown';
  let message = text;
  let attachmentIndex: number | undefined;
  try {
    const parsed = JSON.parse(text);
    if (typeof parsed === 'object' && parsed !== null) {
      if (typeof parsed.error === 'string') {
        code = parsed.error as ImageErrorCode;
      }
      if (typeof parsed.message === 'string') {
        message = parsed.message;
      } else if (typeof parsed.error === 'string') {
        message = parsed.error;
      }
      if (typeof parsed.attachment_index === 'number') {
        attachmentIndex = parsed.attachment_index;
      }
    }
  } catch {
    // Body is not JSON; keep defaults.
  }
  const err = new Error(message) as ImageSubmissionError;
  err.code = code;
  err.attachmentIndex = attachmentIndex;
  return err;
}

/**
 * getQuota returns live image storage usage for the caller. Returns zero'd
 * values when the image feature is disabled.
 */
export async function getQuota(apiKey: string, signal?: AbortSignal): Promise<QuotaResponse> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/quota`, {
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

export async function deleteUserRequest(apiKey: string, requestId: string, scope?: TaskScopeOptions): Promise<void> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const params = new URLSearchParams();
  appendTaskScope(params, scope ? { taskId: scope.taskId } : undefined);
  const query = params.toString();
  const response = await fetch(query ? `${apiBasePath}api/requests/${requestId}?${query}` : `${apiBasePath}api/requests/${requestId}`, {
    method: 'DELETE',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }
}

export async function deleteAllUserRequests(apiKey: string, scope?: TaskScopeOptions): Promise<number> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const params = new URLSearchParams();
  appendTaskScope(params, scope);
  const url = params.toString() ? `${apiBasePath}api/requests?${params.toString()}` : `${apiBasePath}api/requests`;
  const response = await fetch(url, {
    method: 'DELETE',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  const payload = await response.json();
  return Number(payload.deleted ?? 0);
}

type DeleteConsumedOptions = TaskScopeOptions & {
  keepCount?: number;
  keepDays?: number;
};

export async function deleteConsumedRequests(apiKey: string, options?: DeleteConsumedOptions): Promise<number> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const params = new URLSearchParams();
  if (options?.keepCount !== undefined) {
    params.append('keep_count', options.keepCount.toString());
  }
  if (options?.keepDays !== undefined) {
    params.append('keep_days', options.keepDays.toString());
  }
  appendTaskScope(params, options);

  const response = await fetch(`${apiBasePath}api/requests/consumed?${params.toString()}`, {
    method: 'DELETE',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  const payload = await response.json();
  return Number(payload.deleted ?? 0);
}

/**
 * deleteAllPendingRequests deletes only pending requests for the authenticated user.
 * Returns the number of deleted requests.
 */
export async function deleteAllPendingRequests(apiKey: string, scope?: TaskScopeOptions): Promise<number> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const params = new URLSearchParams();
  appendTaskScope(params, scope);
  const url = params.toString() ? `${apiBasePath}api/requests/pending?${params.toString()}` : `${apiBasePath}api/requests/pending`;
  const response = await fetch(url, {
    method: 'DELETE',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  const payload = await response.json();
  return Number(payload.deleted ?? 0);
}

/**
 * reorderUserRequests updates the sort order for multiple pending requests at once.
 */
export async function reorderUserRequests(apiKey: string, orderedIds: string[]): Promise<void> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/requests/reorder`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ ordered_ids: orderedIds }),
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }
}

// ============================================================================
// Saved Commands API
// ============================================================================

/**
 * listSavedCommands fetches all saved commands for the authenticated user.
 */
export async function listSavedCommands(apiKey: string, signal?: AbortSignal): Promise<SavedCommandListResponse> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/saved-commands`, {
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

/**
 * createSavedCommand stores a new saved command for the authenticated user.
 */
export async function createSavedCommand(apiKey: string, label: string, content: string): Promise<SavedCommand> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/saved-commands`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ label, content }),
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  const payload = await response.json();
  return payload.command as SavedCommand;
}

/**
 * updateSavedCommand modifies an existing saved command belonging to the authenticated user.
 */
export async function updateSavedCommand(
  apiKey: string,
  commandId: string,
  updates: { label?: string; content?: string; sort_order?: number }
): Promise<SavedCommand> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/saved-commands/${commandId}`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify(updates),
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  const payload = await response.json();
  return payload.command as SavedCommand;
}

/**
 * deleteSavedCommand removes a single saved command belonging to the authenticated user.
 */
export async function deleteSavedCommand(apiKey: string, commandId: string): Promise<void> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/saved-commands/${commandId}`, {
    method: 'DELETE',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }
}

/**
 * reorderSavedCommands updates the sort order for multiple saved commands at once.
 */
export async function reorderSavedCommands(apiKey: string, orderedIds: string[]): Promise<void> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const response = await fetch(`${apiBasePath}api/saved-commands/reorder`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ ordered_ids: orderedIds }),
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }
}

// ============================================================================
// Hold API
// ============================================================================

export interface HoldState {
  active: boolean;
  /** Whether an agent is currently waiting for a command */
  waiting: boolean;
  expires_at?: string | null;
  /** Remaining seconds until expiry (0 if no agent waiting yet) */
  remaining_secs: number;
}

/**
 * getHoldState retrieves the current hold state for the authenticated user.
 */
export async function getHoldState(apiKey: string, taskId?: string, signal?: AbortSignal): Promise<HoldState> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const params = new URLSearchParams();
  if (taskId) {
    params.append('task_id', taskId);
  }
  const query = params.toString();
  const response = await fetch(`${apiBasePath}api/hold${query ? `?${query}` : ''}`, {
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

/**
 * setHold activates the hold for the authenticated user.
 * When hold is active, the get_user_request tool will wait for a new command
 * before responding instead of returning immediately.
 */
export async function setHold(apiKey: string, taskId?: string): Promise<HoldState> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const params = new URLSearchParams();
  if (taskId) {
    params.append('task_id', taskId);
  }
  const query = params.toString();
  const response = await fetch(`${apiBasePath}api/hold${query ? `?${query}` : ''}`, {
    method: 'POST',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  return response.json();
}

/**
 * releaseHold deactivates the hold for the authenticated user.
 */
export async function releaseHold(apiKey: string, taskId?: string): Promise<HoldState> {
  const authorization = ensureAuthorization(apiKey);
  const apiBasePath = resolveToolApiBase('get_user_requests');
  const params = new URLSearchParams();
  if (taskId) {
    params.append('task_id', taskId);
  }
  const query = params.toString();
  const response = await fetch(`${apiBasePath}api/hold${query ? `?${query}` : ''}`, {
    method: 'DELETE',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  });

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw new Error(message);
  }

  return response.json();
}
