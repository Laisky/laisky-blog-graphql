import { requestWithUnexpectedError, markPrivateError, readPrivateJsonResponse } from '@/lib/private-error';
import { buildAuthorizationHeader, resolveToolApiBase } from '../shared/auth';

export interface AskUserRequest {
  id: string;
  question: string;
  status: string;
  created_at: string;
  updated_at: string;
  ai_identity?: string;
  user_identity?: string;
  answer?: string | null;
  answered_at?: string | null;
}

export interface AskUserListResponse {
  pending?: AskUserRequest[];
  history?: AskUserRequest[];
  user_id?: string;
  ai_id?: string;
  key_hint?: string;
}

export async function listRequests(apiKey: string, signal?: AbortSignal): Promise<AskUserListResponse> {
  const authorization = buildAuthorizationHeader(apiKey);
  if (!authorization) {
    throw new Error('API key is required');
  }
  const apiBasePath = resolveToolApiBase('ask_user');
  const response = await requestWithUnexpectedError(`${apiBasePath}api/requests`, {
    cache: 'no-store',
    headers: {
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    signal,
  }, 'ask-user');

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw markPrivateError(new Error(message), 'Ask user request failed');
  }

  return readPrivateJsonResponse(response, 'Invalid ask user JSON response');
}

export async function submitAnswer(apiKey: string, requestId: string, answer: string): Promise<void> {
  const authorization = buildAuthorizationHeader(apiKey);
  if (!authorization) {
    throw new Error('API key is required');
  }
  const apiBasePath = resolveToolApiBase('ask_user');
  const response = await requestWithUnexpectedError(`${apiBasePath}api/requests/${requestId}`, {
    cache: 'no-store',
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
      Authorization: authorization,
      'Cache-Control': 'no-store',
      Pragma: 'no-cache',
    },
    body: JSON.stringify({ answer }),
  }, 'ask-user');

  if (!response.ok) {
    const message = (await response.text()) || response.statusText;
    throw markPrivateError(new Error(message), 'Ask user request failed');
  }
}
