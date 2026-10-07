import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { request, ApiError } from '../api/client'
import type { AccountSummary } from '../api/types'
import {
  invalidateResource,
  invalidateResourcePrefix,
  loadResource,
  readResource,
  resourceKeys,
  resourceTTLs,
} from '../api/resourceCache'
import AsyncState from '../components/AsyncState'
import AccountFormDialog from '../components/AccountFormDialog'
import CookieDialog from '../components/CookieDialog'
import ICloudLoginDialog from '../components/ICloudLoginDialog'
import AppPasswordDialog from '../components/AppPasswordDialog'
import ProxyDialog from '../components/ProxyDialog'
import MailboxDialog from '../components/MailboxDialog'
import ConfirmDialog from '../components/ConfirmDialog'
import Pagination from '../components/Pagination'
import { useToast } from '../components/ToastProvider'
import {
  IconCheck,
  IconClock,
  IconAlert,
  IconPlus,
  IconEdit,
  IconTrash,
  IconKey,
  IconMail,
  IconRefresh,
  IconSearch,
  IconSettings,
  IconCloud,
} from '../components/icons'

const statusMeta: Record<string, { text: string; badge: string; icon: typeof IconCheck }> = {
  active: { text: '验证通过', badge: 'badge badge-active', icon: IconCheck },
  pending: { text: '待配置', badge: 'badge badge-pending', icon: IconClock },
  error: { text: '异常', badge: 'badge badge-error', icon: IconAlert },
}

function StatusBadge({ status }: { status: string }) {
  const meta = statusMeta[status] ?? {
    text: status,
    badge: 'badge badge-neutral',
    icon: IconClock,
  }
  const Icon = meta.icon
  return (
    <span className={meta.badge}>
      <Icon />
      {meta.text}
    </span>
  )
}

function formatValidation(raw: string): string {
  if (!raw) return '尚未验证'
  const date = new Date(raw)
  if (Number.isNaN(date.getTime())) return raw
  return date.toLocaleString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false })
}

