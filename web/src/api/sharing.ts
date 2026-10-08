import { ApiError } from './client'
import type { ApiResponse, InboxMessage } from './types'

export interface ShareStatus {
  active: boolean
  created_at?: string
}

export interface GeneratedShare extends ShareStatus {
  token: string
  created_at: string
}

export interface SharedMailbox {
  email: string
  created_at: string
}

export interface SharedInbox extends SharedMailbox {
  count: number
  total: number
  page: number
  page_size: number
  messages: InboxMessage[]
}

// The fragment never reaches access logs. Customer requests never carry an
// administrator cookie/CSRF token or touch the administrator's response cache.
export function shareURL(token: string): string {
  return `${window.location.origin}/share#${encodeURIComponent(token)}`
}

export async function sharedRequest<T>(token: string, path: string, signal?: AbortSignal): Promise<T> {
  let response: Response
  try {
    response = await fetch(`/api/shared${path}`, {
      method: 'GET', credentials: 'omit', cache: 'no-store', signal,
      headers: { Accept: 'application/json', Authorization: `Bearer ${token}` },
    })
  } catch (error) {
    if (signal?.aborted) throw error
    throw new ApiError(0, 'NETWORK_ERROR', '网络连接失败，请稍后重试')
  }
  let payload: ApiResponse<T>
  try {
    payload = await response.json() as ApiResponse<T>
  } catch {
    throw new ApiError(response.status, 'INVALID_RESPONSE', '服务暂时不可用，请稍后重试')
  }
  if (!response.ok || !payload.success) {
    throw new ApiError(response.status, payload.code ?? 'INTERNAL_ERROR', payload.message ?? '读取失败')
  }
  return payload.data as T
}
