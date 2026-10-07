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
    await userEvent.click(within(nav).getByRole('link', { name: '邮箱账户' }))
    expect(screen.getByRole('main')).not.toHaveClass('workspace-main--mail')
    expect(screen.getByRole('heading', { name: '邮箱账户' })).toBeInTheDocument()
    const sidebar = screen.getByRole('complementary', { name: '工作区导航' })
    await userEvent.click(within(sidebar).getByRole('link', { name: '使用指南' }))
    expect(screen.getByText('接入指南')).toBeInTheDocument()
  })

  it('主题选择保存唯一界面偏好，系统模式移除明确主题', async () => {
    localStorage.setItem('icloud-mail-theme', 'dark')
    renderShell()
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    const select = screen.getByLabelText('外观', { selector: '#workspace-theme' })
    await userEvent.selectOptions(select, 'light')
    expect(document.documentElement).toHaveAttribute('data-theme', 'light')
    expect(localStorage.getItem('icloud-mail-theme')).toBe('light')
    await userEvent.selectOptions(select, 'system')
    expect(document.documentElement).not.toHaveAttribute('data-theme')
    expect(localStorage.getItem('icloud-mail-theme')).toBeNull()
  })

  it('存储不可用时仍可切换主题，退出调用认证并返回登录页', async () => {
    vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('disabled') })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('disabled') })
    vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('disabled') })
    renderShell()
    await userEvent.selectOptions(screen.getByLabelText('外观', { selector: '#workspace-theme' }), 'dark')
    expect(document.documentElement).toHaveAttribute('data-theme', 'dark')
    await userEvent.click(within(screen.getByRole('complementary')).getByRole('button', { name: '退出登录' }))
    expect(auth.logout).toHaveBeenCalledOnce()
    expect(screen.getByText('登录页')).toBeInTheDocument()
  })
})
