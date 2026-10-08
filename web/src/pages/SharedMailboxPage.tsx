import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useLocation } from 'react-router-dom'
import AsyncState from '../components/AsyncState'
import Pagination from '../components/Pagination'
import ThemeControls from '../components/ThemeControls'
import MailList from '../components/mail/MailList'
import MailReader from '../components/mail/MailReader'
import { IconCloud, IconLock, IconRefresh, IconSearch } from '../components/icons'
import { useSharedMailbox } from '../hooks/useSharedMailbox'
import { formatMailDate } from '../utils/mail'
import '../styles/mail.css'
import '../styles/sharing.css'

export default function SharedMailboxPage() {
  const { hash } = useLocation()
  let token = ''
  try { token = decodeURIComponent(hash.slice(1)) } catch { /* Invalid links expose no mailbox. */ }
  return <SharedMailbox key={token} token={token} />
}

function SharedMailbox({ token }: { token: string }) {
  const inbox = useSharedMailbox(token)
  const [search, setSearch] = useState('')
  const [appliedSearch, setAppliedSearch] = useState('')
  const [focused, setFocused] = useState(false)
  const listRef = useRef<HTMLDivElement>(null)
  const reading = useRef(false)
  const position = useRef({ id: '', top: 0 })
  const index = inbox.result?.messages.findIndex((item) => item.id === inbox.selected?.id) ?? -1

  useEffect(() => {
    if (!focused) return
    const exit = (event: KeyboardEvent) => { if (event.key === 'Escape') setFocused(false) }
    window.addEventListener('keydown', exit)
    return () => window.removeEventListener('keydown', exit)
  }, [focused])

  useLayoutEffect(() => {
    if (!inbox.selected && reading.current && listRef.current) {
      const row = Array.from(listRef.current.querySelectorAll<HTMLButtonElement>('[data-message-id]')).find((item) => item.dataset.messageId === position.current.id)
      ;(row ?? listRef.current).focus({ preventScroll: true })
      listRef.current.scrollTop = position.current.top
    }
    reading.current = Boolean(inbox.selected)
  }, [inbox.selected])

  return (
    <div className={`shared-shell${focused && inbox.selected ? ' shared-shell--focused' : ''}`}>
      <header className="shared-header"><div className="shared-brand"><IconCloud size={25} /><strong>iCloud Mail</strong></div><div className="shared-header-tools"><span className="shared-readonly"><IconLock size={13} />只读邮箱</span><ThemeControls /></div></header>
      <main className="shared-main">
        {!inbox.info ? <div className="shared-access-state"><span className="share-mark"><IconLock size={26} /></span><h1>{inbox.unavailable ? '此链接无法继续使用' : inbox.loading ? '正在打开邮箱' : '暂时无法打开邮箱'}</h1><p role={inbox.error ? 'alert' : 'status'}>{inbox.error || '正在验证访问权限…'}</p>{!inbox.unavailable && !inbox.loading && <button onClick={inbox.refresh}><IconRefresh size={16} />重试</button>}</div> : <section className={`inbox-page shared-inbox${inbox.selected ? ' inbox-detail-page' : ''}`} aria-label="客户只读邮箱">
          {inbox.selected ? <MailReader key={inbox.selected.id} message={inbox.selected} detail={inbox.detail} loading={inbox.detailLoading} error={inbox.detailError} isImap index={index} count={inbox.result?.messages.length ?? 0} focused={focused}
            onFocusChange={() => setFocused((value) => !value)} onBack={() => { setFocused(false); inbox.closeMessage() }}
            onPrevious={() => { const message = inbox.result?.messages[index - 1]; if (message) void inbox.openMessage(message) }}
            onNext={() => { const message = inbox.result?.messages[index + 1]; if (message) void inbox.openMessage(message) }} onRetry={inbox.retryDetail} /> : <>
            <div className="mail-page-heading shared-mail-heading"><div><h1>{inbox.info.email}</h1><p>仅显示 {formatMailDate(inbox.info.created_at)} 之后收到的邮件</p></div><button className="mail-refresh" onClick={inbox.refresh} disabled={inbox.loading}><IconRefresh size={16} /><span>刷新邮件</span></button></div>
            <div className="inbox-panel">
              <div className="inbox-commandbar"><form className="inbox-search" onSubmit={(event) => { event.preventDefault(); setAppliedSearch(search.trim()); inbox.search(search) }}><div className="search-input-wrap"><span className="search-input-icon" aria-hidden="true"><IconSearch size={18} /></span><input type="search" aria-label="搜索邮件主题" maxLength={256} value={search} onChange={(event) => setSearch(event.target.value)} placeholder="搜索邮件主题…" /></div><button type="submit" className="primary" disabled={inbox.loading}>搜索</button></form></div>
              <div className="inbox-summary"><span>{appliedSearch ? `“${appliedSearch}” 的主题搜索结果` : '新收到的邮件'}{inbox.result ? ` · 共 ${inbox.result.total} 封` : ''}</span></div>
              <div className="mail-workspace"><AsyncState loading={inbox.loading} error={inbox.error} empty={!inbox.result || inbox.result.messages.length === 0} emptyText={appliedSearch ? '没有找到匹配的邮件' : '暂无新邮件，历史邮件不会在这里显示'} onRetry={inbox.refresh}>{inbox.result && <MailList messages={inbox.result.messages} search={appliedSearch} density="comfortable" listRef={listRef} onOpen={(message) => { position.current = { id: message.id, top: listRef.current?.scrollTop ?? 0 }; void inbox.openMessage(message) }} />}</AsyncState></div>
              {inbox.result && <Pagination page={inbox.result.page} pageSize={inbox.result.page_size} totalItems={inbox.result.total} onPageChange={inbox.changePage} onPageSizeChange={inbox.changePageSize} disabled={inbox.loading} label="客户邮件列表分页" />}
            </div>
          </>}
        </section>}
      </main>
    </div>
  )
}
