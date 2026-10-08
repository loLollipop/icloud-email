import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import type { FullMessage, InboxMessage } from '../../api/types'
import { copyText } from '../../utils/clipboard'
import { formatMailDate } from '../../utils/mail'
import MailHtmlFrame from '../MailHtmlFrame'
import { useToast } from '../ToastProvider'
import { IconArrowLeft, IconChevronLeft, IconChevronRight, IconCollapse, IconCopy, IconExpand, IconRefresh, IconTrash } from '../icons'

interface MailReaderProps {
  message: InboxMessage
  detail: FullMessage | null
  loading: boolean
  error: string
  isImap: boolean
  index: number
  count: number
  focused: boolean
  onFocusChange: () => void
  onBack: () => void
  onPrevious: () => void
  onNext: () => void
  onRetry: () => void
  onDelete?: () => void
}

export default function MailReader({ message, detail, loading, error, isImap, index, count, focused, onFocusChange, onBack, onPrevious, onNext, onRetry, onDelete }: MailReaderProps) {
  const [mode, setMode] = useState<'html' | 'text'>('html')
  const backRef = useRef<HTMLButtonElement>(null)
  const { show } = useToast()
  const displayed = detail ?? message

  useEffect(() => { backRef.current?.focus({ preventScroll: true }) }, [])

  async function copyRecipient() {
    show(await copyText(displayed.to) ? '收件邮箱已复制' : '复制失败，请手动选择邮箱复制')
  }

  return (
    <article className="mail-detail" aria-label="邮件阅读区">
      <div className="reader-toolbar">
        <button ref={backRef} className="mail-back-button" onClick={onBack} aria-label="← 返回全部邮件"><IconArrowLeft size={17} /><span>返回邮件</span></button>
        <div className="reader-paging">
          <span className="reader-position">{index >= 0 ? `本页 ${index + 1} / ${count}` : '邮件'}</span>
          <button className="icon-button ghost" aria-label="上一封邮件" title="上一封邮件" disabled={index <= 0} onClick={onPrevious}><IconChevronLeft size={17} /></button>
          <button className="icon-button ghost" aria-label="下一封邮件" title="下一封邮件" disabled={index < 0 || index >= count - 1} onClick={onNext}><IconChevronRight size={17} /></button>
        </div>
        <div className="reader-tools">
          <button className="icon-button ghost" aria-label={focused ? '退出专注阅读' : '专注阅读'} title={focused ? '退出专注阅读' : '专注阅读'} aria-pressed={focused} onClick={onFocusChange}>{focused ? <IconCollapse size={17} /> : <IconExpand size={17} />}</button>
          {isImap && onDelete && <button className="icon-button reader-delete" aria-label="删除邮件" title="删除邮件" onClick={onDelete}><IconTrash size={17} /></button>}
        </div>
      </div>
      <header className="mail-reader-header">
        <h2>{displayed.subject || '（无主题）'}</h2>
        <div className="reader-recipient"><span>收件</span><span className="reader-recipient-address">{displayed.to || '未提供收件邮箱'}</span>{displayed.to && <button className="icon-button ghost" aria-label="复制收件邮箱" title="复制收件邮箱" onClick={() => void copyRecipient()}><IconCopy size={14} /></button>}</div>
        <details className="reader-metadata">
          <summary><span>{displayed.from || '（未知发件人）'}</span><time dateTime={displayed.date || undefined}>{formatMailDate(displayed.date)}</time><span className="reader-details-label">详情</span></summary>
          <dl><div><dt>发件人</dt><dd>{displayed.from || '（未知发件人）'}</dd></div><div><dt>收件人</dt><dd>{displayed.to || '—'}</dd></div><div><dt>日期</dt><dd>{formatMailDate(displayed.date)}</dd></div><div><dt>读取状态</dt><dd>{loading ? '正在读取' : error ? '读取失败' : isImap ? '已读取' : '摘要模式'}</dd></div></dl>
        </details>
      </header>
      {detail?.body_truncated && <p className="alert-info mail-truncated-notice">邮件正文过长，已截断显示。</p>}
      {detail?.html_body && detail.body && <div className="reader-format" role="group" aria-label="正文显示方式"><button aria-pressed={mode === 'html'} onClick={() => setMode('html')}>原始排版</button><button aria-pressed={mode === 'text'} onClick={() => setMode('text')}>纯文本</button></div>}
      <div className="reader-content" aria-busy={loading}>
        {loading ? <div className="reader-state" role="status"><span className="loading-dot" />读取中…</div> : error ? <div className="reader-state" role="alert"><h3>这封邮件暂时没能打开</h3><p>{error}</p><button onClick={onRetry}><IconRefresh size={16} />重新读取</button></div> : !isImap ? <div className="mail-summary-only"><span className="badge badge-neutral">摘要模式</span><p>{message.preview || '暂无摘要'}</p><p className="hint">当前仅显示邮件摘要；配置 App 专用密码后可阅读正文和删除。</p><Link className="button-link" to="/accounts">配置邮件读取</Link></div> : detail ? detail.html_body && mode === 'html' ? <MailHtmlFrame html={detail.html_body} /> : <pre className="mail-body">{detail.body || '无正文'}</pre> : <div className="reader-state"><p>邮件正文暂不可用</p><button onClick={onRetry}>重新读取</button></div>}
      </div>
    </article>
  )
}
