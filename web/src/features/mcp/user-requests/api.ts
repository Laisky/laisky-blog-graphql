import { buildAuthorizationHeader, resolveCurrentApiBasePath } from '../shared/auth'

export interface UserRequest {
  id: string
  content: string
  status: string
  task_id: string
  created_at: string
  updated_at: string
  consumed_at?: string | null
  user_identity?: string
}

export interface UserRequestListResponse {
  pending?: UserRequest[]
  consumed?: UserRequest[]
  user_id?: string
  key_hint?: string
}

export interface SavedCommand {
  id: string
  label: string
  content: string
  sort_order: number
  created_at: string
  updated_at: string
}

export interface SavedCommandListResponse {
  commands: SavedCommand[]
  user_id?: string
  key_hint?: string
}

function ensureAuthorization(apiKey: string): string {
  const authorization = buildAuthorizationHeader(apiKey)
  if (!authorization) {
    throw new Error('API key is required')
  }
  return authorization
}

export async function listUserRequests(apiKey: string, signal?: AbortSignal): Promise<UserRequestListResponse> {
  const authorization = ensureAuthorization(apiKey)
  const apiBasePath = resolveCurrentApiBasePath()
  const response = await fetch(`${apiBasePath}api/requests`, {
    cache: 'no-store',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    signal,
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }

  return response.json()
}

export async function createUserRequest(apiKey: string, content: string, taskId?: string): Promise<UserRequest> {
  const authorization = ensureAuthorization(apiKey)
  const apiBasePath = resolveCurrentApiBasePath()
  const response = await fetch(`${apiBasePath}api/requests`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ content, task_id: taskId }),
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }

  const payload = await response.json()
  return payload.request as UserRequest
}

export async function deleteUserRequest(apiKey: string, requestId: string): Promise<void> {
  const authorization = ensureAuthorization(apiKey)
  const apiBasePath = resolveCurrentApiBasePath()
  const response = await fetch(`${apiBasePath}api/requests/${requestId}`, {
    method: 'DELETE',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }
}

export async function deleteAllUserRequests(apiKey: string): Promise<number> {
  const authorization = ensureAuthorization(apiKey)
  const apiBasePath = resolveCurrentApiBasePath()
  const response = await fetch(`${apiBasePath}api/requests`, {
    method: 'DELETE',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }

  const payload = await response.json()
  return Number(payload.deleted ?? 0)
}

// ============================================================================
// Saved Commands API
// ============================================================================

/**
 * listSavedCommands fetches all saved commands for the authenticated user.
 */
export async function listSavedCommands(apiKey: string, signal?: AbortSignal): Promise<SavedCommandListResponse> {
  const authorization = ensureAuthorization(apiKey)
  const apiBasePath = resolveCurrentApiBasePath()
  const response = await fetch(`${apiBasePath}api/saved-commands`, {
    cache: 'no-store',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    signal,
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }

  return response.json()
}

/**
 * createSavedCommand stores a new saved command for the authenticated user.
 */
export async function createSavedCommand(apiKey: string, label: string, content: string): Promise<SavedCommand> {
  const authorization = ensureAuthorization(apiKey)
  const apiBasePath = resolveCurrentApiBasePath()
  const response = await fetch(`${apiBasePath}api/saved-commands`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ label, content }),
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }

  const payload = await response.json()
  return payload.command as SavedCommand
}

/**
 * updateSavedCommand modifies an existing saved command belonging to the authenticated user.
 */
export async function updateSavedCommand(
  apiKey: string,
  commandId: string,
  updates: { label?: string; content?: string; sort_order?: number }
): Promise<SavedCommand> {
  const authorization = ensureAuthorization(apiKey)
  const apiBasePath = resolveCurrentApiBasePath()
  const response = await fetch(`${apiBasePath}api/saved-commands/${commandId}`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify(updates),
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }

  const payload = await response.json()
  return payload.command as SavedCommand
}

/**
 * deleteSavedCommand removes a single saved command belonging to the authenticated user.
 */
export async function deleteSavedCommand(apiKey: string, commandId: string): Promise<void> {
  const authorization = ensureAuthorization(apiKey)
  const apiBasePath = resolveCurrentApiBasePath()
  const response = await fetch(`${apiBasePath}api/saved-commands/${commandId}`, {
    method: 'DELETE',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }
}

/**
 * reorderSavedCommands updates the sort order for multiple saved commands at once.
 */
export async function reorderSavedCommands(apiKey: string, orderedIds: string[]): Promise<void> {
  const authorization = ensureAuthorization(apiKey)
  const apiBasePath = resolveCurrentApiBasePath()
  const response = await fetch(`${apiBasePath}api/saved-commands/reorder`, {
    method: 'PUT',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ ordered_ids: orderedIds }),
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }
}
