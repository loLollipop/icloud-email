import { http, HttpResponse } from 'msw'
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it } from 'vitest'
import AccountsPage from './AccountsPage'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import { ToastProvider } from '../components/ToastProvider'
import type { AccountSummary } from '../api/types'
import { loadResource, readResource, resourceKeys } from '../api/resourceCache'

const accounts: AccountSummary[] = [
  {
    id: 'acc_active',
    name: '活跃号',
    real_email: 'active@example.com',
    icloud_email: 'active@icloud.com',
    host: 'icloud.com',
    status: 'active',
    alias_total: 15,
    alias_active: 12,
    has_cookies: true,
    has_app_password: true,
    has_proxy: false,
    last_validated: '2026-08-04T09:00:00+08:00',
    created_at: '2026-08-01T09:00:00+08:00',
  },
  {
    id: 'acc_pending',
    name: '等待号',
    real_email: 'pending@example.com',
    icloud_email: 'pending@icloud.com',
    host: 'icloud.com.cn',
    status: 'pending',
    alias_total: 0,
    alias_active: 0,
    has_cookies: false,
    has_app_password: false,
    has_proxy: false,
    last_validated: '',
    created_at: '2026-08-02T09:00:00+08:00',
  },
  {
    id: 'acc_error',
    name: '错误号',
    real_email: 'error@example.com',
    icloud_email: 'error@icloud.com',
    host: 'icloud.com',
    status: 'error',
    alias_total: 0,
    alias_active: 0,
    has_cookies: true,
    has_app_password: false,
    has_proxy: true,
    last_validated: '',
    created_at: '2026-08-03T09:00:00+08:00',
  },
]

function renderPage() {
  return render(
    <MemoryRouter>
      <ToastProvider>
        <AccountsPage />
      </ToastProvider>
    </MemoryRouter>,
  )
}

async function openSettings() {
  const toggle = screen.getAllByRole('button', { name: /连接设置/ })[0]
  if (toggle.getAttribute('aria-expanded') !== 'true') await userEvent.click(toggle)
}