export default function AccountsPage() {
  const accountsCached = readResource<AccountSummary[]>(resourceKeys.accounts)
  const [accounts, setAccounts] = useState<AccountSummary[]>(accountsCached?.data ?? [])
  const [loading, setLoading] = useState(!accountsCached)
  const [error, setError] = useState('')
  const [retryKey, setRetryKey] = useState(0)
  const requestGeneration = useRef(0)

  // dialog 状态
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<AccountSummary | null>(null)
  const [cookieFor, setCookieFor] = useState<AccountSummary | null>(null)
  const [loginFor, setLoginFor] = useState<AccountSummary | null>(null)
  const [appPwdFor, setAppPwdFor] = useState<AccountSummary | null>(null)
  const [proxyFor, setProxyFor] = useState<AccountSummary | null>(null)
  const [mailboxFor, setMailboxFor] = useState<AccountSummary | null>(null)
  const [deleteFor, setDeleteFor] = useState<AccountSummary | null>(null)
  const [deleting, setDeleting] = useState(false)
  const [page, setPage] = useState(1)
  const [pageSize, setPageSize] = useState(10)
  const [search, setSearch] = useState('')
  const [statusFilter, setStatusFilter] = useState('all')

  const { show } = useToast()

  const filteredAccounts = useMemo(() => {
    const query = search.trim().toLowerCase()
    return accounts.filter((account) =>
      (statusFilter === 'all' || account.status === statusFilter) &&
      (!query || [account.name, account.icloud_email, account.real_email].some((value) => value.toLowerCase().includes(query))),
    )
  }, [accounts, search, statusFilter])
  const totalPages = Math.max(1, Math.ceil(filteredAccounts.length / pageSize))
  const currentPage = Math.min(page, totalPages)
  const visibleAccounts = useMemo(
    () => filteredAccounts.slice((currentPage - 1) * pageSize, currentPage * pageSize),
    [filteredAccounts, currentPage, pageSize],
  )

  const load = useCallback(async () => {
    const generation = ++requestGeneration.current
    setLoading(true)
    invalidateResource(resourceKeys.accounts)
    try {
      const data = await loadResource(
        resourceKeys.accounts,
        resourceTTLs.accounts,
        () => request<AccountSummary[]>('/api/accounts'),
      )
      if (requestGeneration.current !== generation) return
      setAccounts(data)
      setError('')
    } catch (err) {
      if (requestGeneration.current !== generation) return
      setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
    } finally {
      if (requestGeneration.current === generation) setLoading(false)
    }
  }, [])

  useEffect(() => {
    let cancelled = false
    const generation = ++requestGeneration.current
    loadResource(resourceKeys.accounts, resourceTTLs.accounts, () => request<AccountSummary[]>('/api/accounts'))
      .then((data) => {
        if (cancelled || requestGeneration.current !== generation) return
        setAccounts(data)
        setError('')
      })
      .catch((err) => {
        if (cancelled || requestGeneration.current !== generation) return
        setError(err instanceof ApiError ? err.message : '网络连接失败，请检查服务状态')
      })
      .finally(() => {
        if (!cancelled && requestGeneration.current === generation) setLoading(false)
      })
    return () => {
      cancelled = true
    }
  }, [retryKey])

  function handleRetry() {
    requestGeneration.current++
    setLoading(true)
    invalidateResource(resourceKeys.accounts)
    setRetryKey((k) => k + 1)
  }

  function invalidateAccountResources(accountID: string) {
    invalidateResource(resourceKeys.aliases(accountID))
    invalidateResourcePrefix(resourceKeys.inboxAccountPrefix(accountID))
  }

  async function handleDelete() {
    if (!deleteFor) return
    setDeleting(true)
    try {
      await request(`/api/accounts/${deleteFor.id}`, { method: 'DELETE' })
      invalidateAccountResources(deleteFor.id)
      setDeleteFor(null)
      show('账号已删除')
      void load()
    } catch (err) {
      show(err instanceof ApiError ? err.message : '删除失败')
    } finally {
      setDeleting(false)
    }
  }

  return (
    <section className="accounts-page management-page" aria-label="邮箱账户管理">
      <div className="page-header">
        <div className="page-actions">
          <button onClick={handleRetry} disabled={loading}><IconRefresh size={15} />刷新</button>
          <button className="primary" onClick={() => { setEditing(null); setFormOpen(true) }}>
            <IconPlus size={16} />添加账号
          </button>
        </div>
      </div>
      <div className="account-toolbar">
        <div className="toolbar-field toolbar-field-search">
          <label htmlFor="account-search">搜索账号</label>
          <div className="search-input-wrap">
            <input id="account-search" type="search" value={search}
              onChange={(event) => { setSearch(event.target.value); setPage(1) }} placeholder="搜索名称或邮箱" />
            <span className="search-input-icon" aria-hidden="true"><IconSearch size={16} /></span>
          </div>
        </div>
        <div className="toolbar-field">
          <label htmlFor="account-status">账号状态</label>
          <select id="account-status" value={statusFilter} onChange={(event) => { setStatusFilter(event.target.value); setPage(1) }}>
            <option value="all">全部状态</option><option value="active">验证通过</option><option value="pending">待配置</option><option value="error">异常</option>
          </select>
        </div>
        <span className="account-count">{filteredAccounts.length} 个账号</span>
      </div>
      <div className="management-content">
        <AsyncState loading={loading} error={error} empty={filteredAccounts.length === 0}
          emptyText={accounts.length === 0 ? '暂无账号，点击“添加账号”开始' : '没有匹配的账号'} onRetry={handleRetry}>
          <div className="account-list management-scroll-region">
            <table className="account-table" aria-label="邮箱账户列表">
              <colgroup><col className="account-col-identity" /><col className="account-col-status" /><col className="account-col-aliases" /><col className="account-col-credentials" /><col className="account-col-validation" /><col className="account-col-actions" /></colgroup>
              <thead><tr><th scope="col">账户</th><th scope="col">状态</th><th scope="col">隐藏邮箱</th><th scope="col">连接配置</th><th scope="col">最近验证</th><th scope="col">操作</th></tr></thead>
              <tbody>
                {visibleAccounts.map((acc) => (
                    <tr key={acc.id} className="account-row" aria-label={acc.name}>
                      <td className="account-cell-identity">
                        <div className="account-identity"><strong>{acc.name}</strong><span>{acc.icloud_email || acc.real_email || '未填写邮箱'}</span></div>
                      </td>
                      <td className="account-cell-status"><StatusBadge status={acc.status} />{acc.status_message && <p className="account-status-message">{acc.status_message}</p>}</td>
                      <td className="account-cell-aliases"><span className="account-cell-label">隐藏邮箱</span><strong title="已启用隐藏邮箱 / 全部隐藏邮箱">{acc.alias_active} / {acc.alias_total}</strong></td>
                      <td className="account-cell-credentials">
                        <ul className="credential-tags" aria-label="凭据配置情况（仅表示已填写）">
                          {[
                            { name: 'Cookie', configured: acc.has_cookies },
                            { name: 'App 密码', configured: acc.has_app_password },
                            { name: '收件邮箱', configured: Boolean(acc.mailbox), detail: acc.mailbox?.email },
                            { name: '代理', configured: acc.has_proxy },
                          ].map((credential) => (
                            <li key={credential.name} className={credential.configured ? 'is-configured' : ''}
                              title={`${credential.name}：${credential.configured ? '已配置' : '未配置'}${credential.detail ? ` · ${credential.detail}` : ''}（不代表当前连接状态）`}>
                              {credential.configured ? <IconCheck size={12} /> : <span aria-hidden="true">−</span>}
                              {credential.name}<span className="visually-hidden">：{credential.configured ? '已配置' : '未配置'}</span>
                            </li>
                          ))}
                        </ul>
                      </td>
                      <td className="account-cell-validation"><span className="account-cell-label">最近验证</span><time dateTime={acc.last_validated || undefined}>{formatValidation(acc.last_validated)}</time></td>
                      <td className="account-cell-actions">
                        <div className="account-actions" role="group" aria-label={`账户操作 · ${acc.name}`}>
                          <button type="button" className="account-action-button" title="编辑" aria-label={`编辑 · ${acc.name}`}
                            onClick={() => { setEditing(acc); setFormOpen(true) }}><IconEdit size={16} /></button>
                          <button type="button" className="account-action-button" title="更新 Cookie" aria-label={`更新 Cookie · ${acc.name}`}
                            onClick={() => setCookieFor(acc)}><IconRefresh size={16} /></button>
                          <button type="button" className="account-action-button is-primary" title="iCloud 登录" aria-label={`iCloud 登录 · ${acc.name}`}
                            onClick={() => setLoginFor(acc)}><IconCloud size={16} /></button>
                          <button type="button" className="account-action-button" title="设置 App 密码" aria-label={`设置 App 密码 · ${acc.name}`}
                            onClick={() => setAppPwdFor(acc)}><IconKey size={16} /></button>
                          <button type="button" className="account-action-button" title="接入收件邮箱" aria-label={`接入收件邮箱 · ${acc.name}`}
                            onClick={() => setMailboxFor(acc)}><IconMail size={16} /></button>
                          <button type="button" className="account-action-button" title="设置代理" aria-label={`设置代理 · ${acc.name}`}
                            onClick={() => setProxyFor(acc)}><IconSettings size={16} /></button>
                          <button type="button" className="account-action-button danger" title="删除" aria-label={`删除 · ${acc.name}`}
                            onClick={() => setDeleteFor(acc)}><IconTrash size={16} /></button>
                        </div>
                      </td>
                    </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pagination page={currentPage} pageSize={pageSize} totalItems={filteredAccounts.length}
            onPageChange={setPage} onPageSizeChange={(nextPageSize) => { setPageSize(nextPageSize); setPage(1) }} label="账号列表分页" />
        </AsyncState>
      </div>

      <AccountFormDialog
        open={formOpen}
        onClose={() => setFormOpen(false)}
        onSaved={() => {
          if (editing) invalidateAccountResources(editing.id)
          setFormOpen(false)
          show('账号已保存')
          void load()
        }}
        editing={editing ? { id: editing.id, name: editing.name, icloudEmail: editing.icloud_email, host: editing.host } : null}
      />
      {cookieFor && (
        <CookieDialog
          accountId={cookieFor.id}
          open
          onClose={() => setCookieFor(null)}
          onSaved={() => {
            invalidateAccountResources(cookieFor.id)
            show('Cookie 已更新')
            void load()
          }}
        />
      )}
      {loginFor && (
        <ICloudLoginDialog
          accountId={loginFor.id}
          open
          onClose={() => setLoginFor(null)}
          onSaved={() => {
            invalidateAccountResources(loginFor.id)
            show('登录成功')
            void load()
          }}
        />
      )}
      {appPwdFor && (
        <AppPasswordDialog
          accountId={appPwdFor.id}
          open
          onClose={() => setAppPwdFor(null)}
          onSaved={() => {
            invalidateAccountResources(appPwdFor.id)
            show('App 专用密码已设置')
            void load()
          }}
        />
      )}
      {proxyFor && (
        <ProxyDialog
          accountId={proxyFor.id}
          open
          onClose={() => setProxyFor(null)}
          onSaved={() => {
            invalidateAccountResources(proxyFor.id)
            show('代理已更新')
            void load()
          }}
        />
      )}
      {mailboxFor && (
        <MailboxDialog
          accountId={mailboxFor.id}
          current={mailboxFor.mailbox}
          open
          onClose={() => setMailboxFor(null)}
          onSaved={() => {
            invalidateAccountResources(mailboxFor.id)
            setMailboxFor(null)
            show('收件邮箱已接入')
            void load()
          }}
        />
      )}
      {deleteFor && (
        <ConfirmDialog
          title="删除账号"
          message={`将移除本地账号配置「${deleteFor.name}」，不会删除 Apple 账号本身。`}
          requireText={deleteFor.name}
          open
          busy={deleting}
          onClose={() => setDeleteFor(null)}
          onConfirm={() => void handleDelete()}
        />
      )}
    </section>
  )
}
