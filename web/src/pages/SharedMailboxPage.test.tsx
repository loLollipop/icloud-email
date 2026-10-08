import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { Link, MemoryRouter } from 'react-router-dom'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/server'
import { ToastProvider } from '../components/ToastProvider'
import SharedMailboxPage from './SharedMailboxPage'
import App from '../App'

const message = { id: '101', to: 'customer@icloud.com', from: 'sender@example.test', subject: '新到邮件', date: '2026-10-08T05:01:00Z', preview: '新的内容' }
const info = { email: 'customer@icloud.com', created_at: '2026-10-08T05:00:00Z' }
const response = { ...info, count: 1, total: 1, page: 1, page_size: 20, messages: [message] }
function showPage(path = '/share#customer_token') {
  render(<MemoryRouter initialEntries={[path]}><ToastProvider><SharedMailboxPage /></ToastProvider></MemoryRouter>)
}
function mockInbox() {
  server.use(http.get('/api/shared/inbox', ({ request }) => {
    expect(request.headers.get('authorization')).toBe('Bearer customer_token')
    expect(request.headers.get('x-csrf-token')).toBeNull()
    expect(request.credentials).toBe('omit')
    expect(new URL(request.url).searchParams.has('account_id')).toBe(false)
    return HttpResponse.json({ success: true, data: response })
  }))
}

