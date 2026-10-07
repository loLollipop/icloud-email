import type { RefObject } from 'react'
import type { InboxMessage } from '../../api/types'
import { formatMailDate } from '../../utils/mail'
import { IconChevronRight, IconMail } from '../icons'

function HighlightMatch({ text, query }: { text: string; query: string }) {
  if (!query) return <>{text}</>
  const escaped = query.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
  return <>{text.split(new RegExp(`(${escaped})`, 'gi')).map((part, index) => index % 2 === 1 ? <mark className="mail-search-highlight" key={index}>{part}</mark> : part)}</>
}

interface MailListProps {
  messages: InboxMessage[]
  search: string
  density: 'comfortable' | 'compact'
  listRef: RefObject<HTMLDivElement | null>
  onOpen: (message: InboxMessage, trigger: HTMLButtonElement) => void
}

export default function MailList({ messages, search, density, listRef, onOpen }: MailListProps) {
  return (
    <div ref={listRef} className={`mail-list mail-list--${density}`} aria-label="邮件列表" tabIndex={-1}>
      {messages.map((message) => (
        <button type="button" key={message.id} className="mail-list-item" data-message-id={message.id}
          aria-label={message.subject || '（无主题）'} aria-describedby={`inbox-recipient-${message.id}`}
          onClick={(event) => onOpen(message, event.currentTarget)}>
          <span className="mail-row-mark" aria-hidden="true"><IconMail size={19} /></span>
          <span className="mail-list-copy">
            <span className="mail-list-subject"><HighlightMatch text={message.subject || '（无主题）'} query={search} /></span>
            <span className="mail-list-recipient" id={`inbox-recipient-${message.id}`}>
              <span className="mail-list-recipient-label">收件：</span>
              <span className="mail-list-recipient-address" title={message.to.trim() || undefined}>{message.to.trim() || '未提供收件邮箱'}</span>
            </span>
            <span className="mail-list-preview">{message.preview || '打开查看邮件'}</span>
          </span>
          <time className="mail-list-date" dateTime={message.date || undefined} title={formatMailDate(message.date)}>{formatMailDate(message.date, true)}</time>
          <IconChevronRight className="mail-row-chevron" size={16} />
        </button>
      ))}
    </div>
  )
}
