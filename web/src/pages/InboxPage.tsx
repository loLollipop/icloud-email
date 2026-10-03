import { useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { request, ApiError } from '../api/client'
import type { AccountSummary, Alias, FullMessage, InboxResult, InboxMessage } from '../api/types'
import { invalidateResource, invalidateResourcePrefix, loadResource, readResource, resourceKeys, resourceTTLs } from '../api/resourceCache'
import AsyncState from '../components/AsyncState'
import ConfirmDialog from '../components/ConfirmDialog'
import MailHtmlFrame from '../components/MailHtmlFrame'
import Pagination from '../components/Pagination'
import { useToast } from '../components/ToastProvider'
import { IconKey, IconMail, IconRefresh, IconSearch, IconTrash } from '../components/icons'

type SearchField = 'all' | 'subject' | 'from' | 'to' | 'body'
type DeleteTarget = { accountId: string; message: InboxMessage }

function formatDate(raw: string): string {
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return raw
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(date)
}

function inboxQuery(accountID: string, alias: string, search: string, field: SearchField, page: number, pageSize: number): string {
  const params = new URLSearchParams({ account_id: accountID, scope: 'hme_aliases', page: String(page), page_size: String(pageSize), field })
  if (alias) params.set('alias', alias)
  if (search) params.set('q', search)
  return params.toString()
}

function searchField(raw: string | null): SearchField {
  return raw === 'subject' || raw === 'from' || raw === 'to' || raw === 'body' ? raw : 'all'
}

function pageNumber(raw: string | null): number {
  const value = Number(raw)
  return Number.isInteger(value) && value >= 1 && value <= 1_000_000 ? value : 1
}

export default function InboxPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const paramsRef = useRef(searchParams)
  const accountChangedRef = useRef(false)
  const accountsCached = readResource<AccountSummary[]>(resourceKeys.accounts)
  const [accounts, setAccounts] = useState<AccountSummary[]>(accountsCached?.data ?? [])
  const [accountsLoading, setAccountsLoading] = useState(!accountsCached)
  const [aliases, setAliases] = useState<Alias[]>([])
  const accountId = accounts.find((account) => account.id === searchParams.get('account_id'))?.id ?? accounts[0]?.id ?? ''
  const alias = searchParams.get('alias') ?? ''
  const field = searchField(searchParams.get('field'))
  const appliedSearch = (searchParams.get('q') ?? '').trim()
  const page = pageNumber(searchParams.get('page'))
  const requestedPageSize = Number(searchParams.get('page_size'))
  const pageSize = [10, 20, 50].includes(requestedPageSize) ? requestedPageSize : 20
  const [search, setSearch] = useState(appliedSearch)
  const query = inboxQuery(accountId, alias, appliedSearch, field, page, pageSize)
  const cached = accountId ? readResource<InboxResult>(resourceKeys.inbox(query)) : undefined
  const [snapshot, setSnapshot] = useState<{ query: string; data: InboxResult } | null>(cached ? { query, data: cached.data } : null)
  const result = snapshot?.query === query ? snapshot.data : cached?.data ?? null
  const [loadingQuery, setLoadingQuery] = useState(cached ? '' : query)
  const loading = accountId ? loadingQuery === query || !result : accountsLoading
  const [error, setError] = useState('')
  const [retryKey, setRetryKey] = useState(0)
  const [selected, setSelected] = useState<InboxMessage | null>(null)
  const [detail, setDetail] = useState<FullMessage | null>(null)
  const [detailLoading, setDetailLoading] = useState(false)
  const [deleteFor, setDeleteFor] = useState<DeleteTarget | null>(null)
  const [deleting, setDeleting] = useState(false)
  const inboxRequestRef = useRef(0)
  const detailAbortRef = useRef<AbortController | null>(null)
  const detailRequestRef = useRef(0)
  const currentAccountRef = useRef(accountId)
  const selectedRef = useRef<InboxMessage | null>(null)
  const appliedSearchRef = useRef(appliedSearch)
  const backButtonRef = useRef<HTMLButtonElement | null>(null)
  const { show } = useToast()

  useEffect(() => { paramsRef.current = searchParams }, [searchParams])

  function clearSelection() {
    detailRequestRef.current++
    detailAbortRef.current?.abort()
    detailAbortRef.current = null
    setDetailLoading(false)
    selectedRef.current = null
    setSelected(null)
    setDetail(null)
  }

  async function openMessage(message: InboxMessage) {
    detailAbortRef.current?.abort()
    detailAbortRef.current = null
    const requestID = ++detailRequestRef.current
    selectedRef.current = message
    setSelected(message)
    setDetail(null)
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
      show(err instanceof ApiError ? err.message : '读取邮件详情失败')
    } finally {
      if (requestID === detailRequestRef.current) setDetailLoading(false)
    }
  }

  useEffect(() => () => detailAbortRef.current?.abort(), [])

  useEffect(() => {
    if (selected) backButtonRef.current?.focus()
  }, [selected])

  async function deleteMessage() {
    if (!deleteFor || result?.method !== 'imap') return
    const target = deleteFor
    setDeleting(true)
    try {
      await request(`/api/inbox/${encodeURIComponent(target.message.id)}?account_id=${encodeURIComponent(target.accountId)}`, { method: 'DELETE' })
      setDeleteFor((current) => current === target ? null : current)
      if (currentAccountRef.current === target.accountId && selectedRef.current?.id === target.message.id) clearSelection()
      show('邮件已删除')
      invalidateResourcePrefix(resourceKeys.inboxAccountPrefix(target.accountId))
      setLoadingQuery(query)
      setRetryKey((key) => key + 1)
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
        if (accountChangedRef.current) return
        const queryId = searchParams.get('account_id')
        const target = data.find((account) => account.id === queryId)?.id ?? data[0]?.id ?? ''
        currentAccountRef.current = target
        if (target) {
          const next = new URLSearchParams(paramsRef.current)
          next.set('account_id', target)
          next.delete('limit')
          next.delete('days')
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
  }, [])

  useEffect(() => {
    if (appliedSearchRef.current !== appliedSearch) {
      appliedSearchRef.current = appliedSearch
      // URL navigation restores the visible search together with its results.
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
    clearSelection()
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
        clearSelection()
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
        setSnapshot(null)
      })
      .finally(() => { if (!cancelled && requestID === inboxRequestRef.current) setLoadingQuery('') })
    return () => { cancelled = true }
  }, [accountId, query, retryKey])

  function changeQuery(values: Record<string, string>, resetPage = true) {
    clearSelection()
    setError('')
    const next = new URLSearchParams(paramsRef.current)
    if (currentAccountRef.current) next.set('account_id', currentAccountRef.current)
    next.delete('limit')
    next.delete('days')
    if (resetPage) next.set('page', '1')
    for (const [key, value] of Object.entries(values)) {
      if (value) next.set(key, value)
      else next.delete(key)
    }
    paramsRef.current = next
    setSearchParams(next, { replace: true })
  }

  function refresh() {
    invalidateResource(resourceKeys.inbox(query))
    clearSelection()
    setLoadingQuery(query)
    setRetryKey((key) => key + 1)
  }

  function submitSearch(value: string) {
    const nextSearch = value.trim()
    appliedSearchRef.current = nextSearch
    setSearch(nextSearch)
    if (nextSearch === appliedSearch) return
    changeQuery({ q: nextSearch })
  }

  function handleAccountChange(id: string) {
    accountChangedRef.current = true
    currentAccountRef.current = id
    setAliases([])
    changeQuery({ account_id: id, alias: '' })
  }

  const isImap = result?.method === 'imap'

  if (selected) {
    const readStatus = detailLoading ? '正在读取' : isImap ? (detail ? '已读取' : '读取失败') : '摘要模式'
    return (
      <section className="inbox-page inbox-detail-page">
        <article className="mail-detail" aria-label="邮件阅读区">
          <button ref={backButtonRef} type="button" className="mail-back-button" onClick={clearSelection}>← 返回全部邮件</button>
          <header className="mail-reader-header">
            <h2>{selected.subject || '（无主题）'}</h2>
            <dl>
              <div><dt>发件人</dt><dd>{selected.from || '（未知发件人）'}</dd></div>
              <div><dt>收件人</dt><dd>{selected.to || '—'}</dd></div>
              <div><dt>日期</dt><dd>{formatDate(selected.date)}</dd></div>
              <div><dt>读取状态</dt><dd>{readStatus}</dd></div>
            </dl>
          </header>
          {detailLoading && <p className="hint" role="status">读取中…</p>}
          {!isImap && <div className="mail-summary-only"><p>{selected.preview || '暂无摘要'}</p><p className="hint">当前仅显示邮件摘要；配置 App 专用密码后可阅读正文和删除。</p></div>}
          {detail && <>{detail.body_truncated && <p className="alert-info mail-truncated-notice">邮件正文过长，已截断显示。</p>}{detail.html_body ? <MailHtmlFrame html={detail.html_body} /> : <pre className="mail-body">{detail.body || '无正文'}</pre>}<div className="mail-reader-actions"><button className="danger" onClick={() => setDeleteFor({ accountId, message: detail })}><IconTrash size={14} />删除邮件</button></div></>}
        </article>
        {deleteFor && isImap && <ConfirmDialog title="删除邮件" message="邮件将从收件箱中永久删除。" open busy={deleting} onClose={() => setDeleteFor(null)} onConfirm={() => void deleteMessage()} />}
      </section>
    )
  }

  return (
    <section className="inbox-page">
      <div className="page-header"><div className="page-title"><h2>收件箱</h2><p>仅展示当前账号所有 iCloud 隐私别名收到的邮件</p></div></div>
      <div className="inbox-toolbar card">
        <div className="toolbar-field"><label htmlFor="inbox-account">账号</label><select id="inbox-account" value={accountId} onChange={(event) => handleAccountChange(event.target.value)}>{accounts.map((account) => <option key={account.id} value={account.id}>{account.name}</option>)}</select></div>
        <div className="toolbar-field"><label htmlFor="inbox-alias">别名</label><select id="inbox-alias" value={alias} onChange={(event) => changeQuery({ alias: event.target.value })}><option value="">全部 iCloud 隐私别名</option>{aliases.map((item) => <option key={item.anonymousId} value={item.email}>{item.email}</option>)}</select></div>
        <div className="toolbar-actions"><button onClick={refresh} disabled={loading && !error}><IconRefresh size={16} />刷新邮件</button></div>
      </div>
      <form className="inbox-search" onSubmit={(event) => { event.preventDefault(); submitSearch(search) }}>
        <select aria-label="搜索字段" value={field} onChange={(event) => changeQuery({ field: event.target.value, q: search.trim() })}><option value="all">全部内容</option><option value="subject">主题</option><option value="from">发件人</option><option value="to">收件人</option><option value="body">正文</option></select>
        <div className="search-input-wrap"><input type="search" aria-label="搜索邮件" maxLength={256} value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索主题、邮箱或正文关键词" /><span className="search-input-icon" aria-hidden="true"><IconSearch size={16} /></span></div>
        <button type="submit" className="primary"><IconSearch size={16} />搜索</button>
      </form>
      <AsyncState loading={loading && !error} error={error} empty={!result || result.messages.length === 0} emptyText={appliedSearch ? '没有找到匹配的邮件，试试其他关键词' : '暂无邮件'} onRetry={refresh}>
        {result && result.messages.length > 0 && <>
          <div className="inbox-summary"><span>{appliedSearch ? `“${appliedSearch}” 的搜索结果` : '全部邮件'} · 共 {result.total} 封</span><span className={isImap ? 'badge badge-info' : 'badge badge-neutral'}>{isImap ? <IconKey size={12} /> : <IconMail size={12} />}{isImap ? '可阅读完整邮件' : 'Web API 摘要模式'}</span></div>
          {!isImap && <div className="alert-info inbox-mode-notice">当前仅提供邮件摘要；配置 App 专用密码后可阅读正文和删除。</div>}
          <div className="mail-workspace">
            <div className="mail-list" aria-label="邮件列表">
              {result.messages.map((message) => (
                <button
                  type="button"
                  key={message.id}
                  className="mail-list-item"
                  aria-label={message.subject || '（无主题）'}
                  aria-describedby={`inbox-recipient-${message.id}`}
                  onClick={() => void openMessage(message)}
                >
                  <strong className="mail-list-sender">{message.from || '（未知发件人）'}</strong>
                  <span className="mail-list-copy">
                    <span className="mail-list-subject">{message.subject || '（无主题）'}</span>
                    <span className="mail-list-recipient" id={`inbox-recipient-${message.id}`}>
                      <IconMail size={14} />
                      <span className="mail-list-recipient-label">收件：</span>
                      <span className="mail-list-recipient-address" title={message.to.trim() || undefined}>{message.to.trim() || '未提供收件邮箱'}</span>
                    </span>
                    <span className="mail-list-preview">{message.preview || '—'}</span>
                  </span>
                  <time className="mail-list-date" dateTime={message.date}>{formatDate(message.date)}</time>
                </button>
              ))}
              <Pagination page={result.page} pageSize={result.page_size} totalItems={result.total} onPageChange={(next) => changeQuery({ page: String(next) }, false)} onPageSizeChange={(size) => changeQuery({ page_size: String(size) })} pageSizeOptions={[10, 20, 50]} label="邮件列表分页" />
            </div>
          </div>
        </>}
      </AsyncState>
      {deleteFor && isImap && <ConfirmDialog title="删除邮件" message="邮件将从收件箱中永久删除。" open busy={deleting} onClose={() => setDeleteFor(null)} onConfirm={() => void deleteMessage()} />}
    </section>
  )
}
