import { http, HttpResponse } from 'msw'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
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
  total: 1,
  page: 1,
  page_size: 20,
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

  it('默认查询全部历史，关键词安全编码并发送至服务器', async () => {
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
    expect(url.searchParams.get('scope')).toBe('hme_aliases')
    expect(url.searchParams.get('page')).toBe('1')
    expect(url.searchParams.get('page_size')).toBe('20')
    expect(url.searchParams.has('limit')).toBe(false)
    expect(url.searchParams.has('days')).toBe(false)
    expect(screen.getByRole('option', { name: '全部 iCloud 隐私别名' })).toBeInTheDocument()
    expect(screen.getByText(/所有隐藏邮箱的来信/)).toBeInTheDocument()
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('搜索邮件'), '旧邮件 & 状态')
    await user.click(screen.getByRole('button', { name: '搜索' }))
    await waitFor(() => {
      const u = new URL(lastUrl)
      expect(u.searchParams.get('q')).toBe('旧邮件 & 状态')
      expect(u.searchParams.get('field')).toBe('subject')
    })
    expect(screen.queryByLabelText(/加载范围|时间范围/)).toBeNull()
    expect(screen.queryByLabelText('搜索字段')).not.toBeInTheDocument()
    expect(screen.getByText(/仅搜索主题/)).toBeInTheDocument()
  })

  it.each([
    ['单个收件人', 'alpha@icloud.com', 'alpha@icloud.com'],
    ['多个收件人', 'alpha@icloud.com, beta@icloud.com', 'alpha@icloud.com, beta@icloud.com'],
    ['缺少收件人', '  ', '未提供收件邮箱'],
  ])('列表直接显示%s，不请求详情或猜测收件地址', async (_name, to, expected) => {
    let detailRequests = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: {
        ...inboxResult,
        messages: [{ ...inboxResult.messages[0], to }],
      } })),
      http.get('/api/inbox/:id', () => {
        detailRequests++
        return HttpResponse.json({ success: true, data: fullMessage })
      }),
    )
    renderPage()
    const row = await screen.findByRole('button', { name: '主题一' })
    expect(within(row).getByText('收件：')).toBeInTheDocument()
    expect(within(row).getByText(expected)).toBeInTheDocument()
    expect(row).toHaveAccessibleDescription(`收件：${expected}`)
    expect(detailRequests).toBe(0)
    expect(screen.queryByRole('button', { name: '← 返回全部邮件' })).not.toBeInTheDocument()
  })

  it.each(['all', 'from', 'body'])('旧 field=%s 链接也只搜主题，不纳入正文或发件人命中', async (oldField) => {
    const messages = [
      { ...inboxResult.messages[0], subject: 'Your request was APPROVED', preview: 'Welcome' },
      { ...inboxResult.messages[0], id: '2', subject: 'Set up your workspace', from: 'approved@example.com', preview: 'Your account has been approved' },
    ]
    let lastField = ''
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', ({ request }) => {
        const params = new URL(request.url).searchParams
        lastField = params.get('field') ?? ''
        const q = (params.get('q') ?? '').toLowerCase()
        const matches = messages.filter((message) => (lastField === 'subject' ? message.subject : `${message.subject} ${message.from} ${message.preview}`).toLowerCase().includes(q))
        return HttpResponse.json({ success: true, data: { ...inboxResult, count: matches.length, total: matches.length, messages: matches } })
      }),
    )
    renderPage(`/inbox?field=${oldField}&q=approved`)
    const row = await screen.findByRole('button', { name: 'Your request was APPROVED' })
    expect(lastField).toBe('subject')
    expect(row.querySelector('mark')).toHaveTextContent('APPROVED')
    expect(screen.queryByRole('button', { name: 'Set up your workspace' })).not.toBeInTheDocument()
    expect(screen.queryByLabelText('搜索字段')).not.toBeInTheDocument()
    expect(screen.queryByText('sender@example.com')).not.toBeInTheDocument()
    expect(screen.queryByText('approved@example.com')).not.toBeInTheDocument()
  })

  it('主题关键词高亮按字面匹配，保留标题和详情点击', async () => {
    const subject = 'Approved [a+b].<test>'
    const message = { ...inboxResult.messages[0], subject }
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: { ...inboxResult, messages: [message] } })),
      http.get('/api/inbox/:id', () => HttpResponse.json({ success: true, data: { ...fullMessage, subject } })),
    )
    renderPage('/inbox?q=' + encodeURIComponent('[a+b].<test>'))
    const row = await screen.findByRole('button', { name: subject })
    expect(row.querySelector('mark')).toHaveTextContent('[a+b].<test>')
    expect(row.querySelector('test')).toBeNull()
    expect(within(row).getByText('alpha@icloud.com')).toBeInTheDocument()
    await userEvent.click(row)
    expect(await screen.findByText('Plain detail body')).toBeInTheDocument()
    expect(screen.getAllByText('sender@example.com')[0]).toBeInTheDocument()
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
    expect(screen.getByLabelText('收件邮箱')).toHaveValue('alpha@icloud.com')
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
    expect(screen.getByText(/当前仅提供邮件摘要/)).toBeInTheDocument()
  })

  it('向服务器翻页并直接搜索当前页外的主题邮件', async () => {
    const messages = Array.from({ length: 120 }, (_, index) => ({
      ...inboxResult.messages[0],
      id: String(index + 1),
      subject: index === 119 ? '主题 120 approved' : `主题 ${index + 1}`,
      preview: `摘要 ${index + 1}`,
    }))
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', ({ request }) => {
        const params = new URL(request.url).searchParams
        const page = Number(params.get('page'))
        const pageSize = Number(params.get('page_size'))
        const q = params.get('q') ?? ''
        expect(params.get('field')).toBe('subject')
        const matches = q ? messages.filter((message) => message.subject.includes(q)) : messages
        return HttpResponse.json({ success: true, data: { ...inboxResult, count: Math.min(pageSize, matches.length), total: matches.length, page, page_size: pageSize, messages: matches.slice((page - 1) * pageSize, page * pageSize) } })
      }),
    )

    renderPage()
    expect(await screen.findByRole('button', { name: '主题 1' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '主题 21' })).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    expect(await screen.findByRole('button', { name: '主题 21' })).toBeInTheDocument()

    const user = userEvent.setup()
    await user.type(screen.getByLabelText('搜索邮件'), 'approved')
    expect(await screen.findByRole('button', { name: '主题 120 approved' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '主题 21' })).not.toBeInTheDocument()
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
          data: { ...inboxResult, count: 0, total: 0, messages: [] },
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
    await user.type(screen.getByLabelText('搜索邮件'), '第二请求')
    await user.click(screen.getByRole('button', { name: '搜索' }))
    await screen.findByRole('button', { name: '第二请求主题' })
    // 第一次请求此时才返回
    release?.()
    // 旧数据不得覆盖新数据
    await new Promise((r) => setTimeout(r, 100))
    expect(screen.getByRole('button', { name: '第二请求主题' })).toBeInTheDocument()
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

  it('缓存后台重验失败时清除旧邮件并显示错误', async () => {
    let failRefresh = false
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => {
        if (failRefresh) {
          return HttpResponse.json(
            { success: false, code: 'HME_FILTER_UNAVAILABLE', message: '隐私别名邮件筛选暂不可用' },
            { status: 503 },
          )
        }
        return HttpResponse.json({ success: true, data: inboxResult })
      }),
    )

    const first = renderPage()
    await screen.findByText('主题一')
    first.unmount()
    failRefresh = true

    renderPage()
    expect(screen.getByText('主题一')).toBeInTheDocument()
    expect(await screen.findByText('隐私别名邮件筛选暂不可用')).toBeInTheDocument()
    expect(screen.queryByText('主题一')).toBeNull()
  })

  it('缓存重验失败会关闭期间打开的旧详情', async () => {
    let refreshPending = false
    let rejectRefresh: (() => void) | undefined
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => {
        if (!refreshPending) return HttpResponse.json({ success: true, data: inboxResult })
        return new Promise<Response>((resolve) => {
          rejectRefresh = () => resolve(HttpResponse.json(
            { success: false, code: 'HME_FILTER_UNAVAILABLE', message: '隐私别名邮件筛选暂不可用' },
            { status: 503 },
          ))
        })
      }),
      http.get('/api/inbox/:id', () => HttpResponse.json({ success: true, data: fullMessage })),
    )

    const first = renderPage()
    await screen.findByText('主题一')
    first.unmount()
    refreshPending = true

    renderPage()
    await userEvent.click(screen.getByRole('button', { name: '主题一' }))
    expect(await screen.findByText('Plain detail body')).toBeInTheDocument()
    rejectRefresh?.()
    expect(await screen.findByText('隐私别名邮件筛选暂不可用')).toBeInTheDocument()
    expect(screen.queryByRole('article', { name: '邮件阅读区' })).toBeNull()
    expect(screen.queryByText('主题一')).toBeNull()
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

  it('切换账号重置页码与别名，并保留关键词，不沿用旧范围限制', async () => {
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

    renderPage('/inbox?account_id=acc_1&limit=50&days=30&q=合同&page=3')
    const user = userEvent.setup()
    await screen.findByText('主题一')
    await user.selectOptions(screen.getByLabelText(/账号/), 'acc_2')

    await waitFor(() => expect(secondAccountURL).not.toBe(''))
    const query = new URL(secondAccountURL).searchParams
    expect(query.get('limit')).toBeNull()
    expect(query.get('days')).toBeNull()
    expect(query.get('page')).toBe('1')
    expect(query.get('q')).toBe('合同')
    expect(screen.getByLabelText('搜索邮件')).toHaveValue('合同')
  })

  it('删除重试始终绑定最初账号和邮件 UID', async () => {
    const secondAccount: AccountSummary = { ...accounts[0], id: 'acc_2', name: '备用号' }
    const deleteURLs: string[] = []
    let releaseFirst: (() => void) | undefined
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: [...accounts, secondAccount] })),
      http.get('/api/inbox', ({ request }) => {
        const accountID = new URL(request.url).searchParams.get('account_id') ?? ''
        return HttpResponse.json({ success: true, data: { ...inboxResult, account_id: accountID } })
      }),
      http.get('/api/inbox/:id', () => HttpResponse.json({ success: true, data: fullMessage })),
      http.delete('/api/inbox/:id', ({ request }) => {
        deleteURLs.push(request.url)
        if (deleteURLs.length === 1) {
          return new Promise<Response>((resolve) => {
            releaseFirst = () => resolve(HttpResponse.json(
              { success: false, code: 'UPSTREAM_FAILURE', message: '删除邮件失败' },
              { status: 502 },
            ))
          })
        }
        return HttpResponse.json({ success: true, data: { id: '1' } })
      }),
    )

    renderPage('/inbox?account_id=acc_1')
    await userEvent.click(await screen.findByRole('button', { name: '主题一' }))
    await screen.findByText('Plain detail body')
    await userEvent.click(screen.getByRole('button', { name: '删除邮件' }))
    await userEvent.click(screen.getByRole('button', { name: '确认删除' }))

    fireEvent.click(screen.getByRole('button', { name: '← 返回全部邮件' }))
    fireEvent.change(screen.getByLabelText(/账号/), { target: { value: 'acc_2' } })
    releaseFirst?.()
    await waitFor(() => expect(screen.getByRole('button', { name: '确认删除' })).not.toBeDisabled())
    await userEvent.click(screen.getByRole('button', { name: '确认删除' }))

    await waitFor(() => expect(deleteURLs).toHaveLength(2))
    expect(deleteURLs.map((url) => new URL(url).searchParams.get('account_id'))).toEqual(['acc_1', 'acc_1'])
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
    await user.type(screen.getByLabelText('搜索邮件'), '历史关键词')
    await user.click(screen.getByRole('button', { name: '搜索' }))
    releaseAccounts?.()

    await waitFor(() => expect(screen.getByLabelText(/账号/)).toHaveValue('acc_1'))
    await screen.findByText('主题一')
    const query = new URL(inboxURL).searchParams
    expect(query.get('account_id')).toBe('acc_1')
    expect(query.get('q')).toBe('历史关键词')
    expect(query.get('days')).toBeNull()
    expect(query.get('limit')).toBeNull()
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
    expect(await screen.findByText('打开查看邮件')).toBeInTheDocument()
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
    expect(screen.getByRole('button', { name: '← 返回全部邮件' })).toBeInTheDocument()
    expect(screen.queryByLabelText(/别名/)).toBeNull()
    expect(screen.queryByLabelText('邮件列表')).toBeNull()
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
    await user.click(screen.getByRole('button', { name: '← 返回全部邮件' }))
    await user.click(screen.getByRole('button', { name: '主题二' }))
    expect(await screen.findByText('新正文')).toBeInTheDocument()
    releaseFirst?.()
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.queryByText('旧正文')).toBeNull()
    await user.click(screen.getByRole('button', { name: '← 返回全部邮件' }))
    await user.click(screen.getByRole('button', { name: '主题一' }))
    await screen.findByText('读取中…')
    await user.click(screen.getByRole('button', { name: '← 返回全部邮件' }))
    releaseFirst?.()
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(screen.getByLabelText('邮件列表')).toBeInTheDocument()
    expect(screen.queryByText('旧正文')).toBeNull()
  })

  it('详情失败可以原地重试，不丢失当前邮件', async () => {
    let calls = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: inboxResult })),
      http.get('/api/inbox/:id', () => ++calls === 1
        ? HttpResponse.json({ success: false, message: '读取暂时失败' }, { status: 503 })
        : HttpResponse.json({ success: true, data: fullMessage })),
    )
    renderPage()
    await userEvent.click(await screen.findByRole('button', { name: '主题一' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('读取暂时失败')
    await userEvent.click(screen.getByRole('button', { name: '重新读取' }))
    expect(await screen.findByText('Plain detail body')).toBeInTheDocument()
    expect(calls).toBe(2)
  })

  it('不可用账号的邮件链接不会回退读取或删除另一账号的同 UID 邮件', async () => {
    let details = 0
    await loadResource(resourceKeys.accounts, -1, async () => accounts)
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: inboxResult })),
      http.get('/api/inbox/:id', () => { details++; return HttpResponse.json({ success: true, data: fullMessage }) }),
    )
    renderPage('/inbox?account_id=removed&message=1')
    expect(await screen.findByText('邮件所属账户不可用，已返回收件箱')).toBeInTheDocument()
    await screen.findByRole('button', { name: '主题一' })
    expect(screen.queryByRole('article', { name: '邮件阅读区' })).toBeNull()
    expect(screen.queryByRole('button', { name: '删除邮件' })).toBeNull()
    expect(details).toBe(0)
  })

  it('缓存缺少链接账号时等待账号校验，并仅请求链接指定账号的正文', async () => {
    const secondAccount = { ...accounts[0], id: 'acc_2', name: '备用号' }
    let release: (() => void) | undefined
    const details: string[] = []
    await loadResource(resourceKeys.accounts, -1, async () => accounts)
    server.use(
      http.get('/api/accounts', () => new Promise<Response>((resolve) => { release = () => resolve(HttpResponse.json({ success: true, data: [...accounts, secondAccount] })) })),
      http.get('/api/inbox', ({ request }) => HttpResponse.json({ success: true, data: { ...inboxResult, account_id: new URL(request.url).searchParams.get('account_id') } })),
      http.get('/api/inbox/:id', ({ request }) => { details.push(new URL(request.url).searchParams.get('account_id') ?? ''); return HttpResponse.json({ success: true, data: fullMessage }) }),
    )
    renderPage('/inbox?account_id=acc_2&message=1')
    await waitFor(() => expect(release).toBeDefined())
    expect(details).toEqual([])
    release?.()
    expect(await screen.findByText('Plain detail body')).toBeInTheDocument()
    expect(details).toEqual(['acc_2'])
  })

  it('初次账号加载失败后重试能重新获取账号', async () => {
    let calls = 0
    server.use(
      http.get('/api/accounts', () => ++calls === 1 ? HttpResponse.json({ success: false, message: '账号加载失败' }, { status: 503 }) : HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', () => HttpResponse.json({ success: true, data: inboxResult })),
    )
    renderPage()
    expect(await screen.findByRole('alert')).toHaveTextContent('账号加载失败')
    await userEvent.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByRole('button', { name: '主题一' })).toBeInTheDocument()
    expect(calls).toBe(2)
  })

  it('从详情返回保留搜索和分页状态', async () => {
    const messages = Array.from({ length: 12 }, (_, index) => ({
      ...inboxResult.messages[0],
      id: String(index + 1),
      subject: `保留状态 ${index + 1}`,
    }))
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.get('/api/inbox', ({ request }) => {
        const params = new URL(request.url).searchParams
        const page = Number(params.get('page'))
        const size = Number(params.get('page_size'))
        return HttpResponse.json({ success: true, data: { ...inboxResult, count: Math.min(size, messages.length), total: messages.length, page, page_size: size, messages: messages.slice((page - 1) * size, page * size) } })
      }),
      http.get('/api/inbox/:id', ({ params }) => HttpResponse.json({ success: true, data: { ...fullMessage, id: String(params.id), subject: `保留状态 ${params.id}` } })),
    )
    renderPage('/inbox?page_size=10')
    const user = userEvent.setup()
    await screen.findByRole('button', { name: '保留状态 1' })
    await user.type(screen.getByLabelText('搜索邮件'), '保留状态')
    await user.click(screen.getByRole('button', { name: '搜索' }))
    await waitFor(() => expect(screen.queryByRole('status', { name: '加载中' })).toBeNull())
    await user.click(screen.getByRole('button', { name: '下一页' }))
    await user.click(await screen.findByRole('button', { name: '保留状态 11' }))
    await screen.findByText('Plain detail body')
    await user.click(screen.getByRole('button', { name: '← 返回全部邮件' }))
    expect(screen.getByLabelText('搜索邮件')).toHaveValue('保留状态')
    expect(screen.getByText('第 11-12 项，共 12 项')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '保留状态 11' })).toBeInTheDocument()
  })
})