describe('客户只读邮箱', () => {
  it('无需管理员认证，列表和正文只调用分享 API，无编辑删除或账号菜单', async () => {
    mockInbox()
    server.use(http.get('/api/shared/inbox/101', ({ request }) => {
      expect(request.headers.get('authorization')).toBe('Bearer customer_token')
      return HttpResponse.json({ success: true, data: { ...message, body: '客户只读正文', content_type: 'text/plain' } })
    }))
    showPage()
    await userEvent.click(await screen.findByRole('button', { name: '新到邮件' }))
    expect(await screen.findByText('客户只读正文')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '删除邮件' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /编辑/ })).not.toBeInTheDocument()
    expect(screen.queryByText('admin')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '← 返回全部邮件' }))
    expect(screen.getByRole('button', { name: '新到邮件' })).toBeInTheDocument()
  })

  it('主题搜索和分页只发送允许的参数', async () => {
    const seen: URL[] = []
    server.use(http.get('/api/shared/inbox', ({ request }) => {
      const url = new URL(request.url)
      seen.push(url)
      return HttpResponse.json({ success: true, data: { ...response, total: 42, page: Number(url.searchParams.get('page')), page_size: Number(url.searchParams.get('page_size')) } })
    }))
    showPage()
    await screen.findByRole('button', { name: '新到邮件' })
    await userEvent.type(screen.getByRole('searchbox'), 'approved')
    await userEvent.click(screen.getByRole('button', { name: '搜索' }))
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('q')).toBe('approved'))
    await screen.findByRole('button', { name: '新到邮件' })
    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('page')).toBe('2'))
    expect([...seen.at(-1)!.searchParams.keys()]).toEqual(['page', 'page_size', 'q'])
  })

  it('撤销后清空列表正文并显示失效提示，不再重试旧链接', async () => {
    mockInbox()
    server.use(http.get('/api/shared/inbox/101', () => HttpResponse.json({ success: true, data: { ...message, body: '已经显示的正文', content_type: 'text/plain' } })))
    showPage()
    await userEvent.click(await screen.findByRole('button', { name: '新到邮件' }))
    await screen.findByText('已经显示的正文')
    server.use(http.get('/api/shared', () => HttpResponse.json({ success: false, code: 'SHARE_UNAVAILABLE', message: '分享不可用' }, { status: 404 })))
    fireEvent(window, new Event('focus'))
    expect(await screen.findByText('此链接无法继续使用')).toBeInTheDocument()
    expect(screen.queryByText('已经显示的正文')).not.toBeInTheDocument()
    expect(screen.queryByText('customer@icloud.com')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '重试' })).not.toBeInTheDocument()
  })

  it('正文猜测或过期返回404时不暴露内容', async () => {
    mockInbox()
    server.use(http.get('/api/shared/inbox/101', () => HttpResponse.json({ success: false, code: 'MESSAGE_NOT_FOUND', message: '邮件不存在' }, { status: 404 })))
    showPage()
    await userEvent.click(await screen.findByRole('button', { name: '新到邮件' }))
    expect(await screen.findByText('邮件不存在')).toBeInTheDocument()
    expect(screen.queryByText('客户只读正文')).not.toBeInTheDocument()
  })

  it('暂时无法验证时隐藏内容，但恢复后允许原链接重试', async () => {
    mockInbox()
    server.use(http.get('/api/shared/inbox/101', () => HttpResponse.json({ success: true, data: { ...message, body: '验证过的客户正文', content_type: 'text/plain' } })))
    showPage()
    await userEvent.click(await screen.findByRole('button', { name: '新到邮件' }))
    await screen.findByText('验证过的客户正文')
    server.use(http.get('/api/shared', () => HttpResponse.json({ success: false, code: 'UPSTREAM_FAILURE', message: '共享邮件暂不可用' }, { status: 503 })))
    fireEvent(window, new Event('focus'))
    expect(await screen.findByText('暂时无法打开邮箱')).toBeInTheDocument()
    expect(screen.queryByText('验证过的客户正文')).not.toBeInTheDocument()
    expect(screen.queryByText('customer@icloud.com')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByRole('button', { name: '新到邮件' })).toBeInTheDocument()
    expect(screen.queryByText('此链接无法继续使用')).not.toBeInTheDocument()
  })

  it('撤销时仍在进行的正文请求不能重新显示数据', async () => {
    let finish!: () => void
    const pending = new Promise<void>((resolve) => { finish = resolve })
    mockInbox()
    server.use(http.get('/api/shared/inbox/101', async () => {
      await pending
      return HttpResponse.json({ success: true, data: { ...message, body: '延迟返回的正文', content_type: 'text/plain' } })
    }))
    showPage()
    await userEvent.click(await screen.findByRole('button', { name: '新到邮件' }))
    server.use(http.get('/api/shared', () => HttpResponse.json({ success: false, code: 'SHARE_UNAVAILABLE' }, { status: 404 })))
    fireEvent(window, new Event('focus'))
    await screen.findByText('此链接无法继续使用')
    await act(async () => finish())
    expect(screen.queryByText('延迟返回的正文')).not.toBeInTheDocument()
    expect(screen.queryByRole('article')).not.toBeInTheDocument()
  })

  it('缺失token不请求邮箱', async () => {
    showPage('/share')
    expect(await screen.findByText('此链接无法继续使用')).toBeInTheDocument()
  })

  it('空列表明确说明历史邮件不可查看', async () => {
    server.use(http.get('/api/shared/inbox', () => HttpResponse.json({ success: true, data: { ...response, total: 0, count: 0, messages: [] } })))
    showPage()
    expect(await screen.findByText('暂无新邮件，历史邮件不会在这里显示')).toBeInTheDocument()
  })

  it.each(['list', 'detail', 'access'])('切换token后忽略旧链接延迟的%s响应', async (stage) => {
    mockInbox()
    render(<MemoryRouter initialEntries={['/share#customer_token']}><ToastProvider><Link to="/share#replacement_token">切换链接</Link><SharedMailboxPage /></ToastProvider></MemoryRouter>)
    await screen.findByRole('button', { name: '新到邮件' })
    let finish!: () => void
    const pending = new Promise<void>((resolve) => { finish = resolve })
    const oldPath = stage === 'list' ? '/api/shared/inbox' : stage === 'detail' ? '/api/shared/inbox/101' : '/api/shared'
    server.use(
      http.get('/api/shared/inbox', ({ request }) => request.headers.get('authorization') === 'Bearer replacement_token'
        ? HttpResponse.json({ success: true, data: { ...response, email: 'next@icloud.com', messages: [{ ...message, to: 'next@icloud.com', subject: '新客户邮件' }] } })
        : HttpResponse.json({ success: true, data: response })),
      http.get(oldPath, async ({ request }) => {
        if (request.headers.get('authorization') === 'Bearer replacement_token') return HttpResponse.json({ success: true, data: { ...response, email: 'next@icloud.com', messages: [{ ...message, to: 'next@icloud.com', subject: '新客户邮件' }] } })
        await pending
        return HttpResponse.json({ success: true, data: stage === 'detail' ? { ...message, body: '旧客户延迟正文', content_type: 'text/plain' } : stage === 'access' ? info : response })
      }),
    )
    if (stage === 'list') await userEvent.click(screen.getByRole('button', { name: '刷新邮件' }))
    else if (stage === 'detail') await userEvent.click(screen.getByRole('button', { name: '新到邮件' }))
    else fireEvent(window, new Event('focus'))
    await userEvent.click(screen.getByRole('link', { name: '切换链接' }))
    expect(await screen.findByRole('button', { name: '新客户邮件' })).toBeInTheDocument()
    await act(async () => finish())
    expect(screen.queryByText('旧客户延迟正文')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '新到邮件' })).not.toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'next@icloud.com' })).toBeInTheDocument()
  })

  it.each(['interval', 'visibilitychange', 'pageshow'])('%s重新验证并清除失效邮箱', async (trigger) => {
    let check: (() => void) | undefined
    const setInterval = window.setInterval.bind(window)
    const timer = vi.spyOn(window, 'setInterval').mockImplementation((handler, delay, ...args) => {
      if (delay === 15_000) check = handler as () => void
      return setInterval(handler, delay, ...args)
    })
    try {
      mockInbox()
      showPage()
      await screen.findByRole('button', { name: '新到邮件' })
      expect(timer).toHaveBeenCalledWith(expect.any(Function), 15_000)
      server.use(http.get('/api/shared', () => HttpResponse.json({ success: false, code: 'SHARE_UNAVAILABLE' }, { status: 404 })))
      if (trigger === 'interval') act(() => check?.())
      else fireEvent(trigger === 'visibilitychange' ? document : window, new Event(trigger))
      expect(await screen.findByText('此链接无法继续使用')).toBeInTheDocument()
      expect(screen.queryByRole('button', { name: '新到邮件' })).not.toBeInTheDocument()
    } finally {
      timer.mockRestore()
    }
  })

  it('独立分享路由不加载管理员会话或账户', async () => {
    window.history.replaceState(null, '', '/share#customer_token')
    let administratorCalls = 0
    mockInbox()
    server.use(http.get('/api/auth/session', () => {
      administratorCalls++
      return HttpResponse.json({ success: false, code: 'AUTH_REQUIRED' }, { status: 401 })
    }))
    render(<App />)
    expect(await screen.findByRole('button', { name: '新到邮件' })).toBeInTheDocument()
    expect(administratorCalls).toBe(0)
    window.history.replaceState(null, '', '/')
  })
})
