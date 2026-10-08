import { useCallback, useEffect, useReducer, useRef } from 'react'
import { ApiError } from '../api/client'
import { sharedRequest, type SharedInbox, type SharedMailbox } from '../api/sharing'
import type { FullMessage, InboxMessage } from '../api/types'

interface State {
  info: SharedMailbox | null
  result: SharedInbox | null
  selected: InboxMessage | null
  detail: FullMessage | null
  loading: boolean
  detailLoading: boolean
  error: string
  detailError: string
  unavailable: boolean
}

const initialState: State = { info: null, result: null, selected: null, detail: null, loading: true, detailLoading: false, error: '', detailError: '', unavailable: false }
const unavailableMessage = '链接已失效或分发已终止，请联系管理员获取新的链接。'

export function useSharedMailbox(token: string) {
  const [state, dispatch] = useReducer((current: State, patch: Partial<State>): State => ({ ...current, ...patch }), initialState)
  const query = useRef({ page: 1, pageSize: 20, search: '' })
  const listRequest = useRef<AbortController | null>(null)
  const detailRequest = useRef<AbortController | null>(null)
  const accessRequest = useRef<AbortController | null>(null)
  const generation = useRef(0)
  const unavailable = useRef(false)

  const clearAccess = useCallback((error: unknown) => {
    generation.current++
    unavailable.current = error instanceof ApiError && (error.code === 'SHARE_UNAVAILABLE' || error.status === 401 || error.status === 403)
    listRequest.current?.abort()
    detailRequest.current?.abort()
    dispatch({ ...initialState, loading: false, unavailable: unavailable.current,
      error: unavailable.current ? unavailableMessage : error instanceof ApiError ? error.message : '读取失败，请重试' })
  }, [])

  const loadList = useCallback(async (next = query.current) => {
    if (!token || unavailable.current) {
      clearAccess(new ApiError(404, 'SHARE_UNAVAILABLE', unavailableMessage))
      return
    }
    query.current = next
    listRequest.current?.abort()
    detailRequest.current?.abort()
    const controller = new AbortController()
    listRequest.current = controller
    const version = generation.current
    dispatch({ loading: true, error: '', result: null, selected: null, detail: null, detailError: '', detailLoading: false })
    try {
      const params = new URLSearchParams({ page: String(next.page), page_size: String(next.pageSize), q: next.search })
      const result = await sharedRequest<SharedInbox>(token, `/inbox?${params}`, controller.signal)
      if (controller.signal.aborted || version !== generation.current) return
      query.current.page = result.page
      dispatch({ result, info: { email: result.email, created_at: result.created_at }, loading: false })
    } catch (error) {
      if (!controller.signal.aborted && version === generation.current) clearAccess(error)
    }
  }, [clearAccess, token])

  const validateAccess = useCallback(async () => {
    if (!token || unavailable.current || accessRequest.current) return
    const controller = new AbortController()
    accessRequest.current = controller
    const version = generation.current
    try {
      const info = await sharedRequest<SharedMailbox>(token, '', controller.signal)
      if (!controller.signal.aborted && version === generation.current) dispatch({ info })
    } catch (error) {
      if (!controller.signal.aborted && version === generation.current) clearAccess(error)
    } finally {
      if (accessRequest.current === controller) accessRequest.current = null
    }
  }, [clearAccess, token])

  useEffect(() => {
    const lifecycle = generation
    void loadList()
    const timer = window.setInterval(() => { if (!document.hidden) void validateAccess() }, 15_000)
    const resume = () => { if (!document.hidden) void validateAccess() }
    window.addEventListener('focus', resume)
    window.addEventListener('pageshow', resume)
    document.addEventListener('visibilitychange', resume)
    return () => {
      lifecycle.current++
      listRequest.current?.abort()
      detailRequest.current?.abort()
      accessRequest.current?.abort()
      window.clearInterval(timer)
      window.removeEventListener('focus', resume)
      window.removeEventListener('pageshow', resume)
      document.removeEventListener('visibilitychange', resume)
    }
  }, [loadList, validateAccess])

  const openMessage = useCallback(async (message: InboxMessage) => {
    if (unavailable.current) return
    detailRequest.current?.abort()
    const controller = new AbortController()
    detailRequest.current = controller
    const version = generation.current
    dispatch({ selected: message, detail: null, detailLoading: true, detailError: '' })
    try {
      const detail = await sharedRequest<FullMessage>(token, `/inbox/${encodeURIComponent(message.id)}`, controller.signal)
      if (!controller.signal.aborted && version === generation.current) dispatch({ detail, detailLoading: false })
    } catch (error) {
      if (controller.signal.aborted || version !== generation.current) return
      if (error instanceof ApiError && (error.code === 'SHARE_UNAVAILABLE' || error.status === 401 || error.status === 403)) clearAccess(error)
      else dispatch({ detailLoading: false, detailError: error instanceof ApiError ? error.message : '读取邮件失败' })
    }
  }, [clearAccess, token])

  function closeMessage() {
    detailRequest.current?.abort()
    dispatch({ selected: null, detail: null, detailLoading: false, detailError: '' })
  }

  return { ...state, refresh: () => void loadList(), openMessage, closeMessage,
    search: (value: string) => void loadList({ ...query.current, page: 1, search: value.trim() }),
    changePage: (page: number) => void loadList({ ...query.current, page }),
    changePageSize: (pageSize: number) => void loadList({ ...query.current, page: 1, pageSize }),
    retryDetail: () => { if (state.selected) void openMessage(state.selected) },
  }
}
