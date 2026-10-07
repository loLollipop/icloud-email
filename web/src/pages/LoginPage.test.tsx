import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import LoginPage from './LoginPage'

const auth = vi.hoisted(() => ({ status: 'anonymous', login: vi.fn() }))
vi.mock('../auth/AuthProvider', () => ({ useAuth: () => auth }))

function renderLogin() {
  return render(<MemoryRouter initialEntries={['/login']}><Routes>
    <Route path="/login" element={<LoginPage />} /><Route path="/inbox" element={<p>收件工作区</p>} />
  </Routes></MemoryRouter>)
}

describe('LoginPage', () => {
  beforeEach(() => { auth.status = 'anonymous'; auth.login.mockReset() })

  it('说明管理员密码区别，显隐可切换且成功打开收件箱', async () => {
    auth.login.mockResolvedValue(undefined)
    renderLogin()
    expect(screen.getByRole('heading', { name: '邮件工作区' })).toBeInTheDocument()
    expect(screen.getByText(/不是 Apple 账号密码/)).toBeInTheDocument()
    const input = screen.getByLabelText('管理员密码')
    expect(input).toHaveAttribute('type', 'password')
    const user = userEvent.setup()
    await user.type(input, 'admin-password')
    await user.click(screen.getByRole('button', { name: '显示密码' }))
    expect(input).toHaveAttribute('type', 'text')
    await user.click(screen.getByRole('button', { name: '隐藏密码' }))
    expect(input).toHaveAttribute('type', 'password')
    await user.click(screen.getByRole('button', { name: '进入工作区' }))
    expect(auth.login).toHaveBeenCalledWith('admin-password')
    expect(await screen.findByText('收件工作区')).toBeInTheDocument()
  })

  it('认证完成的登录访问重定向收件箱，会话校验中不显示登录表单', () => {
    auth.status = 'authenticated'
    const view = renderLogin()
    expect(screen.getByText('收件工作区')).toBeInTheDocument()
    view.unmount()
    auth.status = 'checking'
    renderLogin()
    expect(screen.getByText('加载中…')).toHaveAttribute('aria-busy', 'true')
    expect(screen.queryByLabelText('管理员密码')).not.toBeInTheDocument()
  })

  it('登录失败仍保留表单且不在错误消息暴露密码', async () => {
    auth.login.mockRejectedValue(new Error('offline'))
    renderLogin()
    await userEvent.type(screen.getByLabelText('管理员密码'), 'secret-value')
    await userEvent.click(screen.getByRole('button', { name: '进入工作区' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('网络连接失败')
    expect(screen.getByRole('alert')).not.toHaveTextContent('secret-value')
    expect(screen.getByLabelText('管理员密码')).toHaveAttribute('type', 'password')
  })
})
