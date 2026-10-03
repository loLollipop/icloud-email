import { http, HttpResponse } from 'msw'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'
import InboxPage from './InboxPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { loadResource, resourceKeys } from '../api/resourceCache'
import { ToastProvider } from '../components/ToastProvider'
import type { AccountSummary, FullMessage, InboxResult } from '../api/types'

const accounts: AccountSummary[] = [
  {
    id: 'acc_1',
    name: '主号',
    real_email: 'a@example.com',
    icloud_email: 'a@icloud.com',
    host: 'icloud.com',
    status: 'active',
    alias_total: 2,
    alias_active: 2,
    has_cookies: true,
    has_app_password: true,
    has_proxy: false,
    last_validated: '2026-08-04T09:00:00+08:00',
    created_at: '2026-08-01T09:00:00+08:00',
  },
]

const inboxResult: InboxResult = {
  account_id: 'acc_1',
  alias: 'alpha@icloud.com',
  count: 1,
  method: 'imap',
  messages: [
    {
      id: '1',
      from: 'sender@example.com',
      to: 'alpha@icloud.com',
      subject: '主题一',
      date: '2026-08-04T10:00:00+08:00',
      preview: '预览内容',
    },
  ],
}

const fullMessage: FullMessage = {
  ...inboxResult.messages[0],
  body: 'Plain detail body',
  content_type: 'text/plain; charset=utf-8',
}

function renderPage(initialPath = '/inbox') {
  return render(
    <MemoryRouter initialEntries={[initialPath]}>
      <Routes>
        <Route
          path="/inbox"
          element={
            <ToastProvider>
              <InboxPage />
            </ToastProvider>
          }
        />
      </Routes>
    </MemoryRouter>,
  )
}

