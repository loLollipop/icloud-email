import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { describe, expect, it, vi } from 'vitest'
import { server } from '../test/server'
import { setCSRFToken } from '../api/client'
import ShareMailboxDialog from './ShareMailboxDialog'
import { ToastProvider } from './ToastProvider'

const alias = { anonymousId: 'alias_second', email: 'customer@icloud.com', label: '客户', active: true }
const path = '/api/aliases/alias_second/share'
function showDialog(onClose = vi.fn()) {
  setCSRFToken('csrf-test')
  render(<ToastProvider><ShareMailboxDialog accountId="acc_second" alias={alias} onClose={onClose} /></ToastProvider>)
  return onClose
}

describe('分发邮箱', () => {
  it('正确账号生成链接，仅使用生成响应中的 token', async () => {
    server.use(
      http.get(path, ({ request }) => {
        expect(new URL(request.url).searchParams.get('account_id')).toBe('acc_second')
        return HttpResponse.json({ success: true, data: { active: false } })
      }),
      http.post(path, async ({ request }) => {
        expect(await request.json()).toEqual({ account_id: 'acc_second' })
        expect(request.headers.get('x-csrf-token')).toBe('csrf-test')
        return HttpResponse.json({ success: true, data: { active: true, created_at: '2026-10-08T05:00:00Z', token: 'new_token' } })
      }),
    )
    showDialog()
    await userEvent.click(await screen.findByRole('button', { name: '生成分发链接' }))
    expect(await screen.findByLabelText('客户访问链接')).toHaveValue(`${window.location.origin}/share#new_token`)
    expect(screen.getByRole('button', { name: '终止分发' })).toBeInTheDocument()
  })

  it('重新打开已分发的邮箱只显示状态，不回显旧链接', async () => {
    server.use(http.get(path, () => HttpResponse.json({ success: true, data: { active: true, created_at: '2026-10-08T05:00:00Z' } })))
    showDialog()
    expect(await screen.findByRole('button', { name: '终止分发' })).toBeInTheDocument()
    expect(screen.queryByLabelText('客户访问链接')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新生成' })).toBeInTheDocument()
  })

  it('终止需要确认，请求中不允许关闭，失败仍可重试', async () => {
    let finish!: () => void
    const pending = new Promise<void>((resolve) => { finish = resolve })
    server.use(
      http.get(path, () => HttpResponse.json({ success: true, data: { active: true, created_at: '2026-10-08T05:00:00Z' } })),
      http.delete(path, async ({ request }) => {
        expect(await request.json()).toEqual({ account_id: 'acc_second' })
        await pending
        return HttpResponse.json({ success: false, code: 'PERSISTENCE_ERROR', message: '分发保存失败' }, { status: 500 })
      }),
    )
    const close = showDialog()
    await userEvent.click(await screen.findByRole('button', { name: '终止分发' }))
    await userEvent.click(screen.getByRole('button', { name: '确认终止' }))
    expect(screen.getByRole('dialog')).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('button', { name: '取消操作' })).toBeDisabled()
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.mouseDown(screen.getByRole('presentation'))
    expect(close).not.toHaveBeenCalled()
    await act(async () => finish())
    expect(await screen.findByText('分发保存失败')).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('button', { name: '确认终止' })).toBeEnabled())
  })

  it('重新生成明确提示旧链接失效，终止后清除已生成链接', async () => {
    server.use(
      http.get(path, () => HttpResponse.json({ success: true, data: { active: true, created_at: '2026-10-08T04:00:00Z' } })),
      http.post(path, () => HttpResponse.json({ success: true, data: { active: true, created_at: '2026-10-08T05:00:00Z', token: 'replacement' } })),
      http.delete(path, () => HttpResponse.json({ success: true, data: { active: false } })),
    )
    showDialog()
    await userEvent.click(await screen.findByRole('button', { name: '重新生成' }))
    expect(screen.queryByLabelText('客户访问链接')).not.toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('旧链接立即失效')
    await userEvent.click(screen.getByRole('button', { name: '确认重新生成' }))
    expect(await screen.findByLabelText('客户访问链接')).toHaveValue(`${window.location.origin}/share#replacement`)
    await userEvent.click(screen.getByRole('button', { name: '终止分发' }))
    await userEvent.click(screen.getByRole('button', { name: '确认终止' }))
    expect(await screen.findByText('未分发')).toBeInTheDocument()
    expect(screen.queryByLabelText('客户访问链接')).not.toBeInTheDocument()
  })
})
