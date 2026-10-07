import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AppShell from './AppShell'

const auth = vi.hoisted(() => ({ logout: vi.fn().mockResolvedValue(undefined) }))
vi.mock('../auth/AuthProvider', () => ({ useAuth: () => auth }))

function renderShell(path = '/inbox') {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <Routes>
        <Route element={<AppShell />}>
          <Route path="/inbox" element={<p>邮件内容</p>} />
          <Route path="/accounts" element={<p>账户内容</p>} />
          <Route path="/aliases" element={<p>隐藏邮箱内容</p>} />
          <Route path="/help" element={<p>接入指南</p>} />
        </Route>
        <Route path="/login" element={<p>登录页</p>} />
      </Routes>
    </MemoryRouter>,
  )
}

describe('AppShell', () => {
  beforeEach(() => {
    localStorage.clear()
    delete document.documentElement.dataset.theme
    auth.logout.mockClear()
  })
  afterEach(() => {
    vi.restoreAllMocks()
    localStorage.clear()
    delete document.documentElement.dataset.theme
  })

  it('导航使用新品牌，收件箱有独立布局且使用指南可访问', async () => {
    renderShell()
    const nav = screen.getByRole('navigation', { name: '主导航' })
    expect(within(nav).getByRole('link', { name: '收件箱' })).toHaveAttribute('aria-current', 'page')
    expect(screen.getByRole('link', { name: 'iCloud Mail 首页' })).toHaveAttribute('href', '/inbox')
    expect(screen.getByRole('main')).toHaveClass('workspace-main--mail')
    const repository = screen.getByRole('link', { name: '打开 GitHub 仓库 loLollipop/icloud-email' })
    expect(repository).toHaveAttribute('href', 'https://github.com/loLollipop/icloud-email')
    expect(repository).toHaveAttribute('target', '_blank')
    expect(repository).toHaveAttribute('rel', 'noreferrer')
    expect(screen.getByText('admin')).toBeInTheDocument()
    await userEvent.click(within(nav).getByRole('link', { name: '邮箱账户' }))
    expect(screen.getByRole('main')).not.toHaveClass('workspace-main--mail')
    expect(screen.getByRole('heading', { name: '邮箱账户' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('link', { name: '使用指南' }))
    expect(screen.getByText('接入指南')).toBeInTheDocument()
  })

  it('主题按钮直接在浅深色间切换，独立系统按钮恢复跟随系统', async () => {
    localStorage.setItem('icloud-mail-theme', 'dark')
    renderShell()
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    await userEvent.click(screen.getByRole('button', { name: '切换为浅色' }))
    expect(document.documentElement).toHaveAttribute('data-theme', 'light')
    expect(localStorage.getItem('icloud-mail-theme')).toBe('light')
    await userEvent.click(screen.getByRole('button', { name: '切换为深色' }))
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    expect(screen.getByRole('button', { name: '跟随系统' })).toHaveAttribute('aria-pressed', 'false')
    await userEvent.click(screen.getByRole('button', { name: '跟随系统' }))
    expect(document.documentElement).not.toHaveAttribute('data-theme')
    expect(localStorage.getItem('icloud-mail-theme')).toBeNull()
    expect(screen.getByRole('button', { name: '跟随系统' })).toHaveAttribute('aria-pressed', 'true')
    expect(screen.queryByRole('combobox', { name: '外观' })).not.toBeInTheDocument()
  })

  it('存储不可用时仍可切换主题，退出调用认证并返回登录页', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('disabled') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('disabled') })
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('disabled') })
    renderShell()
    await userEvent.click(screen.getByRole('button', { name: '切换为深色' }))
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    await userEvent.click(screen.getByTitle('管理员账户'))
    await userEvent.click(screen.getByRole('button', { name: '退出登录' }))
    expect(auth.logout).toHaveBeenCalledOnce()
    expect(screen.getByText('登录页')).toBeInTheDocument()
  })
})
