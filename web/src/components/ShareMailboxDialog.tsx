import { useEffect, useRef, useState } from 'react'
import { ApiError, request } from '../api/client'
import { shareURL, type GeneratedShare, type ShareStatus } from '../api/sharing'
import type { Alias } from '../api/types'
import { copyText } from '../utils/clipboard'
import { formatMailDate } from '../utils/mail'
import Dialog from './Dialog'
import { useToast } from './ToastProvider'
import { IconCopy, IconRefresh, IconShare } from './icons'
import '../styles/sharing.css'

interface Props {
  accountId: string
  alias: Alias
  onClose: () => void
}

export default function ShareMailboxDialog({ accountId, alias, onClose }: Props) {
  const path = `/api/aliases/${encodeURIComponent(alias.anonymousId)}/share`
  const [status, setStatus] = useState<ShareStatus | null>(null)
  const [link, setLink] = useState('')
  const [loading, setLoading] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [confirmation, setConfirmation] = useState<'generate' | 'revoke' | null>(null)
  const [retry, setRetry] = useState(0)
  const pending = useRef(false)
  const mounted = useRef(false)
  const { show } = useToast()

  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    request<ShareStatus>(`${path}?account_id=${encodeURIComponent(accountId)}`, { signal: controller.signal })
      .then((value) => { if (!controller.signal.aborted) { setStatus(value); setError('') } })
      .catch((err) => { if (!controller.signal.aborted) setError(err instanceof ApiError ? err.message : '加载分发状态失败') })
      .finally(() => { if (!controller.signal.aborted) setLoading(false) })
    return () => controller.abort()
  }, [accountId, path, retry])

  async function changeShare(action: 'generate' | 'revoke') {
    if (pending.current) return
    pending.current = true
    setBusy(true)
    setError('')
    try {
      const value = await request<GeneratedShare | ShareStatus>(path, {
        method: action === 'generate' ? 'POST' : 'DELETE', body: { account_id: accountId },
      })
      if (!mounted.current) return
      setStatus(value)
      setLink(action === 'generate' && 'token' in value ? shareURL(value.token) : '')
      setConfirmation(null)
      show(action === 'generate' ? '新的分发链接已生成' : '分发已终止，旧链接已失效')
    } catch (err) {
      if (mounted.current) setError(err instanceof ApiError ? err.message : '操作失败，请重试')
    } finally {
      pending.current = false
      if (mounted.current) setBusy(false)
    }
  }

  async function copyLink() {
    show(await copyText(link) ? '分发链接已复制' : '复制失败，请选中链接手动复制')
  }

  return (
    <Dialog title="分发邮箱" open busy={busy} onClose={() => { if (!pending.current) onClose() }} className="share-dialog">
      <div className="share-mailbox-identity"><span className="share-mark"><IconShare size={21} /></span><div><strong>{alias.email}</strong><span>客户只读 · 仅生成链接后的新邮件</span></div></div>
      {loading ? <p role="status">正在读取分发状态…</p> : status && <div className="share-status"><span className={status.active ? 'badge badge-active' : 'badge badge-neutral'}>{status.active ? '正在分发' : '未分发'}</span>{status.active && status.created_at && <time dateTime={status.created_at}>开始于 {formatMailDate(status.created_at)}</time>}</div>}
      {link && <div className="share-link-field"><label htmlFor="share-link">客户访问链接</label><div><input id="share-link" value={link} readOnly onFocus={(event) => event.currentTarget.select()} /><button type="button" onClick={() => void copyLink()} aria-label="复制分发链接" title="复制分发链接"><IconCopy size={16} /></button></div><p>请保存此链接，关闭后不再回显；丢失可重新生成。</p></div>}
      {!loading && status && <p className="share-rules">终止或重新生成后，旧链接无法继续访问。重新生成只开放新的分发开始后收到的邮件，不影响管理员查看。</p>}
      {confirmation && <div className="share-confirm" role="alert"><strong>{confirmation === 'revoke' ? '终止当前分发？' : '重新生成分发链接？'}</strong><p>{confirmation === 'revoke' ? '客户将无法再使用当前链接。' : '旧链接立即失效，新的链接将从新的开始时间计算可查看邮件。'}</p></div>}
      {error && <p className="alert-error" role="alert">{error}</p>}
      <div className="form-actions share-dialog-actions">
        <button type="button" disabled={busy} onClick={confirmation ? () => { setConfirmation(null); setError('') } : onClose}>{confirmation ? '取消操作' : '关闭'}</button>
        {!loading && !status && <button type="button" onClick={() => { setLoading(true); setRetry((value) => value + 1) }}><IconRefresh size={15} />重试</button>}
        {!loading && status && (confirmation ? <button type="button" className={confirmation === 'revoke' ? 'danger' : 'primary'} disabled={busy} onClick={() => void changeShare(confirmation)}>{busy ? '处理中…' : confirmation === 'revoke' ? '确认终止' : '确认重新生成'}</button> : <>
          {status.active && <button type="button" className="danger" disabled={busy} onClick={() => setConfirmation('revoke')}>终止分发</button>}
          <button type="button" className="primary" disabled={busy} onClick={() => status.active ? setConfirmation('generate') : void changeShare('generate')}><IconShare size={15} />{busy ? '生成中…' : status.active ? '重新生成' : '生成分发链接'}</button>
        </>)}
      </div>
    </Dialog>
  )
}
