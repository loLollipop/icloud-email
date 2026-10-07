import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import AsyncState from '../components/AsyncState'
import ConfirmDialog from '../components/ConfirmDialog'
import Pagination from '../components/Pagination'
import MailList from '../components/mail/MailList'
import MailReader from '../components/mail/MailReader'
import { IconClose, IconInbox, IconList, IconRefresh, IconSearch } from '../components/icons'
import { useInboxWorkspace } from '../hooks/useInboxWorkspace'
import { readMailPreference, saveMailPreference } from '../utils/mail'
import '../styles/mail.css'

export default function InboxPage() {
  const inbox = useInboxWorkspace()
  const { accounts, accountId, aliases, alias, search, appliedSearch, result, loading, error, selected, detail } = inbox
  const [density, setDensity] = useState<'comfortable' | 'compact'>(() => readMailPreference('density', 'comfortable') === 'compact' ? 'compact' : 'comfortable')
  const [focused, setFocused] = useState(false)
  const listRef = useRef<HTMLDivElement>(null)
  const returnPosition = useRef<{ query: string; top: number; id: string } | null>(null)
  const wasReading = useRef(false)
  const isImap = result?.method === 'imap'
  const selectedIndex = result?.messages.findIndex((item) => item.id === selected?.id) ?? -1

  useLayoutEffect(() => {
    if (!selected && wasReading.current && listRef.current) {
      const saved = returnPosition.current
      if (saved?.query === inbox.query) {
        const row = Array.from(listRef.current.querySelectorAll<HTMLButtonElement>('[data-message-id]')).find((item) => item.dataset.messageId === saved.id)
        ;(row ?? listRef.current).focus({ preventScroll: true })
        listRef.current.scrollTop = saved.top
      }
    }
    wasReading.current = Boolean(selected)
  }, [selected, inbox.query])

  useEffect(() => {
    if (!focused || !selected) return
    const onKey = (event: KeyboardEvent) => { if (event.key === 'Escape') setFocused(false) }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [focused, selected])

  function changeDensity() {
    const next = density === 'comfortable' ? 'compact' : 'comfortable'
    setDensity(next)
    saveMailPreference('density', next)
  }

  return (
    <section className={`inbox-page${selected ? ' inbox-detail-page' : ''}${selected && focused ? ' reader-is-focused' : ''}`}>
      {selected ? <MailReader key={`${accountId}:${selected.id}`} message={selected} detail={detail} loading={inbox.detailLoading} error={inbox.detailError} isImap={isImap} index={selectedIndex} count={result?.messages.length ?? 0} focused={focused}
        onFocusChange={() => setFocused((value) => !value)} onBack={() => { setFocused(false); inbox.closeMessage() }}
        onPrevious={() => { const message = result?.messages[selectedIndex - 1]; if (message) inbox.openMessage(message) }}
        onNext={() => { const message = result?.messages[selectedIndex + 1]; if (message) inbox.openMessage(message) }}
        onRetry={inbox.retryDetail} onDelete={() => inbox.setDeleteFor({ accountId, message: detail ?? selected })} /> : <>
        <div className="mail-page-heading"><h2>收件箱</h2><button className="mail-refresh" onClick={inbox.refresh} disabled={loading && !error}><IconRefresh size={16} /><span>刷新邮件</span></button></div>
        <div className="inbox-panel">
          <div className="inbox-commandbar">
            <form className="inbox-search" onSubmit={(event) => { event.preventDefault(); inbox.submitSearch(search) }}>
              <div className="search-input-wrap"><span className="search-input-icon" aria-hidden="true"><IconSearch size={18} /></span><input type="search" aria-label="搜索邮件" maxLength={256} value={search} onChange={(event) => inbox.setSearch(event.target.value)} placeholder="搜索邮件主题…" /></div>
              <button type="submit" className="primary">搜索</button>
            </form>
            <div className="inbox-filters">
              <div className="toolbar-field"><label htmlFor="inbox-account">账号</label><select id="inbox-account" value={accountId} onChange={(event) => inbox.handleAccountChange(event.target.value)}>{accounts.map((account) => <option key={account.id} value={account.id}>{account.name}</option>)}</select></div>
              <div className="toolbar-field"><label htmlFor="inbox-alias">收件邮箱</label><select id="inbox-alias" value={alias} onChange={(event) => inbox.changeQuery({ alias: event.target.value })}><option value="">全部 iCloud 隐私别名</option>{alias && !aliases.some((item) => item.email === alias) && <option value={alias}>{alias}</option>}{aliases.map((item) => <option key={item.anonymousId} value={item.email}>{item.email}</option>)}</select></div>
              <button className="icon-button ghost density-toggle" aria-label={density === 'comfortable' ? '切换为紧凑列表' : '切换为舒适列表'} title={density === 'comfortable' ? '切换为紧凑列表' : '切换为舒适列表'} aria-pressed={density === 'compact'} onClick={changeDensity}><IconList size={18} /></button>
            </div>
          </div>
          <div className="inbox-summary"><span>{appliedSearch ? `“${appliedSearch}” 的主题搜索结果` : '全部邮件'}{result ? ` · 共 ${result.total} 封` : ''}{appliedSearch && <button className="icon-button ghost" aria-label="清除搜索" title="清除搜索" onClick={() => inbox.submitSearch('')}><IconClose size={14} /></button>}</span></div>
          {!isImap && result && <div className="inbox-mode-notice">当前仅提供邮件摘要；配置 App 专用密码后可阅读正文和删除。<Link to="/accounts">去配置</Link></div>}
          <div className="mail-workspace">
            {!loading && !error && accounts.length === 0 ? <div className="mail-welcome"><span className="mail-welcome-icon"><IconInbox size={30} /></span><h3>连接你的第一个邮箱</h3><p>添加 iCloud 账户后，在这里集中阅读隐藏邮箱收到的邮件。</p><Link className="button-link primary" to="/accounts">添加邮箱账户</Link></div> : <AsyncState loading={loading && !error} error={error} empty={!result || result.messages.length === 0} emptyText={appliedSearch ? '没有找到匹配的邮件，试试其他关键词' : '暂无邮件'} onRetry={inbox.refresh}>
              {result && <MailList messages={result.messages} search={appliedSearch} density={density} listRef={listRef} onOpen={(message) => { returnPosition.current = { query: inbox.query, top: listRef.current?.scrollTop ?? 0, id: message.id }; inbox.openMessage(message) }} />}
            </AsyncState>}
          </div>
          {result && !error && <Pagination page={result.page} pageSize={result.page_size} totalItems={result.total} onPageChange={(next) => inbox.changeQuery({ page: String(next) }, false)} onPageSizeChange={(size) => inbox.changeQuery({ page_size: String(size) })} pageSizeOptions={[10, 20, 50]} label="邮件列表分页" />}
        </div>
      </>}
      {inbox.deleteFor && <ConfirmDialog title="删除邮件" message="邮件将从收件箱中永久删除。" open busy={inbox.deleting} onClose={() => inbox.setDeleteFor(null)} onConfirm={() => void inbox.deleteMessage()} />}
    </section>
  )
}
