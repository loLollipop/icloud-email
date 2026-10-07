import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { IconAccounts, IconAliases, IconChevronDown, IconCloud, IconGithub, IconHelp, IconInbox, IconLogout } from './icons'
import ThemeControls from './ThemeControls'

const navigation = [
  { path: '/inbox', label: '收件箱', icon: IconInbox },
  { path: '/aliases', label: '隐藏邮箱', icon: IconAliases },
  { path: '/accounts', label: '邮箱账户', icon: IconAccounts },
]

export default function AppShell() {
  const { logout } = useAuth()
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const isMail = pathname === '/inbox'
  const isManagement = pathname === '/accounts' || pathname === '/aliases'
  const title = navigation.find((item) => item.path === pathname)?.label ?? '使用指南'

  async function handleLogout() {
    await logout()
    navigate('/login', { replace: true })
  }

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">跳到主要内容</a>
      <aside className="workspace-sidebar" aria-label="工作区导航">
        <NavLink className="brand" to="/inbox" aria-label="iCloud Mail 首页">
          <span className="brand-logo"><IconCloud size={24} /></span>
          <span>iCloud Mail</span>
        </NavLink>
        <nav className="workspace-nav" aria-label="主导航">
          {navigation.map(({ path, label, icon: Icon }) => (
            <NavLink key={path} to={path}><Icon size={18} /><span>{label}</span></NavLink>
          ))}
        </nav>
        <a
          className="workspace-repository-link"
          href="https://github.com/loLollipop/icloud-email"
          target="_blank"
          rel="noreferrer"
          aria-label="打开 GitHub 仓库 loLollipop/icloud-email"
        >
          <IconGithub size={18} />
          <span><strong>GitHub</strong><small>loLollipop/icloud-email</small></span>
        </a>
      </aside>
      <header className="workspace-topbar">
        <div className="workspace-breadcrumb"><span>工作区</span><span aria-hidden="true">/</span><h1>{title}</h1></div>
        <div className="workspace-utilities" aria-label="工作区工具">
          <NavLink className="workspace-icon-button" to="/help" aria-label="使用指南" title="使用指南"><IconHelp size={18} /></NavLink>
          <ThemeControls />
          <details className="workspace-user-menu">
            <summary className="workspace-user-trigger" aria-label="管理员账户菜单" title="管理员账户">
              <span className="workspace-user-avatar" aria-hidden="true">A</span>
              <span className="workspace-user-name">admin</span>
              <IconChevronDown size={14} />
            </summary>
            <div className="workspace-user-popover">
              <span className="workspace-user-popover-label">管理员账户</span>
              <button className="workspace-user-logout" onClick={() => void handleLogout()} aria-label="退出登录">
                <IconLogout size={16} />退出登录
              </button>
            </div>
          </details>
        </div>
      </header>
      <main id="main-content" className={`workspace-main${isMail ? ' workspace-main--mail' : ''}${isManagement ? ' workspace-main--management' : ''}`} tabIndex={-1}>
        <Outlet />
      </main>
    </div>
  )
}
