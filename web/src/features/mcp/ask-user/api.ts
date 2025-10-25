const API_BASE_PATH = (() => {
  if (typeof window === 'undefined') {
    return '/'
  }
  const path = window.location.pathname || '/'
  return path.endsWith('/') ? path : `${path}/`
})()

const BEARER_PREFIX = /^Bearer\s+/i

export function normalizeApiKey(value: string): string {
  let output = (value ?? '').trim()
  while (output && BEARER_PREFIX.test(output)) {
    output = output.replace(BEARER_PREFIX, '').trim()
  }
  return output
}

export function buildAuthorizationHeader(apiKey: string): string {
  const token = normalizeApiKey(apiKey)
  return token ? `Bearer ${token}` : ''
}

export interface AskUserRequest {
  id: string
  question: string
  status: string
  created_at: string
  updated_at: string
  ai_identity?: string
  user_identity?: string
  answer?: string | null
  answered_at?: string | null
}

export interface AskUserListResponse {
  pending?: AskUserRequest[]
  history?: AskUserRequest[]
  user_id?: string
  ai_id?: string
  key_hint?: string
}

export async function listRequests(apiKey: string, signal?: AbortSignal): Promise<AskUserListResponse> {
  const authorization = buildAuthorizationHeader(apiKey)
  if (!authorization) {
    throw new Error('API key is required')
  }
  const response = await fetch(`${API_BASE_PATH}api/requests`, {
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

export async function submitAnswer(
  apiKey: string,
  requestId: string,
  answer: string,
): Promise<void> {
  const authorization = buildAuthorizationHeader(apiKey)
  if (!authorization) {
    throw new Error('API key is required')
  }
  const response = await fetch(`${API_BASE_PATH}api/requests/${requestId}`, {
    cache: 'no-store',
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ answer }),
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }
}