describe('InboxPage', () => {
  beforeEach(() => {
    setCSRFToken('csrf-test')
    server.resetHandlers()
    server.use(
      http.get('/api/aliases', () => HttpResponse.json({
        success: true,
        data: { account_id: 'acc_1', count: 0, aliases: [] },
      })),
    )
  })

  it('账号必选;alias 可空;limit/days 生效;query 经 URLSearchParams', async () => {
    let lastUrl = ''
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', ({ request }) => {
        lastUrl = request.url
        return HttpResponse.json({ success: true, data: inboxResult })
      }),
    )
    renderPage()
    await screen.findByText('主题一')
    // 确认 query 参数
    const url = new URL(lastUrl)
    expect(url.searchParams.get('account_id')).toBe('acc_1')
    expect(url.searchParams.get('limit')).toBe('20')
    expect(url.searchParams.get('days')).toBe('7')
    // 修改 limit/days 再查询
    const user = userEvent.setup()
    await user.selectOptions(screen.getByLabelText(/加载范围/), '100')
    await user.selectOptions(screen.getByLabelText(/时间范围/), '30')
    await user.click(screen.getByRole('button', { name: /应用筛选/ }))
    await waitFor(() => {
      const u = new URL(lastUrl)
      expect(u.searchParams.get('limit')).toBe('100')
      expect(u.searchParams.get('days')).toBe('30')
    })
  })

  it('从 URL 的 alias 参数初始化筛选,支持别名页直达收件箱', async () => {
    const inboxUrls: string[] = []
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/aliases', () =>
        HttpResponse.json({
          success: true,
          data: {
            account_id: 'acc_1',
            count: 1,
            aliases: [
              {
                email: 'alpha@icloud.com',
                anonymousId: 'anon_alpha',
                label: 'Alpha',
                active: true,
              },
            ],
          },
        }),
      ),
      http.get('/api/inbox', ({ request }) => {
        inboxUrls.push(request.url)
        return HttpResponse.json({ success: true, data: inboxResult })
      }),
    )
    renderPage('/inbox?account_id=acc_1&alias=alpha%40icloud.com')
    await screen.findByText('主题一')
    await waitFor(() => {
      expect(inboxUrls.some((url) => new URL(url).searchParams.get('alias') === 'alpha@icloud.com')).toBe(true)
    })
    expect(screen.getByLabelText(/别名/)).toHaveValue('alpha@icloud.com')
  })

  it('展示 method=imap 或 web_api', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: { ...inboxResult, method: 'web_api' },
        }),
      ),
    )
    renderPage()
    await screen.findByText('主题一')
    expect(screen.getByText('Web API 摘要模式')).toBeInTheDocument()
  })

  it('在已加载窗口内按邮箱搜索并分页', async () => {
    const messages = Array.from({ length: 12 }, (_, index) => ({
      ...inboxResult.messages[0],
      id: String(index + 1),
      from: index === 11 ? 'special@example.com' : `sender-${index + 1}@example.com`,
      subject: `主题 ${index + 1}`,
      preview: `摘要 ${index + 1}`,
    }))
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({
        success: true,
        data: { ...inboxResult, count: messages.length, messages },
      })),
    )

    renderPage()
    expect(await screen.findByRole('button', { name: '主题 1' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '主题 11' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    expect(screen.getByRole('button', { name: '主题 11' })).toBeInTheDocument()

    const user = userEvent.setup()
    await user.selectOptions(screen.getByLabelText('搜索字段'), 'from')
    await user.type(screen.getByLabelText('搜索当前已加载邮件'), 'special@example.com')
    expect(screen.getByRole('button', { name: '主题 12' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '主题 11' })).not.toBeInTheDocument()
    expect(screen.getByText('第 1-1 项，共 1 项')).toBeInTheDocument()
  })

  it('Web API 模式只打开摘要，不请求正文或显示删除入口', async () => {
    let detailRequests = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({
        success: true,
        data: { ...inboxResult, method: 'web_api' },
      })),
      http.get('/api/inbox/:id', () => {
        detailRequests++
        return HttpResponse.json({ success: true, data: fullMessage })
      }),
    )

    renderPage()
    await userEvent.click(await screen.findByRole('button', { name: '主题一' }))
    const reader = screen.getByRole('article', { name: '邮件阅读区' })
    expect(within(reader).getByText('预览内容')).toBeInTheDocument()
    expect(within(reader).getByText(/配置 App 专用密码后可阅读正文和删除/)).toBeInTheDocument()
    expect(within(reader).queryByRole('button', { name: /删除邮件/ })).not.toBeInTheDocument()
    expect(detailRequests).toBe(0)
  })

  it('空列表、网络错误、401 状态', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: { account_id: 'acc_1', count: 0, messages: [], method: 'imap' },
        }),
      ),
    )
    renderPage()
    expect(await screen.findByText(/暂无邮件/)).toBeInTheDocument()
  })

  it('恶意 HTML 只作为文本显示,不产生 img 节点', async () => {
    const evil = {
      ...inboxResult,
      messages: [
        {
          id: '2',
          from: 'evil@example.com',
          to: 'alpha@icloud.com',
          subject: '<img src=x onerror=alert(1)>',
          date: '2026-08-04T10:00:00+08:00',
          preview: '<script>alert(2)</script>预览',
        },
      ],
    }
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: evil })),
    )
    renderPage()
    await screen.findByText(/<img src=x onerror=alert\(1\)>/)
    expect(document.querySelector('img')).toBeNull()
    expect(document.querySelector('script')).toBeNull()
  })

  it('快速切换筛选:第一请求晚返回不覆盖第二请求', async () => {
    let release: (() => void) | undefined
    let calls = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => {
        calls++
        if (calls === 1) {
          // 第一次请求挂起,稍后返回旧数据
          return new Promise<Response>((resolve) => {
            release = () =>
              resolve(
                HttpResponse.json({
                  success: true,
                  data: {
                    ...inboxResult,
                    messages: [{ ...inboxResult.messages[0], subject: '旧主题' }],
                  },
                }),
              )
          })
        }
        return HttpResponse.json({
          success: true,
          data: {
            ...inboxResult,
            alias: 'second',
            messages: [
              {
                id: '9',
                from: 's2@example.com',
                to: 'alpha@icloud.com',
                subject: '第二请求主题',
                date: '2026-08-04T11:00:00+08:00',
                preview: '第二请求',
              },
            ],
          },
        })
      }),
    )
    renderPage()
    await screen.findByText(/加载中/)
    // 触发第二次查询(首次挂起中)
    const user = userEvent.setup()
    await user.selectOptions(screen.getByLabelText(/加载范围/), '100')
    await user.click(screen.getByRole('button', { name: /应用筛选/ }))
    await screen.findByText('第二请求主题')
    // 第一次请求此时才返回
    release?.()
    // 旧数据不得覆盖新数据
    await new Promise((r) => setTimeout(r, 100))
    expect(screen.getByText('第二请求主题')).toBeInTheDocument()
    expect(screen.queryByText('旧主题')).toBeNull()
  })

  it('重新挂载先显示缓存并在后台刷新最新邮件', async () => {
    let inboxRequests = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => {
        inboxRequests++
        return HttpResponse.json({
          success: true,
          data: {
            ...inboxResult,
            messages: [{
              ...inboxResult.messages[0],
              subject: inboxRequests === 1 ? '缓存主题' : '刷新主题',
            }],
          },
        })
      }),
    )

    const first = renderPage()
    await screen.findByText('缓存主题')
    first.unmount()

    renderPage()
    expect(screen.getByText('缓存主题')).toBeInTheDocument()
    expect(screen.queryByRole('status', { name: /加载中/ })).toBeNull()
    expect(await screen.findByText('刷新主题')).toBeInTheDocument()
    expect(inboxRequests).toBe(2)
  })

  it('后台账号刷新不会覆盖用户刚选择的账号', async () => {
    const secondAccount: AccountSummary = { ...accounts[0], id: 'acc_2', name: '备用号' }
    const accountOptions = [...accounts, secondAccount]
    await loadResource(resourceKeys.accounts, -1, async () => accountOptions)
    let releaseAccounts: (() => void) | undefined
    let markAccountsStarted: (() => void) | undefined
    const accountsStarted = new Promise<void>((resolve) => { markAccountsStarted = resolve })
    server.use(
      http.get('/api/accounts', () => new Promise<Response>((resolve) => {
        markAccountsStarted?.()
        releaseAccounts = () => resolve(HttpResponse.json({ success: true, data: accountOptions }))
      })),
      http.get('/api/inbox', ({ request }) => {
        const id = new URL(request.url).searchParams.get('account_id')
        return HttpResponse.json({
          success: true,
          data: { ...inboxResult, account_id: id, messages: [{ ...inboxResult.messages[0], subject: id ?? '' }] },
        })
      }),
    )

    renderPage()
    await accountsStarted
    await userEvent.selectOptions(screen.getByLabelText(/账号/), 'acc_2')
    releaseAccounts?.()

    await waitFor(() => expect(screen.getByLabelText(/账号/)).toHaveValue('acc_2'))
    expect(await screen.findByText('acc_2')).toBeInTheDocument()
  })

  it('切换账号时丢弃未应用的范围草稿，使控件与请求保持一致', async () => {
    const secondAccount: AccountSummary = { ...accounts[0], id: 'acc_2', name: '备用号' }
    const accountOptions = [...accounts, secondAccount]
    let secondAccountURL = ''
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accountOptions })),
      http.get('/api/inbox', ({ request }) => {
        const url = new URL(request.url)
        if (url.searchParams.get('account_id') === 'acc_2') secondAccountURL = request.url
        return HttpResponse.json({
          success: true,
          data: { ...inboxResult, account_id: url.searchParams.get('account_id') ?? '' },
        })
      }),
    )

    renderPage('/inbox?account_id=acc_1&limit=50&days=30')
    const user = userEvent.setup()
    await screen.findByText('主题一')
    await user.selectOptions(screen.getByLabelText(/加载范围/), '100')
    await user.selectOptions(screen.getByLabelText(/时间范围/), '90')
    await user.selectOptions(screen.getByLabelText(/账号/), 'acc_2')

    await waitFor(() => expect(secondAccountURL).not.toBe(''))
    const query = new URL(secondAccountURL).searchParams
    expect(query.get('limit')).toBe('50')
    expect(query.get('days')).toBe('30')
    expect(screen.getByLabelText(/加载范围/)).toHaveValue('50')
    expect(screen.getByLabelText(/时间范围/)).toHaveValue('30')
  })

  it('账号列表待加载时修改普通筛选仍会初始化默认账号', async () => {
    let releaseAccounts: (() => void) | undefined
    let markAccountsStarted: (() => void) | undefined
    const accountsStarted = new Promise<void>((resolve) => { markAccountsStarted = resolve })
    let inboxURL = ''
    server.use(
      http.get('/api/accounts', () => new Promise<Response>((resolve) => {
        markAccountsStarted?.()
        releaseAccounts = () => resolve(HttpResponse.json({ success: true, data: accounts }))
      })),
      http.get('/api/aliases', () => HttpResponse.json({
        success: true,
        data: { account_id: 'acc_1', count: 0, aliases: [] },
      })),
      http.get('/api/inbox', ({ request }) => {
        inboxURL = request.url
        return HttpResponse.json({ success: true, data: inboxResult })
      }),
    )

    renderPage()
    await accountsStarted
    const user = userEvent.setup()
    await user.selectOptions(screen.getByLabelText(/加载范围/), '100')
    await user.selectOptions(screen.getByLabelText(/时间范围/), '30')
    releaseAccounts?.()

    await waitFor(() => expect(screen.getByLabelText(/账号/)).toHaveValue('acc_1'))
    await screen.findByText('主题一')
    const query = new URL(inboxURL).searchParams
    expect(query.get('account_id')).toBe('acc_1')
    expect(query.get('limit')).toBe('100')
    expect(query.get('days')).toBe('30')
    expect(screen.queryByRole('status', { name: /加载中/ })).toBeNull()
  })

  it('空 subject 显示(无主题)', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: {
            ...inboxResult,
            messages: [{ ...inboxResult.messages[0], subject: '' }],
          },
        }),
      ),
    )
    renderPage()
    expect(await screen.findByText(/（无主题）/)).toBeInTheDocument()
  })

  it('空摘要显示占位符', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () =>
        HttpResponse.json({
          success: true,
          data: {
            ...inboxResult,
            messages: [{ ...inboxResult.messages[0], preview: '' }],
          },
        }),
      ),
    )
    renderPage()
    expect(await screen.findByText('—')).toBeInTheDocument()
  })

  it('纯文本详情使用 pre fallback', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: inboxResult })),
      http.get('/api/inbox/:id', () => HttpResponse.json({ success: true, data: fullMessage })),
    )
    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: '主题一' }))
    expect(await screen.findByText('Plain detail body')).toBeInTheDocument()
    expect(screen.queryByTitle('邮件 HTML 正文')).toBeNull()
  })

  it('展示 HTML 详情和正文截断提示', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: inboxResult })),
      http.get('/api/inbox/:id', () =>
        HttpResponse.json({
          success: true,
          data: {
            ...fullMessage,
            html_body: '<p>HTML detail</p>',
            body_truncated: true,
          },
        }),
      ),
    )
    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: '主题一' }))
    expect(await screen.findByTitle('邮件 HTML 正文')).toBeInTheDocument()
    expect(screen.getByText(/正文过长，已截断/)).toBeInTheDocument()
    expect(screen.getByRole('article', { name: '邮件阅读区' })).toBeInTheDocument()
  })

  it('快速打开详情时旧请求不覆盖新邮件，关闭会中止请求', async () => {
    const resultWithTwo: InboxResult = {
      ...inboxResult,
      count: 2,
      messages: [
        inboxResult.messages[0],
        { ...inboxResult.messages[0], id: '2', subject: '主题二' },
      ],
    }
    let releaseFirst: (() => void) | undefined
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/aliases', () => HttpResponse.json({ success: true, data: { account_id: 'acc_1', count: 0, aliases: [] } })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: resultWithTwo })),
      http.get('/api/inbox/:id', ({ params }) => {
        if (params.id === '1') {
          return new Promise<Response>((resolve) => {
            releaseFirst = () => resolve(HttpResponse.json({ success: true, data: { ...fullMessage, subject: '旧详情', body: '旧正文' } }))
          })
        }
        return HttpResponse.json({ success: true, data: { ...fullMessage, id: '2', subject: '新详情', body: '新正文' } })
      }),
    )
    renderPage()
    const user = userEvent.setup()
    await user.click(await screen.findByRole('button', { name: '主题一' }))
    expect(await screen.findByText('读取中…')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '主题二' }))
    expect(await screen.findByText('新正文')).toBeInTheDocument()
    releaseFirst?.()
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByText('旧正文')).toBeNull()

    await user.click(screen.getByRole('button', { name: '主题一' }))
    await screen.findByText('读取中…')
    await user.click(screen.getByRole('button', { name: /返回列表/ }))
    releaseFirst?.()
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.getByText('选择一封邮件查看内容')).toBeInTheDocument()
  })
})
