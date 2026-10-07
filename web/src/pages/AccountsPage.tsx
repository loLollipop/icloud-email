import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
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
  IconAliases,
  IconChevronDown,
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
        <p className="page-description">连接 iCloud 账号，管理隐藏邮箱与收件凭据。</p>
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
          <div className="account-grid management-scroll-region">
            {visibleAccounts.map((acc) => (
              <article className="account-card" key={acc.id} aria-label={acc.name}>
                <div className="account-card-header">
                  <span className="account-avatar" aria-hidden="true">{acc.name.slice(0, 1)}</span>
                  <div className="account-identity">
                    <div className="account-heading"><h2>{acc.name}</h2><StatusBadge status={acc.status} /></div>
                    <p>{acc.icloud_email || acc.real_email || '未填写邮箱'}</p>
                  </div>
                </div>
                {acc.status_message && <p className="account-status-message">{acc.status_message}</p>}
                <div className="account-metadata">
                  <div><span>隐藏邮箱 · 启用 / 全部</span><strong title="已启用别名 / 全部别名">{acc.alias_active} / {acc.alias_total}</strong></div>
                  <div><span>最近验证</span><time dateTime={acc.last_validated || undefined}>{formatValidation(acc.last_validated)}</time></div>
                </div>
                <ul className="credential-tags" aria-label="凭据配置情况（仅表示已填写）">
                  {[
                    { name: 'Cookie', configured: acc.has_cookies },
                    { name: 'App 密码', configured: acc.has_app_password },
                    { name: '收件邮箱', configured: Boolean(acc.mailbox) },
                    { name: '代理', configured: acc.has_proxy },
                  ].map((credential) => (
                    <li key={credential.name} className={credential.configured ? 'is-configured' : ''}
                      title={`${credential.name}：${credential.configured ? '已配置' : '未配置'}（不代表当前连接状态）`}>
                      <span className="credential-name">{credential.name}</span>
                      <span className="credential-state">
                        {credential.configured && <IconCheck size={12} />}
                        {credential.configured ? '已配置' : '未配置'}
                      </span>
                    </li>
                  ))}
                </ul>
                <div className="account-primary-actions row-actions">
                  <Link className="account-inbox-link" to={`/inbox?account_id=${encodeURIComponent(acc.id)}`}><IconMail size={14} />收件箱</Link>
                  <Link to={`/aliases?account_id=${encodeURIComponent(acc.id)}`}><IconAliases size={14} />别名</Link>
                  <button className="ghost" onClick={() => { setEditing(acc); setFormOpen(true) }}><IconEdit size={14} />编辑</button>
                </div>
                <details className="account-settings">
                  <summary><IconSettings size={14} /><span>连接设置</span><span className="visually-hidden"> · {acc.name}</span><IconChevronDown className="account-settings-chevron" size={14} /></summary>
                  <div className="account-settings-content">
                    <div className="row-actions">
                      <button onClick={() => setCookieFor(acc)}>更新 Cookie</button>
                      <button onClick={() => setLoginFor(acc)}><IconKey size={14} />iCloud 登录</button>
                      <button onClick={() => setAppPwdFor(acc)}>设置 App 密码</button>
                      <button onClick={() => setMailboxFor(acc)}>接入收件邮箱</button>
                      <button onClick={() => setProxyFor(acc)}>设置代理</button>
                    </div>
                    {acc.mailbox && <p className="hint">收件邮箱：{acc.mailbox.email}</p>}
                    <div className="account-settings-bottom"><span className="hint">账号 ID：{acc.id}</span><button className="danger" onClick={() => setDeleteFor(acc)}><IconTrash size={14} />删除</button></div>
                  </div>
                </details>
              </article>
            ))}
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
