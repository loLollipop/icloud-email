import { useEffect, useMemo, useRef, useState } from 'react'
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

type SearchField = 'all' | 'subject' | 'from' | 'to'
type DeleteTarget = { accountId: string; message: InboxMessage }

function formatDate(raw: string): string {
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return raw
  return new Intl.DateTimeFormat('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit' }).format(date)
}

function inboxQuery(accountID: string, alias: string, loadRange: number, days: number): string {
  const params = new URLSearchParams({ account_id: accountID, scope: 'hme_aliases' })
  if (alias) params.set('alias', alias)
  params.set('limit', String(loadRange))
  params.set('days', String(days))
  return params.toString()
}

export default function InboxPage() {
  const [searchParams, setSearchParams] = useSearchParams()
  const accountChangedRef = useRef(false)
  const accountsCached = readResource<AccountSummary[]>(resourceKeys.accounts)
  const initialAccountID = accountsCached?.data.find((account) => account.id === searchParams.get('account_id'))?.id ?? accountsCached?.data[0]?.id ?? ''
  const initialAlias = searchParams.get('alias') ?? ''
  const requestedLimit = Number(searchParams.get('limit'))
  const requestedDays = Number(searchParams.get('days'))
  const initialLoadRange = [20, 50, 100].includes(requestedLimit) ? requestedLimit : 20
  const initialDays = [1, 7, 30, 90].includes(requestedDays) ? requestedDays : 7
  const draftFiltersRef = useRef({ alias: initialAlias, loadRange: initialLoadRange, days: initialDays })
  const initialResult = initialAccountID ? readResource<InboxResult>(resourceKeys.inbox(inboxQuery(initialAccountID, initialAlias, initialLoadRange, initialDays))) : undefined

  const [accounts, setAccounts] = useState<AccountSummary[]>(accountsCached?.data ?? [])
  const [aliases, setAliases] = useState<Alias[]>([])
  const [accountId, setAccountId] = useState(initialAccountID)
  const [alias, setAlias] = useState(initialAlias)
  const [loadRange, setLoadRange] = useState(initialLoadRange)
  const [days, setDays] = useState(initialDays)
  const [appliedAlias, setAppliedAlias] = useState(initialAlias)
  const [appliedLoadRange, setAppliedLoadRange] = useState(initialLoadRange)
  const [appliedDays, setAppliedDays] = useState(initialDays)
  const [searchField, setSearchField] = useState<SearchField>('all')
  const [search, setSearch] = useState('')
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(10)
  const [result, setResult] = useState<InboxResult | null>(initialResult?.data ?? null)
  const [loading, setLoading] = useState(!initialResult && (!accountsCached || initialAccountID !== ''))
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
  const { show } = useToast()

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
        setAccountId(target)
        currentAccountRef.current = target
        if (!target) setLoading(false)
        if (target) {
          const currentFilters = draftFiltersRef.current
          setAppliedAlias(currentFilters.alias)
          setAppliedLoadRange(currentFilters.loadRange)
          setAppliedDays(currentFilters.days)
          const next: Record<string, string> = {
            account_id: target,
            limit: String(currentFilters.loadRange),
            days: String(currentFilters.days),
          }
          if (currentFilters.alias) next.alias = currentFilters.alias
          setSearchParams(next, { replace: true })
        }
      })
      .catch((err) => {
        if (cancelled) return
        if (!accountsCached) setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
        setLoading(false)
      })
    return () => { cancelled = true }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (!accountId) return
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
    const query = inboxQuery(accountId, appliedAlias, appliedLoadRange, appliedDays)
    const key = resourceKeys.inbox(query)
    const cached = readResource<InboxResult>(key)
    /* eslint-disable react-hooks/set-state-in-effect */
    if (cached) { setResult(cached.data); setLoading(false) } else { setResult(null); setLoading(true) }
    clearSelection()
    /* eslint-enable react-hooks/set-state-in-effect */
    loadResource(key, resourceTTLs.inbox, () => request<InboxResult>(`/api/inbox?${query}`), Boolean(cached))
      .then((data) => {
        if (cancelled || requestID !== inboxRequestRef.current) return
        setResult(data)
        setError('')
      })
      .catch((err) => {
        if (cancelled || requestID !== inboxRequestRef.current) return
        invalidateResource(key)
        setDeleteFor(null)
        clearSelection()
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
        setResult(null)
      })
      .finally(() => { if (!cancelled && requestID === inboxRequestRef.current) setLoading(false) })
    return () => { cancelled = true }
  }, [accountId, appliedAlias, appliedLoadRange, appliedDays, retryKey])

  const filteredMessages = useMemo(() => {
    const query = search.trim().toLowerCase()
    if (!query) return result?.messages ?? []
    return (result?.messages ?? []).filter((message) => {
      const values = searchField === 'all' ? [message.subject, message.from, message.to, message.preview] : [message[searchField]]
      return values.some((value) => value.toLowerCase().includes(query))
    })
  }, [result, search, searchField])
  const totalPages = Math.max(1, Math.ceil(filteredMessages.length / pageSize))
  const currentPage = Math.min(page, totalPages)
  const visibleMessages = useMemo(
    () => filteredMessages.slice((currentPage - 1) * pageSize, currentPage * pageSize),
    [filteredMessages, currentPage, pageSize],
  )

  function applyFilters() {
    draftFiltersRef.current = { alias, loadRange, days }
    const next: Record<string, string> = { account_id: accountId, limit: String(loadRange), days: String(days) }
    if (alias) next.alias = alias
    setSearchParams(next, { replace: true })
    setPage(1)
    clearSelection()
    const unchanged = alias === appliedAlias && loadRange === appliedLoadRange && days === appliedDays
    setAppliedAlias(alias)
    setAppliedLoadRange(loadRange)
    setAppliedDays(days)
    if (unchanged) {
      invalidateResource(resourceKeys.inbox(inboxQuery(accountId, alias, loadRange, days)))
      setLoading(true)
      setRetryKey((key) => key + 1)
    }
  }

  function handleAccountChange(id: string) {
    accountChangedRef.current = true
    currentAccountRef.current = id
    setAccountId(id)
    setAlias('')
    // 丢弃尚未应用的范围草稿，避免控件值与新账号的实际查询条件不一致。
    setLoadRange(appliedLoadRange)
    setDays(appliedDays)
    draftFiltersRef.current = { alias: '', loadRange: appliedLoadRange, days: appliedDays }
    setAppliedAlias('')
    setPage(1)
    clearSelection()
    const cached = readResource<InboxResult>(resourceKeys.inbox(inboxQuery(id, '', appliedLoadRange, appliedDays)))
    setResult(cached?.data ?? null)
    setLoading(!cached)
    setSearchParams({ account_id: id, limit: String(appliedLoadRange), days: String(appliedDays) }, { replace: true })
  }

  const isImap = result?.method === 'imap'

  if (selected) {
    const readStatus = detailLoading ? '正在读取' : isImap ? (detail ? '已读取' : '读取失败') : '摘要模式'
    return (
      <section className="inbox-page inbox-detail-page">
        <article className="mail-detail" aria-label="邮件阅读区">
          <button type="button" className="mail-back-button" onClick={clearSelection}>← 返回全部邮件</button>
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
        <div className="toolbar-field"><label htmlFor="inbox-alias">别名</label><select id="inbox-alias" value={alias} onChange={(event) => { const nextAlias = event.target.value; setAlias(nextAlias); draftFiltersRef.current = { alias: nextAlias, loadRange, days }; setPage(1) }}><option value="">全部 iCloud 隐私别名</option>{aliases.map((item) => <option key={item.anonymousId} value={item.email}>{item.email}</option>)}</select></div>
        <div className="toolbar-field"><label htmlFor="inbox-days">时间范围</label><select id="inbox-days" value={days} onChange={(event) => { const nextDays = Number(event.target.value); setDays(nextDays); draftFiltersRef.current = { alias, loadRange, days: nextDays }; setPage(1) }}>{[1, 7, 30, 90].map((value) => <option key={value} value={value}>{value} 天</option>)}</select></div>
        <div className="toolbar-field"><label htmlFor="inbox-limit">加载范围</label><select id="inbox-limit" value={loadRange} onChange={(event) => { const nextLoadRange = Number(event.target.value); setLoadRange(nextLoadRange); draftFiltersRef.current = { alias, loadRange: nextLoadRange, days }; setPage(1) }}><option value={20}>最近 20 封</option><option value={50}>最近 50 封</option><option value={100}>最近 100 封</option></select></div>
        <div className="toolbar-actions"><button className="primary" onClick={applyFilters}><IconRefresh size={16} />应用筛选 / 刷新</button></div>
      </div>
      <div className="inbox-local-search">
        <select aria-label="搜索字段" value={searchField} onChange={(event) => { setSearchField(event.target.value as SearchField); setPage(1); clearSelection() }}><option value="all">全部字段</option><option value="subject">主题</option><option value="from">发件人</option><option value="to">收件人</option></select>
        <div className="search-input-wrap"><input type="search" aria-label="搜索当前已加载邮件" value={search} onChange={(event) => { setSearch(event.target.value); setPage(1); clearSelection() }} placeholder="搜索当前已加载邮件" /><span className="search-input-icon" aria-hidden="true"><IconSearch size={16} /></span></div>
        <span className="hint">搜索当前已加载邮件</span>
      </div>
      <AsyncState loading={loading} error={error} empty={!result || result.messages.length === 0} emptyText="当前窗口暂无邮件" onRetry={() => { if (accountId) invalidateResourcePrefix(resourceKeys.inboxAccountPrefix(accountId)); setLoading(true); setRetryKey((key) => key + 1) }}>
        {result && result.messages.length > 0 && <>
          <div className="inbox-summary"><span>已加载 {result.messages.length} 封 / 当前窗口</span><span className={isImap ? 'badge badge-info' : 'badge badge-neutral'}>{isImap ? <IconKey size={12} /> : <IconMail size={12} />}{isImap ? 'IMAP 正文模式' : 'Web API 摘要模式'}</span></div>
          {!isImap && <div className="alert-info inbox-mode-notice">当前仅提供邮件摘要；配置 App 专用密码后可阅读正文和删除。</div>}
          <div className="mail-workspace">
            <div className="mail-list" aria-label="邮件列表">
              {visibleMessages.length === 0 && <div className="empty-state">当前已加载邮件中没有匹配项</div>}
              {visibleMessages.map((message) => <button type="button" key={message.id} className="mail-list-item" aria-label={message.subject || '（无主题）'} onClick={() => void openMessage(message)}><strong className="mail-list-sender">{message.from || '（未知发件人）'}</strong><span className="mail-list-copy"><span className="mail-list-subject">{message.subject || '（无主题）'}</span><span className="mail-list-preview">{message.preview || '—'}</span></span><time className="mail-list-date" dateTime={message.date}>{formatDate(message.date)}</time></button>)}
              <Pagination page={currentPage} pageSize={pageSize} totalItems={filteredMessages.length} onPageChange={(next) => { setPage(next); clearSelection() }} onPageSizeChange={(size) => { setPageSize(size); setPage(1); clearSelection() }} pageSizeOptions={[10, 20, 50]} label="当前已加载邮件分页" />
            </div>
          </div>
        </>}
      </AsyncState>
      {deleteFor && isImap && <ConfirmDialog title="删除邮件" message="邮件将从收件箱中永久删除。" open busy={deleting} onClose={() => setDeleteFor(null)} onConfirm={() => void deleteMessage()} />}
    </section>
  )
}