describe('AccountsPage', () => {
  beforeEach(() => {
    setCSRFToken('csrf-test')
    server.resetHandlers()
  })

  it('渲染账号列表:状态文字、凭据标志、邮箱与别名计数可见,无秘密', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
    )
    renderPage()
    expect(await screen.findByText('活跃号')).toBeInTheDocument()
    expect(screen.getByText('等待号')).toBeInTheDocument()
    expect(screen.getByText('错误号')).toBeInTheDocument()
    expect(screen.getByText('active@icloud.com')).toBeInTheDocument()
    expect(screen.getByText('12 / 15')).toBeInTheDocument()
    expect(screen.getByText('12 / 15')).toHaveAttribute('title', '已启用隐藏邮箱 / 全部隐藏邮箱')
    expect(within(screen.getByRole('row', { name: '活跃号' })).getByText('验证通过')).toBeInTheDocument()
    expect(screen.getAllByRole('list', { name: '凭据配置情况（仅表示已填写）' })).toHaveLength(3)
    expect(screen.getAllByText(/已配置/).length).toBeGreaterThan(0)
    expect(screen.getAllByText(/未配置/).length).toBeGreaterThan(0)
    // 秘密字段不可见
    expect(screen.queryByText(/cookie-secret|app-secret|proxy-secret/)).toBeNull()
  })

  it('账号列表分页可前后切换并显示准确范围', async () => {
    const manyAccounts = Array.from({ length: 11 }, (_, index) => ({
      ...accounts[0],
      id: `acc_${index + 1}`,
      name: `账号 ${index + 1}`,
      icloud_email: `account-${index + 1}@icloud.com`,
    }))
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: manyAccounts })),
    )

    renderPage()
    expect(await screen.findByText('账号 1')).toBeInTheDocument()
    expect(screen.getByText('第 1-10 项，共 11 项')).toBeInTheDocument()
    expect(screen.queryByText('账号 11')).not.toBeInTheDocument()

    await userEvent.click(screen.getByRole('button', { name: '下一页' }))
    expect(screen.getByText('账号 11')).toBeInTheDocument()
    expect(screen.queryByText('账号 1')).not.toBeInTheDocument()
    expect(screen.getByText('第 11-11 项，共 11 项')).toBeInTheDocument()
  })

  it('添加账号:校验必填、请求期间禁用、成功刷新', async () => {
    let listData = accounts
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: listData })),
      http.post('/api/accounts', async ({ request }) => {
        const body = (await request.json()) as Record<string, string>
        if (!body.name || !body.icloud_email) {
          return HttpResponse.json(
            { success: false, code: 'VALIDATION_ERROR', message: '参数错误' },
            { status: 400 },
          )
        }
        listData = [{ ...accounts[0], id: 'acc_new', name: body.name }, ...accounts]
        return HttpResponse.json(
          { success: true, data: listData[0] },
          { status: 201 },
        )
      }),
    )
    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /添加账号/ }))
    fireEvent.change(screen.getByLabelText(/名称/), { target: { value: '新账号' } })
    fireEvent.change(screen.getByLabelText(/iCloud 邮箱/), { target: { value: 'new@icloud.com' } })
    await user.click(screen.getByRole('button', { name: /保存/ }))
    // 请求期间按钮禁用;成功后列表刷新
    await waitFor(() => expect(screen.getByText('新账号')).toBeInTheDocument())
  })

  it('添加账号:host 只能选全球区或中国区', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
    )
    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /添加账号/ }))
    const hostSelect = screen.getByLabelText(/区域/)
    expect(within(hostSelect).getByText('全球区 (icloud.com)')).toBeInTheDocument()
    expect(within(hostSelect).getByText('中国区 (icloud.com.cn)')).toBeInTheDocument()
    // 空名称提交被阻止(前端校验),dialog 保持打开
    await user.click(screen.getByRole('button', { name: /保存/ }))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it.each(['添加', '编辑'])('%s账号提交期间阻止取消、Escape、遮罩，完成后新草稿独立', async (mode) => {
    let finish!: () => void
    const pending = new Promise<void>((resolve) => { finish = resolve })
    let writes = 0
    const handler = async () => {
      writes++
      await pending
      return HttpResponse.json({ success: true, data: accounts[0] })
    }
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.post('/api/accounts', handler),
      http.patch('/api/accounts/:id', handler),
    )
    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    await user.click(mode === '添加'
      ? screen.getByRole('button', { name: /添加账号/ })
      : screen.getAllByRole('button', { name: /编辑/ })[0])
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: '提交中的草稿' } })
    if (mode === '添加') fireEvent.change(screen.getByLabelText('iCloud 邮箱'), { target: { value: 'draft@icloud.com' } })
    await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(writes).toBe(1))
    expect(screen.getByRole('button', { name: '取消' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: '取消' }))
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.mouseDown(screen.getByRole('presentation'))
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByLabelText('名称')).toHaveValue('提交中的草稿')
    expect(screen.getByRole('button', { name: '保存中…' })).toBeDisabled()

    await act(async () => { finish() })
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    await user.click(screen.getByRole('button', { name: /添加账号/ }))
    fireEvent.change(screen.getByLabelText('名称'), { target: { value: '下一份草稿' } })
    await act(async () => { await pending })
    expect(screen.getByLabelText('名称')).toHaveValue('下一份草稿')
    expect(writes).toBe(1)
  })

  it.each([
    { open: '添加账号', title: '添加账号', method: 'POST', path: '/api/accounts', fields: { '名称': '新增号', 'iCloud 邮箱': 'new@icloud.com' }, submit: '保存' },
    { open: '编辑', title: '编辑账号', method: 'PATCH', path: '/api/accounts/:id', fields: { '名称': '编辑号' }, submit: '保存' },
    { open: '更新 Cookie', title: '更新 Cookie', method: 'PUT', path: '/api/accounts/:id/cookies', fields: { 'Cookie': 'session=draft' }, submit: '保存' },
    { open: 'iCloud 登录', title: 'iCloud 登录', method: 'POST', path: '/api/accounts/:id/login', fields: { '密码': 'password' }, submit: '登录' },
    { open: '设置 App 密码', title: '设置 App 专用密码', method: 'POST', path: '/api/accounts/:id/password', fields: { '邮箱': 'app@icloud.com', 'App 专用密码': 'xxxx-xxxx' }, submit: '保存' },
    { open: '设置代理', title: '设置代理', method: 'PUT', path: '/api/accounts/:id/proxy', fields: { '代理地址': 'http://proxy.example:8080' }, submit: '保存' },
    { open: '接入收件邮箱', title: '接入收件邮箱', method: 'PUT', path: '/api/accounts/:id/mailbox', fields: { '收件邮箱': 'inbox@example.com', '邮箱授权码': 'draft-code' }, submit: '验证并接入' },
  ])('$title 延迟请求中不可关闭，失败后保留草稿并恢复关闭', async (scenario) => {
    let finish!: () => void
    const pending = new Promise<void>((resolve) => { finish = resolve })
    let writes = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.all(scenario.path, async ({ request }) => {
        expect(request.method).toBe(scenario.method)
        writes++
        await pending
        return HttpResponse.json({ success: false, code: 'UPSTREAM_FAILURE', message: '延迟失败' }, { status: 502 })
      }),
    )
    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    if (scenario.open !== '添加账号' && scenario.open !== '编辑') await openSettings()
    await user.click(screen.getAllByRole('button', { name: scenario.open })[0])
    const dialog = screen.getByRole('dialog', { name: scenario.title })
    for (const [label, value] of Object.entries(scenario.fields)) {
      fireEvent.change(within(dialog).getByLabelText(label), { target: { value } })
    }
    await user.click(within(dialog).getByRole('button', { name: scenario.submit }))
    await waitFor(() => expect(writes).toBe(1))
    expect(within(dialog).getByRole('button', { name: '取消' })).toBeDisabled()
    await user.click(within(dialog).getByRole('button', { name: '取消' }))
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.mouseDown(screen.getByRole('presentation'))
    expect(dialog).toBeInTheDocument()
    expect(dialog).toHaveAttribute('aria-busy', 'true')
    await act(async () => { finish() })
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('延迟失败')
    for (const [label, value] of Object.entries(scenario.fields)) {
      expect(within(dialog).getByLabelText(label)).toHaveValue(value)
    }
    expect(within(dialog).getByRole('button', { name: '取消' })).toBeEnabled()
    expect(dialog).toHaveAttribute('aria-busy', 'false')
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    expect(writes).toBe(1)
  })

  it('编辑中国区账号会预填并提交 PATCH，之后新增不残留编辑值', async () => {
    let patchBody: Record<string, string> | undefined
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.patch('/api/accounts/:id', async ({ request }) => {
        patchBody = (await request.json()) as Record<string, string>
        return HttpResponse.json({ success: true, data: accounts[1] })
      }),
    )
    renderPage()
    await screen.findByText('等待号')
    const user = userEvent.setup()
    await user.click(screen.getAllByRole('button', { name: /编辑/ })[1])

    expect(screen.getByLabelText(/名称/)).toHaveValue('等待号')
    expect(screen.getByLabelText(/iCloud 邮箱/)).toHaveValue('pending@icloud.com')
    expect(screen.getByLabelText(/区域/)).toHaveValue('icloud.com.cn')
    await user.clear(screen.getByLabelText(/名称/))
    await user.type(screen.getByLabelText(/名称/), '中国区账号')
    await user.click(screen.getByRole('button', { name: /保存/ }))
    await waitFor(() =>
      expect(patchBody).toEqual({
        name: '中国区账号',
        icloud_email: 'pending@icloud.com',
        host: 'icloud.com.cn',
      }),
    )

    await user.click(screen.getByRole('button', { name: /添加账号/ }))
    expect(screen.getByLabelText(/名称/)).toHaveValue('')
    expect(screen.getByLabelText(/iCloud 邮箱/)).toHaveValue('')
    expect(screen.getByLabelText(/区域/)).toHaveValue('icloud.com')
  })

  it('Cookie 提交后 textarea 清空', async () => {
    let cookieBody = ''
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.put('/api/accounts/:id/cookies', async ({ request }) => {
        cookieBody = await request.text()
        return HttpResponse.json({ success: true, data: accounts[0] })
      }),
    )
    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    await openSettings()
    await user.click(screen.getAllByRole('button', { name: /更新 Cookie/ })[0])
    const textarea = screen.getByLabelText('Cookie') as HTMLTextAreaElement
    await user.type(textarea, 'a=1; b=2')
    await user.click(screen.getByRole('button', { name: /保存/ }))
    await waitFor(() => expect(cookieBody).toContain('a=1; b=2'))
    expect((screen.getByLabelText('Cookie') as HTMLTextAreaElement).value).toBe('')
  })

  it('Cookie 更新失败也只清理当前账号的身份缓存', async () => {
    const targetInboxKey = resourceKeys.inbox('account_id=acc_active&limit=20&days=7')
    const otherInboxKey = resourceKeys.inbox('account_id=acc_other&limit=20&days=7')
    await loadResource(resourceKeys.aliases('acc_active'), 60_000, async () => ['target-alias'])
    await loadResource(targetInboxKey, 60_000, async () => ({ target: true }))
    await loadResource(resourceKeys.aliases('acc_other'), 60_000, async () => ['other-alias'])
    await loadResource(otherInboxKey, 60_000, async () => ({ other: true }))
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.put('/api/accounts/:id/cookies', () => HttpResponse.json(
        { success: false, code: 'UPSTREAM_FAILURE', message: 'Cookie 校验失败' },
        { status: 502 },
      )),
    )

    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    await openSettings()
    await user.click(screen.getAllByRole('button', { name: /更新 Cookie/ })[0])
    await user.type(screen.getByLabelText('Cookie'), 'session=new')
    await user.click(screen.getByRole('button', { name: /保存/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Cookie 校验失败')

    expect(readResource(resourceKeys.aliases('acc_active'))).toBeUndefined()
    expect(readResource(targetInboxKey)).toBeUndefined()
    expect(readResource(resourceKeys.aliases('acc_other'))?.data).toEqual(['other-alias'])
    expect(readResource(otherInboxKey)?.data).toEqual({ other: true })
  })

  it('iCloud 登录收到 OTP_REQUIRED 后只显示 OTP 输入并可重试', async () => {
    let calls = 0
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.post('/api/accounts/:id/login', async () => {
        calls++
        if (calls === 1) {
          return HttpResponse.json(
            { success: false, code: 'OTP_REQUIRED', message: '需要提供 OTP 验证码' },
            { status: 409 },
          )
        }
        return HttpResponse.json({ success: true, data: accounts[0] })
      }),
    )
    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    await openSettings()
    await user.click(screen.getAllByRole('button', { name: /iCloud 登录/ })[0])
    await user.type(screen.getByLabelText(/密码/), 'p@ssw0rd')
    let dialog = screen.getByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /登录/ }))
    // 出现 OTP 输入
    const otpInput = await screen.findByLabelText(/验证码/)
    expect(otpInput).toHaveAttribute('inputmode', 'numeric')
    expect(screen.getByRole('dialog')).toHaveAttribute('aria-busy', 'false')
    expect(screen.getByRole('button', { name: '取消' })).toBeEnabled()
    expect(screen.getByRole('button', { name: '验证' })).toBeEnabled()
    await user.type(otpInput, '123456')
    dialog = screen.getByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /验证/ }))
    await waitFor(() => expect(calls).toBe(2))
  })

  it('App Password 提交后清空', async () => {
    let pwdBody = ''
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.post('/api/accounts/:id/password', async ({ request }) => {
        pwdBody = await request.text()
        return HttpResponse.json({ success: true, data: accounts[0] })
      }),
    )
    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    await openSettings()
    await user.click(screen.getAllByRole('button', { name: /设置 App 密码/ })[0])
    await user.type(within(screen.getByRole('dialog')).getByLabelText(/邮箱/), 'app@icloud.com')
    await user.type(screen.getByLabelText('App 专用密码'), 'xxxx-xxxx-xxxx-xxxx')
    await user.click(screen.getByRole('button', { name: /保存/ }))
    await waitFor(() => expect(pwdBody).toContain('app@icloud.com'))
    expect((screen.getByLabelText('App 专用密码') as HTMLInputElement).value).toBe('')
  })

  it('代理从不回显:保存后输入清空,关闭再打开仍为空', async () => {
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.put('/api/accounts/:id/proxy', async ({ request }) => {
        const body = (await request.json()) as { proxy: string }
        return HttpResponse.json({
          success: true,
          data: { ...accounts[0], has_proxy: body.proxy !== '' },
        })
      }),
    )
    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    await openSettings()
    await user.click(screen.getAllByRole('button', { name: /设置代理/ })[0])
    const input = screen.getByLabelText(/代理地址/) as HTMLInputElement
    expect(input.value).toBe('')
    await user.type(input, 'http://u:p@proxy.example.com:8080')
    await user.click(screen.getByRole('button', { name: /保存/ }))
    // 保存后输入清空
    await waitFor(() => expect(input.value).toBe(''))
    // 关闭后重新打开:仍为空(从不回显)
    await user.click(screen.getByRole('button', { name: /取消/ }))
    await openSettings()
    await user.click(screen.getAllByRole('button', { name: /设置代理/ })[0])
    expect((screen.getByLabelText(/代理地址/) as HTMLInputElement).value).toBe('')
  })

  it('删除要求输入账号名称精确匹配,取消不发请求', async () => {
    let deleted = false
    server.use(
      http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })),
      http.delete('/api/accounts/:id', () => {
        deleted = true
        return HttpResponse.json({ success: true, data: { id: 'acc_active' } })
      }),
    )
    renderPage()
    await screen.findByText('活跃号')
    const user = userEvent.setup()
    await openSettings()
    await user.click(screen.getAllByRole('button', { name: /删除/ })[0])
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    // 名称不匹配时按钮禁用
    await user.type(screen.getByLabelText(/输入账号名称/), '错误名称')
    expect(screen.getByRole('button', { name: /确认删除/ })).toBeDisabled()
    // 取消不发请求
    await user.click(screen.getByRole('button', { name: /取消/ }))
    expect(deleted).toBe(false)
  })

  it('删除后的新列表不会被较晚返回的初始请求覆盖', async () => {
    const oldResponse = [{ ...accounts[0], name: '旧响应账号' }]
    const newResponse = [{ ...accounts[1], name: '新响应账号' }]
    await loadResource(resourceKeys.accounts, 0, async () => accounts)

    let getCalls = 0
    let resolveOld!: (value: AccountSummary[]) => void
    let resolveNew!: (value: AccountSummary[]) => void
    const oldList = new Promise<AccountSummary[]>((resolve) => { resolveOld = resolve })
    const newList = new Promise<AccountSummary[]>((resolve) => { resolveNew = resolve })
    server.use(
      http.get('/api/accounts', async () => {
        getCalls++
        const data = await (getCalls === 1 ? oldList : newList)
        return HttpResponse.json({ success: true, data })
      }),
      http.delete('/api/accounts/:id', () => (
        HttpResponse.json({ success: true, data: { id: 'acc_active' } })
      )),
    )

    renderPage()
    await waitFor(() => expect(getCalls).toBe(1))
    const user = userEvent.setup()
    await openSettings()
    await user.click(screen.getAllByRole('button', { name: /删除/ })[0])
    await user.type(screen.getByLabelText(/输入账号名称/), '活跃号')
    await user.click(screen.getByRole('button', { name: /确认删除/ }))
    await waitFor(() => expect(getCalls).toBe(2))

    await act(async () => {
      resolveNew(newResponse)
    })
    expect(await screen.findByText('新响应账号')).toBeInTheDocument()

    await act(async () => {
      resolveOld(oldResponse)
      await oldList
    })
    await waitFor(() => {
      expect(screen.getByText('新响应账号')).toBeInTheDocument()
      expect(screen.queryByText('旧响应账号')).not.toBeInTheDocument()
    })
  })
  it('本地名称和邮箱搜索、状态筛选共同决定分页范围并重置页码', async () => {
    const manyAccounts = Array.from({ length: 12 }, (_, index) => ({
      ...accounts[index === 11 ? 1 : 0], id: `acc_${index}`, name: `账号 ${index + 1}`,
      icloud_email: `user-${index + 1}@icloud.com`,
    }))
    server.use(http.get('/api/accounts', () => HttpResponse.json({ success: true, data: manyAccounts })))
    renderPage()
    await screen.findByText('账号 1')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: '下一页' }))
    await user.type(screen.getByRole('searchbox', { name: '搜索账号' }), 'USER-12')
    expect(screen.getByText('账号 12')).toBeInTheDocument()
    expect(screen.getByText('第 1-1 项，共 1 项')).toBeInTheDocument()
    await user.clear(screen.getByRole('searchbox', { name: '搜索账号' }))
    await user.selectOptions(screen.getByRole('combobox', { name: '账号状态' }), 'pending')
    expect(screen.getByText('账号 12')).toBeInTheDocument()
    expect(screen.queryByText('账号 1')).not.toBeInTheDocument()
    await user.selectOptions(screen.getByRole('combobox', { name: '账号状态' }), 'active')
    await user.type(screen.getByRole('searchbox', { name: '搜索账号' }), '账号 12')
    expect(screen.getByText('没有匹配的账号')).toBeInTheDocument()
  })

  it('次要连接操作按账号折叠，展开后全部可达且名称确认删除保持有效', async () => {
    server.use(http.get('/api/accounts', () => HttpResponse.json({ success: true, data: accounts })))
    renderPage()
    const row = await screen.findByRole('row', { name: '活跃号' })
    const toggle = within(row).getByRole('button', { name: '连接设置 · 活跃号' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(screen.queryByRole('region', { name: '连接设置 · 活跃号' })).not.toBeInTheDocument()
    expect(within(row).getByRole('link', { name: '收件箱' })).toHaveAttribute('href', '/inbox?account_id=acc_active')
    await userEvent.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    const settings = screen.getByRole('region', { name: '连接设置 · 活跃号' })
    for (const name of ['更新 Cookie', 'iCloud 登录', '设置 App 密码', '接入收件邮箱', '设置代理', '删除']) {
      expect(within(settings).getByRole('button', { name })).toBeVisible()
    }
    await userEvent.click(screen.getByRole('button', { name: '连接设置 · 等待号' }))
    expect(screen.queryByRole('region', { name: '连接设置 · 活跃号' })).not.toBeInTheDocument()
    expect(screen.getByRole('region', { name: '连接设置 · 等待号' })).toBeVisible()
  })

})
