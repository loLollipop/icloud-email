import { http, HttpResponse } from 'msw'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { setCSRFToken } from '../api/client'
import { server } from '../test/server'
import CreateAliasDialog from './CreateAliasDialog'

describe('CreateAliasDialog', () => {
  it('创建请求期间禁止所有关闭途径，成功后恢复', async () => {
    setCSRFToken('csrf-test')
    let finish!: () => void
    const pending = new Promise<void>((resolve) => { finish = resolve })
    let writes = 0
    server.use(http.post('/api/create', async () => {
      writes++
      await pending
      return HttpResponse.json({ success: true, data: { email: 'alias@icloud.com' } })
    }))
    const onClose = vi.fn()
    const onCreated = vi.fn()
    render(<CreateAliasDialog accountId="acc_test" open onClose={onClose} onCreated={onCreated} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('标签'), '购物')
    await user.click(screen.getByRole('button', { name: '创建' }))
    await waitFor(() => expect(writes).toBe(1))
    expect(screen.getByRole('button', { name: '取消' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: '取消' }))
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.mouseDown(screen.getByRole('presentation'))
    expect(onClose).not.toHaveBeenCalled()
    expect(screen.getByLabelText('标签')).toHaveValue('购物')
    await act(async () => { finish() })
    await waitFor(() => expect(onCreated).toHaveBeenCalledWith('alias@icloud.com'))
    expect(screen.getByRole('button', { name: '取消' })).toBeEnabled()
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
