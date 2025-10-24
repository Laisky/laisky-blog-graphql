const API_BASE_PATH = (() => {
  if (typeof window === 'undefined') {
    return '/'
  }
  const path = window.location.pathname || '/'
  return path.endsWith('/') ? path : `${path}/`
})()

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
  const response = await fetch(`${API_BASE_PATH}api/requests`, {
    headers: {
      Authorization: `Bearer ${apiKey}`,
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
  const response = await fetch(`${API_BASE_PATH}api/requests/${requestId}`, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${apiKey}`,
    },
    body: JSON.stringify({ answer }),
  })

  if (!response.ok) {
    const message = (await response.text()) || response.statusText
    throw new Error(message)
  }
}
