import { useEffect, useState } from 'react'
import { NavLink, Outlet, useLocation, useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthProvider'
import { IconAccounts, IconAliases, IconCloud, IconHelp, IconInbox, IconLogout, IconSun } from './icons'

type Theme = 'system' | 'light' | 'dark'

function readTheme(): Theme {
  try {
    const value = localStorage.getItem('icloud-mail-theme')
    if (value === 'light' || value === 'dark') return value
  } catch { /* Appearance remains usable when storage is unavailable. */ }
  return 'system'
}

const navigation = [
  { path: '/inbox', label: '收件箱', icon: IconInbox },
  { path: '/aliases', label: '隐藏邮箱', icon: IconAliases },
  { path: '/accounts', label: '邮箱账户', icon: IconAccounts },
]

export default function AppShell() {
  const { logout } = useAuth()
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const [theme, setTheme] = useState<Theme>(readTheme)
  const isMail = pathname === '/inbox'
  const title = navigation.find((item) => item.path === pathname)?.label ?? '使用指南'

  useEffect(() => {
    if (theme === 'system') delete document.documentElement.dataset.theme
    else document.documentElement.dataset.theme = theme
    try {
      if (theme === 'system') localStorage.removeItem('icloud-mail-theme')
      else localStorage.setItem('icloud-mail-theme', theme)
    } catch { /* Only this optional UI preference is persisted. */ }
  }, [theme])

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
          <span>iCloud Mail<span className="brand-sub">邮件工作区</span></span>
        </NavLink>
        <nav className="workspace-nav" aria-label="主导航">
          {navigation.map(({ path, label, icon: Icon }) => (
            <NavLink key={path} to={path}><Icon size={18} /><span>{label}</span></NavLink>
          ))}
        </nav>
        <div className="sidebar-footer">
          <NavLink className="sidebar-utility" to="/help"><IconHelp size={17} />使用指南</NavLink>
          <div className="theme-control">
            <label htmlFor="workspace-theme"><IconSun size={16} /><span>外观</span></label>
            <select id="workspace-theme" value={theme} onChange={(event) => setTheme(event.target.value as Theme)}>
              <option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option>
            </select>
          </div>
          <button className="sidebar-utility ghost" onClick={() => void handleLogout()}><IconLogout size={17} />退出登录</button>
        </div>
      </aside>
      <header className="workspace-topbar">
        <div className="workspace-breadcrumb"><span>工作区</span><span aria-hidden="true">/</span><h1>{title}</h1></div>
        <span className="workspace-topbar-note">iCloud 隐藏邮箱与邮件</span>
        <details className="mobile-utilities">
          <summary aria-label="工作区选项">···</summary>
          <div className="mobile-utility-menu">
            <NavLink to="/help">使用指南</NavLink>
            <label htmlFor="mobile-theme">外观</label>
            <select id="mobile-theme" value={theme} onChange={(event) => setTheme(event.target.value as Theme)}>
              <option value="system">跟随系统</option><option value="light">浅色</option><option value="dark">深色</option>
            </select>
            <button className="ghost" onClick={() => void handleLogout()}>退出登录</button>
          </div>
        </details>
      </header>
      <main id="main-content" className={`workspace-main${isMail ? ' workspace-main--mail' : ''}`} tabIndex={-1}>
        <Outlet />
      </main>
    </div>
  )
}
