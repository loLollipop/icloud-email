import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { request, ApiError } from '../api/client'
import type { AccountSummary, Alias, FullMessage, InboxResult, InboxMessage } from '../api/types'
import { invalidateResource, invalidateResourcePrefix, loadResource, readResource, resourceKeys, resourceTTLs } from '../api/resourceCache'
import { useToast } from '../components/ToastProvider'

type DeleteTarget = { accountId: string; message: InboxMessage }

function inboxQuery(accountId: string, alias: string, search: string, page: number, pageSize: number): string {
  const params = new URLSearchParams({ account_id: accountId, scope: 'hme_aliases', page: String(page), page_size: String(pageSize), field: 'subject' })
  if (alias) params.set('alias', alias)
  if (search) params.set('q', search)
  return params.toString()
}

function pageNumber(raw: string | null): number {
  const value = Number(raw)
  return Number.isInteger(value) && value >= 1 && value <= 1_000_000 ? value : 1
}

export function useInboxWorkspace() {
  const [searchParams, setSearchParams] = useSearchParams()
  const paramsRef = useRef(searchParams)
  const accountChangedRef = useRef(false)
  const accountsCached = readResource<AccountSummary[]>(resourceKeys.accounts)
  const [accounts, setAccounts] = useState<AccountSummary[]>(accountsCached?.data ?? [])
  const [accountsLoading, setAccountsLoading] = useState(!accountsCached)
  const [aliases, setAliases] = useState<Alias[]>([])
  const requestedAccountId = searchParams.get('account_id')
  // A message UID belongs to exactly one account. Never resolve an unavailable
  // account's deep link against another account, even from a stale cache.
  const accountId = accounts.find((account) => account.id === requestedAccountId)?.id
    ?? (requestedAccountId && searchParams.has('message') ? '' : accounts[0]?.id ?? '')
  const alias = searchParams.get('alias') ?? ''
  const appliedSearch = (searchParams.get('q') ?? '').trim()
  const page = pageNumber(searchParams.get('page'))
  const requestedPageSize = Number(searchParams.get('page_size'))
  const pageSize = [10, 20, 50].includes(requestedPageSize) ? requestedPageSize : 20
  const [search, setSearch] = useState(appliedSearch)
  const query = inboxQuery(accountId, alias, appliedSearch, page, pageSize)
  const cached = accountId ? readResource<InboxResult>(resourceKeys.inbox(query)) : undefined
  const [snapshot, setSnapshot] = useState<{ query: string; data: InboxResult } | null>(cached ? { query, data: cached.data } : null)
  const result = snapshot?.query === query ? snapshot.data : cached?.data ?? null
  const [loadingQuery, setLoadingQuery] = useState(cached ? '' : query)
  const [error, setError] = useState('')
  const loading = accountsLoading || Boolean(accountId && !error && (loadingQuery === query || !result))
  const [retryKey, setRetryKey] = useState(0)
  const [accountsRetryKey, setAccountsRetryKey] = useState(0)
  const [selected, setSelected] = useState<InboxMessage | null>(null)
  const [detail, setDetail] = useState<FullMessage | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState('')
  const [deleteFor, setDeleteFor] = useState<DeleteTarget | null>(null)
  const [deleting, setDeleting] = useState(false)
  const inboxRequestRef = useRef(0)
  const detailAbortRef = useRef<AbortController | null>(null)
  const detailRequestRef = useRef(0)
  const currentAccountRef = useRef(accountId)
  const selectedRef = useRef<InboxMessage | null>(null)
  const appliedSearchRef = useRef(appliedSearch)
  const { show } = useToast()

  useEffect(() => { paramsRef.current = searchParams }, [searchParams])

  function resetSelection() {
    detailRequestRef.current++
    detailAbortRef.current?.abort()
    detailAbortRef.current = null
    setDetailLoading(false)
    setDetailError('')
    selectedRef.current = null
    setSelected(null)
    setDetail(null)
  }

  function closeMessage() {
    resetSelection()
    const next = new URLSearchParams(paramsRef.current)
    next.delete('message')
    paramsRef.current = next
    setSearchParams(next, { replace: true })
  }

  async function fetchDetail(message: InboxMessage) {
    detailAbortRef.current?.abort()
    const requestID = ++detailRequestRef.current
    selectedRef.current = message
    setSelected(message)
    setDetail(null)
    setDetailError('')
    setDetailLoading(false)
    if (result?.method !== 'imap') return
    const controller = new AbortController()
    detailAbortRef.current = controller
    setDetailLoading(true)
    try {
      const data = await request<FullMessage>(`/api/inbox/${encodeURIComponent(message.id)}?account_id=${encodeURIComponent(accountId)}`, { signal: controller.signal })
      if (requestID !== detailRequestRef.current || controller.signal.aborted) return
      setDetail(data)
    } catch (err) {
      if (requestID !== detailRequestRef.current || controller.signal.aborted) return
      setDetailError(err instanceof ApiError ? err.message : '读取邮件详情失败，请重试')
    } finally {
      if (requestID === detailRequestRef.current) setDetailLoading(false)
    }
  }

  function openMessage(message: InboxMessage) {
    const next = new URLSearchParams(paramsRef.current)
    next.set('account_id', accountId)
    const wasReading = next.has('message')
    next.set('message', message.id)
    paramsRef.current = next
    setSearchParams(next, { replace: wasReading })
    void fetchDetail(message)
  }

  useEffect(() => () => detailAbortRef.current?.abort(), [])

  async function deleteMessage() {
    if (!deleteFor || result?.method !== 'imap') return
    const target = deleteFor
    setDeleting(true)
    try {
      await request(`/api/inbox/${encodeURIComponent(target.message.id)}?account_id=${encodeURIComponent(target.accountId)}`, { method: 'DELETE' })
      setDeleteFor((current) => current === target ? null : current)
      if (currentAccountRef.current === target.accountId && selectedRef.current?.id === target.message.id) closeMessage()
      show('邮件已删除')
      invalidateResourcePrefix(resourceKeys.inboxAccountPrefix(target.accountId))
      if (currentAccountRef.current === target.accountId) {
        setLoadingQuery(query)
        setRetryKey((key) => key + 1)
      }
    } catch (err) {
      show(err instanceof ApiError ? err.message : '删除邮件失败')
    } finally {
      setDeleting(false)
    }
  }

  useEffect(() => {
    let cancelled = false
    loadResource(resourceKeys.accounts, resourceTTLs.accounts, () => request<AccountSummary[]>('/api/accounts'))
      .then((data) => {
        if (cancelled) return
        setAccounts(data)
        setError('')
        if (accountChangedRef.current) return
        const queryId = paramsRef.current.get('account_id')
        const target = data.find((account) => account.id === queryId)?.id ?? data[0]?.id ?? ''
        currentAccountRef.current = target
        if (target) {
          const next = new URLSearchParams(paramsRef.current)
          if (queryId && queryId !== target) {
            if (next.has('message')) show('邮件所属账户不可用，已返回收件箱')
            next.delete('message')
            next.delete('alias')
          }
          next.set('account_id', target)
          next.delete('limit')
          next.delete('days')
          next.delete('field')
          paramsRef.current = next
          setSearchParams(next, { replace: true })
        }
      })
      .catch((err) => {
        if (cancelled) return
        if (!accountsCached) setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
        setLoadingQuery('')
      })
      .finally(() => { if (!cancelled) setAccountsLoading(false) })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [accountsRetryKey])

  useEffect(() => {
    if (appliedSearchRef.current !== appliedSearch) {
      appliedSearchRef.current = appliedSearch
      setSearch(appliedSearch)
    }
  }, [appliedSearch])

  useEffect(() => {
    if (search.trim() === appliedSearch) return
    const timer = setTimeout(() => submitSearch(search), 400)
    return () => clearTimeout(timer)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search, appliedSearch])

  useEffect(() => {
    if (!accountId) return
    currentAccountRef.current = accountId
    let cancelled = false
    loadResource(resourceKeys.aliases(accountId), resourceTTLs.aliases, () => request<{ aliases: Alias[] }>(`/api/aliases?account_id=${encodeURIComponent(accountId)}`))
      .then((data) => { if (!cancelled) setAliases(data.aliases ?? []) })
      .catch(() => { if (!cancelled) setAliases([]) })
    return () => { cancelled = true }
  }, [accountId])

  useEffect(() => {
    if (!accountId) return
    const requestID = ++inboxRequestRef.current
    let cancelled = false
    const key = resourceKeys.inbox(query)
    const cached = readResource<InboxResult>(key)
    /* eslint-disable react-hooks/set-state-in-effect */
    if (cached) { setSnapshot({ query, data: cached.data }); setLoadingQuery('') } else { setSnapshot(null); setLoadingQuery(query) }
    setError('')
    resetSelection()
    /* eslint-enable react-hooks/set-state-in-effect */
    loadResource(key, resourceTTLs.inbox, () => request<InboxResult>(`/api/inbox?${query}`), Boolean(cached))
      .then((data) => {
        if (cancelled || requestID !== inboxRequestRef.current) return
        setSnapshot({ query, data })
        setError('')
      })
      .catch((err) => {
        if (cancelled || requestID !== inboxRequestRef.current) return
        invalidateResource(key)
        setDeleteFor(null)
        resetSelection()
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
        setSnapshot(null)
      })
      .finally(() => { if (!cancelled && requestID === inboxRequestRef.current) setLoadingQuery('') })
    return () => { cancelled = true }
  }, [accountId, query, retryKey])

  // Back/Forward and direct links restore reading without changing list filters.
  // URL navigation is the external source of truth for this state transition.
  /* eslint-disable react-hooks/set-state-in-effect */
  useEffect(() => {
    const messageId = searchParams.get('message')
    if (!messageId) {
      if (selectedRef.current) resetSelection()
      return
    }
    if (!result || loading || selectedRef.current?.id === messageId) return
    const message = result.messages.find((item) => item.id === messageId)
    if (message) void fetchDetail(message)
    else if (result.method === 'imap') void fetchDetail({ id: messageId, subject: '正在打开邮件…', from: '', to: '', date: '', preview: '' })
    // fetchDetail is guarded by identity and request generations; it reads the
    // current account/result. List revalidation must not refetch an open message.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [searchParams, result, loading, accountId])
  /* eslint-enable react-hooks/set-state-in-effect */

  function changeQuery(values: Record<string, string>, resetPage = true) {
    resetSelection()
    setError('')
    const next = new URLSearchParams(paramsRef.current)
    if (currentAccountRef.current) next.set('account_id', currentAccountRef.current)
    for (const key of ['limit', 'days', 'field', 'message']) next.delete(key)
    if (resetPage) next.set('page', '1')
    for (const [key, value] of Object.entries(values)) {
      if (value) next.set(key, value)
      else next.delete(key)
    }
    paramsRef.current = next
    setSearchParams(next, { replace: true })
  }

  function refresh() {
    if (!accounts.length) {
      invalidateResource(resourceKeys.accounts)
      setAccountsLoading(true)
      setError('')
      setAccountsRetryKey((key) => key + 1)
      return
    }
    invalidateResource(resourceKeys.inbox(query))
    closeMessage()
    setLoadingQuery(query)
    setRetryKey((key) => key + 1)
  }

  function submitSearch(value: string) {
    const nextSearch = value.trim()
    appliedSearchRef.current = nextSearch
    setSearch(nextSearch)
    if (nextSearch !== appliedSearch) changeQuery({ q: nextSearch })
  }

  function handleAccountChange(id: string) {
    accountChangedRef.current = true
    currentAccountRef.current = id
    setAliases([])
    changeQuery({ account_id: id, alias: '' })
  }

  return {
    accounts, accountsLoading, accountId, aliases, alias, search, setSearch, appliedSearch,
    query, result, loading, error, selected, detail, detailLoading, detailError,
    deleteFor, setDeleteFor, deleting, deleteMessage, openMessage, closeMessage,
    retryDetail: () => { if (selected) void fetchDetail(selected) },
    changeQuery, refresh, submitSearch, handleAccountChange,
  }
}
